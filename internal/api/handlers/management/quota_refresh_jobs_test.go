package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestQuotaRefreshJobCodexRunsWithConcurrencyLimit(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	var active int64
	var maxActive int64
	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt64(&active, 1)
		defer atomic.AddInt64(&active, -1)
		for {
			maxSeen := atomic.LoadInt64(&maxActive)
			if current <= maxSeen || atomic.CompareAndSwapInt64(&maxActive, maxSeen, current) {
				break
			}
		}
		atomic.AddInt64(&calls, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":1}}}`))
	}))
	defer server.Close()

	oldURL := codexQuotaUsageURL
	codexQuotaUsageURL = server.URL
	defer func() { codexQuotaUsageURL = oldURL }()

	manager := coreauth.NewManager(nil, nil, nil)
	for i := 0; i < 5; i++ {
		_, err := manager.Register(context.Background(), &coreauth.Auth{
			ID:       fmt.Sprintf("codex-%d", i),
			Provider: "codex",
			FileName: fmt.Sprintf("codex-%d.json", i),
			Metadata: map[string]any{"access_token": "token"},
		})
		if err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota-refresh-jobs", strings.NewReader(`{"provider":"codex","concurrency":2}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartQuotaRefreshJob(ctx)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	var started quotaRefreshJob
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var done quotaRefreshJob
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		done, ok = h.quotaRefreshJobs.get(started.ID)
		if !ok {
			t.Fatalf("job %q not found", started.ID)
		}
		if done.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if done.Status != "completed" {
		t.Fatalf("job status = %q", done.Status)
	}
	if done.Total != 5 || done.Done != 5 || done.Success != 5 || done.Failed != 0 {
		t.Fatalf("unexpected job summary: %+v", done)
	}
	if got := atomic.LoadInt64(&calls); got != 5 {
		t.Fatalf("quota calls = %d, want 5", got)
	}
	if got := atomic.LoadInt64(&maxActive); got > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", got)
	}
}

func TestNormalizeQuotaProviderAliases(t *testing.T) {
	if got := normalizeQuotaProvider("anthropic"); got != "claude" {
		t.Fatalf("normalizeQuotaProvider(anthropic) = %q, want claude", got)
	}
	if got := normalizeQuotaProvider("geminicli"); got != "gemini-cli" {
		t.Fatalf("normalizeQuotaProvider(geminicli) = %q, want gemini-cli", got)
	}
}
