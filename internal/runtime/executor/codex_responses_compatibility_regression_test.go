package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// Equivalent terminal frames must survive both executor response modes.
func TestCodexResponsesSSEFrameCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"single_line", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[]}}\n\n"},
		{"trailing_event", "data: {\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[]}}\nevent: response.completed\n\n"},
		{"multiline", "event: response.completed\ndata: {\"type\":\ndata: \"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[]}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tc.frame))
			}))
			defer server.Close()
			e := NewCodexExecutor(&config.Config{})
			nonstream, errNonstream := e.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}, cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":"fixture"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errNonstream != nil || !strings.Contains(string(nonstream.Payload), "resp_fixture") {
				t.Errorf("nonstream terminal lost: %v", errNonstream)
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}
			result, err := e.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":"fixture","stream":true}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Errorf("valid completed SSE misclassified: %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !strings.Contains(output.String(), `"type":"response.completed"`) {
				t.Error("completion event missing")
			}
		})
	}
}

func TestCodexCapacityEventMapsTo429(t *testing.T) {
	body := []byte(`{"error":{"code":"no_capacity","type":"too_many_requests","message":"The system is currently experiencing high demand."}}`)
	err := newCodexStatusErr(codexTerminalFailureStatus(body), body)
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("explicit too_many_requests classified as HTTP %d, want 429", err.StatusCode())
	}
}

func TestCodexCapacityStreamCommitBoundary(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_%t", partial), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_capacity\"}}\n\nevent: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")
				if partial {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"no_capacity\",\"type\":\"too_many_requests\"}}\n\n")
			}))
			defer server.Close()
			e := NewCodexExecutor(&config.Config{})
			r, err := e.ExecuteStream(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":"marker"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			status := 0
			for c := range r.Chunks {
				output.Write(c.Payload)
				if c.Err != nil {
					if e, ok := c.Err.(interface{ StatusCode() int }); ok {
						status = e.StatusCode()
					}
				}
			}
			if status != 429 {
				t.Fatalf("capacity status=%d", status)
			}
			if partial != strings.Contains(output.String(), "partial") || (!partial && output.Len() != 0) {
				t.Fatalf("wrong commit boundary: partial=%t output=%q", partial, output.String())
			}
		})
	}
}
