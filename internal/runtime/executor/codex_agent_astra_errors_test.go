package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAgentAstraReportedErrors(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"reasoning-content", "resource", "prefixed-resource", "masked-resource", "prefixed-masked-resource", "encrypted", "prefixed-encrypted"} {
			name := kind
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				reasoning := `{"type":"reasoning","id":"rs_foreign","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `","summary":[{"type":"summary_text","text":"portable summary"}]}`
				if kind == "reasoning-content" {
					reasoning = `{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"portable reasoning"}]}`
				}
				raw := []byte(`{"model":"gpt-6-astra","input":[` + reasoning + `,{"role":"user","content":"continue"},{"type":"function_call","name":"fixture","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"retained result"}]}`)
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					if !strings.HasSuffix(r.URL.Path, "/responses") {
						t.Errorf("wrong upstream protocol %s", r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer one-fixture-key" {
						t.Error("key changed")
					}
					message := ""
					if kind == "reasoning-content" && gjson.GetBytes(body, "input.0.content.#").Int() > 0 {
						message = "OpenAI Responses bad request: Invalid 'input[0].content': array too long. Expected an array with maximum length 0, but got an array with length 1 instead. [trace_id=fixture]"
					}
					if kind != "reasoning-content" && gjson.GetBytes(body, "input.0.encrypted_content").Exists() {
						if strings.Contains(kind, "resource") {
							message = "The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it."
						} else {
							message = "The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed."
						}
						if strings.Contains(kind, "masked") {
							message = strings.Replace(message, "Azure", "***", 1)
						}
						if strings.HasPrefix(kind, "prefixed") {
							message = "OpenAI Responses bad request: " + message
						}
						message += " [trace_id=fixture]"
					}
					if message != "" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(400)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": nil, "param": "", "message": message}})
						return
					}
					if !strings.Contains(string(body), "portable") || !strings.Contains(string(body), "retained result") {
						t.Error("portable history lost")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
				}))
				defer upstream.Close()
				a := &auth.Auth{ID: "single-agent-key", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "one-fixture-key", "base_url": "http://agentrouter.org/v1"}}
				opts := ex.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: stream}
				req := ex.Request{Model: "gpt-6-astra", Payload: raw}
				executor := NewCodexExecutor(&config.Config{})
				executeAgentAstraFixture(t, executor, a, req, opts)
				want := 2
				if kind == "reasoning-content" {
					want = 1
				}
				if calls != want {
					t.Fatalf("calls=%d want=%d", calls, want)
				}
			})
		}
	}
}

func TestAgentAstraResourceRecoveryDropsRouteBoundItemIDs(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if calls == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, agentResourceMismatchRejection)
					return
				}
				for _, item := range gjson.GetBytes(body, "input").Array() {
					typ := item.Get("type").String()
					role := item.Get("role").String()
					routeBoundID := typ == "reasoning" || (typ == "message" && role == "assistant") || typ == "function_call" || typ == "function_call_output" || typ == "custom_tool_call" || typ == "custom_tool_call_output" || typ == "web_search_call"
					if (routeBoundID && item.Get("id").Exists()) || item.Get("encrypted_content").Exists() {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, agentResourceMismatchRejection)
						return
					}
				}
				if gjson.GetBytes(body, "input.0.summary.0.text").String() != "keep summary one" ||
					gjson.GetBytes(body, "input.1.summary.0.text").String() != "keep summary two" ||
					gjson.GetBytes(body, "input.3.call_id").String() != "call_keep" ||
					gjson.GetBytes(body, "input.4.call_id").String() != "call_keep" ||
					gjson.GetBytes(body, "input.4.output").String() != "keep result" ||
					gjson.GetBytes(body, "input.5.type").String() != "message" ||
					!strings.Contains(gjson.GetBytes(body, "input.5.content.0.text").String(), `"id":"ws_keep"`) ||
					!strings.Contains(gjson.GetBytes(body, "input.5.content.0.text").String(), `"query":"keep query"`) ||
					gjson.GetBytes(body, "input.6.id").String() != "msg_current" ||
					gjson.GetBytes(body, "input.6.content").String() != "continue" {
					t.Fatalf("portable history changed: %s", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, completedHistoryResponse)
			}))
			defer upstream.Close()

			payload := []byte(`{"model":"gpt-6-astra","input":[` +
				`{"type":"reasoning","id":"rs_foreign_one","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `","summary":[{"type":"summary_text","text":"keep summary one"}]},` +
				`{"type":"reasoning","id":"rs_foreign_two","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `","summary":[{"type":"summary_text","text":"keep summary two"}]},` +
				`{"type":"message","id":"msg_foreign","role":"assistant","content":[{"type":"output_text","text":"keep answer"}]},` +
				`{"type":"function_call","id":"fc_foreign","call_id":"call_keep","name":"fixture","arguments":"{}"},` +
				`{"type":"function_call_output","id":"fco_foreign","call_id":"call_keep","output":"keep result"},` +
				`{"type":"web_search_call","id":"ws_keep","status":"completed","action":{"type":"search","query":"keep query"}},` +
				`{"type":"message","id":"msg_current","role":"user","content":"continue"}]}`)
			credential := &auth.Auth{ID: "agent-resource-fixture", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "one-fixture-key", "base_url": "http://agentrouter.org/v1"}}
			executeAgentAstraFixture(t, NewCodexExecutor(&config.Config{}), credential, ex.Request{Model: "gpt-6-astra", Payload: payload}, ex.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: stream})
			if calls != 2 {
				t.Fatalf("calls=%d, want one same-key portable retry", calls)
			}
		})
	}
}

func TestAgentAstraChatClientUsesResponsesUpstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if !strings.HasSuffix(r.URL.Path, "/responses") || gjson.GetBytes(body, "messages").Exists() {
					t.Errorf("Chat protocol leaked upstream: %s", r.URL.Path)
				}
				if gjson.GetBytes(body, "reasoning.effort").String() != "high" || gjson.GetBytes(body, `tools.#(name=="lookup").type`).String() != "function" {
					t.Errorf("tools/reasoning missing: %s", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, completedHistoryResponse)
			}))
			defer server.Close()
			a := &auth.Auth{ID: "agent-chat-client", Provider: "codex", ProxyURL: server.URL, Attributes: map[string]string{"api_key": "fixture", "base_url": "http://agentrouter.org/v1"}}
			payload := []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"lookup"}],"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]}`)
			e := NewCodexExecutor(&config.Config{})
			req := ex.Request{Model: "gpt-6-astra", Payload: payload}
			opts := ex.Options{SourceFormat: tr.FormatOpenAI, Stream: stream}
			executeAgentAstraFixture(t, e, a, req, opts)
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func executeAgentAstraFixture(t *testing.T, executor *CodexExecutor, credential *auth.Auth, req ex.Request, opts ex.Options) {
	t.Helper()
	if !opts.Stream {
		if _, err := executor.Execute(context.Background(), credential, req, opts); err != nil {
			t.Fatal(err)
		}
		return
	}
	result, err := executor.ExecuteStream(context.Background(), credential, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
}
