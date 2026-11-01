package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	apiHandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeProbeExecutor struct {
	gotReq apiHandlers.ModelExecutionRequest
	resp   apiHandlers.ModelExecutionResponse
	errMsg *interfaces.ErrorMessage
}

func (f *fakeProbeExecutor) ExecuteModel(_ context.Context, req apiHandlers.ModelExecutionRequest) (apiHandlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
	f.gotReq = req
	return f.resp, f.errMsg
}

func newProbeTestContext(t *testing.T, payload string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/models/probe", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func newProbeTestHandler(t *testing.T, executor AuthFileModelExecutor) *Handler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "raccoon-1.json",
		FileName: "raccoon-1.json",
		Provider: "freetier",
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	handler := NewHandler(nil, "", manager)
	handler.SetModelExecutor(executor)
	return handler
}

func TestProbeAuthFileModelPinsCredentialAndReportsSuccess(t *testing.T) {
	executor := &fakeProbeExecutor{
		resp: apiHandlers.ModelExecutionResponse{
			StatusCode: http.StatusOK,
			Body: []byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`),
		},
	}
	handler := newProbeTestHandler(t, executor)
	c, rec := newProbeTestContext(t, `{"name":"raccoon-1.json","model":"sn-kimi-k3"}`)

	handler.ProbeAuthFileModel(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true: %s", body["ok"], rec.Body.String())
	}
	if text, _ := body["text"].(string); text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
	if executor.gotReq.AuthID != "raccoon-1.json" {
		t.Fatalf("pinned auth id = %q, want the clicked credential", executor.gotReq.AuthID)
	}
	if executor.gotReq.Model != "sn-kimi-k3" {
		t.Fatalf("probe model = %q, want sn-kimi-k3", executor.gotReq.Model)
	}
	if !strings.Contains(string(executor.gotReq.Body), `"max_tokens":512`) {
		t.Fatalf("probe payload must leave room for reasoning models: %s", executor.gotReq.Body)
	}
	if finish, _ := body["finish"].(string); finish != "stop" {
		t.Fatalf("finish = %q, want stop", finish)
	}
	usage, _ := body["usage"].(map[string]any)
	if usage == nil || int(usage["total_tokens"].(float64)) != 13 {
		t.Fatalf("usage = %#v, want total_tokens 13", body["usage"])
	}
	if latency, ok := body["latency_ms"].(float64); !ok || latency < 0 {
		t.Fatalf("latency_ms = %#v, want a non-negative number", body["latency_ms"])
	}
}

func TestProbeAuthFileModelReportsUpstreamFailure(t *testing.T) {
	executor := &fakeProbeExecutor{
		errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errProbeForbidden},
	}
	handler := newProbeTestHandler(t, executor)
	c, rec := newProbeTestContext(t, `{"name":"raccoon-1.json","model":"sn-kimi-k3"}`)

	handler.ProbeAuthFileModel(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with ok=false body", rec.Code)
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("ok = true, want false: %s", rec.Body.String())
	}
	if status, _ := body["status"].(float64); int(status) != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", body["status"])
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "403") {
		t.Fatalf("error = %q, want upstream message", message)
	}
	if latency, ok := body["latency_ms"].(float64); !ok || latency < 0 {
		t.Fatalf("latency_ms = %#v, want a non-negative number", body["latency_ms"])
	}
}

func TestProbeAuthFileModelRequiresModel(t *testing.T) {
	handler := newProbeTestHandler(t, &fakeProbeExecutor{})
	c, rec := newProbeTestContext(t, `{"name":"raccoon-1.json"}`)

	handler.ProbeAuthFileModel(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestProbeAuthFileModelRejectsUnknownCredential(t *testing.T) {
	handler := newProbeTestHandler(t, &fakeProbeExecutor{})
	c, rec := newProbeTestContext(t, `{"name":"missing.json","model":"sn-kimi-k3"}`)

	handler.ProbeAuthFileModel(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

var errProbeForbidden = probeTestError("upstream 403 forbidden")

type probeTestError string

func (e probeTestError) Error() string { return string(e) }
