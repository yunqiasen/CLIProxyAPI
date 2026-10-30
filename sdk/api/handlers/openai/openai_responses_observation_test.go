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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const observationStreamModel = "observation-stream-model"

type observationStreamExecutor struct {
	chunks []string
}

func (*observationStreamExecutor) Identifier() string { return "observation-stream" }

func (*observationStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *observationStreamExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		for _, payload := range e.chunks {
			select {
			case <-ctx.Done():
				return
			case chunks <- coreexecutor.StreamChunk{Payload: []byte(payload)}:
			}
		}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks, Headers: http.Header{"X-Request-Id": []string{"fixture"}}}, nil
}

func (*observationStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*observationStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*observationStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runObservationStream(t *testing.T, name string, chunks []string) (string, requestlog.Record) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	executor := &observationStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "observation-" + name, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: observationStreamModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1}}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	store := requestlog.NewStore(16)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+observationStreamModel+`","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	ctx := requestlog.WithScope(request.Context(), store, requestlog.Scope{TraceID: "trace-" + name, URL: "/v1/responses", Method: http.MethodPost})
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		changes := store.Changes(0)
		for _, record := range changes.Records {
			if record.State != requestlog.Active {
				return recorder.Body.String(), record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request observation never finished; body=%q", recorder.Body.String())
	return "", requestlog.Record{}
}

func TestResponsesStreamMissingTerminalMarksObservationFailed(t *testing.T) {
	body, record := runObservationStream(t, "missing-terminal", []string{
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
	})
	if record.State != requestlog.Failed {
		t.Fatalf("observation state = %q, want failed; body=%q", record.State, body)
	}
	if record.Status != http.StatusBadGateway {
		t.Fatalf("observation status = %d, want 502", record.Status)
	}
}

func TestResponsesStreamCompletedMarksObservationSucceeded(t *testing.T) {
	body, record := runObservationStream(t, "completed", []string{
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n",
	})
	if record.State != requestlog.Succeeded {
		t.Fatalf("observation state = %q, want succeeded; body=%q", record.State, body)
	}
}

func TestResponsesStreamFailedEventMarksObservationFailed(t *testing.T) {
	body, record := runObservationStream(t, "failed-event", []string{
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream exploded\"}}}\n\n",
	})
	if record.State != requestlog.Failed {
		t.Fatalf("observation state = %q, want failed; body=%q", record.State, body)
	}
}
