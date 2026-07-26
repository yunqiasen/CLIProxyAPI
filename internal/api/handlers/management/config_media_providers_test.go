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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
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

func TestGetMediaProvidersProjectsNoKeyAuthIndex(t *testing.T) {
	cfg := &config.Config{MediaProviders: []config.MediaProvider{{
		Name: "Public Audio", Kind: config.MediaKindAudio, BaseURL: "https://audio.example/v1",
	}}}
	id, _ := synthesizer.NewStableIDGenerator().Next(
		"media-provider:audio:public audio", "", "https://audio.example/v1", "",
	)
	manager := coreauth.NewManager(nil, nil, nil)
	registered, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: "media-audio-public-audio"})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

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
			AuthIndex string `json:"auth-index"`
		} `json:"media-providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].AuthIndex != registered.Index {
		t.Fatalf("no-key auth index = %#v, want %q", body.Items, registered.Index)
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

func TestPatchMediaProvidersPersistsStableAuthIDsAcrossIdentityChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(`media-providers:
  - name: Old Relay
    kind: image
    base-url: https://old.example/v1
    api-key-entries:
      - api-key: old-key
`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldProvider := config.MediaProvider{
		Name: "Old Relay", Kind: config.MediaKindImage, BaseURL: "https://old.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "old-key"}},
	}
	legacyID, _ := synthesizer.NewStableIDGenerator().Next(
		"media-provider:image:old relay", "old-key", "https://old.example/v1", "",
	)
	h := NewHandlerWithoutConfigFilePath(&config.Config{MediaProviders: []config.MediaProvider{oldProvider}}, nil)
	h.configFilePath = path

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/media-providers", strings.NewReader(`{
  "index": 0,
  "value": {
    "name": "Renamed Relay",
    "kind": "image",
    "base-url": "https://new.example/v2",
    "api-key-entries": [
      {"api-key": "new-key", "priority": 20},
      {"api-key": "added-key", "priority": 10}
    ]
  }
}`))
	h.PatchMediaProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	provider := h.cfg.MediaProviders[0]
	if len(provider.APIKeyEntries) != 2 {
		t.Fatalf("key entries = %#v", provider.APIKeyEntries)
	}
	if provider.APIKeyEntries[0].AuthID != legacyID {
		t.Fatalf("edited key auth ID = %q, want legacy %q", provider.APIKeyEntries[0].AuthID, legacyID)
	}
	if provider.APIKeyEntries[1].AuthID == "" || provider.APIKeyEntries[1].AuthID == legacyID {
		t.Fatalf("new key auth ID = %q, want a new stable ID", provider.APIKeyEntries[1].AuthID)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(persisted), "auth-id:") != 2 {
		t.Fatalf("persisted config does not contain both stable auth IDs:\n%s", persisted)
	}
}

func TestPatchMediaProvidersDoesNotReuseDeletedAuthIDForNewKey(t *testing.T) {
	previous := config.MediaProvider{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{
			{APIKey: "old-key", AuthID: "media-provider:image:old"},
		},
	}
	incoming := config.MediaProvider{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "new-key"}},
	}
	prepared := prepareMediaProviderAuthIDs([]config.MediaProvider{previous}, []config.MediaProvider{incoming})
	if len(prepared) != 1 || len(prepared[0].APIKeyEntries) != 1 {
		t.Fatalf("prepared providers = %#v", prepared)
	}
	if prepared[0].APIKeyEntries[0].AuthID == previous.APIKeyEntries[0].AuthID {
		t.Fatalf("new key reused deleted auth ID %q", prepared[0].APIKeyEntries[0].AuthID)
	}
}

func TestPatchMediaProvidersAcceptsOpaqueAuthIndexForEditedKey(t *testing.T) {
	previous := config.MediaProvider{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{
			{APIKey: "old-key", AuthID: "media-provider:image:old"},
		},
	}
	incoming := config.MediaProvider{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "edited-key", AuthID: "media-provider:image:old"}},
	}
	prepared := prepareMediaProviderAuthIDs([]config.MediaProvider{previous}, []config.MediaProvider{incoming})
	if got := prepared[0].APIKeyEntries[0].AuthID; got != "media-provider:image:old" {
		t.Fatalf("edited key auth ID = %q, want opaque auth ID preserved", got)
	}
}

func TestPatchMediaProvidersDecodesOpaqueAuthIndex(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("media-providers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := config.MediaProvider{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{{
			APIKey: "old-key", AuthID: "media-provider:image:persisted",
		}},
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{MediaProviders: []config.MediaProvider{previous}}, nil)
	h.configFilePath = path
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/media-providers", strings.NewReader(`{
		"index": 0,
		"value": {
			"name": "Relay",
			"kind": "image",
			"base-url": "https://relay.example/v1",
			"api-key-entries": [{"api-key": "edited-key", "auth-index": "media-provider:image:persisted"}]
		}
	}`))
	h.PatchMediaProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.MediaProviders[0].APIKeyEntries[0].AuthID; got != "media-provider:image:persisted" {
		t.Fatalf("decoded auth index = %q, want persisted auth ID", got)
	}
}

func TestValidateMediaProvidersRejectsMalformedOperationContract(t *testing.T) {
	base := config.MediaProvider{
		Name:    "Image Relay",
		Kind:    config.MediaKindImage,
		BaseURL: "https://image.example/v1",
		Models: []config.MediaModel{{
			Name:         "image-model",
			Capabilities: []string{config.MediaCapabilityGenerate},
		}},
		Operations: []config.MediaOperation{{
			Name:           "generate",
			Capability:     config.MediaCapabilityGenerate,
			Method:         http.MethodPost,
			Path:           "/generate",
			RequestFormat:  config.MediaRequestJSON,
			ModelMode:      config.MediaModelRequired,
			ResponseFormat: config.MediaResponsePassthrough,
		}},
	}
	cases := []config.MediaProvider{
		func() config.MediaProvider {
			item := base
			item.Operations = []config.MediaOperation{{Name: "bad-method", Method: "TRACE", Path: "/x", RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough}}
			return item
		}(),
		func() config.MediaProvider {
			item := base
			item.Operations = []config.MediaOperation{{Name: "bad-path", Method: http.MethodPost, RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough}}
			return item
		}(),
		func() config.MediaProvider {
			item := base
			item.Operations = []config.MediaOperation{{Name: "bad-capability", Capability: "not-a-capability", Method: http.MethodPost, Path: "/x", RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough}}
			return item
		}(),
		func() config.MediaProvider {
			item := base
			item.Operations = []config.MediaOperation{{Name: "missing-result", Method: http.MethodPost, Path: "/x", RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponseJSONURL}}
			return item
		}(),
		func() config.MediaProvider {
			item := base
			item.Operations = []config.MediaOperation{{Name: "bad-async", Method: http.MethodPost, Path: "/x", RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough, Async: &config.MediaAsyncOperation{TaskIDPath: "id", PollPath: "/tasks/{task_id}", StatusPath: "status"}}}
			return item
		}(),
	}
	for index, item := range cases {
		if err := validateMediaProviders([]config.MediaProvider{item}); err == nil {
			t.Errorf("case %d was accepted: %#v", index, item)
		}
	}
}

func TestValidateMediaProvidersRejectsDuplicateIdentityAndModel(t *testing.T) {
	provider := config.MediaProvider{
		Name:    "Relay",
		Kind:    config.MediaKindVideo,
		BaseURL: "https://video.example",
		Models: []config.MediaModel{
			{Name: "model-a", Capabilities: []string{config.MediaCapabilityTextToVideo}},
			{Name: "MODEL-A", Capabilities: []string{config.MediaCapabilityTextToVideo}},
		},
	}
	if err := validateMediaProviders([]config.MediaProvider{provider}); err == nil {
		t.Fatal("duplicate model name was accepted")
	}
	provider.Models = nil
	if err := validateMediaProviders([]config.MediaProvider{provider, provider}); err == nil {
		t.Fatal("duplicate provider identity was accepted")
	}
}

func TestValidateMediaProvidersAcceptsModelFreeOperation(t *testing.T) {
	provider := config.MediaProvider{
		Name:    "Public Audio",
		Kind:    config.MediaKindAudio,
		BaseURL: "https://audio.example",
		Operations: []config.MediaOperation{{
			Name:           "remove-watermark",
			Capability:     config.MediaCapabilityVoiceConvert,
			Method:         http.MethodPost,
			Path:           "/process",
			RequestFormat:  config.MediaRequestBinary,
			ModelMode:      config.MediaModelNone,
			ResponseFormat: config.MediaResponseBinary,
		}},
	}
	if err := validateMediaProviders([]config.MediaProvider{provider}); err != nil {
		t.Fatalf("model-free provider rejected: %v", err)
	}
}

func TestPatchMediaProvidersResolvesLiveAuthIndexWhenKeyChanges(t *testing.T) {
	previous := config.MediaProvider{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "old-key", AuthID: "media-provider:image:stable"}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	registered, err := manager.Register(t.Context(), &coreauth.Auth{
		ID: "media-provider:image:stable", Provider: "media-image-relay", Label: "Relay",
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{MediaProviders: []config.MediaProvider{previous}}, manager)
	h.configFilePath = t.TempDir() + "/config.yaml"
	if err := os.WriteFile(h.configFilePath, []byte("media-providers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/media-providers", strings.NewReader(`{
		"index": 0,
		"value": {
			"name": "Renamed Relay",
			"kind": "image",
			"base-url": "https://new.example/v2",
			"api-key-entries": [{"api-key": "new-key", "auth-index": "`+registered.Index+`"}]
		}
	}`))
	h.PatchMediaProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.MediaProviders[0].APIKeyEntries[0].AuthID; got != previous.APIKeyEntries[0].AuthID {
		t.Fatalf("auth ID after live-index patch = %q, want %q", got, previous.APIKeyEntries[0].AuthID)
	}
}

func TestPrepareMediaProviderAuthIDsMatchesReorderedRenamedProvidersByAuthID(t *testing.T) {
	previous := []config.MediaProvider{
		{
			Name: "First", Kind: config.MediaKindImage, BaseURL: "https://first.example/v1",
			APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "first-key", AuthID: "media-provider:image:first-stable"}},
		},
		{
			Name: "Second", Kind: config.MediaKindImage, BaseURL: "https://second.example/v1",
			APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "second-key", AuthID: "media-provider:image:second-stable"}},
		},
	}
	incoming := []config.MediaProvider{
		{
			Name: "Second Renamed", Kind: config.MediaKindImage, BaseURL: "https://second-new.example/v2",
			APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "second-new-key", AuthID: "media-provider:image:second-stable"}},
		},
		{
			Name: "First Renamed", Kind: config.MediaKindImage, BaseURL: "https://first-new.example/v2",
			APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "first-new-key", AuthID: "media-provider:image:first-stable"}},
		},
	}

	prepared := prepareMediaProviderAuthIDs(previous, incoming)
	if got := prepared[0].APIKeyEntries[0].AuthID; got != "media-provider:image:second-stable" {
		t.Fatalf("second provider auth ID = %q", got)
	}
	if got := prepared[1].APIKeyEntries[0].AuthID; got != "media-provider:image:first-stable" {
		t.Fatalf("first provider auth ID = %q", got)
	}
}

func TestPrepareMediaProviderAuthIDsPreservesReorderedKeysAndAllocatesOnlyNewIdentity(t *testing.T) {
	previous := []config.MediaProvider{{
		Name: "Relay", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v1",
		APIKeyEntries: []config.MediaAPIKeyEntry{
			{APIKey: "first", AuthID: "media-provider:image:first"},
			{APIKey: "second", AuthID: "media-provider:image:second"},
		},
	}}
	incoming := []config.MediaProvider{{
		Name: "Relay Renamed", Kind: config.MediaKindImage, BaseURL: "https://relay.example/v2",
		APIKeyEntries: []config.MediaAPIKeyEntry{
			{APIKey: "second-edited", AuthID: "media-provider:image:second"},
			{APIKey: "new", AuthID: ""},
			{APIKey: "first-edited", AuthID: "media-provider:image:first"},
		},
	}}

	prepared := prepareMediaProviderAuthIDs(previous, incoming)
	if got := prepared[0].APIKeyEntries[0].AuthID; got != "media-provider:image:second" {
		t.Fatalf("reordered second key auth ID = %q", got)
	}
	if got := prepared[0].APIKeyEntries[2].AuthID; got != "media-provider:image:first" {
		t.Fatalf("reordered first key auth ID = %q", got)
	}
	if got := prepared[0].APIKeyEntries[1].AuthID; got == "" || got == "media-provider:image:first" || got == "media-provider:image:second" {
		t.Fatalf("new key auth ID = %q, want fresh identity", got)
	}
}

func TestPatchMediaProvidersRetainsNoKeyAuthIDFromRuntimeIndex(t *testing.T) {
	previous := config.MediaProvider{
		Name: "Public Audio", Kind: config.MediaKindAudio, BaseURL: "https://audio.example/v1",
		AuthID: "media-provider:audio:stable-public",
	}
	manager := coreauth.NewManager(nil, nil, nil)
	registered, err := manager.Register(t.Context(), &coreauth.Auth{
		ID: previous.AuthID, Provider: "media-audio-public-audio", Label: previous.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{MediaProviders: []config.MediaProvider{previous}}, manager)
	h.configFilePath = t.TempDir() + "/config.yaml"
	if err := os.WriteFile(h.configFilePath, []byte("media-providers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/media-providers", strings.NewReader(`{
		"index": 0,
		"value": {
			"name": "Public Audio Renamed",
			"kind": "audio",
			"base-url": "https://audio-new.example/v2",
			"auth-index": "`+registered.Index+`"
		}
	}`))
	h.PatchMediaProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.MediaProviders[0].AuthID; got != previous.AuthID {
		t.Fatalf("no-key auth ID after edit = %q, want %q", got, previous.AuthID)
	}
}
