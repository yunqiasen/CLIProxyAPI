package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestModelCatalogHidingOnlyChangesDiscovery(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [catalog-key]\nclient:\n  model-catalog:\n    hidden: [catalog-hidden]\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("catalog-fixture", "openai", []*registry.ModelInfo{{ID: "catalog-visible"}, {ID: "catalog-hidden"}})
	t.Cleanup(func() { reg.UnregisterClient("catalog-fixture") })
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer catalog-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	var got struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, item := range got.Data {
		ids = append(ids, item.ID)
	}
	if rec.Code != 200 || !reflect.DeepEqual(ids, []string{"catalog-visible"}) {
		t.Fatalf("catalog: status=%d ids=%v body=%s", rec.Code, ids, rec.Body.String())
	}
}

func TestModelCatalogManagementInventoryAndPreview(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [catalog-key]\nremote-management: {secret-key: catalog-manager}\nclient:\n  model-catalog:\n    hidden: [catalog-hidden]\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("catalog-manager"))
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("catalog-management", "openai", []*registry.ModelInfo{{ID: "catalog-visible"}, {ID: "catalog-hidden"}})
	t.Cleanup(func() { reg.UnregisterClient("catalog-management") })
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:4321"
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.engine.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("GET", "/v8/management/models/catalog", "", "catalog-key"); rec.Code != 401 {
		t.Fatalf("management auth=%d %s", rec.Code, rec.Body.String())
	}
	rec := request("GET", "/v8/management/models/catalog", "", "catalog-manager")
	if rec.Code != 200 {
		t.Fatalf("inventory=%d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Entries []struct {
			ID     string `json:"id"`
			Hidden bool   `json:"hidden"`
		} `json:"entries"`
		Visible []string `json:"visible_ids"`
		Counts  struct {
			Total  int `json:"total"`
			Hidden int `json:"hidden"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Counts.Total != 2 || result.Counts.Hidden != 1 || !reflect.DeepEqual(result.Visible, []string{"catalog-visible"}) {
		t.Fatalf("inventory=%s", rec.Body.String())
	}
	rec = request("POST", "/v8/management/models/catalog/preview", `{"format":"openai","policy":{"hidden":["*"],"order":"preserve","pinned":[]}}`, "catalog-manager")
	if rec.Code != 200 {
		t.Fatalf("preview=%d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Counts.Hidden != 2 || len(result.Visible) != 0 {
		t.Fatalf("preview=%s", rec.Body.String())
	}
	rec = request("GET", "/v1/models?include-hidden=true", "", "catalog-key")
	if strings.Contains(rec.Body.String(), "catalog-hidden") || !strings.Contains(rec.Body.String(), "catalog-visible") {
		t.Fatalf("preview mutated state or public bypass=%s", rec.Body.String())
	}
}

func TestModelCatalogGrokInventoryUsesGrokBuilder(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [catalog-key]\nremote-management: {secret-key: catalog-manager}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("catalog-manager"))
	registerCatalogFixtures(t, "catalog-grok-label", "openai", &registry.ModelInfo{ID: "grok-fixture", DisplayName: "Grok Display Label", ContextLength: 32768})
	public := catalogRequest(t, server, "GET", "/v1/models", "", "catalog-key", "User-Agent", "grok-shell/1.0")
	if public.Code != 200 || !strings.Contains(public.Body.String(), `"api_backend":"responses"`) || !strings.Contains(public.Body.String(), `"context_window":32768`) {
		t.Fatalf("grok public=%s", public.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v8/management/models/catalog?format=grok", ""},
		{"POST", "/v8/management/models/catalog/preview", `{"format":"grok","policy":{}}`},
	} {
		rec := catalogRequest(t, server, tc.method, tc.path, tc.body, "catalog-manager")
		var view struct{ Entries []struct{ ID, Label string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || len(view.Entries) != 1 || view.Entries[0].ID != "grok-fixture" || view.Entries[0].Label != "Grok Display Label" {
			t.Fatalf("grok inventory lost builder metadata: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestModelCatalogInventoryWithNilPluginMap(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [catalog-key]\nremote-management: {secret-key: catalog-manager}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Configs = nil
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("catalog-manager"))
	rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog", "", "catalog-manager")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"model_sort_enabled":false`) {
		t.Fatalf("nil plugin map: %d %s", rec.Code, rec.Body.String())
	}
}
