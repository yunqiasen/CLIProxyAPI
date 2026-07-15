package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetRequestLogRetentionDays(t *testing.T) {
	h := &Handler{cfg: &config.Config{RequestLogRetentionDays: 14}}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-log-retention-days", nil)

	h.GetRequestLogRetentionDays(ctx)

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"request-log-retention-days":14}` {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutRequestLogRetentionDaysPersistsNonNegativeInteger(t *testing.T) {
	configPath := writeTestConfigFile(t)
	h := &Handler{cfg: &config.Config{}, configFilePath: configPath}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/request-log-retention-days", strings.NewReader(`{"value":30}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutRequestLogRetentionDays(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.RequestLogRetentionDays != 30 {
		t.Fatalf("retention days = %d, want 30", h.cfg.RequestLogRetentionDays)
	}
	persisted, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("read persisted config: %v", errRead)
	}
	if !strings.Contains(string(persisted), "request-log-retention-days: 30") {
		t.Fatalf("persisted config missing retention: %s", persisted)
	}
}

func TestPutRequestLogRetentionDaysRejectsNegativeInteger(t *testing.T) {
	h := &Handler{cfg: &config.Config{RequestLogRetentionDays: 7}, configFilePath: writeTestConfigFile(t)}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/request-log-retention-days", strings.NewReader(`{"value":-1}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutRequestLogRetentionDays(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if h.cfg.RequestLogRetentionDays != 7 {
		t.Fatalf("retention days changed to %d", h.cfg.RequestLogRetentionDays)
	}
}

func TestPutRequestLogRetentionDaysConcurrentUpdates(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for value := 0; value < 16; value++ {
		value := value
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/request-log-retention-days", strings.NewReader(fmt.Sprintf(`{"value":%d}`, value)))
			ctx.Request.Header.Set("Content-Type", "application/json")
			h.PutRequestLogRetentionDays(ctx)
		}()
		go func() {
			defer wg.Done()
			<-start
			h.SetConfig(&config.Config{RequestLogRetentionDays: value})
		}()
	}
	close(start)
	wg.Wait()
}
