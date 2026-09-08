package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestCodexFirstOutputTimeoutPersistsAcrossAPIUpdates(t *testing.T) {
	configPath := writeTestConfigFile(t)
	h := &Handler{cfg: &config.Config{}, configFilePath: configPath}
	router := gin.New()
	router.PUT("/keys", h.PutCodexKeys)
	router.PATCH("/keys", h.PatchCodexKey)
	router.GET("/keys", h.GetCodexKeys)
	for _, step := range []struct {
		method, body string
		want         int64
	}{
		{http.MethodPut, `[{"name":"relay","base-url":"https://relay.example/v1","api-key":"fixture","responses-first-output-timeout-seconds":120},{"name":"unchanged","base-url":"https://other.example/v1","api-key":"fixture-other"}]`, 120},
		{http.MethodPatch, `{"index":0,"value":{"responses-first-output-timeout-seconds":5}}`, 5},
		{http.MethodPatch, `{"index":0,"value":{"name":"renamed"}}`, 5},
		{http.MethodPatch, `{"index":0,"value":{"responses-first-output-timeout-seconds":0}}`, 0},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(step.method, "/keys", strings.NewReader(step.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", step.method, rec.Code, rec.Body.String())
		}
		get := httptest.NewRecorder()
		router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/keys", nil))
		items := gjson.GetBytes(get.Body.Bytes(), "codex-api-key").Array()
		if get.Code != 200 || len(items) != 2 || items[0].Get("responses-first-output-timeout-seconds").Int() != step.want || items[1].Get("responses-first-output-timeout-seconds").Int() != 0 {
			t.Fatalf("GET lost scoped wait configuration after %s: %s", step.method, get.Body.String())
		}
		persisted, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(persisted.CodexKey) != 2 || int64(persisted.CodexKey[0].ResponsesFirstOutputTimeoutSeconds) != step.want || persisted.CodexKey[1].ResponsesFirstOutputTimeoutSeconds != 0 {
			t.Fatal("persisted config lost the provider-scoped wait setting")
		}
	}
}
