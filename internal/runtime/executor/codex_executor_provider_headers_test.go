package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorForwardsClientCompatibilityHeadersToProvider(t *testing.T) {
	seen := make(http.Header)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"X-Codex-Window-Id", "Thread-Id", "Session-Id", "X-Openai-Internal-Codex-Responses-Lite"} {
			seen.Set(key, r.Header.Get(key))
		}
		if _, errRead := io.ReadAll(r.Body); errRead != nil {
			t.Fatalf("read provider request: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_headers\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n")
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	clientHeaders := make(http.Header)
	clientHeaders.Set("X-Codex-Window-Id", "window-1")
	clientHeaders.Set("Thread-Id", "thread-1")
	clientHeaders.Set("Session-Id", "session-1")
	clientHeaders.Set("X-Openai-Internal-Codex-Responses-Lite", "true")

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","prompt_cache_key":"cache-1"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      clientHeaders,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	for key, want := range map[string]string{
		"X-Codex-Window-Id":                      "window-1",
		"Thread-Id":                              "thread-1",
		"Session-Id":                             "session-1",
		"X-Openai-Internal-Codex-Responses-Lite": "true",
	} {
		if got := seen.Get(key); got != want {
			t.Fatalf("provider %s = %q, want %q", key, got, want)
		}
	}
}
