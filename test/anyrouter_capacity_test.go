package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// The proxy fixture preserves the actual provider URL without using the network.
func TestAnyRouterChannelCapacityDoesNotBlockOtherSessions(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			capacityKeys := []string{}
			streamCapacityKeys := []string{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				fixtureSession := ""
				if metadata, ok := body["metadata"].(map[string]any); ok {
					fixtureSession, _ = metadata["fixture_session"].(string)
				}
				session := fixtureSession
				mu.Lock()
				calls[session]++
				if session == "capacity-limited-session" {
					capacityKeys = append(capacityKeys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				}
				if session == "capacity-stream-session" {
					streamCapacityKeys = append(streamCapacityKeys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				}
				mu.Unlock()
				if session == "committed-session" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible output\"}\n\n")
					fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"get_channel_failed\",\"message\":\"model at capacity\"}}}\n\n")
					return
				}
				if session == "capacity-limited-session" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(500)
					fmt.Fprint(w, `{"error":{"message":"The model has reached its capacity limit","type":"new_api_error","code":"get_channel_failed"}}`)
					return
				}
				if session == "capacity-stream-session" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"get_channel_failed\",\"message\":\"model at capacity\"}}}\n\n")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			defer upstream.Close()
			cfg := &config.Config{CodexKey: []config.CodexKey{{Name: "Any", BaseURL: "http://anyrouter.top/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "fixture-key-one"}, {APIKey: "fixture-key-two"}}}}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(2, 0, 30)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			for _, a := range auths {
				registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "cpa-6a"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				if _, err = manager.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
			execute := func(session string) (string, error) {
				body := []byte(fmt.Sprintf(`{"model":"cpa-6a","input":[{"role":"user","content":[{"type":"input_text","text":"Reply OK."}]}],"store":false,"stream":%t,"prompt_cache_key":%q,"metadata":{"fixture_session":%q}}`, stream, session, session))
				req := cliproxyexecutor.Request{Model: "cpa-6a", Payload: body}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: body, Stream: stream}
				if !stream {
					r, e := manager.Execute(context.Background(), []string{"codex"}, req, opts)
					return string(r.Payload), e
				}
				result, e := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				if e != nil {
					return "", e
				}
				var out strings.Builder
				for ch := range result.Chunks {
					if ch.Err != nil {
						return out.String(), ch.Err
					}
					out.Write(ch.Payload)
				}
				return out.String(), nil
			}
			for attempt := 0; attempt < 2; attempt++ {
				_, err = execute("capacity-limited-session")
				if err == nil || !strings.Contains(err.Error(), "get_channel_failed") {
					t.Fatalf("capacity request %d: got %v, want original upstream channel error", attempt, err)
				}
				var status cliproxyexecutor.StatusError
				if !errors.As(err, &status) || status.StatusCode() != 500 {
					t.Fatalf("lost original upstream status: %v", err)
				}
				mu.Lock()
				keys := append([]string(nil), capacityKeys[attempt*2:]...)
				mu.Unlock()
				if len(keys) != 2 || keys[0] == keys[1] {
					t.Fatalf("expected each credential exactly once: %v", keys)
				}
				output, err := execute("healthy-session")
				if err != nil || !strings.Contains(output, "OK") {
					t.Fatalf("a different session sharing these keys should still work: error=%v output=%s", err, output)
				}
			}
			_, err = execute("capacity-stream-session")
			if err == nil || !strings.Contains(err.Error(), "get_channel_failed") {
				t.Fatalf("stream capacity request: got %v, want original upstream channel error", err)
			}
			var streamStatus cliproxyexecutor.StatusError
			if !errors.As(err, &streamStatus) || streamStatus.StatusCode() != 502 {
				t.Fatalf("stream capacity status = %v, want 502 after SSE failure", err)
			}
			mu.Lock()
			streamKeys := append([]string(nil), streamCapacityKeys...)
			mu.Unlock()
			if len(streamKeys) != 2 || streamKeys[0] == streamKeys[1] {
				t.Fatalf("expected SSE capacity to try each credential once: %v", streamKeys)
			}
			output, err := execute("healthy-after-stream-capacity")
			if err != nil || !strings.Contains(output, "OK") {
				t.Fatalf("SSE capacity failure cooled shared credentials: error=%v output=%s", err, output)
			}
			if stream {
				output, err := execute("committed-session")
				if err == nil || !strings.Contains(output, "visible output") {
					t.Fatalf("expected a terminal error after visible output: error=%v output=%s", err, output)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if calls["capacity-limited-session"] != 4 {
				t.Fatalf("unbounded or missing credential rotation: calls=%v", calls)
			}
			if stream && calls["committed-session"] != 1 {
				t.Fatalf("replayed a committed stream: calls=%v", calls)
			}
			if calls["healthy-session"] != 2 {
				t.Fatalf("healthy session calls=%v", calls)
			}
			if calls["capacity-stream-session"] != 2 || calls["healthy-after-stream-capacity"] != 1 {
				t.Fatalf("SSE capacity calls=%v", calls)
			}
		})
	}
}

func TestCodexProviderSwitchUsesProviderScopedIdentity(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			type observedRequest struct {
				promptCacheKey string
				sessionHeader  string
				metadata       map[string]any
			}

			var mu sync.Mutex
			observed := map[string]observedRequest{}
			newUpstream := func(provider string) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					key, _ := body["prompt_cache_key"].(string)
					metadata, _ := body["client_metadata"].(map[string]any)
					request := observedRequest{
						promptCacheKey: key,
						sessionHeader:  r.Header.Get("Session_id"),
						metadata:       metadata,
					}
					mu.Lock()
					if provider == "agent" {
						observed[provider] = request
					} else {
						observed[provider] = request
					}
					agentKey := observed["agent"].promptCacheKey
					mu.Unlock()

					if provider == "any" && (key == "switch-session" || key == agentKey) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprint(w, `{"error":{"code":"get_channel_failed","message":"model at capacity"}}`)
						return
					}

					response := map[string]any{
						"type": "response.completed",
						"response": map[string]any{
							"id":               "resp_" + provider,
							"object":           "response",
							"status":           "completed",
							"prompt_cache_key": key,
							"client_metadata":  metadata,
							"output": []map[string]any{{
								"type": "message",
								"role": "assistant",
								"content": []map[string]any{{
									"type": "output_text",
									"text": "OK_" + provider,
								}},
							}},
						},
					}
					payload, err := json.Marshal(response)
					if err != nil {
						t.Error(err)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", payload)
				}))
			}
			agentUpstream := newUpstream("agent")
			defer agentUpstream.Close()
			anyUpstream := newUpstream("any")
			defer anyUpstream.Close()

			cfg := &config.Config{CodexKey: []config.CodexKey{
				{
					Name:     "Any",
					BaseURL:  "http://anyrouter.top/v1",
					ProxyURL: anyUpstream.URL,
					Models:   []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}},
					APIKeyEntries: []config.NativeAPIKeyEntry{{
						APIKey: "fixture-any-key",
					}},
				},
				{
					Name:     "AgentRouter",
					BaseURL:  "http://agentrouter.org/v1",
					ProxyURL: agentUpstream.URL,
					Models:   []config.CodexModel{{Name: "gpt-5.6-sol", Alias: "cpa-5.6s"}},
					APIKeyEntries: []config.NativeAPIKeyEntry{{
						APIKey: "fixture-agent-key",
					}},
				},
			}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
				Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
			})
			if err != nil {
				t.Fatal(err)
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 30)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			for _, auth := range auths {
				modelID := "cpa-6a"
				if strings.EqualFold(auth.Label, "AgentRouter") {
					modelID = "cpa-5.6s"
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: modelID}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, err = manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}

			execute := func(model string) string {
				body := []byte(fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":[{"type":"input_text","text":"Reply OK."}]}],"store":false,"stream":%t,"prompt_cache_key":"switch-session","client_metadata":{"x-codex-installation-id":"install-switch","session_id":"switch-session","thread_id":"thread-switch","turn_id":"turn-switch","x-codex-window-id":"switch-session:10","x-codex-turn-metadata":"{\"prompt_cache_key\":\"switch-session\",\"turn_id\":\"turn-switch\",\"window_id\":\"switch-session:10\"}"}}`, model, stream))
				request := cliproxyexecutor.Request{Model: model, Payload: body}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: body, Stream: stream}
				if stream {
					result, executeErr := manager.ExecuteStream(context.Background(), []string{"codex"}, request, opts)
					if executeErr != nil {
						t.Fatalf("%s stream execute error: %v", model, executeErr)
					}
					var output strings.Builder
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("%s stream chunk error: %v", model, chunk.Err)
						}
						output.Write(chunk.Payload)
					}
					return output.String()
				}
				response, executeErr := manager.Execute(context.Background(), []string{"codex"}, request, opts)
				if executeErr != nil {
					t.Fatalf("%s execute error: %v", model, executeErr)
				}
				return string(response.Payload)
			}

			agentOutput := execute("cpa-5.6s")
			anyOutput := execute("cpa-6a")
			if !strings.Contains(agentOutput, "OK_agent") || !strings.Contains(anyOutput, "OK_any") {
				t.Fatalf("provider outputs: agent=%q any=%q", agentOutput, anyOutput)
			}
			mu.Lock()
			agentRequest := observed["agent"]
			anyRequest := observed["any"]
			mu.Unlock()
			if agentRequest.promptCacheKey == "" || anyRequest.promptCacheKey == "" {
				t.Fatalf("missing provider prompt cache keys: agent=%#v any=%#v", agentRequest, anyRequest)
			}
			if agentRequest.promptCacheKey == "switch-session" || anyRequest.promptCacheKey == "switch-session" {
				t.Fatalf("raw client session leaked upstream: agent=%q any=%q", agentRequest.promptCacheKey, anyRequest.promptCacheKey)
			}
			if agentRequest.promptCacheKey == anyRequest.promptCacheKey {
				t.Fatalf("provider identities are shared: %q", agentRequest.promptCacheKey)
			}
			if agentRequest.sessionHeader != agentRequest.promptCacheKey || anyRequest.sessionHeader != anyRequest.promptCacheKey {
				t.Fatalf("Session_id mismatch: agent=%q/%q any=%q/%q", agentRequest.sessionHeader, agentRequest.promptCacheKey, anyRequest.sessionHeader, anyRequest.promptCacheKey)
			}
			for provider, output := range map[string]string{"agent": agentOutput, "any": anyOutput} {
				if !strings.Contains(output, "switch-session") || !strings.Contains(output, "install-switch") || !strings.Contains(output, "switch-session:10") {
					t.Fatalf("%s response did not restore client identifiers: %q", provider, output)
				}
				if strings.Contains(output, observed[provider].promptCacheKey) {
					t.Fatalf("%s response still exposes provider identity %q: %q", provider, observed[provider].promptCacheKey, output)
				}
			}
		})
	}
}
