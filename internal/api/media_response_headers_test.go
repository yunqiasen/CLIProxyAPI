package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestMediaStandardImagesPreserveVerifiedResultHeader(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, plugin := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%t/plugin=%t", passthrough, plugin), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Set-Cookie", "upstream-private=fixture")
					w.Header().Set("X-Upstream-Only", "fixture")
					w.Header().Set(helps.MediaResultHeader, "pending")
					io.WriteString(w, `{"task_id":"provenance","data":[{"url":"https://example.test/result.png","b64_json":"","revised_prompt":""}]}`)
				}))
				defer upstream.Close()
				p := config.MediaProvider{Name: "header-test", Kind: "image", BaseURL: upstream.URL, Models: []config.MediaModel{{Name: "raw-image", Alias: "header-image", Capabilities: []string{"generate"}}}}
				cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
				manager := registerMediaOperationRuntime(t, cfg, p, "first")
				for _, a := range manager.List() {
					registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "header-image", Type: registry.OpenAIImageModelType}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				}
				server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
				server.handlers.Cfg.PassthroughHeaders = passthrough
				intercepted := false
				if plugin {
					server.handlers.SetPluginHost(&mockModelListInterceptorHost{interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
						intercepted = true
						headers := req.ResponseHeaders.Clone()
						headers.Set(helps.MediaResultHeader, "plugin-spoof")
						headers.Set("X-Plugin-Checked", "true")
						return pluginapi.ResponseInterceptResponse{Headers: headers, Body: req.Body}
					}})
				}
				ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/images/generations", "application/json", `{"model":"header-image","prompt":"a green circle","response_format":"url"}`)
				openai.NewOpenAIAPIHandler(server.handlers).ImagesGenerations(ctx)
				if rec.Code != 200 {
					t.Fatalf("image request: %d %s", rec.Code, rec.Body)
				}
				if got := rec.Header().Get(helps.MediaResultHeader); got != helps.MediaResultCompleted {
					t.Errorf("completed image lost verified result header: %q", got)
				}
				if rec.Header().Get("Set-Cookie") != "" {
					t.Fatal("upstream cookie escaped filtering")
				}
				if !passthrough && rec.Header().Get("X-Upstream-Only") != "" {
					t.Fatal("upstream headers were enabled implicitly")
				}
				if plugin && (!intercepted || rec.Header().Get("X-Plugin-Checked") != "true") {
					t.Fatal("plugin interception was bypassed")
				}
			})
		}
	}
}

func TestMediaGenericRouteFiltersUpstreamHeadersAndKeepsResult(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=fixture")
		w.Header().Set("X-CPA-Trace-ID", "upstream-spoof")
		w.Header().Set("Connection", "X-Hop-Private")
		w.Header().Set("X-Hop-Private", "private")
		w.Header().Set(helps.MediaResultHeader, "pending")
		io.WriteString(w, `{"url":"https://example.test/speech.wav"}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "header-test", Kind: "audio", BaseURL: upstream.URL, Operations: []config.MediaOperation{{Name: "speech", Method: "POST", Path: "/speech", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(registerMediaOperationRuntime(t, cfg, p, "first"))}
	ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/audio/speech", "application/json", `{}`)
	server.handleMediaOperationFor(ctx, "audio", "speech")
	if rec.Code != 200 || rec.Header().Get(helps.MediaResultHeader) != helps.MediaResultCompleted {
		t.Fatalf("result: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	for _, key := range []string{"Set-Cookie", "X-CPA-Trace-ID", "X-Hop-Private", "Connection"} {
		if rec.Header().Get(key) != "" {
			t.Errorf("forwarded reserved/private %s", key)
		}
	}
}
