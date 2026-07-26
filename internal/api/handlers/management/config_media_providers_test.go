package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetMediaProvidersProjectsAuthIndexes(t *testing.T) {
	priority := 7
	cfg := &config.Config{MediaProviders: []config.MediaProvider{{
		Name: "Image Relay", Kind: config.MediaKindImage, BaseURL: "https://image.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "key-a", Priority: &priority}},
	}}}
	manager := coreauth.NewManager(nil, nil, nil)
	// The projection should still be valid when the manager has no live auth yet.
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/media-providers", nil)
	h.GetMediaProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Name          string `json:"name"`
			APIKeyEntries []struct {
				APIKey    string `json:"api-key"`
				Priority  *int   `json:"priority"`
				AuthIndex string `json:"auth-index"`
			} `json:"api-key-entries"`
		} `json:"media-providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Name != "Image Relay" || len(body.Items[0].APIKeyEntries) != 1 {
		t.Fatalf("body = %#v", body)
	}
	if body.Items[0].APIKeyEntries[0].APIKey != "key-a" || body.Items[0].APIKeyEntries[0].Priority == nil || *body.Items[0].APIKeyEntries[0].Priority != 7 {
		t.Fatalf("key projection = %#v", body.Items[0].APIKeyEntries[0])
	}
	if body.Items[0].APIKeyEntries[0].AuthIndex != "" {
		t.Fatalf("unexpected auth index without live auth: %#v", body.Items[0].APIKeyEntries[0])
	}
}

func TestPutMediaProvidersRejectsInvalidOperationAndNormalizes(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("media-providers: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.configFilePath = path
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/media-providers", strings.NewReader(`[{
		"name":" image ","kind":"IMAGE","base-url":"https://image.example/v1/",
		"api-key-entries":[{"api-key":" key "}],
		"operations":[{"name":"remove","method":"post","path":"remove","request-format":"json","model-mode":"none","response-format":"passthrough"}]
	}]`))
	h.PutMediaProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.MediaProviders) != 1 || h.cfg.MediaProviders[0].Kind != config.MediaKindImage || h.cfg.MediaProviders[0].BaseURL != "https://image.example/v1" || h.cfg.MediaProviders[0].Operations[0].Path != "/remove" {
		t.Fatalf("normalized config = %#v", h.cfg.MediaProviders)
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/media-providers", strings.NewReader(`[{"name":"bad","kind":"image","base-url":"https://x","operations":[{"name":"bad","method":"POST","path":"/x","request-format":"xml","model-mode":"none","response-format":"passthrough"}]}]`))
	h.PutMediaProviders(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid operation status = %d body=%s", rec.Code, rec.Body.String())
	}
}
