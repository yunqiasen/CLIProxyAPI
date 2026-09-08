package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketSignatureRecovery(t *testing.T) {
	for _, mode := range []string{"nonstream", "stream", "downstream_websocket"} {
		stream := mode != "nonstream"
		ctx := context.Background()
		if mode == "downstream_websocket" {
			ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
		}
		name := mode

		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					_, payload, err := conn.ReadMessage()
					if err != nil {
						return
					}
					attempt := calls.Add(1)
					if attempt == 1 {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_rejected"}}`))
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[],"content":[],"encrypted_content":"gAAAA-fixture-state"}}`))
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"foreign reasoning"}}`))
						continue
					}
					if len(gjson.GetBytes(payload, "input").Array()) != 1 {
						t.Error("retry retained rejected reasoning")
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[]}}`))
					return
				}
			}))
			defer srv.Close()
			e := NewCodexWebsocketsExecutor(&config.Config{})
			auth := newCodexSignatureTestAuth(srv.URL)
			auth.ID = "signature-test"
			req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"marker"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				r, err := e.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				for c := range r.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
					output.Write(c.Payload)
				}
				if strings.Contains(output.String(), "resp_rejected") {
					t.Error("rejected response identity leaked downstream")
				}
			} else {
				if _, err := e.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("attempts=%d", calls.Load())
			}
		})
	}
}

func TestCodexWebsocketSignatureRecoveryBoundaries(t *testing.T) {
	for _, mode := range []string{"repeated_rejection", "partial_text", "partial_tool", "incremental", "managed"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					_, _, err := conn.ReadMessage()
					if err != nil {
						return
					}
					attempt := calls.Add(1)
					if attempt == 1 {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_first"}}`))
						if mode == "partial_text" {
							_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"partial"}`))
						}
						if mode == "partial_tool" {
							_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.function_call_arguments.delta","delta":"partial"}`))
						}
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,"error":{"code":"invalid_encrypted_content","type":"invalid_request_error"}}`))
				}
			}))
			defer srv.Close()
			e := NewCodexWebsocketsExecutor(&config.Config{})
			auth := newCodexSignatureTestAuth(srv.URL)
			auth.ID = "signature-boundary"
			prefix := ""
			if mode == "incremental" {
				prefix = `"previous_response_id":"resp_remote",`
			}
			req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{` + prefix + `"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"marker"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true}
			if mode == "managed" {
				opts.ExecutionLifecycle = newTerminalFailureLifecycle()
			}
			result, err := e.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			failed := false
			for chunk := range result.Chunks {
				failed = failed || chunk.Err != nil
				output.Write(chunk.Payload)
			}
			want := int32(1)
			if mode == "repeated_rejection" {
				want = 2
			}
			if !failed || calls.Load() != want {
				t.Fatalf("failed=%v attempts=%d want=%d", failed, calls.Load(), want)
			}
			if strings.HasPrefix(mode, "partial_") && !strings.Contains(output.String(), "partial") {
				t.Fatal("partial output lost")
			}
			if strings.Contains(output.String(), "response.completed") {
				t.Fatal("failure became success")
			}
		})
	}
}
