package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const delayedResponsesKeepAliveModel = "delayed-responses-keepalive-model"

type delayedResponsesKeepAliveExecutor struct {
	delay   time.Duration
	outcome string
}

func (*delayedResponsesKeepAliveExecutor) Identifier() string { return "delayed-responses-keepalive" }

func (*delayedResponsesKeepAliveExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *delayedResponsesKeepAliveExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		timer := time.NewTimer(e.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if e.outcome == "closed" {
			return
		}
		chunk := coreexecutor.StreamChunk{Err: errors.New("upstream silent failure")}
		if e.outcome == "completed" {
			chunk = coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[]}}\n\n")}
		}
		select {
		case chunks <- chunk:
		case <-ctx.Done():
		}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks, Headers: http.Header{"X-Request-Id": []string{"fixture"}}}, nil
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

func TestResponsesHandlerKeepAliveBootstrapLifecycle(t *testing.T) {
	for _, outcome := range []string{"error", "closed", "completed", "canceled"} {
		for _, codex := range []bool{false, true} {
			name := outcome
			if codex {
				name += "_codex"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					executor := &delayedResponsesKeepAliveExecutor{delay: 3500 * time.Millisecond, outcome: outcome}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetRetryConfig(0, 0, 1)
					manager.RegisterExecutor(executor)
					auth := &coreauth.Auth{ID: "keepalive-" + name, Provider: executor.Identifier(), Status: coreauth.StatusActive}
					if _, err := manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: delayedResponsesKeepAliveModel}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1}}, manager)
					h := NewOpenAIResponsesAPIHandler(base)
					router := gin.New()
					router.POST("/v1/responses", h.Responses)
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+delayedResponsesKeepAliveModel+`","input":"hi","stream":true}`))
					request.Header.Set("Content-Type", "application/json")
					if codex {
						request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
					}
					ctx, cancel := context.WithCancel(request.Context())
					defer cancel()
					request = request.WithContext(ctx)
					if outcome == "canceled" {
						go func() { time.Sleep(2500 * time.Millisecond); cancel() }()
					}
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					synctest.Wait()
					body := recorder.Body.String()
					expectedBeats := 3
					if outcome == "canceled" {
						expectedBeats = 2
					}
					if got := strings.Count(body, ": keep-alive\n\n"); got != expectedBeats {
						t.Fatalf("heartbeats=%d want=%d body=%q", got, expectedBeats, body)
					}
					if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
						t.Fatalf("heartbeat response status=%d headers=%v", recorder.Code, recorder.Header())
					}
					switch outcome {
					case "error", "closed":
						event := "event: error"
						if codex {
							event = "event: response.failed"
						}
						if !strings.Contains(body, event) {
							t.Fatalf("missing terminal SSE failure: %q", body)
						}
						if strings.Contains(body, "event: response.completed") {
							t.Fatalf("failure reported as completion: %q", body)
						}
					case "completed":
						if !strings.Contains(body, "event: response.completed") || strings.Contains(body, "event: error") || strings.Contains(body, "event: response.failed") {
							t.Fatalf("completion corrupted: %q", body)
						}
					case "canceled":
						if strings.Contains(body, "data:") {
							t.Fatalf("cancellation fabricated output: %q", body)
						}
					}
					size := recorder.Body.Len()
					time.Sleep(2 * time.Second)
					synctest.Wait()
					if recorder.Body.Len() != size {
						t.Fatal("heartbeat writer survived handler return")
					}
				})
			})
		}
	}
}
