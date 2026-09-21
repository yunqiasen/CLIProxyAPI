package test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Exercise the public HTTP contract with client-retained output, rather than
// calling the recovery helper or constructing an already-repaired conversation.
func TestCodexConversationSwitchContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"http", "sse", "websocket"} {
		stream := transport != "http"
		for _, rejectionSSE := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rejectionSSE=%t", transport, rejectionSSE), func(t *testing.T) {
				var mu sync.Mutex
				sessions := map[string]string{}
				attempts := map[string]int{}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					host := r.URL.Hostname()
					if host != "agentrouter.org" && host != "anyrouter.top" {
						t.Errorf("unexpected route: %s", host)
					}
					if r.Header.Get("Authorization") != "Bearer "+host+"-selected" || gjson.GetBytes(body, "model").String() != "gpt-6-astra" {
						t.Error("selected key or resolved upstream model changed")
					}
					items := gjson.GetBytes(body, "input").Array()
					marker := items[0].Get("content").String()
					turn := items[len(items)-1].Get("content").String()
					identity := host + "/" + strings.Split(turn, "_turn")[0]
					key := gjson.GetBytes(body, "prompt_cache_key").String()
					if key == "" || r.Header.Get("Session_id") != key || r.Header.Get("Thread-Id") != key || gjson.Get(r.Header.Get("X-Codex-Turn-Metadata"), "prompt_cache_key").String() != key {
						t.Error("body, session header and turn metadata disagree")
					}
					mu.Lock()
					if prior := sessions[identity]; prior != "" && prior != key {
						t.Errorf("session changed while continuing or returning to %s", host)
					}
					for other, value := range sessions {
						if other != identity && value == key {
							t.Errorf("session identity shared across conversations or routes: %s and %s", identity, other)
						}
					}
					sessions[identity] = key
					attempts[identity+"/"+turn]++
					mu.Unlock()
					for _, other := range []string{"conversation0", "conversation1"} {
						if other != marker && strings.Contains(string(body), other) {
							t.Errorf("another conversation's history entered %s", marker)
						}
					}
					calls := map[string]string{}
					results := map[string]bool{}
					for _, item := range items {
						typ, id := item.Get("type").String(), item.Get("call_id").String()
						switch typ {
						case "function_call", "custom_tool_call":
							if id == "" || calls[id] != "" {
								t.Error("tool correlation is empty or duplicated")
							}
							calls[id] = typ
						case "function_call_output", "custom_tool_call_output":
							if calls[id]+"_output" != typ || results[id] {
								t.Error("tool result is orphaned or duplicated")
							}
							results[id] = true
						}
					}
					if len(calls) != len(results) {
						t.Error("tool result disappeared from continued history")
					}
					bound := false
					for _, item := range items {
						if item.Get("role").String() != "user" && (item.Get("id").Exists() || item.Get("encrypted_content").Exists()) {
							bound = true
						}
					}
					if bound {
						message := "The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=contract]"
						if host == "anyrouter.top" {
							message = "bad response status code 400 (request id: contract)"
						}
						rejection := map[string]any{"type": "invalid_request_error", "message": message, "param": ""}
						if rejectionSSE {
							w.Header().Set("Content-Type", "text/event-stream")
							writeConversationEvent(w, map[string]any{"type": "response.created", "response": map[string]any{"id": "rejected_scaffolding", "status": "in_progress", "output": []any{}}})
							writeConversationEvent(w, map[string]any{"type": "response.failed", "response": map[string]any{"id": "rejected_scaffolding", "status": "failed", "error": rejection}})
						} else {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							_ = json.NewEncoder(w).Encode(map[string]any{"error": rejection})
						}
						return
					}
					if len(items) > 1 {
						for _, detail := range []string{marker + " summary", marker + " search", marker + " page", marker + " function result", marker + " patch result", "call_" + marker, "patch_" + marker, "msg_user_" + marker} {
							if !strings.Contains(string(body), detail) {
								t.Errorf("retained history lost %q", detail)
							}
						}
					}
					output := conversationSwitchOutput(marker, turn)
					w.Header().Set("Content-Type", "text/event-stream")
					for i, item := range output {
						writeConversationEvent(w, map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
					}
					writeConversationEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_" + marker + "_" + turn, "status": "completed", "output": output, "prompt_cache_key": key}})
				}))
				t.Cleanup(upstream.Close)
				cfg := &config.Config{}
				for _, host := range []string{"agentrouter.org", "anyrouter.top"} {
					cfg.CodexKey = append(cfg.CodexKey, config.CodexKey{Name: host, BaseURL: "http://" + host + "/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: host}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: host + "-selected"}}})
				}
				auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil || len(auths) != 2 {
					t.Fatalf("route synthesis: %v", err)
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 1)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				for _, auth := range auths {
					host := strings.TrimSuffix(strings.TrimPrefix(auth.Attributes["base_url"], "http://"), "/v1")
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: host}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, err = manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
				}
				h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.POST("/v1/responses", h.Responses)
				router.GET("/v1/responses", h.ResponsesWebsocket)
				server := httptest.NewServer(router)
				t.Cleanup(server.Close)
				for session, start := range []string{"agentrouter.org", "anyrouter.top"} {
					t.Run(fmt.Sprintf("session%d", session), func(t *testing.T) {
						t.Parallel()
						marker := fmt.Sprintf("conversation%d", session)
						var conn *websocket.Conn
						t.Cleanup(func() {
							if conn != nil {
								_ = conn.Close()
							}
						})
						other := "agentrouter.org"
						if start == other {
							other = "anyrouter.top"
						}
						history := []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"role":"user","content":%q}`, marker))}
						for step, host := range []string{start, start, other, other, start, other, start} {
							clientSession := marker
							if step >= 5 {
								clientSession += "_fork"
							}
							turn := fmt.Sprintf("%s_turn%d", clientSession, step)
							if step > 0 {
								history = append(history, json.RawMessage(fmt.Sprintf(`{"type":"message","id":"msg_user_%s","role":"user","content":%q}`, marker+"_"+turn, turn)))
							}
							before, _ := json.Marshal(history)
							payloadBody := map[string]any{"model": host, "stream": stream, "store": false, "prompt_cache_key": clientSession, "input": history, "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}, map[string]any{"type": "custom", "name": "apply_patch"}}}
							if transport == "websocket" {
								payloadBody["type"] = "response.create"
							}
							payload, _ := json.Marshal(payloadBody)
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", bytes.NewReader(payload))
							if errRequest != nil {
								cancel()
								t.Fatal(errRequest)
							}
							req.Header.Set("Content-Type", "application/json")
							req.Header.Set("Session_id", clientSession)
							req.Header.Set("Thread-Id", clientSession)
							req.Header.Set("X-Codex-Turn-Metadata", fmt.Sprintf(`{"prompt_cache_key":%q,"turn_id":%q}`, clientSession, turn))
							var raw []byte
							if transport == "websocket" {
								if step == 5 && conn != nil {
									_ = conn.Close()
									conn = nil
								}
								if conn == nil {
									var errDial error
									conn, _, errDial = websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", req.Header)
									if errDial != nil {
										cancel()
										t.Fatal(errDial)
									}
								}
								if errDeadline := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
									cancel()
									t.Fatal(errDeadline)
								}
								if errWrite := conn.WriteMessage(websocket.TextMessage, payload); errWrite != nil {
									cancel()
									t.Fatal(errWrite)
								}
								for {
									_, event, errRead := conn.ReadMessage()
									if errRead != nil {
										cancel()
										t.Fatalf("websocket continuation interrupted: step=%d err=%v body=%s", step, errRead, raw)
									}
									raw = append(raw, []byte("data: ")...)
									raw = append(raw, event...)
									raw = append(raw, '\n', '\n')
									typ := gjson.GetBytes(event, "type").String()
									if typ == "error" || typ == "response.failed" {
										cancel()
										t.Fatalf("websocket continuation failed: %s", event)
									}
									if typ == "response.completed" {
										break
									}
								}
							} else {
								resp, errDo := server.Client().Do(req)
								if errDo != nil {
									cancel()
									t.Fatal(errDo)
								}
								var errRead error
								raw, errRead = io.ReadAll(resp.Body)
								errClose := resp.Body.Close()
								if errRead != nil || errClose != nil || resp.StatusCode != http.StatusOK {
									cancel()
									t.Fatalf("continuation interrupted: host=%s step=%d status=%d read=%v close=%v body=%s", host, step, resp.StatusCode, errRead, errClose, raw)
								}
							}
							cancel()
							if strings.Contains(string(raw), "rejected_scaffolding") || strings.Contains(string(raw), "response.failed") {
								t.Fatalf("rejected attempt leaked to client: %s", raw)
							}
							completed := gjson.ParseBytes(raw)
							if stream {
								count := 0
								for _, line := range strings.Split(string(raw), "\n") {
									event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
									if event.Get("type").String() == "response.completed" {
										completed = event.Get("response")
										count++
									}
								}
								if count != 1 || strings.Count(string(raw), `"type":"response.output_item.done"`) != 6 {
									t.Fatalf("completion count=%d", count)
								}
							}
							if completed.Get("status").String() != "completed" || completed.Get("prompt_cache_key").String() != clientSession {
								t.Fatalf("completion or client session identity changed: %s", completed.Raw)
							}
							after, _ := json.Marshal(history)
							if !bytes.Equal(before, after) {
								t.Fatal("client history mutated")
							}
							var output []json.RawMessage
							if errUnmarshal := json.Unmarshal([]byte(completed.Get("output").Raw), &output); errUnmarshal != nil {
								t.Fatal(errUnmarshal)
							}
							if len(output) != 6 || gjson.GetBytes(output[2], "type").String() != "web_search_call" || gjson.GetBytes(output[5], "type").String() != "custom_tool_call" {
								t.Fatalf("native output contract changed: %s", completed.Raw)
							}
							history = append(history, output...)
							history = append(history, json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":%q}`, gjson.GetBytes(output[4], "call_id").String(), marker+" function result")), json.RawMessage(fmt.Sprintf(`{"type":"custom_tool_call_output","call_id":%q,"output":%q}`, gjson.GetBytes(output[5], "call_id").String(), marker+" patch result")))
							mu.Lock()
							requestTurn := turn
							want := 2
							if step == 0 {
								requestTurn, want = marker, 1
							}
							got := attempts[host+"/"+clientSession+"/"+requestTurn]
							mu.Unlock()
							if got != want {
								t.Fatalf("unexpected replay: host=%s step=%d calls=%d want=%d", host, step, got, want)
							}
						}
					})
				}
			})
		}
	}
}

func conversationSwitchOutput(marker, turn string) []any {
	id := marker + "_" + turn
	encrypted := base64.URLEncoding.EncodeToString(append([]byte{0x80}, make([]byte, 72)...))
	return []any{
		map[string]any{"type": "message", "id": "msg_" + id, "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": marker + " answer"}}},
		map[string]any{"type": "reasoning", "id": "rs_" + id, "encrypted_content": encrypted, "summary": []any{map[string]any{"type": "summary_text", "text": marker + " summary"}}},
		map[string]any{"type": "web_search_call", "id": "ws_" + id, "status": "completed", "action": map[string]any{"type": "search", "query": marker + " search"}},
		map[string]any{"type": "web_search_call", "id": "page_" + id, "status": "completed", "action": map[string]any{"type": "open_page", "url": "https://example.test/" + marker}, "title": marker + " page"},
		map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": "call_" + id, "name": "lookup", "arguments": "{}"},
		map[string]any{"type": "custom_tool_call", "id": "ct_" + id, "call_id": "patch_" + id, "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"},
	}
}

func writeConversationEvent(w http.ResponseWriter, event map[string]any) {
	body, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
}
