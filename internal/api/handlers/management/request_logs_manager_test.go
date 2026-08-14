package management

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func writeManagerRequestLog(t *testing.T, dir, suffix string, timestamp time.Time) string {
	t.Helper()
	name := fmt.Sprintf("v1-responses-%s-%s.log", timestamp.Format("2006-01-02T150405"), suffix)
	path := filepath.Join(dir, name)
	content := fmt.Sprintf(`=== REQUEST INFO ===
Timestamp: %s
URL: /v1/responses
Method: POST

=== REQUEST BODY ===
{"model":"test-model","input":"hello"}

=== API REQUEST 1 ===
Upstream URL: https://api.example.com/v1/responses
HTTP Method: POST
Auth: provider=claude, provider_name=relay-a, auth_id=auth-%s, type=api_key

Body:
{"model":"upstream-model","input":"hello"}

=== RESPONSE ===
Status: 200
Content-Type: application/json

{"output_text":"ok"}
`, timestamp.Format(time.RFC3339), suffix)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write request log: %v", err)
	}
	return path
}

func waitForRequestLogManager(t *testing.T, manager *requestLogIndexManager, condition func(requestLogSyncStatus) bool) requestLogSyncStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := manager.Status()
		if condition(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := manager.Status()
	t.Fatalf("request log manager condition timed out: %#v", status)
	return status
}

func newTestRequestLogIndexManager(t *testing.T, dir string, opts requestLogIndexManagerOptions) *requestLogIndexManager {
	t.Helper()
	opts.SyncInterval = 0
	manager, err := newRequestLogIndexManager(dir, opts)
	if err != nil {
		t.Fatalf("new request log manager: %v", err)
	}
	manager.Start()
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close request log manager: %v", err)
		}
	})
	return manager
}

func TestRequestLogIndexManagerInitialScan(t *testing.T) {
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "initial", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	items, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list snapshot: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Provider != "relay-a" {
		t.Fatalf("snapshot total/items = %d/%#v", total, items)
	}
}

func TestRequestLogIndexManagerKeepsDistinctFilesWithSameShortRequestID(t *testing.T) {
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "same-request", time.Now().Add(-2*time.Minute))
	writeManagerRequestLog(t, dir, "same-request", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	items, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list colliding request IDs: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("colliding request IDs total/items = %d/%d, want 2/2", total, len(items))
	}
	if items[0].ID == items[1].ID {
		t.Fatalf("colliding request IDs share storage ID %q", items[0].ID)
	}
}

func TestRequestLogStoreMigratesLegacyShortIDsToFilenameIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "legacy-short-id", time.Now().Add(-time.Minute))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat legacy ID log: %v", err)
	}

	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, status, success, has_error, parser_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-short-id", filepath.Base(path), path, info.Size(), info.ModTime().Unix(), info.ModTime().Format(time.RFC3339Nano), info.ModTime().Unix(), 200, 1, 0, 1, time.Now().Unix(), time.Now().Unix())
	if err == nil {
		_, err = store.db.ExecContext(context.Background(), `PRAGMA user_version = 0`)
	}
	if err != nil {
		_ = store.close()
		t.Fatalf("insert legacy short ID row: %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	store, err = openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer store.close()
	wantID := requestLogIDFromFilename(filepath.Base(path))
	var gotID string
	if err := store.db.QueryRowContext(context.Background(), `SELECT id FROM request_log_entries WHERE name = ?`, filepath.Base(path)).Scan(&gotID); err != nil {
		t.Fatalf("query migrated ID: %v", err)
	}
	if gotID != wantID || gotID == "legacy-short-id" {
		t.Fatalf("migrated ID = %q, want %q", gotID, wantID)
	}
}

func TestRequestLogStoreSkipsCompletedFilenameIDMigration(t *testing.T) {
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "post-migration-legacy", time.Now().Add(-time.Minute))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat post-migration log: %v", err)
	}

	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	legacyID := "post-migration-legacy"
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, status, success, has_error, parser_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, legacyID, filepath.Base(path), path, info.Size(), info.ModTime().Unix(), info.ModTime().Format(time.RFC3339Nano), info.ModTime().Unix(), 200, 1, 0, requestLogParserRevision, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		_ = store.close()
		t.Fatalf("insert row after migration: %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}

	store, err = openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer store.close()
	var gotID string
	if err := store.db.QueryRowContext(context.Background(), `SELECT id FROM request_log_entries WHERE name = ?`, filepath.Base(path)).Scan(&gotID); err != nil {
		t.Fatalf("query post-migration row: %v", err)
	}
	if gotID != legacyID {
		t.Fatalf("completed migration reran: id = %q, want %q", gotID, legacyID)
	}
}

func TestRequestLogStoreResolveIDTreatsLegacyWildcardsLiterally(t *testing.T) {
	dir := t.TempDir()
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open request log store: %v", err)
	}
	defer store.close()

	now := time.Now().Unix()
	rows := []struct {
		id        string
		name      string
		timestamp int64
	}{
		{id: "literal-row", name: "v1-responses-legacy_%.log", timestamp: now - 10},
		{id: "wildcard-row", name: "v1-responses-legacy-AB.log", timestamp: now},
	}
	for _, row := range rows {
		_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, status, success, has_error, parser_revision, created_at, updated_at) VALUES (?, ?, ?, 0, 0, '', ?, 200, 1, 0, ?, ?, ?)`, row.id, row.name, filepath.Join(dir, row.name), row.timestamp, requestLogParserRevision, now, now)
		if err != nil {
			t.Fatalf("insert resolve row %q: %v", row.id, err)
		}
	}

	resolved, err := store.resolveID(context.Background(), "legacy_%")
	if err != nil {
		t.Fatalf("resolve literal wildcard ID: %v", err)
	}
	if resolved != "literal-row" {
		t.Fatalf("resolved ID = %q, want literal-row", resolved)
	}
}

func TestRequestLogIndexManagerDetailAcceptsLegacyShortRequestID(t *testing.T) {
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "legacy-detail-id", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	base := strings.TrimSuffix(filepath.Base(path), ".log")
	legacyID := base[strings.LastIndex(base, "-")+1:]
	detail, err := manager.Detail(context.Background(), legacyID)
	if err != nil {
		t.Fatalf("load detail by legacy request ID: %v", err)
	}
	if detail.ID != requestLogIDFromFilename(filepath.Base(path)) || detail.Provider != "relay-a" {
		t.Fatalf("legacy detail = id:%q provider:%q", detail.ID, detail.Provider)
	}
}

func TestRequestLogIndexManagerRefreshesLegacyMediaModelOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := writeMultipartImageEditRequestLog(t, dir, "manager-media")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat multipart image log: %v", err)
	}

	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	id := requestLogIDFromFilename(filepath.Base(path))
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, url, method, model, status, success, has_error, parser_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, filepath.Base(path), path, info.Size(), info.ModTime().Unix(), info.ModTime().Format(time.RFC3339Nano), info.ModTime().Unix(), "/v1/images/edits", "POST", "", 200, 1, 0, 0, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		_ = store.close()
		t.Fatalf("insert legacy media row: %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	var mu sync.Mutex
	var batches []int
	manager, err := newRequestLogIndexManager(dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		BatchHook: func(size int) {
			mu.Lock()
			batches = append(batches, size)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("new request log manager: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close request log manager: %v", err)
		}
	})

	if err := manager.sync(context.Background()); err != nil {
		t.Fatalf("first manager sync: %v", err)
	}
	items, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list refreshed media row: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Model != "public-image" {
		t.Fatalf("refreshed media rows = total:%d items:%#v", total, items)
	}

	if err := manager.sync(context.Background()); err != nil {
		t.Fatalf("second manager sync: %v", err)
	}
	mu.Lock()
	gotBatches := append([]int(nil), batches...)
	mu.Unlock()
	if !reflect.DeepEqual(gotBatches, []int{1}) {
		t.Fatalf("batch sizes = %#v, want one legacy refresh only", gotBatches)
	}
}

func TestRequestLogIndexManagerRefreshesLegacyNullStateAndProviderRevision(t *testing.T) {
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "legacy-provider", time.Now().Add(-time.Minute))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat provider log: %v", err)
	}

	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	id := requestLogIDFromFilename(filepath.Base(path))
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, url, method, model, provider, provider_name, auth_id, auth_type, upstream_url, upstream_model, channel_model, status, success, has_error, parser_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, filepath.Base(path), path, info.Size(), info.ModTime().Unix(), info.ModTime().Format(time.RFC3339Nano), info.ModTime().Unix(), "POST", "claude", "WrongRelay", "wrong-auth", "api_key", "https://wrong.example/v1/messages", "wrong-model", "WrongRelay / wrong-model", 200, 1, 0, 1, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		_ = store.close()
		t.Fatalf("insert legacy provider row: %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	manager, err := newRequestLogIndexManager(dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	if err != nil {
		t.Fatalf("new request log manager: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close request log manager: %v", err)
		}
	})
	if err := manager.sync(context.Background()); err != nil {
		t.Fatalf("sync legacy provider row: %v", err)
	}

	var providerName, authID, upstreamURL string
	var parserRevision int
	if err := manager.store.db.QueryRowContext(context.Background(), `SELECT provider_name, auth_id, upstream_url, parser_revision FROM request_log_entries WHERE name = ?`, filepath.Base(path)).Scan(&providerName, &authID, &upstreamURL, &parserRevision); err != nil {
		t.Fatalf("query refreshed provider row: %v", err)
	}
	if providerName != "relay-a" || authID != "auth-legacy-provider" || upstreamURL != "https://api.example.com/v1/responses" || parserRevision != requestLogParserRevision {
		t.Fatalf("refreshed provider row = name:%q auth:%q url:%q revision:%d", providerName, authID, upstreamURL, parserRevision)
	}
}

func TestRequestLogIndexManagerCoalescesTriggersAndServesReadsDuringBlockedScan(t *testing.T) {
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "snapshot", time.Now().Add(-time.Minute))
	var scans atomic.Int32
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		ScanHook: func(ctx context.Context) error {
			scans.Add(1)
			if !block.Load() {
				return nil
			}
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return scans.Load() == 1 && !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	block.Store(true)
	manager.TriggerSync()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("blocked scan did not start")
	}
	for i := 0; i < 32; i++ {
		manager.TriggerSync()
	}

	readDone := make(chan error, 1)
	go func() {
		_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
		if err == nil && total != 1 {
			err = fmt.Errorf("snapshot total = %d, want 1", total)
		}
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("snapshot read: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("snapshot read waited for blocked scan")
	}

	block.Store(false)
	close(release)
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return scans.Load() == 3 && !status.Syncing
	})
	if got := scans.Load(); got != 3 {
		t.Fatalf("scan count = %d, want initial + active + one coalesced", got)
	}
}

func TestRequestLogIndexManagerUsesBoundedBatchesAndPreservesMissingRawRows(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		paths = append(paths, writeManagerRequestLog(t, dir, fmt.Sprintf("batch-%d", i), time.Now().Add(time.Duration(i-10)*time.Minute)))
	}
	var mu sync.Mutex
	var batches []int
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		BatchSize:     2,
		BatchHook: func(size int) {
			mu.Lock()
			batches = append(batches, size)
			mu.Unlock()
		},
	})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	mu.Lock()
	gotBatches := append([]int(nil), batches...)
	mu.Unlock()
	if !reflect.DeepEqual(gotBatches, []int{2, 2, 1}) {
		t.Fatalf("batch sizes = %#v, want [2 2 1]", gotBatches)
	}

	if err := os.Remove(paths[0]); err != nil {
		t.Fatalf("remove raw log: %v", err)
	}
	previous := manager.Status().LastSyncedAt
	manager.TriggerSync()
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && status.LastSyncedAt.After(previous)
	})
	_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list after deletion: %v", err)
	}
	if total != 5 {
		t.Fatalf("total after raw deletion = %d, want preserved structured history 5", total)
	}
}

func TestRequestLogIndexManagerPreservesMissingRawRowsWhenRetentionIsZero(t *testing.T) {
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "permanent-history", time.Now().Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 0 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove raw log: %v", err)
	}
	previous := manager.Status().LastSyncedAt
	manager.TriggerSync()
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && status.LastSyncedAt.After(previous)
	})

	_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list after raw deletion: %v", err)
	}
	if total != 1 {
		t.Fatalf("total after raw deletion with permanent retention = %d, want 1", total)
	}
}

func TestRequestLogIndexManagerPrunesMissingRawRowsAfterRetentionExpires(t *testing.T) {
	dir := t.TempDir()
	initialNow := time.Now().Truncate(time.Second)
	var nowUnix atomic.Int64
	nowUnix.Store(initialNow.Unix())
	path := writeManagerRequestLog(t, dir, "expiring-history", initialNow.Add(-time.Minute))
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		Now:           func() time.Time { return time.Unix(nowUnix.Load(), 0) },
	})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove raw log: %v", err)
	}

	nowUnix.Store(initialNow.AddDate(0, 0, 8).Unix())
	previous := manager.Status().LastSyncedAt
	manager.TriggerSync()
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && status.LastSyncedAt.After(previous)
	})
	_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list after retention expiry: %v", err)
	}
	if total != 0 {
		t.Fatalf("total after retention expiry = %d, want 0", total)
	}
}

func TestRequestLogIndexManagerRetentionZeroAndPositivePruning(t *testing.T) {
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "old", time.Now().AddDate(0, 0, -30))
	writeManagerRequestLog(t, dir, "new", time.Now().Add(-time.Minute))
	var retention atomic.Int32
	retention.Store(0)
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return int(retention.Load()) }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("retention 0 total/error = %d/%v, want 2/nil", total, err)
	}

	retention.Store(7)
	previous := manager.Status().LastSyncedAt
	manager.TriggerSync()
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && status.LastSyncedAt.After(previous)
	})
	_, total, err = manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("positive retention total/error = %d/%v, want 1/nil", total, err)
	}
}

func TestRequestLogIndexManagerPreservesSnapshotAndLastErrorAndClosesCleanly(t *testing.T) {
	dir := t.TempDir()
	writeManagerRequestLog(t, dir, "stable", time.Now().Add(-time.Minute))
	var fail atomic.Bool
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		ScanHook: func(context.Context) error {
			if fail.Load() {
				return errors.New("scan fixture failed")
			}
			return nil
		},
	})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})

	fail.Store(true)
	manager.TriggerSync()
	status := waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && status.LastSyncError != ""
	})
	if status.LastSyncError != "scan fixture failed" {
		t.Fatalf("last sync error = %q", status.LastSyncError)
	}
	_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("snapshot after failed scan total/error = %d/%v", total, err)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	manager.TriggerSync()
}

func TestRequestLogIndexManagerHandlerLifecycle(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	if err := h.StartRequestLogIndex(); err != nil {
		t.Fatalf("start request log index: %v", err)
	}
	if h.requestLogIndex == nil {
		t.Fatal("SetLogDirectory did not initialize request log index manager")
	}
	if err := h.Close(); err != nil {
		t.Fatalf("handler close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("handler second close: %v", err)
	}
}

func TestRequestLogDetailBackfillAppliesRetentionBeforeBackgroundPrune(t *testing.T) {
	dir := t.TempDir()
	path := writeManagerRequestLog(t, dir, "old-detail", time.Now().AddDate(0, 0, -30))
	var retention atomic.Int32
	retention.Store(0)
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return int(retention.Load()) }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	retention.Store(7)

	_, err := manager.Detail(context.Background(), requestLogIDFromFilename(filepath.Base(path)))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("detail error = %v, want sql.ErrNoRows after retention changed", err)
	}
}
