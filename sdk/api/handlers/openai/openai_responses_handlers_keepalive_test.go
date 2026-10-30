package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const delayedResponsesKeepAliveModel = "delayed-responses-keepalive-model"

type delayedResponsesKeepAliveExecutor struct{}

func (*delayedResponsesKeepAliveExecutor) Identifier() string { return "delayed-responses-keepalive" }

func (*delayedResponsesKeepAliveExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*delayedResponsesKeepAliveExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk)
	go func() {
		time.Sleep(1200 * time.Millisecond)
		chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream silent failure")}
		close(chunks)
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*delayedResponsesKeepAliveExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*delayedResponsesKeepAliveExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*delayedResponsesKeepAliveExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestResponsesHandlerSendsKeepAliveBeforeFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &delayedResponsesKeepAliveExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "delayed-responses-keepalive-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: delayedResponsesKeepAliveModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1}}, manager)
	base.Cfg.Streaming.KeepAliveSeconds = 1
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+delayedResponsesKeepAliveModel+`","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after heartbeat; body=%q", recorder.Code, body)
	}
	if !strings.Contains(body, ": keep-alive") {
		t.Fatalf("heartbeat missing before first payload: %q", body)
	}
	if !strings.Contains(body, "event: response.failed") || !strings.Contains(body, "upstream silent failure") {
		t.Fatalf("post-heartbeat failure not delivered as SSE: %q", body)
	}
	if keepAliveIndex := strings.Index(body, ": keep-alive"); keepAliveIndex >= 0 {
		if failureIndex := strings.Index(body, "event: response.failed"); failureIndex >= 0 && failureIndex < keepAliveIndex {
			t.Fatalf("failure preceded heartbeat: %q", body)
		}
	}
}
