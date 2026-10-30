package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestProviderActiveToolSearchProductionProbeParity(t *testing.T) {
	for _, host := range []string{"anyrouter.top", "agentrouter.org"} {
		t.Run(host, func(t *testing.T) {
			history := `[{"role":"user","content":"Find and use lookup."},{"type":"tool_search_call","id":"tsc_active","call_id":"search_active","status":"completed","execution":"client","arguments":{"query":"lookup"}},{"type":"tool_search_output","id":"tso_active","call_id":"search_active","status":"completed","execution":"client","tools":[{"type":"namespace","name":"fixture","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"defer_loading":true}]}]}]`
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				b, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "Bearer selected-key" {
					t.Error("key changed")
				}
				loaded := false
				for _, tool := range gjson.GetBytes(b, "tools").Array() {
					if tool.Get("type").String() == "namespace" && tool.Get("name").String() == "fixture" && tool.Get("tools.0.name").String() == "lookup" && !tool.Get("tools.0.defer_loading").Bool() {
						loaded = true
					}
				}
				if !loaded {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"message":"discovered tool missing from callable catalog"}}`)
					return
				}
				for _, item := range gjson.GetBytes(b, "input").Array() {
					if strings.HasPrefix(item.Get("type").String(), "tool_search_") {
						t.Error("resource-bound native discovery retained")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_active\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"lookup\",\"namespace\":\"fixture\",\"call_id\":\"call_use\",\"arguments\":\"{}\",\"status\":\"completed\"}]}}\n\n")
			}))
			defer upstream.Close()
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "selected-key", BaseURL: "http://" + host + "/v1", ProxyURL: upstream.URL, DisableImageGeneration: true, ResponsesFirstOutputTimeoutSeconds: 120, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a"}}}}, Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "cpa-6a", Protocol: "codex", FromProtocol: sdktranslator.FormatOpenAIResponse.String()}}, Params: map[string]any{"input": history}}}}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil || len(auths) != 1 {
				t.Fatalf("synthesis: %v", err)
			}
			original := []byte(`{"model":"cpa-6a","tools":[{"type":"tool_search","execution":"client"}],"input":` + history + `}`)
			before := bytes.Clone(original)
			for _, mode := range []string{"http", "stream", "probe"} {
				t.Run(mode, func(t *testing.T) {
					e := runtimeexecutor.NewCodexExecutor(cfg)
					req := coreexecutor.Request{Model: "gpt-6-astra", Payload: original}
					opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: original, Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "cpa-6a"}}
					var result []byte
					var err error
					switch mode {
					case "http":
						response, eErr := e.Execute(context.Background(), auths[0], req, opts)
						result, err = response.Payload, eErr
					case "stream":
						opts.Stream = true
						stream, eErr := e.ExecuteStream(context.Background(), auths[0], req, opts)
						err = eErr
						if err == nil {
							response, cErr := collectProviderConnectivityStream(context.Background(), stream)
							result, err = response.Payload, cErr
						}
					case "probe":
						h := &Handler{cfg: cfg}
						key, base := "selected-key", "http://"+host+"/v1"
						draft, _ := json.Marshal(map[string]any{"base-url": base, "proxy-url": upstream.URL, "models": []map[string]string{{"name": "gpt-6-astra", "alias": "cpa-6a"}}, "disable-image-generation": true, "responses-first-output-timeout-seconds": 120})
						response, _, pErr := h.performProviderConnectivityTest(context.Background(), providerConnectivityTestRequest{Provider: "codex", APIKey: &key, BaseURL: &base, Model: "cpa-6a", CodexConfig: draft})
						err = pErr
						if pErr == nil {
							result = []byte(response.Body)
							if response.StatusCode != 200 {
								t.Errorf("probe status=%d", response.StatusCode)
							}
						}
					}
					if err != nil || !strings.Contains(string(result), "call_use") {
						t.Fatalf("%s did not produce a usable function call: %v %s", mode, err, result)
					}
				})
			}
			if calls != 3 || !bytes.Equal(original, before) {
				t.Fatalf("calls=%d, or client payload changed", calls)
			}
		})
	}
}
