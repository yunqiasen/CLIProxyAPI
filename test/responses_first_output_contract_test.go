package test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func firstOutputTimeoutConfig(t *testing.T) func(*config.Config) {
	t.Helper()
	return func(cfg *config.Config) {
		if err := yaml.Unmarshal([]byte("codex-api-key:\n  - responses-first-output-timeout-seconds: 1\n"), cfg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResponsesFirstOutputTimeoutRecoversMissingHeaders(t *testing.T) {
	assertResponsesFirstOutputRecovery(t, "", true)
}

func TestResponsesFirstOutputTimeoutRecoversNonstream(t *testing.T) {
	assertResponsesFirstOutputRecovery(t, "", false)
}

func assertResponsesFirstOutputRecovery(t *testing.T, prelude string, stream bool) {
	t.Helper()
	var primary, secondary atomic.Int32
	canceled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") == "Bearer fixture-primary" {
			primary.Add(1)
			if prelude != "" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, prelude)
				w.(http.Flusher).Flush()
			}
			select {
			case <-r.Context().Done():
				close(canceled)
			case <-release:
			}
			return
		}
		secondary.Add(1)
		budgetFailoverUpstream().ServeHTTP(w, r)
	}), []string{"primary", "secondary"}, firstOutputTimeoutConfig(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": stream})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.server.URL+"/v1/responses", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := fixture.server.Client().Do(req)
	if err != nil {
		t.Fatalf("silent upstream still stalls instead of using a healthy key: %v", err)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if errRead != nil || resp.StatusCode != 200 || (stream && strings.Count(string(body), `"type":"response.completed"`) != 1) || !strings.Contains(string(body), "second key answer") || primary.Load() != 1 || secondary.Load() != 1 {
		t.Fatalf("missing bounded recovery: primary=%d secondary=%d status=%d body=%s err=%v", primary.Load(), secondary.Load(), resp.StatusCode, body, errRead)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("timed-out upstream attempt stayed connected")
	}
	item := fixture.waitForLog(t)
	if !item.Get("success").Bool() || item.Get("has_error").Bool() || item.Get("auth_id").String() != t.Name()+"-secondary" {
		t.Fatalf("timeout recovery blamed the wrong credential: %s", item.Raw)
	}
}

func TestResponsesFirstOutputTimeoutWebsocketExhaustion(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	defer close(release)
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		attempts.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}), []string{"primary", "secondary"}, firstOutputTimeoutConfig(t))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("all silent keys ended in another unexplained disconnect: %v", err)
	}
	if gjson.GetBytes(raw, "type").String() != "error" || gjson.GetBytes(raw, "status").Int() != 504 || gjson.GetBytes(raw, "error.code").String() != "upstream_response_timeout" || attempts.Load() != 2 {
		t.Fatalf("bounded timeout reason lost: attempts=%d event=%s", attempts.Load(), raw)
	}
	if _, raw, err = conn.ReadMessage(); err == nil {
		t.Fatalf("extra event after exhausted timeout: %s", raw)
	}
	item := fixture.waitForLog(t)
	if item.Get("success").Bool() || !item.Get("has_error").Bool() || item.Get("status").Int() != 504 || item.Get("auth_id").String() != t.Name()+"-secondary" || !strings.Contains(item.Get("error_preview").String(), "first-output wait limit") {
		t.Fatalf("timeout log lost reason or final credential: %s", item.Raw)
	}
}

func TestResponsesFirstOutputTimeoutIgnoresProvisionalScaffolding(t *testing.T) {
	for _, prelude := range []struct{ name, frames string }{
		{"created", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"silent_attempt\",\"output\":[]}}\n\n"},
		{"opaque_reasoning", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"gAAAA-provisional-state\"}}\n\n"},
	} {
		for _, stream := range []bool{true, false} {
			mode := "stream"
			if !stream {
				mode = "nonstream"
			}
			t.Run(prelude.name+"/"+mode, func(t *testing.T) {
				assertResponsesFirstOutputRecovery(t, prelude.frames, stream)
			})
		}
	}
}

func TestResponsesFirstOutputTimeoutStopsAfterRealWork(t *testing.T) {
	for _, event := range []struct{ name, body string }{
		{"text", `{"type":"response.output_text.delta","delta":"working"}`},
		{"reasoning", `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`},
		{"tool", `{"type":"response.output_item.added","item":{"type":"function_call","name":"exec_command","call_id":"call_once","arguments":""}}`},
		{"native_search", `{"type":"response.output_item.added","item":{"type":"web_search_call","id":"search_once","status":"in_progress"}}`},
	} {
		for _, stream := range []bool{true, false} {
			mode := "stream"
			if !stream {
				mode = "nonstream"
			}
			t.Run(event.name+"/"+mode, func(t *testing.T) {
				var attempts atomic.Int32
				fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					attempts.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+event.body+"\n\n")
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						return
					case <-time.After(1200 * time.Millisecond):
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"same_attempt\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"slow complete answer\"}]}]}}\n\n")
				}), []string{"primary", "secondary"}, firstOutputTimeoutConfig(t))
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				status, body := fixture.requestResponses(t, ctx, stream)
				if status != 200 || !strings.Contains(string(body), "slow complete answer") || strings.Contains(string(body), "upstream_response_timeout") || attempts.Load() != 1 {
					t.Fatalf("first-output wait cut off or replayed active work: attempts=%d status=%d body=%s", attempts.Load(), status, body)
				}
				if stream && !strings.Contains(string(body), event.body) {
					t.Fatalf("real output was lost: %s", body)
				}
			})
		}
	}
}

func (f *responsesQuotaFixture) requestResponses(t *testing.T, ctx context.Context, stream bool) (int, []byte) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"model": f.model, "input": "hello", "stream": stream})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if errRead != nil {
		t.Fatal(errRead)
	}
	return resp.StatusCode, body
}

func TestResponsesFirstOutputTimeoutHTTPExhaustion(t *testing.T) {
	for _, stream := range []bool{true, false} {
		mode := "stream"
		if !stream {
			mode = "nonstream"
		}
		t.Run(mode, func(t *testing.T) {
			var attempts atomic.Int32
			release := make(chan struct{})
			defer close(release)
			fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"silent_attempt\",\"output\":[]}}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}), []string{"primary", "secondary"}, firstOutputTimeoutConfig(t))
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			status, body := fixture.requestResponses(t, ctx, stream)
			if status != 504 || gjson.GetBytes(body, "error.code").String() != "upstream_response_timeout" || attempts.Load() != 2 || strings.Contains(string(body), "response.completed") || strings.Contains(string(body), "silent_attempt") {
				t.Fatalf("exhausted timeout lost status or replay bound: attempts=%d status=%d body=%s", attempts.Load(), status, body)
			}
			item := fixture.waitForLog(t)
			if item.Get("success").Bool() || !item.Get("has_error").Bool() || item.Get("status").Int() != 504 || item.Get("auth_id").String() != t.Name()+"-secondary" {
				t.Fatalf("wrong exhausted timeout log: %s", item.Raw)
			}
		})
	}
}

func TestResponsesFirstOutputTimeoutPreservesCallerCancellation(t *testing.T) {
	for _, scaffold := range []bool{false, true} {
		name := "waiting_headers"
		if scaffold {
			name = "waiting_output"
		}
		t.Run(name, func(t *testing.T) {
			received := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			var attempts atomic.Int32
			fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if attempts.Add(1) != 1 {
					return
				}
				if scaffold {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"output\":[]}}\n\n")
					w.(http.Flusher).Flush()
				}
				close(received)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
			}), []string{"primary", "secondary"}, firstOutputTimeoutConfig(t))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.server.URL+"/v1/responses", strings.NewReader(string(payload)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			done := make(chan error, 1)
			go func() {
				resp, errRequest := fixture.server.Client().Do(req)
				if resp != nil {
					_ = resp.Body.Close()
				}
				done <- errRequest
			}()
			select {
			case <-received:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream did not receive request")
			}
			cancel()
			select {
			case <-canceled:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("caller cancellation did not stop the upstream immediately")
			}
			select {
			case err = <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("caller cancellation changed to another error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("caller remained stuck after cancellation")
			}
			item := fixture.waitForLog(t)
			if attempts.Load() != 1 || strings.Contains(item.Raw, "upstream_response_timeout") {
				t.Fatalf("caller cancellation became timeout/retry: attempts=%d item=%s", attempts.Load(), item.Raw)
			}
		})
	}
}

func TestResponsesFirstOutputTimeoutIsOptIn(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(1200 * time.Millisecond)
		budgetFailoverUpstream().ServeHTTP(w, r)
	}), []string{"secondary"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, body := fixture.requestResponses(t, ctx, true)
	if status != 200 || !strings.Contains(string(body), "second key answer") {
		t.Fatalf("unconfigured provider acquired a deadline: status=%d body=%s", status, body)
	}
}

func TestResponsesFirstOutputTimeoutSurvivesHeartbeatBufferBound(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Repeat("data: {\"type\":\"keepalive\"}\n\n", 20))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}), []string{"primary"}, firstOutputTimeoutConfig(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, body := fixture.requestResponses(t, ctx, true)
	if !strings.Contains(string(body), "upstream_response_timeout") || strings.Contains(string(body), "response.completed") {
		t.Fatalf("heartbeats disabled the first-output wait or fabricated completion: %s", body)
	}
}

func TestResponsesFirstOutputTimeoutWebsocketRecovery(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	defer close(release)
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		attempts.Add(1)
		if r.Header.Get("Authorization") == "Bearer fixture-primary" {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		budgetFailoverUpstream().ServeHTTP(w, r)
	}), []string{"primary", "secondary"}, firstOutputTimeoutConfig(t))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for {
		_, raw, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("timeout did not reach healthy websocket attempt: %v", errRead)
		}
		kind := gjson.GetBytes(raw, "type").String()
		if kind == "error" || kind == "response.failed" {
			t.Fatalf("timeout leaked before healthy credential was tried: %s", raw)
		}
		text.WriteString(gjson.GetBytes(raw, "delta").String())
		if kind == "response.completed" {
			break
		}
	}
	if text.String() != "second key answer" || attempts.Load() != 2 {
		t.Fatalf("wrong websocket recovery: attempts=%d text=%q", attempts.Load(), text.String())
	}
	if err = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, raw, errRead := conn.ReadMessage(); errRead == nil {
		t.Fatalf("extra event after closing completed turn: %s", raw)
	}
	item := fixture.waitForLog(t)
	if !item.Get("success").Bool() || item.Get("has_error").Bool() || item.Get("auth_id").String() != t.Name()+"-secondary" {
		t.Fatalf("websocket recovery lost final credential: %s", item.Raw)
	}
}
