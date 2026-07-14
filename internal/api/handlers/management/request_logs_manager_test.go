package management

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestRequestLogIndexManagerUsesBoundedBatchesAndDetectsDeletion(t *testing.T) {
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
	if total != 4 {
		t.Fatalf("total after deletion = %d, want 4", total)
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
