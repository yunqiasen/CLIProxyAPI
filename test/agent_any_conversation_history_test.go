package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// The client keeps the output of a successful Agent turn, performs a local tool,
// continues on Agent, then forwards that same stored history to Any.
func TestAgentToAnyConversationRetainsGeneratedSearchHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			sessions := map[string]string{}
			firstOutput := json.RawMessage(`[
				{"type":"message","id":"msg_agent_first","role":"assistant","content":[{"type":"output_text","text":"preserved first answer"}]},
				{"type":"web_search_call","id":"ws_agent_search","status":"completed","action":{"type":"search","query":"preserved search query","queries":["preserved search query"]}},
				{"type":"web_search_call","id":"ws_agent_page","status":"completed","action":{"type":"open_page","url":"https://example.test/preserved-source"}},
				{"type":"function_call","id":"fc_agent","name":"lookup","call_id":"call_preserved","arguments":"{}"}
			]`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host := r.URL.Hostname()
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if host != "agentrouter.org" && host != "anyrouter.top" {
					t.Errorf("unexpected upstream: %s", host)
				}
				if r.Header.Get("Authorization") != "Bearer "+host+"-selected-key" {
					t.Error("request changed the selected credential")
				}
				mu.Lock()
				calls[host]++
				session := r.Header.Get("Session_id")
				if session == "" || (sessions[host] != "" && sessions[host] != session) {
					t.Errorf("session changed during the same conversation on %s", host)
				}
				sessions[host] = session
				mu.Unlock()
				items := gjson.GetBytes(body, "input").Array()
				if len(items) == 1 && strings.Contains(items[0].Raw, "start the conversation") {
					writeResourceHistoryCompletion(w, firstOutput)
					return
				}
				bound := false
				for _, item := range items {
					typ := item.Get("type").String()
					bound = bound || (item.Get("id").Exists() && (typ == "web_search_call" || typ == "function_call" || (typ == "message" && item.Get("role").String() == "assistant")))
				}
				if bound {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					if host == "agentrouter.org" {
						_, _ = io.WriteString(w, agentResourceSwitchRejection)
					} else {
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","param":"","message":"bad response status code 400 (request id: 20260920121127464443788CqZ9dieq)"}}`)
					}
					return
				}
				for _, retained := range []string{"preserved first answer", "preserved search query", "https://example.test/preserved-source", "call_preserved", "preserved tool result", "msg_client_owned"} {
					if !strings.Contains(string(body), retained) {
						t.Errorf("%s lost conversation data: %s", host, retained)
					}
				}
				for _, item := range items {
					if item.Get("type").String() == "web_search_call" {
						t.Errorf("%s retained a rejected native search reference", host)
					}
				}
				output, _ := json.Marshal([]any{map[string]any{"type": "message", "id": "msg_" + host, "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "continued on " + host}}}})
				writeResourceHistoryCompletion(w, output)
			}))
			defer upstream.Close()

			cfg := &config.Config{CodexKey: []config.CodexKey{
				{Name: "AgentRouter", BaseURL: "http://agentrouter.org/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "agent-history"}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "agentrouter.org-selected-key"}}},
				{Name: "Any", BaseURL: "http://anyrouter.top/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "any-history"}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "anyrouter.top-selected-key"}}},
			}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil || len(auths) != 2 {
				t.Fatalf("synthesize routes: count=%d err=%v", len(auths), err)
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			for _, auth := range auths {
				alias := "agent-history"
				if strings.Contains(auth.Attributes["base_url"], "anyrouter.top") {
					alias = "any-history"
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: alias}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, err = manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			execute := func(alias string, history []json.RawMessage) []json.RawMessage {
				t.Helper()
				payload, errMarshal := json.Marshal(map[string]any{"model": alias, "store": false, "stream": stream, "prompt_cache_key": "generated-history-" + fmt.Sprint(stream), "input": history})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				req := coreexecutor.Request{Model: alias, Payload: payload}
				opts := coreexecutor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: payload, Stream: stream}
				var completed gjson.Result
				if stream {
					result, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						for _, line := range strings.Split(string(chunk.Payload), "\n") {
							event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
							if event.Get("type").String() == "response.completed" {
								completed = event.Get("response")
							}
						}
					}
				} else {
					result, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					completed = gjson.ParseBytes(result.Payload)
				}
				if completed.Get("status").String() != "completed" {
					t.Fatalf("%s did not complete: %s", alias, completed.Raw)
				}
				var output []json.RawMessage
				if err = json.Unmarshal([]byte(completed.Get("output").Raw), &output); err != nil {
					t.Fatal(err)
				}
				return output
			}

			history := []json.RawMessage{json.RawMessage(`{"role":"user","content":"start the conversation"}`)}
			generated := execute("agent-history", history)
			if len(generated) != 4 || gjson.GetBytes(generated[1], "type").String() != "web_search_call" || gjson.GetBytes(generated[2], "action.type").String() != "open_page" {
				t.Fatal("successful native Agent search output was rewritten")
			}
			history = append(history, generated...)
			history = append(history, json.RawMessage(`{"type":"function_call_output","call_id":"call_preserved","output":"preserved tool result"}`), json.RawMessage(`{"type":"message","id":"msg_client_owned","role":"user","content":"continue on Agent"}`))
			history = append(history, execute("agent-history", history)...)
			if gjson.GetBytes(history[2], "id").String() != "ws_agent_search" || gjson.GetBytes(history[3], "id").String() != "ws_agent_page" {
				t.Fatal("recovery mutated the client's stored search history")
			}
			history = append(history, json.RawMessage(`{"role":"user","content":"continue on Any"}`))
			output := execute("any-history", history)
			if !strings.Contains(string(output[0]), "continued on anyrouter.top") {
				t.Fatal("the Agent-to-Any conversation did not complete")
			}
			mu.Lock()
			defer mu.Unlock()
			if calls["agentrouter.org"] != 3 || calls["anyrouter.top"] != 2 {
				t.Fatalf("unbounded replay or route/key rotation: %v", calls)
			}
		})
	}
}

func writeResourceHistoryCompletion(w http.ResponseWriter, output json.RawMessage) {
	w.Header().Set("Content-Type", "text/event-stream")
	body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "status": "completed", "output": output}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
}
