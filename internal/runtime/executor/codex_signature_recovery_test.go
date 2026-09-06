package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexSignatureRecoveryPreservesVisibleHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			original := []byte(`{"model":"gpt-6-astra","input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `","summary":[]},{"type":"message","role":"user","content":"marker"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"fixture","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"result"}]}`)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				calls++
				if calls == 1 {
					if !gjson.GetBytes(b, "input.0.encrypted_content").Exists() {
						t.Error("normal request lost valid-shape reasoning")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"foreign encrypted reasoning"}}`)
					return
				}
				if calls > 2 {
					t.Error("signature repair retried more than once")
				}
				if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("credential changed")
				}
				input := gjson.GetBytes(b, "input").Array()
				if len(input) != 3 || input[0].Get("content").String() != "marker" || input[1].Get("call_id").String() != "call_1" || input[2].Get("output").String() != "result" {
					t.Error("portable retry changed visible/tool history")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer srv.Close()
			e := NewCodexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: original}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				r, err := e.ExecuteStream(context.Background(), newCodexSignatureTestAuth(srv.URL), req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for c := range r.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
				}
			} else {
				if _, err := e.Execute(context.Background(), newCodexSignatureTestAuth(srv.URL), req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Errorf("attempts=%d, want 2", calls)
			}
			if !strings.Contains(string(original), "rs_foreign") {
				t.Error("caller request mutated")
			}
		})
	}
}

func TestCodexSignatureSSERecoveryCommitBoundary(t *testing.T) {
	for _, responseFormat := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI} {
		for _, partial := range []bool{false, true} {
			name := "handshake_only"
			if partial {
				name = "partial_output"
			}
			t.Run(responseFormat.String()+"/"+name, func(t *testing.T) {
				attempts := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts++
					w.Header().Set("Content-Type", "text/event-stream")
					if attempts == 1 {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_rejected\"}}\n\n")
						_, _ = io.WriteString(w, "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")
						if partial {
							_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
						}
						_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_encrypted_content\",\"type\":\"invalid_request_error\"}}\n\n")
						return
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
				}))
				defer srv.Close()
				payload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"hello"}]}`)
				e := NewCodexExecutor(&config.Config{})
				r, err := e.ExecuteStream(context.Background(), newCodexSignatureTestAuth(srv.URL), cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: responseFormat, Stream: true})
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				failed := false
				for c := range r.Chunks {
					if c.Err != nil {
						failed = true
					}
					output.Write(c.Payload)
				}
				if partial {
					if attempts != 1 || !failed || !strings.Contains(output.String(), "partial") {
						t.Fatalf("partial response replayed or lost: attempts=%d failed=%v", attempts, failed)
					}
				} else {
					if attempts != 2 || failed || strings.Contains(output.String(), "resp_rejected") || output.Len() == 0 {
						t.Fatalf("handshake recovery failed: attempts=%d failed=%v output=%s", attempts, failed, output.String())
					}
				}
			})
		}
	}
}

func TestCodexSignatureRecoveryRetriesOnlyOnce(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	payload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"hello"}]}`)
	e := NewCodexExecutor(&config.Config{})
	_, err := e.ExecuteStream(context.Background(), newCodexSignatureTestAuth(srv.URL), cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err == nil || attempts != 2 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestCodexSignatureRecoveryPreservesManagedAttemptBoundary(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	payload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"hello"}]}`)
	e := NewCodexExecutor(&config.Config{})
	_, err := e.ExecuteStream(context.Background(), newCodexSignatureTestAuth(srv.URL), cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true, ExecutionLifecycle: newTerminalFailureLifecycle()})
	if err == nil || attempts != 1 {
		t.Fatalf("managed execution silently retried: attempts=%d error=%v", attempts, err)
	}
}

func TestCodexNonstreamSignatureSSERecovery(t *testing.T) {
	for _, mode := range []string{"handshake_only", "partial_output", "repeated_rejection"} {
		t.Run(mode, func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Content-Type", "text/event-stream")
				if attempts == 1 || mode == "repeated_rejection" {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_rejected\"}}\n\n")
					_, _ = io.WriteString(w, "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")
					if mode == "partial_output" {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_encrypted_content\",\"type\":\"invalid_request_error\"}}\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer srv.Close()
			payload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"marker"}]}`)
			e := NewCodexExecutor(&config.Config{})
			r, err := e.Execute(context.Background(), newCodexSignatureTestAuth(srv.URL), cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			want := 2
			if mode == "partial_output" {
				want = 1
			}
			if attempts != want {
				t.Fatalf("attempts=%d want=%d", attempts, want)
			}
			if mode == "handshake_only" {
				if err != nil || !strings.Contains(string(r.Payload), "resp_ok") {
					t.Fatalf("recovery failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("terminal failure became success")
			}
		})
	}
}
