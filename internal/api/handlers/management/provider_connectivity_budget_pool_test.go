package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestProviderConnectivityAndProductionShareAgentBudgetPoolRecovery(t *testing.T) {
	const budgetError = `{"error":{"message":"Budget pool quota has been exhausted. Please ask an administrator to increase the limit or select another budget pool.","type":"bad_response_status_code","param":"","code":"bad_response_status_code"}}`
	for _, model := range []config.CodexModel{{Name: "gpt-5.6-sol", Alias: "cpa-5.6s"}, {Name: "gpt-6-astra", Alias: "cpa-6a"}} {
		t.Run(model.Alias, func(t *testing.T) {
			var calls atomic.Int32
			var recovered atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer budget-selected-key" || gjson.GetBytes(body, "model").String() != model.Name {
					t.Error("probe or production changed the pinned key, alias or Responses endpoint")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if !recovered.Load() {
					w.WriteHeader(http.StatusPaymentRequired)
					_, _ = io.WriteString(w, budgetError)
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			defer upstream.Close()
			priority := 10
			provider := config.CodexKey{Name: "AgentRouter", BaseURL: "http://agentrouter.org/v1", ProxyURL: upstream.URL, Models: []config.CodexModel{model}, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "budget-selected-key", Priority: &priority}, {APIKey: "budget-other-key"}}}
			cfg := &config.Config{CodexKey: []config.CodexKey{provider}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil || len(auths) != 2 {
				t.Fatalf("synthesize fixture: auths=%d err=%v", len(auths), err)
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(2, 30*time.Second, 1)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			var selectedID string
			for _, a := range auths {
				if a.Attributes["api_key"] == "budget-selected-key" {
					selectedID = a.ID
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model.Alias}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				if _, err := manager.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
			h := &Handler{cfg: cfg, authManager: manager}
			router := gin.New()
			router.POST("/v0/management/provider-connectivity-test", h.ProviderConnectivityTest)
			probe := func() providerConnectivityTestResponse {
				key := "budget-selected-key"
				saved, _ := manager.GetByID(selectedID)
				draft := provider
				draft.APIKeyEntries = nil
				body, _ := json.Marshal(map[string]any{"provider": "codex", "model": model.Alias, "api_key": key, "auth_index": saved.Index, "codex_config": draft})
				request := httptest.NewRequest(http.MethodPost, "/v0/management/provider-connectivity-test", bytes.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				var response providerConnectivityTestResponse
				if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
					t.Fatalf("probe envelope status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				return response
			}
			production := func() (string, error) {
				body, _ := json.Marshal(map[string]any{"model": model.Alias, "input": "Hi", "stream": true, "store": false})
				response, err := manager.ExecuteStream(context.Background(), []string{"codex"}, coreexecutor.Request{Model: model.Alias, Payload: body}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: body, Stream: true})
				if err != nil {
					return "", err
				}
				collected, err := collectProviderConnectivityStream(context.Background(), response)
				return string(collected.Payload), err
			}
			_, err = production()
			var status coreexecutor.StatusError
			if !errors.As(err, &status) || status.StatusCode() != http.StatusPaymentRequired || !strings.Contains(err.Error(), budgetError) {
				t.Fatalf("production did not preserve the budget 402: %v", err)
			}
			failed := probe()
			if failed.StatusCode != http.StatusPaymentRequired || !strings.Contains(failed.Body, budgetError) || strings.Contains(failed.Body, "response.completed") {
				t.Fatalf("probe falsely accepted a budget error: %+v", failed)
			}
			// Verify production recovery before a successful probe could clear any state.
			recovered.Store(true)
			output, err := production()
			if err != nil || !strings.Contains(output, "response.completed") || !strings.Contains(output, "OK") {
				t.Fatalf("production still blocked after recovery: err=%v body=%s", err, output)
			}
			passed := probe()
			if passed.StatusCode != http.StatusOK || !strings.Contains(passed.Body, "response.completed") || !strings.Contains(passed.Body, "OK") {
				t.Fatalf("probe did not recover with production: %+v", passed)
			}
			if calls.Load() != 4 {
				t.Fatalf("unexpected rotation or replay: calls=%d", calls.Load())
			}
			current, _ := manager.GetByID(selectedID)
			if current.Success != 1 || current.Failed != 1 {
				t.Fatalf("probe polluted production counts: success=%d failed=%d", current.Success, current.Failed)
			}
		})
	}
}
