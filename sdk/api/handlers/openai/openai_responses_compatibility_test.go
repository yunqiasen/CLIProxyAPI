package openai

import (
	"context"
	"errors"
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
)

type responsesCompatibilityExecutor struct {
	chunks []coreexecutor.StreamChunk
	calls  int
}

func (*responsesCompatibilityExecutor) Identifier() string { return "responses-compatibility" }
func (*responsesCompatibilityExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected non-streaming execution")
}
func (e *responsesCompatibilityExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls++
	chunks := make(chan coreexecutor.StreamChunk, len(e.chunks))
	for _, chunk := range e.chunks {
		chunks <- chunk
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}
func (*responsesCompatibilityExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (*responsesCompatibilityExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected token count")
}
func (*responsesCompatibilityExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected raw HTTP request")
}

type responsesCompatibilityStatusError struct {
	status  int
	message string
}

func (e responsesCompatibilityStatusError) Error() string   { return e.message }
func (e responsesCompatibilityStatusError) StatusCode() int { return e.status }

func compatibilitySSE(payload string) coreexecutor.StreamChunk {
	return coreexecutor.StreamChunk{Payload: []byte("data: " + payload + "\n\n")}
}

func TestResponsesCompatibility(t *testing.T) {
	created := compatibilitySSE(`{"type":"response.created","response":{"id":"resp_fixture"}}`)
	partial := compatibilitySSE(`{"type":"response.output_text.delta","delta":"partial"}`)
	completed := compatibilitySSE(`{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","output":[]}}`)
	quota := coreexecutor.StreamChunk{Err: responsesCompatibilityStatusError{429, `{"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}`}}
	tests := []struct {
		name       string
		chunks     []coreexecutor.StreamChunk
		userAgent  string
		wantStatus int
		wantText   string
		wantError  string
	}{
		{"codex quota after handshake", []coreexecutor.StreamChunk{created, quota}, "Codex Desktop/26.803.41515", 200, "response.created", "response.failed"},
		{"generic quota after handshake", []coreexecutor.StreamChunk{created, quota}, "compatible-client", 200, "response.created", "event: error"},
		{"partial output then EOF", []coreexecutor.StreamChunk{partial, {Err: errors.New("unexpected EOF")}}, "Codex Desktop/26.803.41515", 200, "partial", "response.failed"},
		{"handshake then clean close", []coreexecutor.StreamChunk{created}, "Codex Desktop/26.803.41515", 200, "response.created", "response.failed"},
		{"empty stream", nil, "Codex Desktop/26.803.41515", 500, "empty_stream", ""},
		{"incomplete first frame", []coreexecutor.StreamChunk{{Payload: []byte("event: response.created")}, quota}, "Codex Desktop/26.803.41515", 429, "rate_limit_exceeded", ""},
		{"multiline completion", []coreexecutor.StreamChunk{{Payload: []byte("data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")}}, "Codex Desktop/26.803.41515", 200, "response.completed", ""},
		{"CRLF split", []coreexecutor.StreamChunk{{Payload: []byte("data: {\"type\":\"response.completed\",\r")}, {Payload: []byte("\ndata: \"response\":{\"status\":\"completed\",\"output\":[]}}\r\n\r\n")}}, "Codex Desktop/26.803.41515", 200, "response.completed", ""},
		{"split JSON completion", []coreexecutor.StreamChunk{{Payload: []byte(`data: {"type":"response.completed",`)}, {Payload: []byte(`"response":{"status":"completed","output":[]}}`)}}, "Codex Desktop/26.803.41515", 200, "response.completed", ""},
		{"inline failure", []coreexecutor.StreamChunk{created, compatibilitySSE(`{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}}`)}, "Codex Desktop/26.803.41515", 200, "rate_limit_exceeded", "response.failed"},
		{"successful text", []coreexecutor.StreamChunk{partial, completed}, "Codex Desktop/26.803.41515", 200, "partial", ""},
		{"completed tool turn", []coreexecutor.StreamChunk{compatibilitySSE(`{"type":"response.output_item.done","output_index":0,"item":{"type":"tool_search_call","status":"completed","arguments":{"query":"fixture"}}}`), completed}, "Codex Desktop/26.803.41515", 200, "tool_search_call", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			executor := &responsesCompatibilityExecutor{chunks: tc.chunks}
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
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, manager))
			router := gin.New()
			router.POST("/v1/responses", h.Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", tc.userAgent)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			body := recorder.Body.String()
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", recorder.Code, tc.wantStatus, body)
			}
			if !strings.Contains(body, tc.wantText) {
				t.Fatalf("missing %q in %q", tc.wantText, body)
			}
			if tc.wantError != "" {
				if !strings.Contains(body, tc.wantError) {
					t.Fatalf("missing terminal failure %q in %q", tc.wantError, body)
				}
				if got := strings.Count(body, `"type":"response.failed"`); got > 1 {
					t.Fatalf("duplicate terminal failures: %q", body)
				}
			} else if tc.wantStatus == http.StatusOK && (strings.Contains(body, "response.failed") || strings.Contains(body, "event: error")) {
				t.Fatalf("successful response gained an error: %q", body)
			}
			if executor.calls != 1 {
				t.Fatalf("stream replayed %d times, want one execution", executor.calls)
			}
		})
	}
}
