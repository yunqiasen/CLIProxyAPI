package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
