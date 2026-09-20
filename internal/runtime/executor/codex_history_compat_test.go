package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexAnyHistoryCompatibilityPreservesPortableHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			original := []byte(`{"model":"gpt-6-astra","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"https://example.test/image.png"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"existing summary"}],"content":[{"type":"reasoning_text","text":"preserved reasoning"}]},{"type":"web_search_call","id":"ws_history","status":"completed","action":{"type":"search","query":"history query","queries":["history query"]},"results":[{"url":"https://example.test/source"}]},{"type":"function_call","name":"fixture","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"retained result"},{"type":"message","role":"user","content":"continue"}]}`)
			before := bytes.Clone(original)
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				b, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(b, "input.1.content.#").Int() != 0 || gjson.GetBytes(b, "input.2.type").String() == "web_search_call" {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"type":"new_api_error","code":"invalid_responses_request","message":"invalid codex request"}}`)
					return
				}
				if gjson.GetBytes(b, "input.#").Int() != 6 {
					t.Errorf("history item count changed: %s", b)
				}
				if gjson.GetBytes(b, "input.1.summary.1.text").String() != "preserved reasoning" {
					t.Error("reasoning text lost")
				}
				history := gjson.GetBytes(b, "input.2.content.0.text").String()
				for _, part := range []string{"history query", "https://example.test/source", "ws_history"} {
					if !strings.Contains(history, part) {
						t.Errorf("search detail lost: %s", part)
					}
				}
				if gjson.GetBytes(b, "input.0.content.1.image_url").String() != "https://example.test/image.png" || gjson.GetBytes(b, "input.3.call_id").String() != "call_1" || gjson.GetBytes(b, "input.4.output").String() != "retained result" {
					t.Error("portable history mutated")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			defer upstream.Close()
			e := NewCodexExecutor(&config.Config{})
			auth := &coreauth.Auth{ID: "any-fixture", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "fixture-key", "base_url": "http://anyrouter.top/v1"}}
			req := coreexecutor.Request{Model: "gpt-6-astra", Payload: original}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				r, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for c := range r.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
				}
			} else {
				if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 1 {
				t.Errorf("calls=%d, want one normalized attempt", calls)
			}
			if !bytes.Equal(original, before) {
				t.Fatal("caller body mutated")
			}
		})
	}
}

func TestCodexAgentMessageOnlySignatureRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			original := []byte(`{"model":"gpt-6-astra","store":false,"input":[{"type":"reasoning","id":"rs_foreign","summary":[],"encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"type":"message","role":"user","content":"preserved user"},{"type":"function_call","name":"fixture","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"preserved tool"}]}`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				b, _ := io.ReadAll(r.Body)
				if calls == 1 {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":null,"param":"","message":"OpenAI Responses bad request: The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed. [trace_id=dc0409a2b66df568b4a9daa0d6b6d9c3]"}}`)
					return
				}
				if len(gjson.GetBytes(b, `input.#(type=="reasoning")#`).Array()) != 0 || gjson.GetBytes(b, "input.2.output").String() != "preserved tool" {
					t.Error("signature recovery changed portable history or retained invalid reasoning")
				}
				if r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("probe rotated key")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer upstream.Close()
			e := NewCodexExecutor(&config.Config{})
			auth := &coreauth.Auth{ID: "agent-fixture", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "fixture-key", "base_url": "http://agentrouter.org/v1"}}
			req := coreexecutor.Request{Model: "gpt-6-astra", Payload: original}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				r, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for c := range r.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
				}
			} else {
				if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatalf("calls=%d, want one repair with same key", calls)
			}
		})
	}
}

func TestCodexAgentSignatureRecoveryKeepsReadableReasoning(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if calls == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, agentMessageOnlySignatureRejection)
			return
		}
		if gjson.GetBytes(b, "input.0.summary.0.text").String() != "keep summary" || gjson.GetBytes(b, "input.0.summary.1.text").String() != "keep readable reasoning" || gjson.GetBytes(b, "input.0.content.#").Int() != 0 || gjson.GetBytes(b, "input.0.encrypted_content").Exists() || gjson.GetBytes(b, "input.0.id").Exists() {
			t.Error("rejected opaque reasoning discarded readable history")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedHistoryResponse)
	}))
	defer upstream.Close()
	payload := []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `","summary":[{"type":"summary_text","text":"keep summary"}],"content":[{"type":"reasoning_text","text":"keep readable reasoning"}]},{"role":"user","content":"continue"}]}`)
	a := &coreauth.Auth{ID: "agent-readable", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "fixture", "base_url": "http://agentrouter.org/v1"}}
	_, err := NewCodexExecutor(&config.Config{}).Execute(context.Background(), a, coreexecutor.Request{Model: "gpt-6-astra", Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

const agentMessageOnlySignatureRejection = `{"error":{"type":"invalid_request_error","code":null,"param":"","message":"OpenAI Responses bad request: The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed. [trace_id=fixture]"}}`
const agentResourceMismatchRejection = `{"error":{"type":"invalid_request_error","code":null,"param":"","message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`
const agentMaskedResourceMismatchRejection = `{"error":{"type":"invalid_request_error","param":"","message":"OpenAI Responses bad request: The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`
const completedHistoryResponse = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n"

func TestCodexAgentMessageOnlySSERecoveryBoundaries(t *testing.T) {
	for _, scenario := range []struct{ name, rejection string }{
		{"encrypted", agentMessageOnlySignatureRejection},
		{"resource", agentResourceMismatchRejection},
		{"masked-resource", agentMaskedResourceMismatchRejection},
	} {
		rejection := scenario.rejection
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"provisional", "text", "reasoning", "tool", "repeated"} {
				t.Run(fmt.Sprintf("%s/stream_%t/%s", scenario.name, stream, mode), func(t *testing.T) {
					calls := 0
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						w.Header().Set("Content-Type", "text/event-stream")
						if calls == 1 || mode == "repeated" {
							_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_rejected\"}}\n\n")
							kind := map[string]string{"text": "response.output_text.delta", "reasoning": "response.reasoning_summary_text.delta", "tool": "response.function_call_arguments.delta"}[mode]
							if kind != "" {
								_, _ = fmt.Fprintf(w, "data: {\"type\":%q,\"delta\":\"visible\"}\n\n", kind)
							}
							_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":%s}\n\n", rejection)
							return
						}
						_, _ = io.WriteString(w, completedHistoryResponse)
					}))
					defer upstream.Close()
					a := &coreauth.Auth{ID: "agent-sse", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "fixture", "base_url": "http://agentrouter.org/v1"}}
					req := coreexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"},{"role":"user","content":"continue"}]}`)}
					opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
					e := NewCodexExecutor(&config.Config{})
					failed := false
					if stream {
						r, err := e.ExecuteStream(context.Background(), a, req, opts)
						if err != nil {
							failed = true
						} else {
							for chunk := range r.Chunks {
								failed = failed || chunk.Err != nil
							}
						}
					} else {
						_, err := e.Execute(context.Background(), a, req, opts)
						failed = err != nil
					}
					want := 1
					if mode == "provisional" || mode == "repeated" {
						want = 2
					}
					if calls != want || failed != (mode != "provisional") {
						t.Fatalf("calls=%d want=%d failed=%t", calls, want, failed)
					}
				})
			}
		}
	}
}

func TestCodexWebsocketAnyAgentHistoryCompatibility(t *testing.T) {
	for _, route := range []struct {
		host     string
		source   sdktranslator.Format
		resource bool
		masked   bool
	}{
		{"anyrouter.top", sdktranslator.FormatOpenAIResponse, false, false},
		{"agentrouter.org", sdktranslator.FormatOpenAIResponse, false, false},
		{"agentrouter.org", sdktranslator.FormatOpenAIResponse, true, false},
		{"agentrouter.org", sdktranslator.FormatOpenAIResponse, true, true},
		{"anyrouter.top", sdktranslator.FromString(codexOpenAIImageSourceFormat), false, false},
	} {
		host := route.host
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/resource_%t/masked_%t/stream_%t", host, route.source, route.resource, route.masked, stream), func(t *testing.T) {
				var calls atomic.Int32
				upgrader := websocket.Upgrader{}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					for {
						_, b, errRead := conn.ReadMessage()
						if errRead != nil {
							return
						}
						attempt := calls.Add(1)
						if route.source.String() == codexOpenAIImageSourceFormat {
							if gjson.GetBytes(b, "input.0.type").String() != "web_search_call" {
								t.Error("direct image source history was normalized")
							}
						} else if host == "anyrouter.top" {
							if gjson.GetBytes(b, "input.0.content.0.text").String() == "" || gjson.GetBytes(b, "input.0.type").String() == "web_search_call" {
								t.Error("websocket lost search history compatibility")
							}
						} else if attempt == 1 {
							rejection := agentMessageOnlySignatureRejection
							if route.resource {
								rejection = agentResourceMismatchRejection
								if route.masked {
									rejection = agentMaskedResourceMismatchRejection
								}
							}
							_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,`+strings.TrimPrefix(rejection, "{")))
							continue
						} else if gjson.GetBytes(b, "input.0.type").String() == "reasoning" {
							t.Error("websocket retained rejected reasoning")
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[]}}`))
						return
					}
				}))
				defer upstream.Close()
				// CONNECT stays local while the executor retains the real routing hostname.
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodConnect {
						t.Error("expected CONNECT")
						w.WriteHeader(400)
						return
					}
					target, err := net.Dial("tcp", strings.TrimPrefix(upstream.URL, "http://"))
					if err != nil {
						t.Error(err)
						return
					}
					downstream, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						target.Close()
						return
					}
					_, _ = io.WriteString(downstream, "HTTP/1.1 200 Connection Established\r\n\r\n")
					go func() { _, _ = io.Copy(target, downstream); _ = target.Close() }()
					go func() { _, _ = io.Copy(downstream, target); _ = downstream.Close() }()
				}))
				defer proxy.Close()
				first := `{"type":"web_search_call","status":"completed","action":{"type":"search","query":"preserved"}}`
				if host == "agentrouter.org" {
					first = `{"type":"reasoning","id":"rs_foreign","encrypted_content":"` + validCodexReasoningEncryptedContentForTest() + `"}`
				}
				req := coreexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[` + first + `,{"role":"user","content":"continue"}]}`)}
				a := &coreauth.Auth{ID: "websocket-history", Provider: "codex", ProxyURL: proxy.URL, Attributes: map[string]string{"api_key": "fixture", "base_url": "http://" + host + "/v1"}}
				e := NewCodexWebsocketsExecutor(&config.Config{})
				opts := coreexecutor.Options{SourceFormat: route.source, Stream: stream}
				if stream {
					r, err := e.ExecuteStream(context.Background(), a, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for c := range r.Chunks {
						if c.Err != nil {
							t.Fatal(c.Err)
						}
					}
				} else {
					if _, err := e.Execute(context.Background(), a, req, opts); err != nil {
						t.Fatal(err)
					}
				}
				want := int32(1)
				if host == "agentrouter.org" {
					want = 2
				}
				if calls.Load() != want {
					t.Fatalf("calls=%d want=%d", calls.Load(), want)
				}
			})
		}
	}
}
