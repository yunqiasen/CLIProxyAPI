package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func sumRecentRequestBuckets(buckets []coreauth.RecentRequestBucket) (int64, int64) {
	var success int64
	var failed int64
	for _, bucket := range buckets {
		success += bucket.Success
		failed += bucket.Failed
	}
	return success, failed
}

func TestGetAPIKeyUsage_IncludesPersistedRequestLogCounts(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "codex-key",
			"base_url": "https://codex.example.com",
		},
	}); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}

	logsDir := t.TempDir()
	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open request log store: %v", err)
	}
	defer store.close()
	now := time.Now()
	for _, row := range []struct {
		id      string
		success int
	}{
		{id: "ok-1", success: 1},
		{id: "fail-1", success: 0},
	} {
		_, err := store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, provider, auth_id, status, success, has_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.id, row.id+".log", row.id+".log", 1, now.Unix(), now.Format(time.RFC3339Nano), now.Unix(), "codex", "codex-auth", 200, row.success, boolInt(row.success == 0), now.Unix(), now.Unix())
		if err != nil {
			t.Fatalf("insert request log row: %v", err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetLogDirectory(logsDir)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	entry := payload["codex"]["https://codex.example.com|codex-key"]
	if entry.Success != 1 || entry.Failed != 1 {
		t.Fatalf("persisted totals = %d/%d, want 1/1", entry.Success, entry.Failed)
	}
	success, failed := sumRecentRequestBuckets(entry.RecentRequests)
	if success != 1 || failed != 1 {
		t.Fatalf("persisted recent totals = %d/%d, want 1/1", success, failed)
	}
}

func TestGetAPIKeyUsage_IncludesPersistedSuccessAndFailureDetails(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "codex-key",
			"base_url": "https://codex.example.com",
		},
	}); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}

	logsDir := t.TempDir()
	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open request log store: %v", err)
	}
	defer store.close()
	now := time.Now()
	insertUsageRow := func(id, upstreamModel string, status int, success int, errorPreview string) {
		t.Helper()
		_, err := store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, provider, auth_id, status, success, has_error, model, upstream_model, error_preview, error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, id+".log", id+".log", 1, now.Unix(), now.Format(time.RFC3339Nano), now.Unix(), "codex", "codex-auth", status, success, boolInt(success == 0), upstreamModel, upstreamModel, errorPreview, errorPreview, now.Unix(), now.Unix())
		if err != nil {
			t.Fatalf("insert request log row %s: %v", id, err)
		}
	}

	insertUsageRow("ok-gpt-1", "gpt-5", 200, 1, "")
	insertUsageRow("ok-gpt-2", "gpt-5", 200, 1, "")
	insertUsageRow("ok-gemini-1", "gemini-2.5-pro", 200, 1, "")
	insertUsageRow("fail-empty-1", "minimaxai/minimax-m2.7", 500, 0, "empty_stream")
	insertUsageRow("fail-empty-2", "minimaxai/minimax-m2.7", 500, 0, "empty_stream")
	insertUsageRow("fail-empty-3", "minimaxai/minimax-m2.7", 500, 0, "empty_stream")
	insertUsageRow("fail-rate-1", "qwen/qwen3", 429, 0, "rate limit")
	insertUsageRow("fail-rate-2", "qwen/qwen3", 429, 0, "rate limit")
	for i := 0; i < 11; i++ {
		insertUsageRow(fmt.Sprintf("fail-single-%02d", i), fmt.Sprintf("model-%02d", i), 500+i, 0, fmt.Sprintf("single error %02d", i))
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetLogDirectory(logsDir)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	entry := payload["codex"]["https://codex.example.com|codex-key"]
	if len(entry.SuccessDetails) != 2 {
		t.Fatalf("success details len = %d, want 2: %#v", len(entry.SuccessDetails), entry.SuccessDetails)
	}
	if entry.SuccessDetails[0].Model != "gpt-5" || entry.SuccessDetails[0].Status != 200 || entry.SuccessDetails[0].Count != 2 {
		t.Fatalf("first success detail = %#v, want gpt-5/200/2", entry.SuccessDetails[0])
	}
	if len(entry.FailureDetails) != 10 {
		t.Fatalf("failure details len = %d, want 10: %#v", len(entry.FailureDetails), entry.FailureDetails)
	}
	if entry.FailureDetails[0].Model != "minimaxai/minimax-m2.7" || entry.FailureDetails[0].Status != 500 || entry.FailureDetails[0].Error != "empty_stream" || entry.FailureDetails[0].Count != 3 {
		t.Fatalf("first failure detail = %#v, want minimax/500/empty_stream/3", entry.FailureDetails[0])
	}
	if entry.FailureDetails[1].Model != "qwen/qwen3" || entry.FailureDetails[1].Status != 429 || entry.FailureDetails[1].Error != "rate limit" || entry.FailureDetails[1].Count != 2 {
		t.Fatalf("second failure detail = %#v, want qwen/429/rate limit/2", entry.FailureDetails[1])
	}
}

func TestGetAPIKeyUsage_GroupsByProviderAndAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "codex-key",
			"base_url": "https://codex.example.com",
		},
	}); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "claude-key",
			"base_url": "https://claude.example.com",
		},
	}); err != nil {
		t.Fatalf("register claude auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: false})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "claude-auth", Provider: "claude", Model: "claude-4", Success: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	codexEntry := payload["codex"]["https://codex.example.com|codex-key"]
	if codexEntry.Success != 1 || codexEntry.Failed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codexEntry.Success, codexEntry.Failed)
	}
	if len(codexEntry.RecentRequests) != 20 {
		t.Fatalf("codex buckets len = %d, want 20", len(codexEntry.RecentRequests))
	}
	codexSuccess, codexFailed := sumRecentRequestBuckets(codexEntry.RecentRequests)
	if codexSuccess != 1 || codexFailed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codexSuccess, codexFailed)
	}

	claudeEntry := payload["claude"]["https://claude.example.com|claude-key"]
	if claudeEntry.Success != 1 || claudeEntry.Failed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claudeEntry.Success, claudeEntry.Failed)
	}
	if len(claudeEntry.RecentRequests) != 20 {
		t.Fatalf("claude buckets len = %d, want 20", len(claudeEntry.RecentRequests))
	}
	claudeSuccess, claudeFailed := sumRecentRequestBuckets(claudeEntry.RecentRequests)
	if claudeSuccess != 1 || claudeFailed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claudeSuccess, claudeFailed)
	}
}
