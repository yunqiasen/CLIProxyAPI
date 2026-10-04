package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagementV8ForkOperationsShareAccessControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("remote-management: {secret-key: fixture-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteManagement.AllowRemote = true
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("fixture-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	routes := map[string]bool{}
	for _, route := range s.engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for i, route := range []struct{ method, path string }{
		{"POST", "/provider-connectivity-test"},
		{"POST", "/quota-refresh-jobs"}, {"GET", "/quota-refresh-jobs/:id"},
		{"GET", "/request-logs"}, {"GET", "/request-logs/export"},
		{"GET", "/request-logs/failure-details"}, {"GET", "/request-logs/:id"},
		{"GET", "/request-log-retention-days"}, {"PUT", "/request-log-retention-days"},
		{"GET", "/auth-files/download-zip"}, {"POST", "/auth-files/download-zip"},
		{"POST", "/management-panel/update"},
		{"GET", "/media-providers"}, {"PUT", "/media-providers"},
		{"PATCH", "/media-providers"}, {"DELETE", "/media-providers"},
	} {
		path := "/v8/management" + route.path
		if !routes[route.method+" "+path] {
			t.Errorf("missing fork operation %s %s", route.method, path)
			continue
		}
		req := httptest.NewRequest(route.method, path, nil)
		req.RemoteAddr = fmt.Sprintf("127.0.0.%d:1234", i+2)
		rec := httptest.NewRecorder()
		s.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s = %d", route.method, path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v8/management/provider-connectivity-test", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer fixture-password")
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("selected probe handler not reached: %d %s", rec.Code, rec.Body.String())
	}
}
