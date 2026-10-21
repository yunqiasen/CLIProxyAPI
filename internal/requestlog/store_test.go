package requestlog_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
)

func TestLogicalRequestKeepsOrderedSameKeyRecovery(t *testing.T) {
	s := requestlog.NewStore(8)
	r := s.Begin(requestlog.Record{ID: "call", TraceID: "transport", Model: "alias", Timestamp: time.Unix(1, 0)})
	r.SelectRoute(requestlog.Attempt{Provider: "site-A", AuthID: "key-1", Model: "upstream"})
	r.BeginAttempt(requestlog.Attempt{Provider: "site-A", AuthID: "key-1", Model: "upstream", URL: "https://user:secret@host/v1/responses?api_key=secret"})
	r.Response(429)
	r.SelectRoute(requestlog.Attempt{Provider: "site-B", AuthID: "key-2"})
	r.BeginAttempt(requestlog.Attempt{Provider: "site-B", AuthID: "key-2"})
	r.Response(400)
	r.Failure(errors.New("The requested item was created under a different Azure OpenAI resource"))
	r.BeginAttempt(requestlog.Attempt{Provider: "site-B", AuthID: "key-2"})
	r.Response(200)
	r.Output()
	active, ok := s.Get("call")
	if !ok || active.State != requestlog.Active || active.Phase != "responding" || len(active.Attempts) != 3 {
		t.Fatalf("active: %+v", active)
	}
	if active.Attempts[0].URL != "https://host/v1/responses" {
		t.Fatal("URL credentials leaked")
	}
	r.Finish(requestlog.Succeeded, 200, nil)
	got, _ := s.Get("call")
	if got.ID != "call" || got.State != requestlog.Succeeded || !got.Recovered || got.ErrorKind != "" {
		t.Fatalf("terminal: %+v", got)
	}
	if got.Attempts[0].ErrorKind != "rate_limit" || got.Attempts[1].ErrorKind != "compatibility" || got.Attempts[2].State != requestlog.Succeeded {
		t.Fatalf("attempts: %+v", got.Attempts)
	}
	r.Failure(errors.New("late error"))
	r.Finish(requestlog.Canceled, 499, context.Canceled)
	again, _ := s.Get("call")
	if again.Version != got.Version {
		t.Fatal("late events mutated terminal request")
	}
	got.Attempts[0].AuthID = "mutated"
	again, _ = s.Get("call")
	if again.Attempts[0].AuthID != "key-1" {
		t.Fatal("returned slice aliases store")
	}
}

func TestContextSeparatesWebsocketCallsAndSharesNestedExecution(t *testing.T) {
	s := requestlog.NewStore(8)
	root := requestlog.WithScope(context.Background(), s, requestlog.Scope{TraceID: "ws", URL: "/v1/responses", Method: "GET"})
	first, owner1 := requestlog.Start(root, "first", "alias")
	nested, ownerNested := requestlog.Start(first, "nested", "other")
	if ownerNested != nil || requestlog.FromContext(nested) != requestlog.FromContext(first) {
		t.Fatal("nested execution owns a duplicate logical request")
	}
	owner1.Finish(requestlog.Succeeded, 200, nil)
	second, owner2 := requestlog.Start(root, "second", "alias")
	requestlog.FromContext(second).BeginAttempt(requestlog.Attempt{Provider: "site"})
	owner2.Finish(requestlog.Canceled, 499, context.Canceled)
	changes := s.Changes(0)
	if len(changes.Records) != 2 {
		t.Fatalf("calls: %+v", changes)
	}
	a, _ := s.Get("first")
	b, _ := s.Get("second")
	if a.State != requestlog.Succeeded || b.State != requestlog.Canceled || a.TraceID != b.TraceID {
		t.Fatalf("calls: %+v %+v", a, b)
	}
}

func TestBoundedObservationNeverEvictsActiveAndReportsLoss(t *testing.T) {
	s := requestlog.NewStore(2)
	live := s.Begin(requestlog.Record{ID: "live"})
	done := s.Begin(requestlog.Record{ID: "done"})
	done.Finish(requestlog.Succeeded, 200, nil)
	cursor := s.Changes(0).Version
	s.Begin(requestlog.Record{ID: "new"})
	if _, ok := s.Get("live"); !ok {
		t.Fatal("active request evicted")
	}
	if _, ok := s.Get("done"); ok {
		t.Fatal("terminal request not evicted")
	}
	dropped := s.Begin(requestlog.Record{ID: "overflow"})
	dropped.Output()
	dropped.Finish(requestlog.Succeeded, 200, nil)
	changes := s.Changes(cursor)
	if changes.Dropped != 1 || changes.Reset {
		t.Fatalf("loss not reported: %+v", changes)
	}
	live.Finish(requestlog.Failed, 503, errors.New("no auth available"))
}

func TestConcurrentObservationAndSnapshots(t *testing.T) {
	s := requestlog.NewStore(1)
	r := s.Begin(requestlog.Record{ID: "race"})
	r.BeginAttempt(requestlog.Attempt{Provider: "site"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				r.Output()
				s.Changes(0)
				s.Get("race")
			}
		}()
	}
	wg.Wait()
	r.Finish(requestlog.Succeeded, 200, nil)
	got, _ := s.Get("race")
	if got.State != requestlog.Succeeded {
		t.Fatal(got.State)
	}
}

func TestAttemptContextsKeepLateCallbacksOnTheirOriginalRoute(t *testing.T) {
	s := requestlog.NewStore(8)
	root := requestlog.WithScope(context.Background(), s, requestlog.Scope{TraceID: "transport"})
	ctx, owner := requestlog.Start(root, "interleaved", "alias")
	first := requestlog.FromContext(requestlog.WithAttempt(ctx))
	first.SelectRoute(requestlog.Attempt{Provider: "site-A", AuthID: "key-1"})
	first.BeginAttempt(requestlog.Attempt{Provider: "site-A", AuthID: "key-1"})
	first.Response(200)
	second := requestlog.FromContext(requestlog.WithAttempt(ctx))
	second.SelectRoute(requestlog.Attempt{Provider: "site-B", AuthID: "key-2"})
	second.BeginAttempt(requestlog.Attempt{Provider: "site-B", AuthID: "key-2"})
	second.Response(200)
	second.Output()
	first.Response(429)
	first.Failure(errors.New("rate_limit_exceeded"))
	first.Output()
	active, _ := s.Get("interleaved")
	if active.Provider != "site-B" || active.AuthID != "key-2" || active.Phase != "responding" {
		t.Fatalf("late callback replaced current route: %+v", active)
	}
	if active.Attempts[0].Status != 429 || active.Attempts[0].ErrorKind != "rate_limit" || active.Attempts[1].State != requestlog.Active || active.Attempts[1].Status != 200 {
		t.Fatalf("callbacks mixed attempts: %+v", active.Attempts)
	}
	owner.Finish(requestlog.Succeeded, 200, nil)
	got, _ := s.Get("interleaved")
	if got.State != requestlog.Succeeded || !got.Recovered || got.Attempts[1].State != requestlog.Succeeded {
		t.Fatalf("final outcome: %+v", got)
	}
}

func TestObservationBoundsKeepUTF8AndProtectExistingID(t *testing.T) {
	s := requestlog.NewStore(4)
	prefix := strings.Repeat("x", 128)
	s.Begin(requestlog.Record{ID: prefix + "first", Model: "original"})
	s.Begin(requestlog.Record{ID: prefix + "second", Model: "replacement"})
	got, _ := s.Get(prefix)
	if got.Model != "original" {
		t.Fatalf("normalized ID replaced an active record: %+v", got)
	}
	r := s.Begin(requestlog.Record{ID: "unicode", Model: strings.Repeat("模型", 300)})
	r.BeginAttempt(requestlog.Attempt{Provider: strings.Repeat("供应商", 300)})
	got, _ = s.Get("unicode")
	if len(got.Model) > 256 || len(got.Provider) > 256 || !utf8.ValidString(got.Model) || !utf8.ValidString(got.Provider) {
		t.Fatalf("metadata exceeded byte bounds or broke UTF8: model=%d provider=%d", len(got.Model), len(got.Provider))
	}
}

func TestTerminalUpstreamErrorIsNotReportedAsSuccessful(t *testing.T) {
	s := requestlog.NewStore(1)
	r := s.Begin(requestlog.Record{ID: "explicit-error"})
	r.BeginAttempt(requestlog.Attempt{Provider: "site"})
	r.Response(200)
	r.Failure(errors.New("encrypted content is invalid"))
	r.Finish(requestlog.Succeeded, 200, nil)
	got, _ := s.Get("explicit-error")
	if got.State != requestlog.Failed || got.ErrorKind != "compatibility" || got.Recovered {
		t.Fatalf("explicit terminal error hidden: %+v", got)
	}
}

func TestTruncatedAttemptHistoryPreservesRecoveryAndTotal(t *testing.T) {
	s := requestlog.NewStore(1)
	r := s.Begin(requestlog.Record{ID: "many-attempts"})
	for range 128 {
		r.BeginAttempt(requestlog.Attempt{Provider: "site"})
		r.Response(200)
	}
	r.Failure(errors.New("connection reset"))
	r.BeginAttempt(requestlog.Attempt{Provider: "site"})
	r.Response(200)
	r.Finish(requestlog.Succeeded, 200, nil)
	got, _ := s.Get("many-attempts")
	if len(got.Attempts) != 128 || !got.AttemptsTruncated || !got.Recovered || got.Attempts[127].Index != 129 {
		t.Fatalf("truncation lost request outcome: count=%d recovered=%v last=%+v", len(got.Attempts), got.Recovered, got.Attempts[len(got.Attempts)-1])
	}
}

func TestEvictingPersistedTerminalRecordDoesNotResetWriterCursor(t *testing.T) {
	s := requestlog.NewStore(1)
	r := s.Begin(requestlog.Record{ID: "old"})
	r.Finish(requestlog.Succeeded, 200, nil)
	persisted := s.Changes(0).Version
	s.Begin(requestlog.Record{ID: "new"})
	changes := s.Changes(persisted)
	if changes.Reset || changes.Dropped != 0 || len(changes.Records) != 1 || changes.Records[0].ID != "new" {
		t.Fatalf("persisted eviction reset the writer cursor: %+v", changes)
	}
}

func TestEvictingUnpersistedTerminalChangeReportsCursorGap(t *testing.T) {
	s := requestlog.NewStore(1)
	r := s.Begin(requestlog.Record{ID: "old"})
	cursor := s.Changes(0).Version
	r.Finish(requestlog.Failed, 500, nil)
	s.Begin(requestlog.Record{ID: "new"})
	if !s.Changes(cursor).Reset {
		t.Fatal("unpersisted final state loss was hidden")
	}
}
