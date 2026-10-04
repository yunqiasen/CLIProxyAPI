package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagementPanelUpdateReportsActualOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler *Handler
		status  int
	}{
		{"nil", nil, http.StatusInternalServerError},
		{"unloaded", &Handler{}, http.StatusServiceUnavailable},
		{"disabled", &Handler{cfg: &config.Config{RemoteManagement: config.RemoteManagement{DisableControlPanel: true}}}, http.StatusConflict},
		{"failed_download", &Handler{cfg: &config.Config{}, configFilePath: filepath.Join(t.TempDir(), "config.yaml")}, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MANAGEMENT_STATIC_PATH", filepath.Join(t.TempDir(), "static"))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/management-panel/update", nil).WithContext(ctx)
			tc.handler.PostManagementPanelUpdate(c)
			if recorder.Code != tc.status {
				t.Fatalf("status=%d, want %d; %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
}
