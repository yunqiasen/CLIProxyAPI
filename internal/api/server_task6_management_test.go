package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRequestLogRetentionDaysManagementRoutes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)
	if errWrite := os.WriteFile(server.configFilePath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	putReq := httptest.NewRequest(http.MethodPut, "/v0/management/request-log-retention-days", strings.NewReader(`{"value":21}`))
	putReq.Header.Set("Authorization", "Bearer test-management-key")
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	server.engine.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v0/management/request-log-retention-days", nil)
	getReq.Header.Set("Authorization", "Bearer test-management-key")
	getRec := httptest.NewRecorder()
	server.engine.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || strings.TrimSpace(getRec.Body.String()) != `{"request-log-retention-days":21}` {
		t.Fatalf("GET status=%d body=%s", getRec.Code, getRec.Body.String())
	}
}
