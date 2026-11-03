package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// supplierFallbackSelector exercises the route-aware path rather than the
// incremental built-in scheduler, while retaining the same fill-first semantics.
type supplierFallbackSelector struct{ coreauth.FillFirstSelector }

func TestProviderPriority_ResponsesExhaustSupplierBeforeFailover(t *testing.T) {
	for _, strategy := range []string{"fill-first", "round-robin", "weighted-round-robin", "route-aware"} {
		for _, stream := range []bool{false, true} {
			for failures := 0; failures <= 2; failures++ {
				t.Run(fmt.Sprintf("%s/stream=%t/failures=%d", strategy, stream, failures), func(t *testing.T) {
					var mu sync.Mutex
					var calls []string
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
						mu.Lock()
						calls = append(calls, key)
						mu.Unlock()
						if (key == "any-high" && failures >= 1) || (key == "any-low" && failures >= 2) {
							w.WriteHeader(http.StatusTooManyRequests)
							_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"fixture key cooling"}}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\",\"output_index\":0,\"content_index\":0}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_priority\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
					}))
					defer upstream.Close()
					anyHigh, anyLow, agentHigh := 9, -50, 100
					cfg := &config.Config{CodexKey: []config.CodexKey{
						{Name: "Any", Priority: 10, BaseURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "any-high", Priority: &anyHigh}, {APIKey: "any-low", Priority: &anyLow}}},
						{Name: "AgentRouter", Priority: 9, BaseURL: upstream.URL, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "agent-high", Priority: &agentHigh}}},
					}}
					auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
					if err != nil {
						t.Fatal(err)
					}
					var selector coreauth.Selector
					switch strategy {
					case "fill-first":
						selector = &coreauth.FillFirstSelector{}
					case "round-robin":
						selector = &coreauth.RoundRobinSelector{}
					case "weighted-round-robin":
						selector = &coreauth.WeightedRoundRobinSelector{}
					default:
						selector = &supplierFallbackSelector{}
					}
					manager := coreauth.NewManager(nil, selector, nil)
					manager.SetConfig(cfg)
					manager.SetRetryConfig(0, 0, 0)
					manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
					for _, auth := range auths {
						registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "cpa-6a"}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
						if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
							t.Fatal(errRegister)
						}
					}
					req := cliproxyexecutor.Request{Model: "cpa-6a", Payload: []byte(`{"model":"cpa-6a","input":[{"role":"user","content":"OK"}],"stream":true}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}
					if stream {
						result, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
						if errExecute != nil {
							t.Fatal(errExecute)
						}
						var output strings.Builder
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							output.Write(chunk.Payload)
						}
						if !strings.Contains(output.String(), "response.completed") {
							t.Fatalf("missing terminal event: %s", output.String())
						}
					} else if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					want := []string{"any-high", "any-low", "agent-high"}[:failures+1]
					mu.Lock()
					defer mu.Unlock()
					if !reflect.DeepEqual(calls, want) {
						t.Fatalf("route attempts = %v, want %v", calls, want)
					}
				})
			}
		}
	}
}
