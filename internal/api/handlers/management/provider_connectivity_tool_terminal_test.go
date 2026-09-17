package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestProviderConnectivityAndProductionShareAgentToolTerminalAndHistory(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			var requests [][]byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests = append(requests, body)
				if r.Header.Get("Authorization") != "Bearer selected-key" || gjson.GetBytes(body, "model").String() != "gpt-6-astra" || gjson.GetBytes(body, "reasoning.effort").String() != "high" {
					t.Error("selected key, alias, or thinking policy bypassed")
				}
				if gjson.GetBytes(body, "input.0.summary.0.text").String() != "portable summary" || gjson.GetBytes(body, "input.0.summary.1.text").String() != "portable content" || gjson.GetBytes(body, "input.0.id").Exists() || gjson.GetBytes(body, "input.0.encrypted_content").Exists() || gjson.GetBytes(body, "input.0.content.#").Int() != 0 {
					t.Error("probe or production lost readable history")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_echo","type":"function_call","call_id":"call_echo","name":"echo","arguments":"","status":"in_progress"}}`+"\n\n")
				_, _ = io.WriteString(w, `data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_echo","arguments":"{\"text\":\"OK\"}"}`+"\n\n")
				if complete {
					_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_echo","type":"function_call","call_id":"call_echo","name":"echo","arguments":"{\"text\":\"OK\"}","status":"completed"}}`+"\n\n")
				}
			}))
			defer upstream.Close()
			token := append([]byte{0x80}, make([]byte, 72)...)
			history := `[{"type":"reasoning","id":"rs_` + strings.Repeat("a", 62) + `","encrypted_content":"` + base64.URLEncoding.EncodeToString(token) + `","summary":[{"type":"summary_text","text":"portable summary"}],"content":[{"type":"reasoning_text","text":"portable content"}]},{"role":"user","content":"Call echo"}]`
			provider := config.CodexKey{Name: "fixture", APIKey: "selected-key", BaseURL: "http://agentrouter.org/v1", ProxyURL: upstream.URL, DisableImageGeneration: true, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a", Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}}}
			cfg := &config.Config{CodexKey: []config.CodexKey{provider}, Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "cpa-6a", Protocol: "codex", FromProtocol: sdktranslator.FormatOpenAIResponse.String()}}, Params: map[string]any{"input": history, "tools": `[{"type":"function","name":"echo","parameters":{"type":"object","properties":{"text":{"type":"string"}}}}]`, "tool_choice": `{"type":"function","name":"echo"}`}}}}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			if _, err = manager.Register(context.Background(), auths[0]); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auths[0].ID, "codex", []*registry.ModelInfo{{ID: "cpa-6a"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auths[0].ID) })
			payload := []byte(`{"model":"cpa-6a","input":"Hi","stream":true,"store":false}`)
			stream, err := manager.ExecuteStream(context.Background(), []string{"codex"}, coreexecutor.Request{Model: "cpa-6a(high)", Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true, OriginalRequest: payload, Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "cpa-6a(high)", coreexecutor.RequestPathMetadataKey: "/v1/responses"}})
			if err != nil {
				t.Fatal(err)
			}
			production, errProduction := collectProviderConnectivityStream(context.Background(), stream)
			if complete != (errProduction == nil) {
				t.Fatalf("production completion=%t err=%v", complete, errProduction)
			}
			h := &Handler{cfg: cfg, authManager: manager}
			draft, _ := json.Marshal(provider)
			key := "selected-key"
			probe, _, err := h.performProviderConnectivityTest(context.Background(), providerConnectivityTestRequest{Provider: "codex", APIKey: &key, Model: "cpa-6a(high)", CodexConfig: draft})
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				if probe.StatusCode != 200 || strings.Count(probe.Body, `"type":"response.completed"`) != 1 || strings.Count(string(production.Payload), `"type":"response.completed"`) != 1 {
					t.Fatalf("probe and production terminal mismatch: status=%d body=%s", probe.StatusCode, probe.Body)
				}
			} else if probe.StatusCode < 400 || strings.Contains(probe.Body, `"type":"response.completed"`) {
				t.Fatal("probe accepted incomplete tools")
			}
			if len(requests) != 2 || !reflect.DeepEqual(gjson.GetBytes(requests[0], "input").Value(), gjson.GetBytes(requests[1], "input").Value()) {
				t.Fatalf("request/probe divergence or replay: calls=%d", len(requests))
			}
		})
	}
}
