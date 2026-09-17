package test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

const agentBudgetPoolError = `{"error":{"message":"Budget pool quota has been exhausted. Please ask an administrator to increase the limit or select another budget pool.","type":"bad_response_status_code","param":"","code":"bad_response_status_code"}}`

func TestAgentBudgetPoolFailureDoesNotBlockRecoveredUpstream(t *testing.T) {
	for _, tc := range []struct {
		name, model, upstreamModel string
		format                     sdktranslator.Format
	}{
		{"sol-responses", "cpa-5.6s", "gpt-5.6-sol", sdktranslator.FormatOpenAIResponse},
		{"sol-chat", "cpa-5.6s", "gpt-5.6-sol", sdktranslator.FormatOpenAI},
		{"astra-responses", "cpa-6a", "gpt-6-astra", sdktranslator.FormatOpenAIResponse},
		{"astra-chat", "cpa-6a", "gpt-6-astra", sdktranslator.FormatOpenAI},
	} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				var calls atomic.Int32
				var recovered atomic.Bool
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
					}
					if r.URL.Path != "/v1/responses" || r.Host != "agentrouter.org" || r.Header.Get("Authorization") != "Bearer fixture-agent-budget-key" || gjson.GetBytes(body, "model").String() != tc.upstreamModel {
						t.Error("request did not preserve the selected Agent route, key and alias")
					}
					// Agent returned a JSON error with this content type in the captured 402.
					w.Header().Set("Content-Type", "text/event-stream")
					if !recovered.Load() {
						w.WriteHeader(http.StatusPaymentRequired)
						_, _ = io.WriteString(w, agentBudgetPoolError)
						return
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\",\"output_index\":0,\"content_index\":0}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
				}))
				defer upstream.Close()
				cfg := &config.Config{CodexKey: []config.CodexKey{{Name: "AgentRouter", BaseURL: "http://agentrouter.org/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{{Name: tc.upstreamModel, Alias: tc.model}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "fixture-agent-budget-key"}}}}}
				auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil || len(auths) != 1 {
					t.Fatalf("synthesize Agent: auths=%d err=%v", len(auths), err)
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(2, 30*time.Second, 1)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				registry.GetGlobalRegistry().RegisterClient(auths[0].ID, "codex", []*registry.ModelInfo{{ID: tc.model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auths[0].ID) })
				if _, err = manager.Register(context.Background(), auths[0]); err != nil {
					t.Fatal(err)
				}
				execute := func() (string, error) {
					return executeBudgetPoolTestRequest(manager, tc.format, tc.model, stream)
				}
				_, err = execute()
				var status cliproxyexecutor.StatusError
				if !errors.As(err, &status) || status.StatusCode() != http.StatusPaymentRequired || !strings.Contains(err.Error(), agentBudgetPoolError) {
					t.Fatalf("expected original budget pool 402, got %v", err)
				}
				if calls.Load() != 1 {
					t.Fatalf("budget failure replayed the selected key: calls=%d", calls.Load())
				}
				_, err = execute()
				if !errors.As(err, &status) || status.StatusCode() != http.StatusPaymentRequired || !strings.Contains(err.Error(), agentBudgetPoolError) || calls.Load() != 2 {
					t.Fatalf("second request lost upstream error or did not reach the same key: calls=%d err=%v", calls.Load(), err)
				}
				recovered.Store(true)
				output, err := execute()
				if err != nil || !strings.Contains(output, "OK") {
					t.Fatalf("CPA blocked the recovered upstream with stale credential cooldown: err=%v output=%s", err, output)
				}
				if calls.Load() != 3 {
					t.Fatalf("next request did not reach the same key: calls=%d", calls.Load())
				}
			})
		}
	}
}

func executeBudgetPoolTestRequest(manager *coreauth.Manager, format sdktranslator.Format, model string, stream bool) (string, error) {
	body := []byte(fmt.Sprintf(`{"model":%q,"input":"Reply only OK.","stream":%t,"store":false}`, model, stream))
	if format == sdktranslator.FormatOpenAI {
		body = []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Reply only OK."}],"stream":%t}`, model, stream))
	}
	req := cliproxyexecutor.Request{Model: model, Payload: body}
	opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: body, Stream: stream}
	if !stream {
		response, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, opts)
		return string(response.Payload), errExecute
	}
	response, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
	if errExecute != nil {
		return "", errExecute
	}
	var output strings.Builder
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			return output.String(), chunk.Err
		}
		output.Write(chunk.Payload)
	}
	return output.String(), nil
}

func TestAgentBudgetPoolRotationRemainsBounded(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for _, tc := range []struct {
			name          string
			limit         int
			healthyBackup bool
		}{
			{"all budget pools unavailable", 2, false},
			{"healthy backup", 2, true},
			{"credential limit", 1, true},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				var mu sync.Mutex
				var keys []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					mu.Lock()
					keys = append(keys, key)
					mu.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					if key == "budget-key-one" || !tc.healthyBackup {
						w.WriteHeader(http.StatusPaymentRequired)
						_, _ = io.WriteString(w, agentBudgetPoolError)
						return
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_backup\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
				}))
				defer upstream.Close()
				priority := 10
				cfg := &config.Config{CodexKey: []config.CodexKey{{Name: "AgentRouter", BaseURL: "http://agentrouter.org/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-5.6-sol", Alias: "cpa-5.6s"}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "budget-key-one", Priority: &priority}, {APIKey: "budget-key-two"}}}}}
				auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil {
					t.Fatal(err)
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(3, 30*time.Second, tc.limit)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				for _, a := range auths {
					registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "cpa-5.6s"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
					if _, err := manager.Register(context.Background(), a); err != nil {
						t.Fatal(err)
					}
				}
				for request := 0; request < 2; request++ {
					output, err := executeBudgetPoolTestRequest(manager, sdktranslator.FormatOpenAIResponse, "cpa-5.6s", stream)
					if tc.healthyBackup && tc.limit == 2 {
						if err != nil || !strings.Contains(output, "OK") {
							t.Fatalf("healthy fallback not used: err=%v output=%s", err, output)
						}
					} else {
						var status cliproxyexecutor.StatusError
						if !errors.As(err, &status) || status.StatusCode() != http.StatusPaymentRequired || !strings.Contains(err.Error(), agentBudgetPoolError) {
							t.Fatalf("original budget error lost: %v", err)
						}
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if len(keys) != tc.limit*2 {
					t.Fatalf("credential retry limit changed: keys=%v", keys)
				}
				for i, key := range keys {
					want := "budget-key-one"
					if tc.limit == 2 && i%2 == 1 {
						want = "budget-key-two"
					}
					if key != want {
						t.Fatalf("credential retried or skipped: keys=%v", keys)
					}
				}
			})
		}
	}
}
