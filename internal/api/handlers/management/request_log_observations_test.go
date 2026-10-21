package management

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
)

// fakeObservationSource is a test double for the parent's requestlog.Store.
// It lets tests push records and control the Changes() output deterministically.
type fakeObservationSource struct {
	mu       sync.Mutex
	records  []requestlog.Record
	version  uint64
	dropped  uint64
	reset    bool
	getByID  map[string]requestlog.Record
	failOnce bool
}

func newFakeObservationSource() *fakeObservationSource {
	return &fakeObservationSource{getByID: make(map[string]requestlog.Record)}
}

func (f *fakeObservationSource) push(rec requestlog.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version++
	rec.Version = f.version
	f.records = append(f.records, rec)
	f.getByID[rec.ID] = rec
}

func (f *fakeObservationSource) setDropped(n uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = n
}

func (f *fakeObservationSource) setReset(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reset = b
}

func (f *fakeObservationSource) Changes(after uint64) requestlog.ChangeSet {
	f.mu.Lock()
	defer f.mu.Unlock()
	var records []requestlog.Record
	for _, r := range f.records {
		if f.reset || r.Version > after {
			records = append(records, r)
		}
	}
	dropped := f.dropped
	// Dropped is cumulative, just like the real bounded Store.
	cs := requestlog.ChangeSet{
		Version: f.version,
		Records: records,
		Dropped: dropped,
		Reset:   f.reset,
	}
	if f.reset {
		f.reset = false
	}
	return cs
}

func (f *fakeObservationSource) Get(id string) (requestlog.Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.getByID[id]
	return r, ok
}

// openRouteTestStore opens a requestLogStore with the observation schema in a temp dir.
func openRouteTestStore(t *testing.T) *requestLogStore {
	t.Helper()
	store, err := openRequestLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })
	return store
}

// insertTestObservation inserts an observation row directly into the DB for store-level tests.
func insertTestObservation(t *testing.T, store *requestLogStore, rec requestlog.Record) {
	t.Helper()
	if err := upsertObservationWithExecer(context.Background(), store.db, rec, time.Now().Unix()); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
}

func makeRecord(id, traceID, state string, ts time.Time) requestlog.Record {
	return requestlog.Record{
		ID:        id,
		TraceID:   traceID,
		Timestamp: ts,
		URL:       "/v1/responses",
		Method:    "POST",
		Model:     "test-model",
		Provider:  "relay-a",
		AuthID:    "auth-" + id,
		State:     state,
	}
}

func makeRecordWithAttempts(id, traceID, state string, ts time.Time, recovered bool, attempts []requestlog.Attempt) requestlog.Record {
	r := makeRecord(id, traceID, state, ts)
	r.Recovered = recovered
	r.Attempts = attempts
	return r
}

// --- Test: Legacy coexistence ---

func TestRoutesViewCoexistsWithLegacyList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	// Write a raw log file and index it.
	writeManagerRequestLog(t, dir, "legacy-coexist", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	// Insert an observation with a trace_id that does NOT match any raw entry.
	insertTestObservation(t, manager.store, makeRecord("obs-1", "trace-no-raw", requestlog.Succeeded, time.Now()))

	// Legacy list (no view)
	items, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("legacy list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("legacy list total/items = %d/%d", total, len(items))
	}

	// Routes view list
	routeItems, routeTotal, _, err := manager.ListRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, "")
	if err != nil {
		t.Fatalf("routes list: %v", err)
	}
	// Should have 1 raw-backed row + 1 observation-only row = 2
	if routeTotal != 2 || len(routeItems) != 2 {
		t.Fatalf("routes list total/items = %d/%d, want 2/2", routeTotal, len(routeItems))
	}

	// Verify observation-only row has empty raw fields
	var obsRow *requestLogRouteListItem
	var rawRow *requestLogRouteListItem
	for i := range routeItems {
		if routeItems[i].ID == "obs-1" {
			obsRow = &routeItems[i]
		} else {
			rawRow = &routeItems[i]
		}
	}
	if obsRow == nil || obsRow.RawLogID != "" || obsRow.PromptPreview != "" {
		t.Fatalf("observation-only row should have empty raw fields: %#v", obsRow)
	}
	if rawRow == nil || rawRow.State != requestlog.Succeeded {
		t.Fatalf("raw-only row should derive its final state: %#v", rawRow)
	}
}

// --- Test: Trace collision/no spoof ---

func TestRoutesJoinByExactTraceIDNoPartialMatch(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	// Observation with trace "abc123"
	insertTestObservation(t, store, makeRecord("obs-trace", "abc123", requestlog.Succeeded, ts))
	// Raw entry with trace "abc123456" (partial match should NOT join)
	candidate := requestLogCandidate{name: "v1-responses-spoof.log", path: "/tmp/spoof.log", logTime: ts, modTime: ts, size: 10}
	parsed := parsedRequestLog{
		requestLogListItem: requestLogListItem{ID: "spoof-id", Name: candidate.name, Timestamp: ts.Format(time.RFC3339), Model: "m", Provider: "p", Status: 200, Success: true},
		traceID:            "abc123456",
	}
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatalf("upsert raw: %v", err)
	}

	items, total, err := store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, "", nil)
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (no partial join)", total)
	}
	// The observation should NOT have a raw_log_id (no exact match)
	for _, item := range items {
		if item.ID == "obs-trace" && item.RawLogID != "" {
			t.Fatalf("observation joined by partial trace_id: raw_log_id=%q", item.RawLogID)
		}
	}
}

// --- Test: Active to completed same ID ---

func TestRoutesActiveToCompletedSameID(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	// Insert active
	rec := makeRecord("same-id", "trace-active", requestlog.Active, ts)
	insertTestObservation(t, store, rec)

	items, _, _ := store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Active, nil)
	if len(items) != 1 || items[0].State != requestlog.Active {
		t.Fatalf("active row: %#v", items)
	}

	// Update to succeeded (same ID)
	rec.State = requestlog.Succeeded
	rec.Timestamp = ts.Add(time.Second)
	insertTestObservation(t, store, rec)

	items, _, _ = store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Active, nil)
	if len(items) != 0 {
		t.Fatalf("active filter should return 0 after completion, got %d", len(items))
	}
	items, _, _ = store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Succeeded, nil)
	if len(items) != 1 || items[0].ID != "same-id" || items[0].State != requestlog.Succeeded {
		t.Fatalf("succeeded row: %#v", items)
	}
}

// --- Test: Multiple WS calls share trace, separate observation IDs ---

func TestRoutesMultipleWSCallsSeparateObservationIDs(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	// Two observations share the same trace_id
	insertTestObservation(t, store, makeRecord("ws-call-1", "ws-trace-shared", requestlog.Succeeded, ts))
	insertTestObservation(t, store, makeRecord("ws-call-2", "ws-trace-shared", requestlog.Failed, ts.Add(time.Second)))

	// One raw entry with the same trace_id
	candidate := requestLogCandidate{name: "v1-responses-ws.log", path: "/tmp/ws.log", logTime: ts, modTime: ts, size: 10}
	parsed := parsedRequestLog{
		requestLogListItem: requestLogListItem{ID: "ws-raw-id", Name: candidate.name, Timestamp: ts.Format(time.RFC3339), Model: "m", Provider: "p", Status: 200, Success: true, PromptPreview: "shared-prompt"},
		traceID:            "ws-trace-shared",
	}
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatalf("upsert raw: %v", err)
	}

	items, total, err := store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, "", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 2 observations + 0 raw-only (raw entry has matching observation) = 2
	if total != 2 {
		t.Fatalf("total = %d, want 2 (separate observation IDs)", total)
	}
	// Both should have raw_log_id pointing to the same raw entry
	for _, item := range items {
		if item.RawLogID != "ws-raw-id" {
			t.Fatalf("observation %q raw_log_id = %q, want ws-raw-id", item.ID, item.RawLogID)
		}
	}
	// IDs must be different
	if items[0].ID == items[1].ID {
		t.Fatalf("observations share ID: %q", items[0].ID)
	}
}

// --- Test: Recovered success distinct from failed ---

func TestRoutesRecoveredDistinctFromFailed(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	// A recovered success (succeeded + recovered=true)
	insertTestObservation(t, store, makeRecordWithAttempts("rec-1", "trace-rec", requestlog.Succeeded, ts, true,
		[]requestlog.Attempt{{Index: 0, State: requestlog.Failed, ErrorKind: "rate_limit"}, {Index: 1, State: requestlog.Succeeded, Provider: "relay-b"}}))
	// A plain failure
	insertTestObservation(t, store, makeRecordWithAttempts("fail-1", "trace-fail", requestlog.Failed, ts, false,
		[]requestlog.Attempt{{Index: 0, State: requestlog.Failed, ErrorKind: "auth_error"}}))

	// Filter by recovered
	items, total, err := store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, "recovered", nil)
	if err != nil {
		t.Fatalf("list recovered: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "rec-1" {
		t.Fatalf("recovered filter = total:%d items:%#v", total, items)
	}
	if !items[0].Recovered {
		t.Fatal("recovered flag not set")
	}

	// Filter by failed
	items, total, err = store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Failed, nil)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "fail-1" {
		t.Fatalf("failed filter = total:%d items:%#v", total, items)
	}

	// Filter by succeeded (includes recovered since recovered is succeeded+recovered)
	items, total, err = store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Succeeded, nil)
	if err != nil {
		t.Fatalf("list succeeded: %v", err)
	}
	if total != 1 || items[0].ID != "rec-1" {
		t.Fatalf("succeeded filter = total:%d items:%#v", total, items)
	}
}

// --- Test: Cancellation ---

func TestRoutesCancellationState(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	insertTestObservation(t, store, makeRecord("cancel-1", "trace-cancel", requestlog.Canceled, ts))

	items, total, err := store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Canceled, nil)
	if err != nil {
		t.Fatalf("list canceled: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].State != requestlog.Canceled {
		t.Fatalf("canceled filter = total:%d items:%#v", total, items)
	}
}

// --- Test: Restart marks active as interrupted ---

func TestRoutesRestartMarksActiveInterrupted(t *testing.T) {
	dir := t.TempDir()
	// Open store, insert active observation, close
	store1, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open store1: %v", err)
	}
	insertTestObservation(t, store1, makeRecord("restart-obs", "trace-restart", requestlog.Active, time.Now()))
	if err := store1.close(); err != nil {
		t.Fatalf("close store1: %v", err)
	}

	// Reopen - ensureObservationSchema runs, but markActive happens in observationWriter.Start()
	store2, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open store2: %v", err)
	}
	defer store2.close()

	// Simulate restart by calling markActiveObservationsInterrupted
	n, err := store2.markActiveObservationsInterrupted(context.Background())
	if err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}
	if n != 1 {
		t.Fatalf("interrupted count = %d, want 1", n)
	}

	items, total, err := store2.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Interrupted, nil)
	if err != nil {
		t.Fatalf("list interrupted: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].State != requestlog.Interrupted {
		t.Fatalf("interrupted row: total:%d items:%#v", total, items)
	}
	// Active filter should return 0
	items, total, _ = store2.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, requestlog.Active, nil)
	if total != 0 {
		t.Fatalf("active filter after restart = %d, want 0", total)
	}
}

// --- Test: Retention prunes observations ---

func TestRoutesRetentionPrunesObservations(t *testing.T) {
	dir := t.TempDir()
	var nowUnix atomic.Int64
	initialNow := time.Now().Truncate(time.Second)
	nowUnix.Store(initialNow.Unix())
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		Now:           func() time.Time { return time.Unix(nowUnix.Load(), 0) },
	})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	// Insert an old observation (8 days before initialNow)
	insertTestObservation(t, manager.store, makeRecord("old-obs", "trace-old", requestlog.Succeeded, initialNow.AddDate(0, 0, -8)))
	// Insert a recent observation (relative to the advanced time, 1 minute before now)
	insertTestObservation(t, manager.store, makeRecord("new-obs", "trace-new", requestlog.Succeeded, initialNow.AddDate(0, 0, 8).Add(-time.Minute)))

	// Advance time past retention and trigger sync to prune
	nowUnix.Store(initialNow.AddDate(0, 0, 8).Unix())
	manager.TriggerSync()
	// Poll for the prune to take effect
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, total, err := manager.store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, "", nil)
		if err != nil {
			t.Fatalf("list after retention: %v", err)
		}
		if total == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("retention prune did not complete within 3s")
}

// --- Test: CJK body search via routes view ---

func TestRoutesCJKBodySearchFindsRawBackedRow(t *testing.T) {
	dir := t.TempDir()
	stamp, timestamp := requestLogTestTimestamp(0)
	logPath := filepath.Join(dir, "v1-responses-"+stamp+"-cjk.log")
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp,
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== HEADERS ===",
		"X-CPA-Log-Trace-ID: trace-cjk-001",
		"",
		"=== REQUEST BODY ===",
		`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"中文提示词"}]}]}`,
		"",
		"=== RESPONSE ===",
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"output":[{"type":"message","content":[{"type":"output_text","text":"中文输出"}]}]}`,
		"",
	}, "\n")
	if err := writeFile(logPath, content); err != nil {
		t.Fatalf("write log: %v", err)
	}

	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	// Insert an observation with the same trace_id
	insertTestObservation(t, manager.store, makeRecord("obs-cjk", "trace-cjk-001", requestlog.Succeeded, time.Now()))

	// Search for CJK text
	items, total, _, err := manager.ListRoutes(context.Background(), requestLogQueryOptions{Query: "中文提示词", Limit: 10}, "")
	if err != nil {
		t.Fatalf("routes CJK search: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("CJK search total/items = %d/%d", total, len(items))
	}
	if items[0].ID != "obs-cjk" {
		t.Fatalf("CJK search returned wrong row: %q", items[0].ID)
	}
	if items[0].RawLogID == "" {
		t.Fatal("CJK search observation should have raw_log_id")
	}
}

// --- Test: Bounded-page metadata ---

func TestRoutesBoundedPageMetadata(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	for i := 0; i < 25; i++ {
		insertTestObservation(t, store, makeRecord(
			"page-"+strings.Repeat("0", 3-len(itoaTest(i)))+itoaTest(i),
			"trace-page-"+itoaTest(i),
			requestlog.Succeeded, ts.Add(time.Duration(i)*time.Second)))
	}

	// Page 1
	items, total, err := store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10, Offset: 0}, "", nil)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if total != 25 || len(items) != 10 {
		t.Fatalf("page 1: total=%d items=%d, want 25/10", total, len(items))
	}
	// Page 2
	items, total, err = store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10, Offset: 10}, "", nil)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if total != 25 || len(items) != 10 {
		t.Fatalf("page 2: total=%d items=%d, want 25/10", total, len(items))
	}
	// Page 3 (partial)
	items, total, err = store.listRoutes(context.Background(), requestLogQueryOptions{Limit: 10, Offset: 20}, "", nil)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if total != 25 || len(items) != 5 {
		t.Fatalf("page 3: total=%d items=%d, want 25/5", total, len(items))
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

// --- Test: Detail route returns observation with attempts ---

func TestRoutesDetailReturnsAttempts(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	attempts := []requestlog.Attempt{
		{Index: 0, Provider: "relay-a", State: requestlog.Failed, ErrorKind: "rate_limit"},
		{Index: 1, Provider: "relay-b", State: requestlog.Succeeded, Model: "upstream-model"},
	}
	rec := makeRecordWithAttempts("detail-obs", "trace-detail", requestlog.Succeeded, ts, true, attempts)
	insertTestObservation(t, store, rec)

	detail, err := store.detailRoute(context.Background(), "detail-obs", nil)
	if err != nil {
		t.Fatalf("detail route: %v", err)
	}
	if detail.State != requestlog.Succeeded || !detail.Recovered {
		t.Fatalf("detail state/recovered = %q/%v", detail.State, detail.Recovered)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("detail attempts = %d, want 2", len(detail.Attempts))
	}
	if detail.Attempts[0].ErrorKind != "rate_limit" || detail.Attempts[1].Provider != "relay-b" {
		t.Fatalf("detail attempts: %#v", detail.Attempts)
	}
	if detail.AttemptCount != 2 {
		t.Fatalf("attempt_count = %d, want 2", detail.AttemptCount)
	}
}

// --- Test: Detail route returns empty content when raw not indexed ---

func TestRoutesDetailEmptyContentWhenRawAbsent(t *testing.T) {
	store := openRouteTestStore(t)
	ts := time.Now()
	insertTestObservation(t, store, makeRecord("no-raw-obs", "trace-no-raw", requestlog.Active, ts))

	detail, err := store.detailRoute(context.Background(), "no-raw-obs", nil)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Prompt != "" || detail.Output != "" {
		t.Fatalf("detail should have empty content: prompt=%q output=%q", detail.Prompt, detail.Output)
	}
	if detail.State != requestlog.Active {
		t.Fatalf("state = %q, want active", detail.State)
	}
	if detail.RawLogID != "" {
		t.Fatalf("raw_log_id should be empty: %q", detail.RawLogID)
	}
}

// --- Test: Detail route falls back to raw detail for legacy IDs ---

func TestRoutesDetailFallsBackToRawForLegacyID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "legacy-detail", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	rawID := requestLogIDFromFilename(filepath.Base(path))

	// Try routes detail - should fall back to raw detail
	detail, err := manager.DetailRoute(context.Background(), rawID)
	if err != nil {
		t.Fatalf("detail route fallback: %v", err)
	}
	if detail.ID != rawID {
		t.Fatalf("detail ID = %q, want %q", detail.ID, rawID)
	}
	if detail.Prompt == "" {
		t.Fatal("detail should have prompt content from raw log")
	}
	if detail.State != requestlog.Succeeded {
		t.Fatalf("legacy detail state should be succeeded: %q", detail.State)
	}
}

// --- Test: Coalescing writer persists observations ---

func TestObservationWriterPersistsRecords(t *testing.T) {
	dir := t.TempDir()
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })

	src := newFakeObservationSource()
	w := newObservationWriter(src, store, 10*time.Millisecond, nil)
	w.Start()
	t.Cleanup(w.Close)

	// Push a record
	src.push(makeRecord("write-1", "trace-write", requestlog.Succeeded, time.Now()))

	// Wait for persistence
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := store.detailRoute(context.Background(), "write-1", nil)
		if err == nil && detail.State == requestlog.Succeeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	detail, err := store.detailRoute(context.Background(), "write-1", nil)
	if err != nil {
		t.Fatalf("observation not persisted: %v", err)
	}
	if detail.State != requestlog.Succeeded {
		t.Fatalf("persisted state = %q", detail.State)
	}
}

// --- Test: Coalescing writer keeps cursor on failure ---

func TestObservationWriterKeepsCursorOnFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })

	src := newFakeObservationSource()
	w := newObservationWriter(src, store, 10*time.Millisecond, nil)
	w.Start()
	t.Cleanup(w.Close)

	// Push a record that will succeed
	src.push(makeRecord("ok-1", "trace-ok", requestlog.Succeeded, time.Now()))
	// Wait for it
	waitForObservation(t, store, "ok-1", 2*time.Second)

	// Close the DB to force failure
	_ = store.db.Close()

	// Push another record
	src.push(makeRecord("fail-1", "trace-fail", requestlog.Succeeded, time.Now().Add(time.Second)))

	// Flush explicitly to observe the failed write deterministically.
	w.syncOnce()

	status := w.Status()
	if status.LastWriteError == "" {
		t.Fatal("expected write error after DB close")
	}

	// Cursor should still point to the last successful version
	if status.LastVersion != 1 {
		t.Fatalf("cursor = %d, want 1 (kept on failure)", status.LastVersion)
	}
}

// --- Test: Coalescing writer reports missed observations ---

func TestObservationWriterReportsMissedObservations(t *testing.T) {
	dir := t.TempDir()
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })

	src := newFakeObservationSource()
	src.setDropped(5)

	w := newObservationWriter(src, store, 10*time.Millisecond, nil)
	w.Start()
	t.Cleanup(w.Close)

	// Push a record to trigger a sync
	src.push(makeRecord("missed-1", "trace-missed", requestlog.Succeeded, time.Now()))
	waitForObservation(t, store, "missed-1", 2*time.Second)

	status := w.Status()
	if status.MissedObservations != 5 {
		t.Fatalf("missed = %d, want 5", status.MissedObservations)
	}
	warning := status.warning()
	if !strings.Contains(warning, "omitted") {
		t.Fatalf("warning should mention omitted observations: %q", warning)
	}
}

// --- Test: HTTP handler view=routes ---

func TestHandlerRoutesViewList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "handler-routes", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	insertTestObservation(t, manager.store, makeRecord("handler-obs", "trace-handler", requestlog.Succeeded, time.Now()))

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs?view=routes&limit=10", nil)
	h.GetRequestLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []requestLogRouteListItem `json:"items"`
		Total int                       `json:"total"`
		View  string                    `json:"view"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.View != "routes" {
		t.Fatalf("view = %q, want routes", payload.View)
	}
	if payload.Total != 2 {
		t.Fatalf("total = %d, want 2", payload.Total)
	}
}

// --- Test: HTTP handler view=routes with state filter ---

func TestHandlerRoutesViewStateFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	insertTestObservation(t, manager.store, makeRecord("active-1", "trace-active1", requestlog.Active, time.Now()))
	insertTestObservation(t, manager.store, makeRecord("succeeded-1", "trace-succeeded1", requestlog.Succeeded, time.Now()))

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs?view=routes&state=active&limit=10", nil)
	h.GetRequestLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []requestLogRouteListItem `json:"items"`
		Total int                       `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0].State != requestlog.Active {
		t.Fatalf("active filter: total=%d items=%#v", payload.Total, payload.Items)
	}
}

// --- Test: HTTP handler view=routes detail ---

func TestHandlerRoutesViewDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	rec := makeRecordWithAttempts("detail-handler", "trace-detail-h", requestlog.Failed, time.Now(), false,
		[]requestlog.Attempt{{Index: 0, State: requestlog.Failed, ErrorKind: "timeout"}})
	insertTestObservation(t, manager.store, rec)

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager

	recHTTP := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recHTTP)
	ctx.Params = gin.Params{{Key: "id", Value: "detail-handler"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs/detail?view=routes", nil)
	h.GetRequestLogDetail(ctx)

	if recHTTP.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recHTTP.Code, recHTTP.Body.String())
	}
	var detail requestLogRouteDetail
	if err := json.Unmarshal(recHTTP.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.State != requestlog.Failed || len(detail.Attempts) != 1 {
		t.Fatalf("detail = state:%q attempts:%d", detail.State, len(detail.Attempts))
	}
}

// --- Test: HTTP handler view=routes export ---

func TestHandlerRoutesViewExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	insertTestObservation(t, manager.store, makeRecord("export-1", "trace-export", requestlog.Succeeded, time.Now()))

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs/export?view=routes&format=csv", nil)
	h.ExportRequestLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "export-1") || !strings.Contains(body, "succeeded") {
		t.Fatalf("export body missing data: %s", body)
	}
}

// --- Test: SetRequestObservations integration ---

func TestSetRequestObservationsWiresCoalescingWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	src := newFakeObservationSource()

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetRequestObservations(src)
	h.SetLogDirectory(dir)
	if err := h.StartRequestLogIndex(); err != nil {
		t.Fatalf("start index: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	manager, _ := h.requestLogIndexSnapshot()
	if manager == nil || manager.obsWriter == nil {
		t.Fatal("observation writer not wired")
	}

	// Push a record, trigger sync, verify it persists
	src.push(makeRecord("wired-1", "trace-wired", requestlog.Succeeded, time.Now()))
	manager.TriggerSync()
	waitForObservation(t, manager.store, "wired-1", 3*time.Second)
}

// --- Test: Trace ID parsed from log headers ---

func TestParseRequestLogExtractsTraceID(t *testing.T) {
	dir := t.TempDir()
	stamp, timestamp := requestLogTestTimestamp(0)
	logPath := filepath.Join(dir, "v1-responses-"+stamp+"-traceid.log")
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp,
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== HEADERS ===",
		"X-CPA-Log-Trace-ID: trace-from-log-123",
		"",
		"=== REQUEST BODY ===",
		`{"model":"gpt-test","input":"hello"}`,
		"",
		"=== RESPONSE ===",
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"output_text":"ok"}`,
		"",
	}, "\n")
	if err := writeFile(logPath, content); err != nil {
		t.Fatalf("write log: %v", err)
	}

	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })
	if err := syncRequestLogStore(context.Background(), store, dir); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var traceID string
	if err := store.db.QueryRowContext(context.Background(), `SELECT trace_id FROM request_log_entries WHERE trace_id = ?`, "trace-from-log-123").Scan(&traceID); err != nil {
		t.Fatalf("query trace_id: %v", err)
	}
	if traceID != "trace-from-log-123" {
		t.Fatalf("trace_id = %q", traceID)
	}
}

// --- Test: Observation store nil still serves routes view ---

func TestRoutesViewWorksWithoutObservationStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "no-obs-store", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	// No observation store, no observations in DB
	items, total, warning, err := manager.ListRoutes(context.Background(), requestLogQueryOptions{Limit: 10}, "")
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total/items = %d/%d, want 1/1 (raw-only)", total, len(items))
	}
	if items[0].State != requestlog.Succeeded {
		t.Fatalf("raw-only row should derive its final state: %q", items[0].State)
	}
	if warning != "" {
		t.Fatalf("warning should be empty without observation store: %q", warning)
	}
}

// --- Test: V0/V8 endpoints preserve legacy contract without view ---

func TestLegacyEndpointsPreserveContractWithoutView(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "legacy-contract", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	// Also insert an observation
	insertTestObservation(t, manager.store, makeRecord("legacy-obs", "trace-legacy", requestlog.Succeeded, time.Now()))

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager

	// Legacy list (no view=routes) should only return raw entries, not observations
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs?limit=10", nil)
	h.GetRequestLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Items []requestLogListItem `json:"items"`
		Total int                  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Legacy list should only have the 1 raw entry, not the observation
	if payload.Total != 1 {
		t.Fatalf("legacy total = %d, want 1 (raw only)", payload.Total)
	}
}

// --- Helper ---

func waitForObservation(t *testing.T, store *requestLogStore, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := store.detailRoute(context.Background(), id, nil)
		if err == nil {
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unexpected error waiting for %s: %v", id, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("observation %s not persisted within %s", id, timeout)
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
