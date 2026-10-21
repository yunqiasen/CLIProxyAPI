package test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
)

func observationRecords(t *testing.T, s *requestlog.Store, count int) []requestlog.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows := s.Changes(0).Records
		ready := len(rows) == count
		for _, r := range rows {
			if r.State == requestlog.Active {
				ready = false
			}
		}
		if ready {
			return rows
		}
		runtime.Gosched()
	}
	t.Fatalf("expected %d terminal records, got %+v", count, s.Changes(0))
	return nil
}

func TestRequestLogObservationHTTPRetryKeepsSuccessAndAttempts(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, budgetFailoverUpstream(), []string{"primary", "secondary"})
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
	resp, err := fixture.server.Client().Post(fixture.server.URL+"/v1/responses", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || !strings.Contains(string(body), "second key answer") {
		t.Fatalf("response: %s %v", body, err)
	}
	rows := observationRecords(t, fixture.observations, 1)
	r := rows[0]
	if r.State != requestlog.Succeeded || !r.Recovered || len(r.Attempts) != 2 {
		t.Fatalf("request retry observation: %+v", r)
	}
	if r.Attempts[0].ErrorKind != "quota" || r.Attempts[0].State != requestlog.Failed || r.Attempts[1].AuthID != t.Name()+"-secondary" {
		t.Fatalf("route: %+v", r.Attempts)
	}
	if r.ID == r.TraceID {
		t.Fatal("execution ID conflated with transport trace")
	}
}

func TestRequestLogObservationWebsocketTurnsNotIdleConnection(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}), []string{"primary"})
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if len(fixture.observations.Changes(0).Records) != 0 {
		t.Fatal("idle WS counted as active")
	}
	for turn := 1; turn <= 2; turn++ {
		if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
			t.Fatal(err)
		}
		for {
			_, raw, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatal(errRead)
			}
			if strings.Contains(string(raw), "response.completed") {
				break
			}
		}
		rows := observationRecords(t, fixture.observations, turn)
		for _, r := range rows {
			if r.State != requestlog.Succeeded || len(r.Attempts) != 1 {
				t.Fatalf("WS request: %+v", r)
			}
		}
	}
	rows := fixture.observations.Changes(0).Records
	if rows[0].ID == rows[1].ID || rows[0].TraceID != rows[1].TraceID {
		t.Fatalf("WS identity: %+v", rows)
	}
}

func TestRequestLogObservationActiveThenCanceled(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}), []string{"primary"})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
	req, err := http.NewRequestWithContext(ctx, "POST", fixture.server.URL+"/v1/responses", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, errDo := fixture.server.Client().Do(req)
		if errDo == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture did not receive upstream request")
	}
	rows := fixture.observations.Changes(0).Records
	if len(rows) != 1 || rows[0].State != requestlog.Active || len(rows[0].Attempts) != 1 {
		t.Fatalf("active request: %+v", rows)
	}
	id := rows[0].ID
	cancel()
	<-done
	terminal := observationRecords(t, fixture.observations, 1)[0]
	if terminal.ID != id || terminal.State != requestlog.Canceled || terminal.Recovered {
		t.Fatalf("canceled request: %+v", terminal)
	}
}
