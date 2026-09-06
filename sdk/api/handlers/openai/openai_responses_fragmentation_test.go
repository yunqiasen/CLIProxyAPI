package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func runFragmentedResponsesRequest(t *testing.T, chunks []coreexecutor.StreamChunk) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	executor := &responsesCompatibilityExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	id := strings.ReplaceAll(t.Name(), "/", "-")
	auth := &coreauth.Auth{ID: id, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	model := id + "-model"
	registry.GetGlobalRegistry().RegisterClient(id, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if executor.calls != 1 {
		t.Fatalf("stream replayed %d times, want one execution", executor.calls)
	}
	return recorder
}

func TestResponsesCompatibilityArbitraryChunkBoundaries(t *testing.T) {
	const delta = "literal data: value, event: value, id: value, retry: value, : value, 中文"
	for _, tc := range []struct{ name, stream string }{
		{"event and data", fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", delta)},
		{"data only", fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", delta)},
		{"data before event", fmt.Sprintf("data: {\"delta\":%q}\nevent: response.output_text.delta\n\ndata: {\"response\":{\"status\":\"completed\",\"output\":[]}}\nevent: response.completed\n\n", delta)},
		{"CRLF multiline", fmt.Sprintf("event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":%q}\r\n\r\nevent: response.completed\r\ndata: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\",\"output\":[]}}\r\n\r\n", delta)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertResponse := func(t *testing.T, chunks []coreexecutor.StreamChunk) {
				t.Helper()
				recorder := runFragmentedResponsesRequest(t, chunks)
				body := recorder.Body.String()
				if recorder.Code != http.StatusOK || strings.Contains(body, "response.failed") {
					t.Fatalf("valid fragmented response failed: status=%d body=%q", recorder.Code, body)
				}
				var gotDelta strings.Builder
				var completed int
				for _, frame := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
					payload, ok := responsesSSEDataPayload([]byte(frame))
					if !ok {
						continue
					}
					eventType := gjson.GetBytes(payload, "type").String()
					eventName := responsesSSEEventName([]byte(frame))
					if eventType != "" && eventName != "" && eventType != eventName {
						t.Fatalf("fragmentation changed event name: type=%q event=%q frame=%q", eventType, eventName, frame)
					}
					if eventType == "" {
						eventType = eventName
					}
					switch eventType {
					case "response.output_text.delta":
						gotDelta.WriteString(gjson.GetBytes(payload, "delta").String())
					case "response.completed":
						completed++
					}
				}
				if gotDelta.String() != delta || completed != 1 {
					t.Fatalf("fragmentation changed response: delta=%q completed=%d body=%q", gotDelta.String(), completed, body)
				}
			}
			for split := 1; split < len(tc.stream); split++ {
				t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
					assertResponse(t, []coreexecutor.StreamChunk{{Payload: []byte(tc.stream[:split])}, {Payload: []byte(tc.stream[split:])}})
				})
			}
			t.Run("single bytes", func(t *testing.T) {
				chunks := make([]coreexecutor.StreamChunk, len(tc.stream))
				for i := 0; i < len(tc.stream); i++ {
					chunks[i].Payload = []byte{tc.stream[i]}
				}
				assertResponse(t, chunks)
			})
		})
	}
}

func TestResponsesCompatibilityTrailingEventField(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(fmt.Sprintf("suffix-%q", suffix), func(t *testing.T) {
			recorder := runFragmentedResponsesRequest(t, []coreexecutor.StreamChunk{
				{Payload: []byte("data: {\"response\":{\"status\":\"completed\",\"output\":[]}}\n")},
				{Payload: []byte("event: response.completed" + suffix)},
			})
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK || strings.Contains(body, "response.failed") || !strings.Contains(body, "event: response.completed") {
				t.Fatalf("trailing event field lost: status=%d body=%q", recorder.Code, body)
			}
		})
	}
}
