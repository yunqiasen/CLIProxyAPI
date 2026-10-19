package management

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedLegacySearchEntry(t *testing.T, store *requestLogStore, id, prompt string, ts time.Time) {
	t.Helper()
	parsed := parsedRequestLog{
		requestLogListItem: requestLogListItem{ID: id, Name: id + ".log", Timestamp: ts.Format(time.RFC3339), Status: 200, Success: true},
		prompt:             prompt,
	}
	candidate := requestLogCandidate{name: parsed.Name, path: filepath.Join(filepath.Dir(store.path), parsed.Name), logTime: ts}
	if err := upsertParsedWithExecer(t.Context(), store.db, parsed, candidate); err != nil {
		t.Fatal(err)
	}
}

func searchTableCount(t *testing.T, store *requestLogStore, table string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRequestLogSearchMatchesLegacyListAndExportSemantics(t *testing.T) {
	store := newSearchTestStore(t)
	ts := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	// Enumerate the public search contract independently of the indexed field list.
	fields := []string{"id", "name", "timestamp_text", "url", "method", "model", "provider", "provider_name", "auth_id", "auth_type", "upstream_url", "upstream_model", "channel_model", "ip", "ip_location", "prompt", "output", "error", "system_prompt", "called_tools_preview", "session_id", "thread_id", "turn_id"}
	queries := []string{"", "   ", "needle", "NeEdLe", "%", "_", "need%e", "needle_", "200", "429", "success", "失败", "错误", "成功", "中文", "é", "É", "尾部关键词", "back\\slash", "invalid-\xff", "after-null", "not-a-match", "' OR 1=1 --"}
	for i, field := range fields {
		id := fmt.Sprintf("record-%02d", i)
		seedLegacySearchEntry(t, store, id, "", ts.Add(time.Duration(i/2)*time.Second))
		token := "needle-" + field
		if _, err := store.db.ExecContext(t.Context(), "UPDATE request_log_entries SET "+field+" = ? WHERE id = ?", token, id); err != nil {
			t.Fatal(err)
		}
		queries = append(queries, token)
	}
	seedLegacySearchEntry(t, store, "long-content", strings.Repeat("PREFIX ", 8192)+"尾部关键词 中文 É é back\\slash invalid-\xff\x00after-null", ts)
	seedLegacySearchEntry(t, store, "null-content", "", ts)
	if _, err := store.db.ExecContext(t.Context(), `UPDATE request_log_entries SET prompt = NULL, output = NULL, error = NULL, system_prompt = NULL, status = 429, success = 0 WHERE id = 'null-content'`); err != nil {
		t.Fatal(err)
	}
	if ready, err := requestLogSearchReady(t.Context(), store.db); err != nil || ready {
		t.Fatalf("legacy rows should still use the original search: ready=%v err=%v", ready, err)
	}
	type result struct {
		items   []requestLogListItem
		total   int
		exports [2]string
	}
	read := func(opts requestLogQueryOptions) result {
		t.Helper()
		items, total, err := store.list(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		got := result{items: items, total: total}
		for i, format := range []string{"csv", "jsonl"} {
			var buf bytes.Buffer
			if err := store.export(t.Context(), &buf, opts, format); err != nil {
				t.Fatal(err)
			}
			got.exports[i] = buf.String()
		}
		return got
	}
	options := make([]requestLogQueryOptions, 0)
	for _, query := range queries {
		for _, offset := range []int{0, 3, 100} {
			options = append(options, requestLogQueryOptions{Query: query, Limit: 7, Offset: offset})
		}
	}
	want := make([]result, len(options))
	for i, opts := range options {
		want[i] = read(opts)
	}
	wantDetail, err := store.detail(t.Context(), "long-content")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.backfillSearch(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	if ready, err := requestLogSearchReady(t.Context(), store.db); err != nil || !ready {
		t.Fatalf("backfill readiness=%v err=%v", ready, err)
	}
	for i, opts := range options {
		if got := read(opts); !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("indexed list/export changed for query=%q offset=%d (rows=%d/%d total=%d/%d)", opts.Query, opts.Offset, len(got.items), len(want[i].items), got.total, want[i].total)
		}
	}
	gotDetail, err := store.detail(t.Context(), "long-content")
	if err != nil || !reflect.DeepEqual(gotDetail, wantDetail) {
		t.Fatalf("indexing changed full details: %v", err)
	}
}

func TestRequestLogSearchBackfillResumesAfterRestartWithoutRawFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	ts := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"first", "second", "third"} {
		seedLegacySearchEntry(t, store, id, "shared legacy content", ts)
	}
	if err := store.backfillSearchBatch(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if got := searchTableCount(t, store, "request_log_search_entries"); got != 1 {
		t.Fatalf("batch indexed %d rows, want 1", got)
	}
	items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "legacy", Limit: 10})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("partial migration lost results: total=%d rows=%d err=%v", total, len(items), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.backfillSearch(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled migration: %v", err)
	}
	if got := searchTableCount(t, store, "request_log_search_entries"); got != 1 {
		t.Fatalf("canceled migration wrote %d rows", got)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	manager, err := newRequestLogIndexManager(dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 0 }, BatchSize: 1, Now: func() time.Time { return ts }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := manager.sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := searchTableCount(t, manager.store, "request_log_search_entries"); got != 3 {
		t.Fatalf("resumed rows=%d", got)
	}
	if got := searchTableCount(t, manager.store, "request_log_search_texts"); got != 1 {
		t.Fatalf("shared content copies=%d", got)
	}
	items, total, err = manager.List(t.Context(), requestLogQueryOptions{Query: "legacy", Limit: 10})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("restarted search total=%d rows=%d err=%v", total, len(items), err)
	}
}

func TestRequestLogSearchTracksLegacyUpdatesDeletesAndRetention(t *testing.T) {
	store := newSearchTestStore(t)
	ts := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"updated", "deleted", "expired"} {
		putSearchTestRow(t, store, id, "old searchable body", ts)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE request_log_entries SET prompt = 'new searchable body', status = 503, success = 0 WHERE id = 'updated'`); err != nil {
		t.Fatal(err)
	}
	for _, migrated := range []bool{false, true} {
		if migrated {
			if err := store.backfillSearch(t.Context(), 2); err != nil {
				t.Fatal(err)
			}
		}
		items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "new searchable", Limit: 10})
		if err != nil || total != 1 || len(items) != 1 || items[0].Status != 503 {
			t.Fatalf("updated search migration=%v rows=%d total=%d err=%v", migrated, len(items), total, err)
		}
	}
	if err := store.deleteIDs(t.Context(), []string{"deleted"}); err != nil {
		t.Fatal(err)
	}
	putSearchTestRow(t, store, "retained", "retained body", ts.Add(time.Hour))
	if err := store.pruneBefore(t.Context(), ts.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneSearchTexts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := searchTableCount(t, store, "request_log_search_entries"); got != 1 {
		t.Fatalf("compact rows after pruning=%d", got)
	}
	var obsolete int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM request_log_search_texts WHERE content LIKE '%searchable body%'`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatalf("orphaned content=%d err=%v", obsolete, err)
	}
	items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "body", Limit: 10})
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != "retained" {
		t.Fatalf("retention search total=%d rows=%d err=%v", total, len(items), err)
	}
}

func TestRequestLogSearchWriteFailureRollsBackDetailsAndIndex(t *testing.T) {
	store := newSearchTestStore(t)
	ts := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	putSearchTestRow(t, store, "kept", "old body", ts)
	textCount := searchTableCount(t, store, "request_log_search_texts")
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_search_test BEFORE INSERT ON request_log_search_refs WHEN NEW.entry_id = 'fail' BEGIN SELECT RAISE(ABORT, 'injected index write failure'); END`); err != nil {
		t.Fatal(err)
	}
	batch := make([]parsedRequestLogCandidate, 0, 2)
	for _, id := range []string{"kept", "fail"} {
		batch = append(batch, parsedRequestLogCandidate{candidate: requestLogCandidate{name: id + ".log", path: id + ".log", logTime: ts}, parsed: parsedRequestLog{requestLogListItem: requestLogListItem{ID: id, Name: id + ".log", Timestamp: ts.Format(time.RFC3339), Success: true}, prompt: "new body"}})
	}
	if err := store.upsertParsedBatch(t.Context(), batch); err == nil || !strings.Contains(err.Error(), "injected index write failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	detail, err := store.detail(t.Context(), "kept")
	if err != nil || detail.Prompt != "old body" {
		t.Fatalf("detail was partially written: %v", err)
	}
	if _, err := store.detail(t.Context(), "fail"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed insert remained: %v", err)
	}
	if got := searchTableCount(t, store, "request_log_search_texts"); got != textCount {
		t.Fatalf("failed batch left %d texts, want %d", got, textCount)
	}
	items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "old body", Limit: 10})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("rollback search total=%d rows=%d err=%v", total, len(items), err)
	}
	if _, err := store.db.ExecContext(t.Context(), `DROP TRIGGER fail_search_test`); err != nil {
		t.Fatal(err)
	}
	if err := store.upsertParsedBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	items, total, err = store.list(t.Context(), requestLogQueryOptions{Query: "new body", Limit: 10})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("retry search total=%d rows=%d err=%v", total, len(items), err)
	}
}

func TestRequestLogSearchReadsDoNotReserveWriterLock(t *testing.T) {
	store := newSearchTestStore(t)
	putSearchTestRow(t, store, "existing", "find me", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	writer, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback() }()
	if _, err := writer.ExecContext(t.Context(), `UPDATE request_log_entries SET prompt = 'pending' WHERE id = 'existing'`); err != nil {
		t.Fatal(err)
	}
	items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "find me", Limit: 10})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("read blocked on writer: total=%d rows=%d err=%v", total, len(items), err)
	}
	var buf bytes.Buffer
	if err := store.export(t.Context(), &buf, requestLogQueryOptions{Query: "find me", Limit: 10}, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "find me") || strings.Contains(buf.String(), "pending") {
		t.Fatal("export did not preserve the committed snapshot")
	}
}

func TestRequestLogSearchSkipsBodiesWhenShortFieldsMatchAll(t *testing.T) {
	store := newSearchTestStore(t)
	putSearchTestRow(t, store, "entry", "unique-body-needle", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	// An evaluated body raises a SQLite error. A wildcard matching all short
	// records must not read those bodies, even if SQLite reorders AND predicates.
	if _, err := store.db.ExecContext(t.Context(), `ALTER TABLE request_log_search_texts RENAME TO saved_search_texts;
CREATE VIEW request_log_search_texts AS SELECT id, digest, json_extract('invalid json', '$') AS content FROM saved_search_texts;`); err != nil {
		t.Fatal(err)
	}
	items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "%", Limit: 10})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("short-only match scanned full bodies: total=%d rows=%d err=%v", total, len(items), err)
	}
	var output bytes.Buffer
	if err := store.export(t.Context(), &output, requestLogQueryOptions{Query: "%", Limit: 10}, "jsonl"); err != nil {
		t.Fatalf("short-only export scanned the body index: %v", err)
	}
	if _, _, err := store.list(t.Context(), requestLogQueryOptions{Query: "unique-body-needle", Limit: 10}); err == nil {
		t.Fatal("body-only query did not evaluate the body index fixture")
	}
}
