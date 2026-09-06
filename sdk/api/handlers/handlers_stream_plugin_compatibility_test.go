package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginResponsesStreamCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		chunks     []string
		want       string
		wantErrors int
	}{
		{"fragmented field and string", []string{"da", `ta: {"type":"response.output_text.delta","delta":"literal `, "event: value\"}\n\n"}, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"literal event: value\"}\n\n", 0},
		{"bare websocket JSON", []string{`{"type":"response.completed",`, `"response":{"status":"completed"}}`}, `{"type":"response.completed","response":{"status":"completed"}}`, 0},
		{"trailing event field", []string{"data: {\"response\":{\"status\":\"completed\"}}\n", "event: response.completed"}, "data: {\"response\":{\"status\":\"completed\"}}\nevent: response.completed", 0},
		{"malformed frame", []string{"data: {\"type\":\"response.completed\"\n\n"}, "", 1},
		{"partial answer before malformed frame", []string{"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", "data: {\"type\":\"response.completed\"\n\n"}, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &handlerDirectExecutorRouteHost{}
			host.hasRouters = true
			host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "fixture-plugin"}, true
			}
			host.stream = func(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
				chunks := make(chan coreexecutor.StreamChunk, len(tc.chunks))
				for _, chunk := range tc.chunks {
					chunks <- coreexecutor.StreamChunk{Payload: []byte(chunk)}
				}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			h.SetModelRouterHost(host)
			model := strings.ReplaceAll(t.Name(), "/", "-")
			data, _, errs := h.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, model)), "")
			var body strings.Builder
			for chunk := range data {
				body.Write(chunk)
			}
			if body.String() != tc.want {
				t.Fatalf("plugin stream=%q want=%q", body.String(), tc.want)
			}
			count := 0
			for errMsg := range errs {
				if errMsg == nil {
					continue
				}
				count++
				if errMsg.StatusCode != http.StatusBadGateway {
					t.Fatalf("plugin stream error=%+v want 502", errMsg)
				}
			}
			if count != tc.wantErrors {
				t.Fatalf("plugin stream errors=%d want=%d", count, tc.wantErrors)
			}
		})
	}
}
