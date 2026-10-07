package handlers_test

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestModelCatalogConcurrentReloadUsesWholeSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &config.SDKConfig{Client: config.ClientConfig{ModelCatalog: config.ModelCatalogPolicy{Hidden: []string{"b"}}}}
	b := &config.SDKConfig{Client: config.ClientConfig{ModelCatalog: config.ModelCatalogPolicy{Hidden: []string{"a"}}}}
	h := handlers.NewBaseAPIHandlers(a, nil)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			h.UpdateClients(b)
			h.UpdateClients(a)
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 100 {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("GET", "/v1/models", nil)
				h.WriteModelListResponse(c, "openai", []byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
				got := rec.Body.String()
				if got != `{"data":[{"id":"a"}]}` && got != `{"data":[{"id":"b"}]}` {
					t.Errorf("mixed snapshot: %s", got)
				}
			}
		})
	}
	wg.Wait()
	snapshot := h.CatalogDisplayPolicy()
	snapshot.Hidden[0] = "changed"
	if h.CatalogDisplayPolicy().Hidden[0] == "changed" {
		t.Fatal("snapshot aliases runtime policy")
	}
}
