package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestProviderConnectivityImagePolicyMatchesProduction(t *testing.T) {
	for _, host := range []string{"anyrouter.top", "agentrouter.org"} {
		for _, disabled := range []bool{false, true} {
			for _, lite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/disabled=%t/lite=%t", host, disabled, lite), func(t *testing.T) {
					type capture struct {
						body   []byte
						header http.Header
					}
					requests := make(chan capture, 3)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						requests <- capture{body, r.Header.Clone()}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_policy\",\"status\":\"completed\",\"output\":[]}}\n\n")
					}))
					defer upstream.Close()
					provider := config.CodexKey{APIKey: "selected-key", BaseURL: "http://" + host + "/v1", ProxyURL: upstream.URL, DisableImageGeneration: disabled, Headers: map[string]string{"X-OpenAI-Internal-Codex-Responses-Lite": fmt.Sprint(lite)}, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}}}
					cfg := &config.Config{CodexKey: []config.CodexKey{provider}}
					credentials, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
					if err != nil {
						t.Fatal(err)
					}
					credential := credentials[0]
					manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
					if _, err = manager.Register(context.Background(), credential); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "cpa-6a"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
					raw := []byte(`{"model":"cpa-6a","input":"Hi","tools":[{"type":"image_generation"},{"type":"function","name":"image_gen.imagegen","description":"Generate an image","parameters":{"type":"object","properties":{}}}],"parallel_tool_calls":true,"tool_choice":"auto"}`)
					stream, err := manager.ExecuteStream(context.Background(), []string{"codex"}, ex.Request{Model: "cpa-6a", Payload: raw}, ex.Options{SourceFormat: tr.FormatOpenAIResponse, OriginalRequest: raw, Stream: true})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = collectProviderConnectivityStream(context.Background(), stream); err != nil {
						t.Fatal(err)
					}
					h := &Handler{cfg: cfg, authManager: manager}
					probe := func(draft bool) {
						request := map[string]any{"provider": "codex", "auth_index": credential.EnsureIndex(), "model": "cpa-6a"}
						if draft {
							draftJSON, err := json.Marshal(provider)
							if err != nil {
								t.Fatal(err)
							}
							var draftFields map[string]any
							if err = json.Unmarshal(draftJSON, &draftFields); err != nil {
								t.Fatal(err)
							}
							// Like the edit-sheet snapshot, explicitly reset an omitted false field.
							draftFields["disable-image-generation"] = nil
							if disabled {
								draftFields["disable-image-generation"] = true
							}
							delete(draftFields, "api-key")
							delete(draftFields, "api-key-entries")
							request["codex_config"] = draftFields
						}
						body, err := json.Marshal(request)
						if err != nil {
							t.Fatal(err)
						}
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Request = httptest.NewRequest("POST", "/v0/management/provider-connectivity-test", bytes.NewReader(body))
						h.ProviderConnectivityTest(c)
						var response providerConnectivityTestResponse
						if err = json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						if recorder.Code != 200 || response.StatusCode != 200 {
							t.Fatalf("probe status=%d response=%s", recorder.Code, recorder.Body.String())
						}
					}
					probe(false)
					// Saved settings deliberately disagree; the draft must win without mutation.
					saved := provider
					saved.DisableImageGeneration = !disabled
					saved.Headers = map[string]string{"X-OpenAI-Internal-Codex-Responses-Lite": fmt.Sprint(!lite)}
					h.cfg = &config.Config{CodexKey: []config.CodexKey{saved}}
					probe(true)
					if h.cfg.CodexKey[0].DisableImageGeneration != !disabled || h.cfg.CodexKey[0].Headers["X-OpenAI-Internal-Codex-Responses-Lite"] != fmt.Sprint(!lite) {
						t.Fatal("draft mutated saved config")
					}
					if len(requests) != 3 {
						t.Fatalf("requests=%d want one production and one per probe", len(requests))
					}
					production := <-requests
					captures := []capture{production, <-requests, <-requests}
					for i, got := range captures {
						if got.header.Get("Authorization") != "Bearer selected-key" {
							t.Error("selected key replaced")
						}
						if got.header.Get("X-OpenAI-Internal-Codex-Responses-Lite") != fmt.Sprint(lite) {
							t.Errorf("request %d Lite header differs", i)
						}
						if gjson.GetBytes(got.body, "model").String() != "gpt-6-astra" {
							t.Errorf("request %d alias unresolved", i)
						}
						if image := gjson.GetBytes(got.body, `tools.#(type=="image_generation")`).Exists(); image == disabled {
							t.Errorf("request %d image switch ignored: %s", i, got.body)
						}
						if lite && gjson.GetBytes(got.body, "parallel_tool_calls").Bool() {
							t.Errorf("request %d Lite policy ignored: %s", i, got.body)
						}
						for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
							if !reflect.DeepEqual(gjson.GetBytes(production.body, field).Value(), gjson.GetBytes(got.body, field).Value()) {
								t.Errorf("request %d field %s differs: %s vs %s", i, field, production.body, got.body)
							}
						}
					}
				})
			}
		}
	}
}
