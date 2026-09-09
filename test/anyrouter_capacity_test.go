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
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				session, _ := body["prompt_cache_key"].(string)
				mu.Lock()
				calls[session]++
				if session == "capacity-limited-session" {
					capacityKeys = append(capacityKeys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
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
				body := []byte(fmt.Sprintf(`{"model":"cpa-6a","input":[{"role":"user","content":[{"type":"input_text","text":"Reply OK."}]}],"store":false,"stream":%t,"prompt_cache_key":%q}`, stream, session))
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
		})
	}
}
