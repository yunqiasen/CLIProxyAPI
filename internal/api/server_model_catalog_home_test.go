package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
)

func TestModelCatalogHomePresentationAndInventoryCredentials(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [home-client]\nremote-management: {secret-key: catalog-manager}\nclient: {model-catalog: {hidden: [claude-hidden]}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("catalog-manager"))
	cfg.Home.Enabled = true
	seen := make(chan http.Header, 20)
	client := newHomeCatalogClient(t, `{"codex":[{"id":"claude-hidden"},{"id":"claude-visible"}]}`, func(raw []byte) {
		var request struct {
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
			return
		}
		headers := make(http.Header)
		for k, v := range request.Headers {
			headers.Set(k, v)
		}
		seen <- headers
	})
	previous := home.Current()
	home.SetCurrent(client)
	t.Cleanup(func() { home.SetCurrent(previous) })
	// Reuse catalog handlers without the unrelated Home heartbeat-readiness gate.
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
	engine.GET("/v1beta/models", server.geminiModelsHandler(gemini.NewGeminiAPIHandler(server.handlers)))
	engine.GET("/inventory", server.modelCatalogInventory)
	for _, tc := range []struct{ format, path, header, value string }{
		{"openai", "/v1/models", "", ""}, {"claude", "/v1/models", "Anthropic-Version", "2023-06-01"},
		{"codex", "/v1/models?client_version=cpa", "", ""}, {"grok", "/v1/models", "User-Agent", "grok-shell/1.0.0"},
		{"gemini", "/v1beta/models", "", ""},
	} {
		t.Run(tc.format, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			req.Header.Set("Authorization", "Bearer home-client")
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != 200 || strings.Contains(rec.Body.String(), `"claude-hidden"`) || strings.Contains(rec.Body.String(), `"models/claude-hidden"`) || !strings.Contains(rec.Body.String(), "claude-visible") {
				t.Fatalf("public: %d %s", rec.Code, rec.Body.String())
			}
			if got := (<-seen).Get("Authorization"); got != "Bearer home-client" {
				t.Fatalf("public credential=%q", got)
			}
			req = httptest.NewRequest("GET", "/inventory?format="+tc.format, nil)
			req.Header.Set("Authorization", "Bearer catalog-manager")
			rec = httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "claude-hidden") || strings.Contains(rec.Body.String(), "catalog-manager") || strings.Contains(rec.Body.String(), "home-client") {
				t.Fatalf("inventory: %d %s", rec.Code, rec.Body.String())
			}
			if got := (<-seen).Get("Authorization"); got != "Bearer home-client" {
				t.Fatalf("management bearer forwarded: %q", got)
			}
		})
	}
	// The actual management routes retain the existing Home availability restriction.
	rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog", "", "catalog-manager")
	if rec.Code == 200 {
		t.Fatal("Home management restriction changed")
	}
	cfg.APIKeys = nil
	req := httptest.NewRequest("GET", "/inventory", nil)
	req.Header.Set("Authorization", "Bearer catalog-manager")
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "catalog_client_key_required") {
		t.Fatalf("missing client key: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case h := <-seen:
		t.Fatalf("unexpected Home request: %v", h)
	default:
	}
}

func TestModelCatalogHomeInventoryErrorsAreNotEmptySuccess(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [home-client]\nremote-management: {secret-key: catalog-manager}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	cfg.Home.Enabled = true
	previous := home.Current()
	t.Cleanup(func() { home.SetCurrent(previous) })
	engine := gin.New()
	engine.GET("/inventory", server.modelCatalogInventory)
	for _, tc := range []struct {
		name, payload string
		status        int
	}{
		{"not connected", "", 503}, {"denied", `{"error":{"type":"invalid_credential","message":"denied"}}`, 401}, {"malformed", "not json", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.payload == "" {
				home.SetCurrent(nil)
			} else {
				home.SetCurrent(newHomeCatalogClient(t, tc.payload))
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest("GET", "/inventory", nil))
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}
