package management

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// This opt-in differential benchmark copies the private snapshot. It never opens
// the source for writing and never contacts the running CPA or an upstream API.
func TestRequestLogSearchSnapshotTiming(t *testing.T) {
	sourcePath := os.Getenv("CPA_REQUEST_LOG_SNAPSHOT")
	if sourcePath == "" {
		t.Skip("private snapshot benchmark is opt-in")
	}
	dir := t.TempDir()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(filepath.Join(dir, requestLogDBFilename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.close() }()
	if ready, err := requestLogSearchReady(t.Context(), store.db); err != nil || ready {
		t.Fatalf("benchmark expects a pre-index snapshot: ready=%v err=%v", ready, err)
	}
	type measurement struct {
		Query     string  `json:"query"`
		Total     int     `json:"total"`
		Rows      int     `json:"rows"`
		LegacyMS  float64 `json:"legacy_ms"`
		IndexedMS float64 `json:"indexed_ms"`
		Ratio     float64 `json:"speedup"`
		Same      bool    `json:"same_items_and_total"`
	}
	queries := []string{"", "exec_command", "zz-cpa-query-no-match-20261019", "工具", "%", "a"}
	measurements := make([]measurement, len(queries))
	original := make([][]requestLogListItem, len(queries))
	for i, query := range queries {
		start := time.Now()
		items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: query, Limit: 30})
		if err != nil {
			t.Fatal(err)
		}
		original[i] = items
		measurements[i] = measurement{Query: query, Total: total, Rows: len(items), LegacyMS: float64(time.Since(start)) / float64(time.Millisecond)}
		t.Logf("legacy query=%q total=%d rows=%d ms=%.1f", query, total, len(items), measurements[i].LegacyMS)
	}
	start := time.Now()
	if err := store.backfillSearch(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	migrationMS := float64(time.Since(start)) / float64(time.Millisecond)
	for i, query := range queries {
		start := time.Now()
		items, total, err := store.list(t.Context(), requestLogQueryOptions{Query: query, Limit: 30})
		if err != nil {
			t.Fatal(err)
		}
		m := &measurements[i]
		m.IndexedMS = float64(time.Since(start)) / float64(time.Millisecond)
		m.Ratio = m.LegacyMS / m.IndexedMS
		m.Same = total == m.Total && reflect.DeepEqual(items, original[i])
		if !m.Same {
			t.Errorf("indexed results changed for query %q", query)
		}
		t.Logf("indexed query=%q total=%d rows=%d ms=%.1f speedup=%.1fx", query, total, len(items), m.IndexedMS, m.Ratio)
	}
	var bytes, texts int64
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*), COALESCE(SUM(length(CAST(content AS BLOB))), 0) FROM request_log_search_texts`).Scan(&texts, &bytes); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"migration_ms": migrationMS, "unique_texts": texts, "text_bytes": bytes, "measurements": measurements}
	if reportPath := os.Getenv("CPA_REQUEST_LOG_BENCHMARK_REPORT"); reportPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reportPath, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
