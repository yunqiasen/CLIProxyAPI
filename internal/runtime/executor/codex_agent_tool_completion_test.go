package executor

import (
	"context"
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

// This fixture follows the event sequence captured from Agent's forced-tool route:
// a complete function call, but no response lifecycle or response.completed event.
const agentCompletedFunctionWithoutTerminal = `data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_echo","call_id":"call_echo","name":"cpa_echo","arguments":"","status":"in_progress"}}

data: {"type":"response.function_call_arguments.delta","sequence_number":1,"output_index":0,"item_id":"fc_echo","delta":"{\"text\":"}

data: {"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_echo","delta":"\"OK\"}"}

data: {"type":"response.function_call_arguments.done","sequence_number":3,"output_index":0,"item_id":"fc_echo","arguments":"{\"text\":\"OK\"}"}

data: {"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"type":"function_call","id":"fc_echo","call_id":"call_echo","name":"cpa_echo","arguments":"{\"text\":\"OK\"}","status":"completed"}}

`

func TestAgentAstraCompletedToolRecoversMissingTerminal(t *testing.T) {
	for _, format := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", format, stream), func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					if r.Header.Get("Authorization") != "Bearer selected-key" || r.URL.Path != "/v1/responses" {
						t.Error("wrong credential or upstream protocol")
					}
					if gjson.GetBytes(body, "tool_choice.name").String() != "cpa_echo" || gjson.GetBytes(body, "reasoning.effort").String() != "high" {
						t.Errorf("tool choice or reasoning changed: %s", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, agentCompletedFunctionWithoutTerminal)
				}))
				defer upstream.Close()
				credential := &auth.Auth{ID: "agent-tool", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "selected-key", "base_url": "http://agentrouter.org/v1"}}
				payload := []byte(`{"model":"gpt-6-astra","input":"Call cpa_echo","reasoning":{"effort":"high"},"tools":[{"type":"function","name":"cpa_echo","parameters":{"type":"object","properties":{"text":{"type":"string"}}}}],"tool_choice":{"type":"function","name":"cpa_echo"}}`)
				if format == tr.FormatOpenAI {
					payload = []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"Call cpa_echo"}],"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"cpa_echo","parameters":{"type":"object","properties":{"text":{"type":"string"}}}}}],"tool_choice":{"type":"function","function":{"name":"cpa_echo"}}}`)
				}
				e := NewCodexExecutor(&config.Config{})
				opts := ex.Options{SourceFormat: format, Stream: stream}
				req := ex.Request{Model: "gpt-6-astra", Payload: payload}
				if !stream {
					result, err := e.Execute(context.Background(), credential, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					path := "output.0.arguments"
					if format == tr.FormatOpenAI {
						path = "choices.0.message.tool_calls.0.function.arguments"
						if gjson.GetBytes(result.Payload, "choices.0.finish_reason").String() != "tool_calls" {
							t.Fatalf("Chat tool completion missing: %s", result.Payload)
						}
					} else if gjson.GetBytes(result.Payload, "status").String() != "completed" {
						t.Fatalf("Responses completion missing: %s", result.Payload)
					}
					if gjson.GetBytes(result.Payload, path).String() != `{"text":"OK"}` {
						t.Fatalf("tool arguments changed: %s", result.Payload)
					}
				} else {
					result, err := e.ExecuteStream(context.Background(), credential, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					terminalCount := 0
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						data := strings.TrimSpace(strings.TrimPrefix(string(chunk.Payload), "data:"))
						if format == tr.FormatOpenAIResponse && gjson.Get(data, "type").String() == "response.completed" {
							terminalCount++
							if gjson.Get(data, "response.output.0.arguments").String() != `{"text":"OK"}` || gjson.Get(data, "response.usage").Type != gjson.Null {
								t.Fatalf("terminal lost output or invented usage: %s", data)
							}
						}
						if format == tr.FormatOpenAI && gjson.Get(data, "choices.0.finish_reason").String() == "tool_calls" {
							terminalCount++
						}
					}
					if terminalCount != 1 {
						t.Fatalf("terminal count=%d, want 1", terminalCount)
					}
				}
				if calls != 1 {
					t.Fatalf("completed tools replayed: calls=%d", calls)
				}
			})
		}
	}
}
