package test

import (
	"context"
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
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const agentResourceSwitchRejection = `{"error":{"type":"invalid_request_error","code":null,"param":"","message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`

func TestAgentRateLimitSwitchKeepsPortableConversation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				calls[key]++
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				if key == "rate-limited-key" {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_limited\",\"status\":\"in_progress\"}}\n\n")
					_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"too_many_requests\",\"code\":\"rate_limit_exceeded\",\"message\":\"Your requests to gpt-6-astra for gpt-6-astra in eastus2 have exceeded rate limit.\"}}\n\n")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_limited\",\"status\":\"failed\",\"error\":{\"type\":\"too_many_requests\",\"code\":\"rate_limit_exceeded\",\"message\":\"Your requests to gpt-6-astra for gpt-6-astra in eastus2 have exceeded rate limit.\"}}}\n\n")
					return
				}
				for _, item := range gjson.GetBytes(body, "input").Array() {
					typ := item.Get("type").String()
					role := item.Get("role").String()
					routeBoundID := typ == "reasoning" || (typ == "message" && role == "assistant") || typ == "function_call" || typ == "function_call_output" || typ == "custom_tool_call" || typ == "custom_tool_call_output"
					if (routeBoundID && item.Get("id").Exists()) || item.Get("encrypted_content").Exists() {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, agentResourceSwitchRejection)
						return
					}
				}
				if gjson.GetBytes(body, "input.0.summary.0.text").String() != "keep summary one" ||
					gjson.GetBytes(body, "input.1.summary.0.text").String() != "keep summary two" ||
					gjson.GetBytes(body, "input.3.call_id").String() != "call_keep" ||
					gjson.GetBytes(body, "input.4.output").String() != "keep result" ||
					gjson.GetBytes(body, "input.5.id").String() != "ws_keep" ||
					gjson.GetBytes(body, "input.6.id").String() != "msg_current" {
					t.Errorf("portable history changed: %s", body)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\",\"output_index\":0,\"content_index\":0}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			defer upstream.Close()

			highPriority := 10
			lowPriority := 9
			cfg := &config.Config{CodexKey: []config.CodexKey{{
				Name: "AgentRouter", BaseURL: "http://agentrouter.org/v1", ProxyURL: upstream.URL,
				Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}},
				APIKeyEntries: []config.NativeAPIKeyEntry{
					{APIKey: "rate-limited-key", Priority: &highPriority},
					{APIKey: "portable-recovery-key", Priority: &lowPriority},
				},
			}}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil || len(auths) != 2 {
				t.Fatalf("synthesize Agent fixture: auths=%d err=%v", len(auths), err)
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(2, 0, 2)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			for _, auth := range auths {
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "cpa-6a"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, err = manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}

			payload := []byte(`{"model":"cpa-6a","store":false,"stream":` + fmt.Sprint(stream) + `,"prompt_cache_key":"same-conversation","input":[` +
				`{"type":"reasoning","id":"rs_foreign_one","encrypted_content":"opaque-state-one","summary":[{"type":"summary_text","text":"keep summary one"}]},` +
				`{"type":"reasoning","id":"rs_foreign_two","encrypted_content":"opaque-state-two","summary":[{"type":"summary_text","text":"keep summary two"}]},` +
				`{"type":"message","id":"msg_foreign","role":"assistant","content":[{"type":"output_text","text":"keep answer"}]},` +
				`{"type":"function_call","id":"fc_foreign","call_id":"call_keep","name":"fixture","arguments":"{}"},` +
				`{"type":"function_call_output","id":"fco_foreign","call_id":"call_keep","output":"keep result"},` +
				`{"type":"web_search_call","id":"ws_keep","status":"completed","action":{"type":"search","query":"keep query"}},` +
				`{"type":"message","id":"msg_current","role":"user","content":"continue"}]}`)
			req := cliproxyexecutor.Request{Model: "cpa-6a", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: stream}
			var output string
			if stream {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				var collected strings.Builder
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					collected.Write(chunk.Payload)
				}
				output = collected.String()
			} else {
				response, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				output = string(response.Payload)
			}
			if !strings.Contains(output, "OK") {
				t.Fatalf("conversation did not continue: %s", output)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls["rate-limited-key"] != 1 || calls["portable-recovery-key"] != 2 {
				t.Fatalf("unexpected credential attempts: %v", calls)
			}
		})
	}
}
