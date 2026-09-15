package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorLitePolicySources(t *testing.T) {
	for _, transport := range []string{"http-stream", "http-execute", "ws-stream", "ws-execute", "compact"} {
		for _, tc := range []struct {
			name, client, provider, model           string
			metadata, ginHeader, explicit, disabled bool
			wantLite                                bool
			global                                  config.DisableImageGenerationMode
		}{
			{name: "default"},
			{name: "global-on", global: config.DisableImageGenerationAll},
			{name: "global-chat", global: config.DisableImageGenerationChat},
			{name: "global-passthrough", global: config.DisableImageGenerationPassthrough, explicit: true},
			{name: "global-passthrough-lite", global: config.DisableImageGenerationPassthrough, explicit: true, provider: "true", wantLite: true},
			{name: "client", client: "true", wantLite: true},
			{name: "provider", provider: "true", wantLite: true},
			{name: "provider-over-client", client: "true", provider: "false"},
			{name: "model", model: "true", wantLite: true},
			{name: "model-over-provider", provider: "true", model: "false"},
			{name: "model-over-client", client: "false", model: "true", wantLite: true},
			{name: "metadata", metadata: true, wantLite: true},
			{name: "gin-fallback", ginHeader: true, wantLite: true},
			{name: "explicit-tool-lite", provider: "true", explicit: true, wantLite: true},
			{name: "disabled", disabled: true, explicit: true},
			{name: "disabled-lite", disabled: true, explicit: true, provider: "true", wantLite: true},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				const model = "test-lite-policy-model"
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient("lite-policy-fixture", "codex", []*registry.ModelInfo{{ID: model, Config: &registry.ModelConfig{OverrideHeader: map[string]string{codexResponsesLiteHeader: tc.model}}}})
				// An absent override must stay absent, rather than erase the client header.
				if tc.model == "" {
					reg.UnregisterClient("lite-policy-fixture")
				}
				t.Cleanup(func() { reg.UnregisterClient("lite-policy-fixture") })
				type capture struct {
					body   []byte
					header string
				}
				captured := make(chan capture, 1)
				upstreamErrors := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					terminal := []byte(`{"type":"response.completed","response":{"id":"resp_lite","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
					if websocket.IsWebSocketUpgrade(r) {
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							upstreamErrors <- err
							return
						}
						defer conn.Close()
						_, body, err := conn.ReadMessage()
						if err != nil {
							upstreamErrors <- err
							return
						}
						captured <- capture{body, r.Header.Get(codexResponsesLiteHeader)}
						if err = conn.WriteMessage(websocket.TextMessage, terminal); err != nil {
							upstreamErrors <- err
						}
						return
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						upstreamErrors <- err
						return
					}
					captured <- capture{body, r.Header.Get(codexResponsesLiteHeader)}
					if transport == "compact" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"resp_compact","object":"response.compaction","output":[]}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(append(append([]byte("data: "), terminal...), '\n', '\n'))
				}))
				defer server.Close()
				credential := &auth.Auth{ID: "lite-fixture", Provider: "codex", Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture", "plan_type": "pro"}}
				if tc.provider != "" {
					credential.Attributes["header:"+codexResponsesLiteHeader] = tc.provider
				}
				if tc.disabled {
					credential.Attributes[auth.AttributeCodexDisableImageGeneration] = "true"
				}
				headers := make(http.Header)
				if tc.client != "" {
					headers.Set(codexResponsesLiteHeader, tc.client)
				}
				raw := `{"model":"test-lite-policy-model","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`
				if tc.explicit {
					raw = `{"model":"test-lite-policy-model","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"image_generation"}]`
				}
				if tc.metadata {
					raw += `,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true}`
				}
				raw += "}"
				ctx := context.Background()
				if tc.ginHeader {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
					c.Request.Header.Set(codexResponsesLiteHeader, "true")
					ctx = context.WithValue(ctx, "gin", c)
					headers = nil
				}
				req := ex.Request{Model: model, Payload: []byte(raw)}
				opts := ex.Options{SourceFormat: tr.FromString("openai-response"), Headers: headers}
				cfg := &config.Config{}
				cfg.DisableImageGeneration = tc.global
				var err error
				switch transport {
				case "http-execute":
					_, err = NewCodexExecutor(cfg).Execute(ctx, credential, req, opts)
				case "compact":
					opts.Alt = "responses/compact"
					_, err = NewCodexExecutor(cfg).Execute(ctx, credential, req, opts)
				case "ws-execute":
					_, err = NewCodexWebsocketsExecutor(cfg).Execute(ctx, credential, req, opts)
				default:
					var result *ex.StreamResult
					if transport == "ws-stream" {
						result, err = NewCodexWebsocketsExecutor(cfg).ExecuteStream(ctx, credential, req, opts)
					} else {
						result, err = NewCodexExecutor(cfg).ExecuteStream(ctx, credential, req, opts)
					}
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-upstreamErrors:
					t.Fatal(err)
				default:
				}
				var got capture
				select {
				case got = <-captured:
				default:
					t.Fatal("upstream request missing")
				}
				wantImage := !tc.disabled && (tc.explicit || (!tc.wantLite && transport != "compact" && tc.global == config.DisableImageGenerationOff))
				if image := gjson.GetBytes(got.body, `tools.#(type=="image_generation")`).Exists(); image != wantImage {
					t.Errorf("image=%v want=%v body=%s", image, wantImage, got.body)
				}
				if tc.wantLite && gjson.GetBytes(got.body, "parallel_tool_calls").Bool() {
					t.Errorf("Lite left parallel calls enabled: %s", got.body)
				}
				wantHeader := tc.client
				if tc.ginHeader {
					wantHeader = "true"
				}
				if tc.provider != "" {
					wantHeader = tc.provider
				}
				if tc.model != "" {
					wantHeader = tc.model
				}
				if got.header != wantHeader {
					t.Errorf("header=%q want=%q", got.header, wantHeader)
				}
				if headers != nil && headers.Get(codexResponsesLiteHeader) != tc.client {
					t.Error("caller header mutated")
				}
			})
		}
	}
}
