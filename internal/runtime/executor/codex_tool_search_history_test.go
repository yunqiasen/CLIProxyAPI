package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexAnyToolSearchHistoryCompatibility(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "future-model"} {
		for _, stream := range []bool{false, true} {
			name := model + "/http"
			if stream {
				name = model + "/sse"
			}
			t.Run(name, func(t *testing.T) {
				original := []byte(`{"model":"` + model + `","store":false,"input":[{"type":"tool_search_call","id":"tsc_old","call_id":"call_search","execution":"client","status":"completed","arguments":{"query":"find tasks"}},{"type":"tool_search_output","id":"tso_old","call_id":"call_search","execution":"client","status":"completed","tools":[{"type":"namespace","name":"fixture","tools":[{"type":"function","name":"lookup"}]}]},{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_lookup","output":"preserved tool"},{"role":"user","content":"continue"}]}`)
				before := bytes.Clone(original)
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					if strings.Contains(string(body), `"type":"tool_search_call"`) || strings.Contains(string(body), `"type":"tool_search_output"`) {
						w.WriteHeader(400)
						_, _ = io.WriteString(w, `{"error":{"type":"new_api_error","code":"invalid_responses_request","message":"invalid codex request"}}`)
						return
					}
					if gjson.GetBytes(body, "input.#").Int() != 5 || !strings.Contains(gjson.GetBytes(body, "input.0.content.0.text").String(), "find tasks") || !strings.Contains(gjson.GetBytes(body, "input.1.content.0.text").String(), "lookup") || gjson.GetBytes(body, "input.2.call_id").String() != "call_lookup" || gjson.GetBytes(body, "input.3.output").String() != "preserved tool" {
						t.Error("history or tool call pairing changed")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, completedHistoryResponse)
				}))
				defer upstream.Close()
				auth := &coreauth.Auth{ID: "any-tool-discovery", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "fixture-key", "base_url": "http://anyrouter.top/v1"}}
				req := coreexecutor.Request{Model: model, Payload: original}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
				e := NewCodexExecutor(&config.Config{})
				if stream {
					res, err := e.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
						t.Fatal(err)
					}
				}
				if calls != 1 || !bytes.Equal(original, before) {
					t.Fatalf("calls=%d or caller data mutated", calls)
				}
			})
		}
	}
}
