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
		for _, kind := range []string{"reasoning-content", "resource", "prefixed-resource", "encrypted", "prefixed-encrypted"} {
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
