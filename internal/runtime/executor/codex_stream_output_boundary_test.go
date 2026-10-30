package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexStreamToolOutputSignatureBoundary(t *testing.T) {
	const added = `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_tool","call_id":"call_tool","name":"lookup","arguments":"","status":"in_progress"}}`
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_tool","call_id":"call_tool","name":"lookup","arguments":"{}","status":"completed"}}`
	for _, transport := range []string{"http", "websocket", "downstream_websocket"} {
		for _, buffering := range []bool{false, true} {
			for _, tc := range []struct {
				name   string
				events []string
			}{
				{name: "added", events: []string{added}},
				{name: "done", events: []string{done}},
				{name: "added_done", events: []string{added, `{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_tool","arguments":"{}"}`, done}},
			} {
				t.Run(fmt.Sprintf("%s/buffering_%t/%s", transport, buffering, tc.name), func(t *testing.T) {
					calls, output, streamErr := runCodexStreamSignatureBoundary(t, transport, buffering, tc.events)
					if calls != 1 {
						t.Errorf("replayed after tool output: attempts=%d, want 1", calls)
					}
					if streamErr == nil || (!strings.Contains(streamErr.Error(), "invalid_encrypted_content") && !strings.Contains(streamErr.Error(), "thinking_signature_invalid")) {
						t.Errorf("signature failure not retained: %v", streamErr)
					}
					if !strings.Contains(output, "call_tool") || strings.Contains(output, "response.completed") {
						t.Errorf("tool output lost or failure became completion: %s", output)
					}
				})
			}
		}
	}
}

func TestCodexStreamCommittedPrefixSignatureBoundary(t *testing.T) {
	for _, transport := range []string{"http", "websocket", "downstream_websocket"} {
		for _, buffering := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/buffering_%t", transport, buffering), func(t *testing.T) {
				// Exceed the legacy 48-frame bootstrap by only a few frames, so
				// the second bootstrap has not filled its own 16-frame budget.
				events := make([]string, 50)
				for i := range events {
					events[i] = `{"type":"response.in_progress","response":{"id":"resp_rejected","status":"in_progress","output":[]}}`
				}
				calls, output, streamErr := runCodexStreamSignatureBoundary(t, transport, buffering, events)
				if calls != 1 || streamErr == nil || !strings.Contains(output, "resp_rejected") || strings.Contains(output, "resp_recovered") {
					t.Fatalf("committed prefix replayed: attempts=%d error=%v output=%s", calls, streamErr, output)
				}
			})
		}
	}
}

func TestCodexStreamSilentSignatureRecoveryStillWorks(t *testing.T) {
	for _, transport := range []string{"http", "websocket", "downstream_websocket"} {
		for _, buffering := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/buffering_%t", transport, buffering), func(t *testing.T) {
				scaffold := []string{`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[],"content":[]}}`}
				calls, output, streamErr := runCodexStreamSignatureBoundary(t, transport, buffering, scaffold)
				if calls != 2 || streamErr != nil || !strings.Contains(output, "resp_recovered") || strings.Contains(output, "resp_rejected") {
					t.Fatalf("silent recovery: attempts=%d error=%v output=%s", calls, streamErr, output)
				}
			})
		}
	}
}

// runCodexStreamSignatureBoundary drives real executors through local HTTP/WS
// peers. The first response rejects state; an accidental replay completes so the
// test fails immediately rather than relying on a clock or hanging on a read.
func runCodexStreamSignatureBoundary(t *testing.T, transport string, buffering bool, events []string) (int32, string, error) {
	t.Helper()
	const created = `{"type":"response.created","response":{"id":"resp_rejected","status":"in_progress","output":[]}}`
	const rejection = `{"type":"error","status":400,"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"foreign reasoning"}}`
	const completed = `{"type":"response.completed","response":{"id":"resp_recovered","status":"completed","output":[]}}`
	frames := append([]string{created}, events...)
	frames = append(frames, rejection)
	var calls atomic.Int32
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if transport == "http" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			if calls.Add(1) == 1 {
				for _, frame := range frames {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
				}
			} else {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", completed)
			}
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Logf("fixture close: %v", errClose)
			}
		}()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
			if calls.Add(1) == 1 {
				for _, frame := range frames {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
						return
					}
				}
			} else {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(completed))
				return
			}
		}
	}))
	defer srv.Close()
	cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering}}
	auth := newCodexSignatureTestAuth(srv.URL)
	auth.ID = t.Name()
	auth.Attributes[cliproxyauth.AttributeResponsesFirstOutputTimeoutSeconds] = "3600"
	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"role":"user","content":"Find the answer."}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true}
	ctx := context.Background()
	var result *cliproxyexecutor.StreamResult
	var err error
	if transport == "http" {
		result, err = NewCodexExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
	} else {
		if transport == "downstream_websocket" {
			ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
		}
		result, err = NewCodexWebsocketsExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
	}
	if err != nil {
		return calls.Load(), "", err
	}
	var output strings.Builder
	for chunk := range result.Chunks {
		output.Write(chunk.Payload)
		if chunk.Err != nil {
			err = chunk.Err
		}
	}
	return calls.Load(), output.String(), err
}

func TestCodexStreamBufferedToolFailureBoundary(t *testing.T) {
	for _, transport := range []string{"http", "websocket", "downstream_websocket"} {
		for _, toolType := range []string{"function_call", "custom_tool_call"} {
			for _, failure := range []string{"overload", "eof"} {
				t.Run(transport+"/"+toolType+"/"+failure, func(t *testing.T) {
					added := fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":%q,"id":"tool_committed","call_id":"call_committed","name":"lookup","arguments":"","input":"","status":"in_progress"}}`, toolType)
					events := []string{codexCreatedEvent, added}
					if failure == "overload" {
						events = append(events, codexOverloadEvent)
					}
					req, opts := codexTestRequest()
					var result *cliproxyexecutor.StreamResult
					var err error
					if transport == "http" {
						server := codexSSEServer(events...)
						defer server.Close()
						result, err = NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
					} else {
						server := codexWebsocketServer(t, events...)
						defer server.Close()
						ctx := context.Background()
						if transport == "downstream_websocket" {
							ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
						}
						result, err = NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(ctx, codexTestAuth(server.URL), req, opts)
					}
					if err != nil || result == nil {
						t.Fatalf("tool declaration became a retryable attempt failure: %v", err)
					}
					output, streamErr := drainChunks(result)
					if streamErr == nil || !strings.Contains(output, "call_committed") || strings.Contains(output, "response.completed") {
						t.Fatalf("tool declaration or terminal failure lost: %v %s", streamErr, output)
					}
				})
			}
		}
	}
}

func TestCodexStreamBufferedMetadataSignatureRecovery(t *testing.T) {
	for _, transport := range []string{"websocket", "downstream_websocket"} {
		for _, event := range []string{"codex.rate_limits", "codex.response.metadata"} {
			t.Run(transport+"/"+event, func(t *testing.T) {
				calls, output, err := runCodexStreamSignatureBoundary(t, transport, true, []string{fmt.Sprintf(`{"type":%q,"metadata":"rejected-metadata"}`, event)})
				if calls != 2 || err != nil || !strings.Contains(output, "resp_recovered") || strings.Contains(output, "resp_rejected") || strings.Contains(output, "rejected-metadata") {
					t.Fatalf("unpublished metadata blocked recovery or leaked: calls=%d error=%v output=%s", calls, err, output)
				}
			})
		}
	}
}

func TestCodexStreamSignatureRecoverySharedBudget(t *testing.T) {
	const rejection = `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"foreign reasoning"}}`
	for _, buffering := range []bool{false, true} {
		for _, mode := range []string{"repeated_sse", "http_then_sse", "retry_429", "managed"} {
			t.Run(fmt.Sprintf("buffering_%t/%s", buffering, mode), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := calls.Add(1)
					body, _ := io.ReadAll(r.Body)
					if r.Header.Get("Authorization") != "Bearer test" {
						t.Error("signature repair changed credential")
					}
					if attempt > 2 {
						t.Error("signature repair exceeded one retry")
					}
					if attempt == 2 && (strings.Contains(gjson.GetBytes(body, "input").Raw, "encrypted_content") || !strings.Contains(string(body), "Keep user history.")) {
						t.Errorf("retry state or portable history is wrong: %s", body)
					}
					if attempt == 1 && mode == "http_then_sse" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, rejection)
						return
					}
					if attempt == 2 && mode == "retry_429" {
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"retry quota exhausted"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"error\",\"status\":400,\"error\":%s}\n\n", gjson.Get(rejection, "error").Raw)
				}))
				defer server.Close()
				req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"role":"user","content":"Keep user history."}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true}
				if mode == "managed" {
					opts.ExecutionLifecycle = newTerminalFailureLifecycle()
				}
				result, err := NewCodexExecutor(codexBufferingConfig(buffering)).ExecuteStream(context.Background(), newCodexSignatureTestAuth(server.URL), req, opts)
				if err == nil {
					_, err = drainChunks(result)
				}
				want := int32(2)
				if mode == "managed" {
					want = 1
				}
				if err == nil || calls.Load() != want {
					t.Fatalf("attempts=%d want=%d error=%v", calls.Load(), want, err)
				}
				if mode == "retry_429" && (statusCodeFromTestError(t, err) != http.StatusTooManyRequests || !strings.Contains(err.Error(), "retry quota exhausted")) {
					t.Fatalf("retry error lost its status or body: %v", err)
				}
			})
		}
	}
}
