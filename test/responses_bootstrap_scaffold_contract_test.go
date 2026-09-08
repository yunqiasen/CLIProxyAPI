package test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestResponsesFailoverAfterEmptyReasoningScaffold(t *testing.T) {
	for _, tc := range []struct{ name, prelude string }{
		{"lifecycle_only", `data: {"type":"response.created","response":{"id":"failed_attempt","status":"in_progress","output":[]}}` + "\n\n"},
		{"empty_reasoning_item", `data: {"type":"response.created","response":{"id":"failed_attempt","status":"in_progress","output":[]}}` + "\n\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_empty","type":"reasoning","summary":[]}}` + "\n\n"},
		{"opaque_reasoning_scaffold", `data: {"type":"response.created","response":{"id":"failed_attempt","status":"in_progress","output":[]}}` + "\n\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_empty","type":"reasoning","summary":[],"content":[],"encrypted_content":"gAAAA-fixture-opaque-state"}}` + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertResponsesBootstrapRecovery(t, tc.prelude+`data: {"type":"response.failed","response":{"id":"failed_attempt","status":"failed","error":{"code":"server_error","message":"Our servers are currently overloaded. Please try again later."},"output":[]}}`+"\n\n")
		})
	}
}

func assertResponsesBootstrapRecovery(t *testing.T, firstFrames string) {
	t.Helper()
	var mu sync.Mutex
	var attempts []string
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
		mu.Lock()
		attempts = append(attempts, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if key == "primary" {
			_, _ = io.WriteString(w, firstFrames)
			return
		}
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"healthy_attempt","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"healthy key answer"}]}]}}`+"\n\n")
	}), []string{"primary", "secondary"})
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
	resp, err := fixture.server.Client().Post(fixture.server.URL+"/v1/responses", "application/json", strings.NewReader(string(payload)))
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
	mu.Lock()
	got := append([]string(nil), attempts...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []string{"primary", "secondary"}) || strings.Count(string(body), `"type":"response.completed"`) != 1 || strings.Contains(string(body), `"type":"response.failed"`) || !strings.Contains(string(body), "healthy key answer") || strings.Contains(string(body), "failed_attempt") {
		t.Fatalf("no completed answer despite healthy credential: attempts=%v status=%d body=%s", got, resp.StatusCode, body)
	}
	item := fixture.waitForLog(t)
	if !item.Get("success").Bool() || item.Get("auth_id").String() != t.Name()+"-secondary" || item.Get("has_error").Bool() {
		t.Fatalf("request log does not describe recovered attempt: %s", item.Raw)
	}
}

func TestResponsesRecoversPrematureEOFBeforeOutput(t *testing.T) {
	for _, tc := range []struct{ name, frames string }{
		{"empty_stream", ""},
		{"lifecycle_only", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"failed_attempt\",\"output\":[]}}\n\n"},
		{"reasoning_scaffold", "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"gAAAA-fixture-state\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertResponsesBootstrapRecovery(t, tc.frames)
		})
	}
}

func TestResponsesDoesNotReplayAfterRealOutput(t *testing.T) {
	for _, tc := range []struct{ name, event, marker string }{
		{"text", `{"type":"response.output_text.delta","delta":"partial answer"}`, "partial answer"},
		{"reasoning_text", `{"type":"response.reasoning_summary_text.delta","delta":"partial thought"}`, "partial thought"},
		{"reasoning_item_with_text", `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[{"type":"summary_text","text":"partial thought"}]}}`, "partial thought"},
		{"reasoning_item_done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[],"encrypted_content":"gAAAA-completed-state"}}`, "gAAAA-completed-state"},
		{"function_start", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","name":"exec_command","call_id":"call_fixture","arguments":""}}`, "call_fixture"},
		{"function_arguments", `{"type":"response.function_call_arguments.delta","delta":"partial arguments"}`, "partial arguments"},
		{"native_search", `{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"search_fixture","status":"in_progress"}}`, "search_fixture"},
	} {
		for _, failure := range []string{"overload", "EOF"} {
			t.Run(tc.name+"/"+failure, func(t *testing.T) {
				var mu sync.Mutex
				var attempts []string
				fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
					mu.Lock()
					attempts = append(attempts, key)
					mu.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					if key != "primary" {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"wrong_replay\",\"output\":[]}}\n\n")
						return
					}
					_, _ = io.WriteString(w, "data: "+tc.event+"\n\n")
					if failure == "overload" {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"upstream overloaded\"}}}\n\n")
					}
				}), []string{"primary", "secondary"})
				payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
				req, err := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/responses", strings.NewReader(string(payload)))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
				resp, err := fixture.server.Client().Do(req)
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
				mu.Lock()
				got := append([]string(nil), attempts...)
				mu.Unlock()
				if !reflect.DeepEqual(got, []string{"primary"}) || strings.Contains(string(body), `"type":"response.completed"`) || strings.Count(string(body), `"type":"response.failed"`) != 1 || !strings.Contains(string(body), tc.marker) {
					t.Fatalf("partial output was lost or replayed: attempts=%v body=%s", got, body)
				}
				item := fixture.waitForLog(t)
				if item.Get("success").Bool() || !item.Get("has_error").Bool() || item.Get("auth_id").String() != t.Name()+"-primary" {
					t.Fatalf("partial failure attribution lost: %s", item.Raw)
				}
			})
		}
	}
}

func TestResponsesWebsocketRecoversBeforeOutput(t *testing.T) {
	for _, failure := range []string{"overload", "EOF"} {
		t.Run(failure, func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
				mu.Lock()
				attempts = append(attempts, key)
				mu.Unlock()
				if key == "secondary" {
					budgetFailoverUpstream().ServeHTTP(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"failed_attempt\",\"output\":[]}}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"gAAAA-fixture-state\"}}\n\n")
				if failure == "overload" {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"upstream overloaded\"}}}\n\n")
				}
			}), []string{"primary", "secondary"})
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for {
				_, raw, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("healthy credential not reached before disconnect: %v", errRead)
				}
				kind := gjson.GetBytes(raw, "type").String()
				if kind == "error" || kind == "response.failed" || strings.Contains(string(raw), "failed_attempt") {
					t.Fatalf("provisional failure leaked into the recovered turn: %s", raw)
				}
				output.WriteString(gjson.GetBytes(raw, "delta").String())
				if kind == "response.completed" {
					break
				}
			}
			mu.Lock()
			got := append([]string(nil), attempts...)
			mu.Unlock()
			if !reflect.DeepEqual(got, []string{"primary", "secondary"}) || output.String() != "second key answer" {
				t.Fatalf("wrong recovered attempt or output: attempts=%v output=%q", got, output.String())
			}
			if err = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Time{}); err != nil {
				t.Fatal(err)
			}
			if _, raw, errRead := conn.ReadMessage(); errRead == nil {
				t.Fatalf("extra event after completion: %s", raw)
			}
			item := fixture.waitForLog(t)
			if !item.Get("success").Bool() || item.Get("has_error").Bool() || item.Get("auth_id").String() != t.Name()+"-secondary" {
				t.Fatalf("recovered websocket attribution lost: %s", item.Raw)
			}
		})
	}
}

func TestResponsesPreservesReasoningScaffoldOnSuccess(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ok\",\"output\":[]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_preserved\",\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"gAAAA-preserved-state\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_preserved\",\"delta\":\"thinking\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n")
	}), []string{"primary"})
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
	resp, err := fixture.server.Client().Post(fixture.server.URL+"/v1/responses", "application/json", strings.NewReader(string(payload)))
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
	var types []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "data: ") {
			types = append(types, gjson.Get(strings.TrimPrefix(line, "data: "), "type").String())
		}
	}
	if !reflect.DeepEqual(types, []string{"response.created", "response.output_item.added", "response.reasoning_summary_text.delta", "response.completed"}) || !strings.Contains(string(body), "rs_preserved") || !strings.Contains(string(body), "gAAAA-preserved-state") {
		t.Fatalf("successful reasoning history was dropped or reordered: %s", body)
	}
}

func TestResponsesExhaustedEOFRemainsFailure(t *testing.T) {
	var mu sync.Mutex
	var attempts []string
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		attempts = append(attempts, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"no_output\",\"output\":[]}}\n\n")
	}), []string{"primary", "secondary"})
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
	resp, err := fixture.server.Client().Post(fixture.server.URL+"/v1/responses", "application/json", strings.NewReader(string(payload)))
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
	mu.Lock()
	got := append([]string(nil), attempts...)
	mu.Unlock()
	if resp.StatusCode != http.StatusBadGateway || !reflect.DeepEqual(got, []string{"primary", "secondary"}) || !strings.Contains(gjson.GetBytes(body, "error.message").String(), "stream closed before response.completed") || strings.Contains(string(body), `"type":"response.completed"`) {
		t.Fatalf("exhausted EOF was hidden or retried indefinitely: status=%d attempts=%v body=%s", resp.StatusCode, got, body)
	}
	item := fixture.waitForLog(t)
	if item.Get("success").Bool() || !item.Get("has_error").Bool() || item.Get("status").Int() != http.StatusBadGateway || item.Get("auth_id").String() != t.Name()+"-secondary" {
		t.Fatalf("exhausted request log is inaccurate: %s", item.Raw)
	}
}
