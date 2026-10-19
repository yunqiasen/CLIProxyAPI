package management

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newSearchTestStore(t *testing.T) *requestLogStore {
	t.Helper()
	store, err := openRequestLogStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func putSearchTestRow(t *testing.T, store *requestLogStore, id, prompt string, ts time.Time) {
	t.Helper()
	candidate := requestLogCandidate{name: id + ".log", path: filepath.Join(filepath.Dir(store.path), id+".log"), logTime: ts, modTime: ts, size: 42}
	parsed := parsedRequestLog{
		requestLogListItem: requestLogListItem{ID: id, Name: candidate.name, Timestamp: ts.Format(time.RFC3339), Model: "client-alias", Provider: "provider-label", ProtocolProvider: "codex", UpstreamModel: "vendor-model", Status: 200, Success: true, CalledToolsPreview: "exec_command", PromptPreview: "preview only", SessionID: "session-one"},
		prompt:             prompt,
		output:             "final-output",
		promptMetadata:     requestPromptMetadata{SystemPrompt: strings.Repeat("shared system instructions ", 4096) + "尾部关键词"},
	}
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatal(err)
	}
}

func TestRequestLogSearchUsesSharedContentIndex(t *testing.T) {
	store := newSearchTestStore(t)
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"first", "second", "third"} {
		putSearchTestRow(t, store, id, "shared prompt full-body-only", now)
	}
	items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: "尾部关键词", Limit: 10})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("full content search total=%d rows=%d err=%v", total, len(items), err)
	}
	var copies, entries int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM request_log_search_texts WHERE content LIKE '%尾部关键词%'`).Scan(&copies); err != nil {
		t.Fatalf("search still has no shared-content index: %v", err)
	}
	if copies != 1 {
		t.Fatalf("stored %d copies of a shared system prompt, want 1", copies)
	}
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM request_log_search_entries`).Scan(&entries); err != nil || entries != 3 {
		t.Fatalf("compact entries=%d err=%v", entries, err)
	}
}

func TestRequestLogSearchCanceledReadDoesNotFallBackToRawScan(t *testing.T) {
	for _, storage := range []string{"sqlite", "file"} {
		for _, query := range []string{"hello", ""} {
			t.Run(storage+"/"+query, func(t *testing.T) {
				dir := t.TempDir()
				writeManagerRequestLog(t, dir, "cancel-search", time.Now())
				h := &Handler{cfg: &config.Config{RequestLogRetentionDays: 7}, logDir: dir}
				if storage == "sqlite" {
					store, err := openRequestLogStore(dir)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = store.close() }()
					managerContext, stopManager := context.WithCancel(t.Context())
					defer stopManager()
					h.requestLogIndex = &requestLogIndexManager{ctx: managerContext, store: store, trigger: make(chan struct{}, 1)}
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				r := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(r)
				c.Request = httptest.NewRequest("GET", "/v8/management/request-logs?q="+query, nil).WithContext(ctx)
				h.GetRequestLogs(c)
				if r.Body.Len() != 0 {
					t.Fatalf("canceled search scanned raw logs and generated a %d-byte response", r.Body.Len())
				}
			})
		}
	}
}

func TestRequestLogSearchReadsOneSnapshotWhileEntriesChange(t *testing.T) {
	store := newSearchTestStore(t)
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	batch := make([]parsedRequestLogCandidate, 24)
	for i := range batch {
		id := fmt.Sprintf("entry-%03d", i)
		batch[i] = parsedRequestLogCandidate{
			candidate: requestLogCandidate{name: id + ".log", path: id + ".log", logTime: now},
			parsed: parsedRequestLog{
				requestLogListItem: requestLogListItem{ID: id, Name: id + ".log", Timestamp: now.Format(time.RFC3339), PromptPreview: "revision-one", Success: true, Status: 200},
				prompt:             "revision-one",
			},
		}
	}
	if err := store.upsertParsedBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	writerDone := make(chan error, 1)
	go func() {
		for revision := 0; ; revision++ {
			text := "revision-one"
			if revision%2 == 0 {
				text = "revision-two"
			}
			for i := range batch {
				batch[i].parsed.prompt = text
				batch[i].parsed.PromptPreview = text
			}
			if err := store.upsertParsedBatch(ctx, batch); err != nil {
				writerDone <- err
				return
			}
		}
	}()
	defer func() {
		cancel()
		if err := <-writerDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("concurrent writer: %v", err)
		}
	}()
	for attempt := 0; attempt < 160; attempt++ {
		items, total, err := store.list(ctx, requestLogQueryOptions{Query: "revision-one", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if total == 0 && len(items) == 0 {
			continue
		}
		if total != len(batch) || len(items) != 5 {
			t.Fatalf("read crossed snapshots: total=%d rows=%d", total, len(items))
		}
		for _, item := range items {
			if item.PromptPreview != "revision-one" {
				t.Fatalf("search returned a different revision at attempt %d: %q", attempt, item.PromptPreview)
			}
		}
	}
}

func TestRequestLogSearchErrorsIdentifyFailedReadStage(t *testing.T) {
	options := requestLogQueryOptions{Query: "body-only", Limit: 10}
	list := func(ctx context.Context, store *requestLogStore) error {
		_, _, err := store.list(ctx, options)
		return err
	}
	export := func(ctx context.Context, store *requestLogStore) error {
		return store.export(ctx, io.Discard, options, "jsonl")
	}
	for _, test := range []struct {
		name  string
		table string
		read  func(context.Context, *requestLogStore) error
		stage string
	}{
		{"list readiness", "request_log_search_state", list, "read request log search readiness"},
		{"list matching", "request_log_search_refs", list, "search request log page"},
		{"export readiness", "request_log_search_state", export, "read request log export search readiness"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSearchTestStore(t)
			putSearchTestRow(t, store, "entry", "body-only", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
			if _, err := store.db.ExecContext(t.Context(), "DROP TABLE "+test.table); err != nil {
				t.Fatal(err)
			}
			err := test.read(t.Context(), store)
			if err == nil || !strings.Contains(err.Error(), test.stage) {
				t.Fatalf("read error does not identify %q: %v", test.stage, err)
			}
			if errors.Unwrap(err) == nil {
				t.Fatalf("read error lost its underlying database error: %v", err)
			}
		})
	}
}
