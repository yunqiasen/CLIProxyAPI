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
