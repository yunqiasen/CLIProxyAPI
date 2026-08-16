package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetGroupedNativeKeysIncludesNestedAuthIndexes(t *testing.T) {
	tests := []struct {
		name        string
		kind        string
		provider    string
		responseKey string
		config      *config.Config
		get         func(*Handler, *gin.Context)
	}{
		{
			name: "gemini", kind: "gemini:apikey", provider: "gemini", responseKey: "gemini-api-key",
			config: &config.Config{GeminiKey: []config.GeminiKey{{Name: "gemini-group", BaseURL: "https://gemini.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "gemini-a"}, {APIKey: "gemini-b"}}}}},
			get:    (*Handler).GetGeminiKeys,
		},
		{
			name: "claude", kind: "claude:apikey", provider: "claude", responseKey: "claude-api-key",
			config: &config.Config{ClaudeKey: []config.ClaudeKey{{Name: "claude-group", BaseURL: "https://claude.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "claude-a"}, {APIKey: "claude-b"}}}}},
			get:    (*Handler).GetClaudeKeys,
		},
		{
			name: "codex", kind: "codex:apikey", provider: "codex", responseKey: "codex-api-key",
			config: &config.Config{CodexKey: []config.CodexKey{{Name: "codex-group", BaseURL: "https://codex.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "codex-a"}, {APIKey: "codex-b"}}}}},
			get:    (*Handler).GetCodexKeys,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			idGen := synthesizer.NewStableIDGenerator()
			baseURL := "https://" + tt.name + ".example/v1"
			for index, key := range []string{tt.name + "-a", tt.name + "-b"} {
				authID, _ := idGen.Next(tt.kind, key, baseURL, strconv.Itoa(index))
				_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
					ID:       authID,
					Index:    "auth-" + key,
					Provider: tt.provider,
					Status:   coreauth.StatusActive,
				})
				if errRegister != nil {
					t.Fatalf("register auth %s: %v", key, errRegister)
				}
			}

			h := NewHandlerWithoutConfigFilePath(tt.config, manager)
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/"+tt.responseKey, nil)
			tt.get(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var body map[string][]struct {
				AuthIndex     string `json:"auth-index"`
				APIKeyEntries []struct {
					APIKey    string `json:"api-key"`
					AuthIndex string `json:"auth-index"`
				} `json:"api-key-entries"`
			}
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
				t.Fatalf("decode response: %v", errDecode)
			}
			entries := body[tt.responseKey]
			if len(entries) != 1 || len(entries[0].APIKeyEntries) != 2 {
				t.Fatalf("unexpected response: %s", rec.Body.String())
			}
			if entries[0].AuthIndex != "" {
				t.Fatalf("group auth-index = %q, want empty", entries[0].AuthIndex)
			}
			for _, keyEntry := range entries[0].APIKeyEntries {
				if got, want := keyEntry.AuthIndex, "auth-"+keyEntry.APIKey; got != want {
					t.Fatalf("nested auth-index for %s = %q, want %q", keyEntry.APIKey, got, want)
				}
			}
		})
	}
}

func TestGetNativeKeysPreservesLegacyTopLevelAuthIndex(t *testing.T) {
	tests := []struct {
		name        string
		kind        string
		provider    string
		responseKey string
		config      *config.Config
		get         func(*Handler, *gin.Context)
	}{
		{name: "gemini", kind: "gemini:apikey", provider: "gemini", responseKey: "gemini-api-key", config: &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "legacy", BaseURL: "https://legacy.example"}}}, get: (*Handler).GetGeminiKeys},
		{name: "claude", kind: "claude:apikey", provider: "claude", responseKey: "claude-api-key", config: &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "legacy", BaseURL: "https://legacy.example"}}}, get: (*Handler).GetClaudeKeys},
		{name: "codex", kind: "codex:apikey", provider: "codex", responseKey: "codex-api-key", config: &config.Config{CodexKey: []config.CodexKey{{APIKey: "legacy", BaseURL: "https://legacy.example"}}}, get: (*Handler).GetCodexKeys},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idGen := synthesizer.NewStableIDGenerator()
			authID, _ := idGen.Next(tt.kind, "legacy", "https://legacy.example")
			manager := coreauth.NewManager(nil, nil, nil)
			_, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Index: "legacy-index", Provider: tt.provider, Status: coreauth.StatusActive})
			if errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			h := NewHandlerWithoutConfigFilePath(tt.config, manager)
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/"+tt.responseKey, nil)
			tt.get(h, ctx)

			var body map[string][]struct {
				AuthIndex string `json:"auth-index"`
			}
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
				t.Fatalf("decode response: %v", errDecode)
			}
			if got := body[tt.responseKey][0].AuthIndex; got != "legacy-index" {
				t.Fatalf("auth-index = %q, want legacy-index; body=%s", got, rec.Body.String())
			}
		})
	}
}

func TestPutNativeKeysPersistsStableIdentityFromAuthIndex(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		provider string
		put      func(*Handler, *gin.Context)
		entries  func(*config.Config) []config.NativeAPIKeyEntry
	}{
		{name: "gemini", kind: "gemini:apikey", provider: "gemini", put: (*Handler).PutGeminiKeys, entries: func(cfg *config.Config) []config.NativeAPIKeyEntry { return cfg.GeminiKey[0].APIKeyEntries }},
		{name: "claude", kind: "claude:apikey", provider: "claude", put: (*Handler).PutClaudeKeys, entries: func(cfg *config.Config) []config.NativeAPIKeyEntry { return cfg.ClaudeKey[0].APIKeyEntries }},
		{name: "codex", kind: "codex:apikey", provider: "codex", put: (*Handler).PutCodexKeys, entries: func(cfg *config.Config) []config.NativeAPIKeyEntry { return cfg.CodexKey[0].APIKeyEntries }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const stableID = "stable-native-auth-id"
			manager := coreauth.NewManager(nil, nil, nil)
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: stableID, Index: "public-auth-index", Provider: tt.provider, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "old-key"}}); err != nil {
				t.Fatal(err)
			}
			h := NewHandler(&config.Config{}, writeTestConfigFile(t), manager)
			if got := h.liveAuthIDByIndex()["public-auth-index"]; got != stableID {
				t.Fatalf("live auth reverse lookup = %q", got)
			}
			body := `[{"name":"relay","base-url":"https://native.example/v1","api-key-entries":[{"api-key":"changed-key","auth-index":"public-auth-index"}]}]`
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/"+tt.name+"-api-key", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			tt.put(h, ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			entries := tt.entries(h.cfg)
			if len(entries) != 1 || entries[0].AuthID != stableID {
				t.Fatalf("persisted entries = %#v", entries)
			}
		})
	}
}

func TestPatchNativeKeysPersistsStableIdentityFromAuthIndex(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		config   *config.Config
		patch    func(*Handler, *gin.Context)
		entries  func(*config.Config) []config.NativeAPIKeyEntry
	}{
		{name: "gemini", provider: "gemini", config: &config.Config{GeminiKey: []config.GeminiKey{{Name: "relay", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "old-key", AuthID: "stable-native-auth-id"}}}}}, patch: (*Handler).PatchGeminiKey, entries: func(cfg *config.Config) []config.NativeAPIKeyEntry { return cfg.GeminiKey[0].APIKeyEntries }},
		{name: "claude", provider: "claude", config: &config.Config{ClaudeKey: []config.ClaudeKey{{Name: "relay", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "old-key", AuthID: "stable-native-auth-id"}}}}}, patch: (*Handler).PatchClaudeKey, entries: func(cfg *config.Config) []config.NativeAPIKeyEntry { return cfg.ClaudeKey[0].APIKeyEntries }},
		{name: "codex", provider: "codex", config: &config.Config{CodexKey: []config.CodexKey{{Name: "relay", BaseURL: "https://native.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "old-key", AuthID: "stable-native-auth-id"}}}}}, patch: (*Handler).PatchCodexKey, entries: func(cfg *config.Config) []config.NativeAPIKeyEntry { return cfg.CodexKey[0].APIKeyEntries }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "stable-native-auth-id", Index: "public-auth-index", Provider: tt.provider, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "old-key"}}); err != nil {
				t.Fatal(err)
			}
			h := NewHandler(tt.config, writeTestConfigFile(t), manager)
			body := `{"index":0,"value":{"api-key-entries":[{"api-key":"changed-key","auth-index":"public-auth-index"}]}}`
			rec := performPatch(t, func(c *gin.Context) { tt.patch(h, c) }, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			entries := tt.entries(h.cfg)
			if len(entries) != 1 || entries[0].AuthID != "stable-native-auth-id" {
				t.Fatalf("persisted entries = %#v", entries)
			}
		})
	}
}

func TestPrepareNativeAuthIDsDoesNotReuseDeletedIdentityForNewKey(t *testing.T) {
	previous := []config.NativeAPIKeyEntry{{APIKey: "old-key", AuthID: "claude:apikey:old"}}
	prepared := prepareNativeAuthIDs("claude:apikey", previous, []config.NativeAPIKeyEntry{{APIKey: "new-key"}})
	if len(prepared) != 1 || prepared[0].AuthID == "" || prepared[0].AuthID == previous[0].AuthID {
		t.Fatalf("prepared entries = %#v", prepared)
	}
}

func TestPrepareNativeAuthIDsRejectsUntrustedIncomingIdentity(t *testing.T) {
	prepared := prepareNativeAuthIDs("claude:apikey", nil, []config.NativeAPIKeyEntry{{APIKey: "new-key", AuthID: "other-provider-auth"}})
	if len(prepared) != 1 || prepared[0].AuthID == "" || prepared[0].AuthID == "other-provider-auth" {
		t.Fatalf("prepared entries = %#v", prepared)
	}
}

func TestPutNativeKeysAcceptsGroupedEntries(t *testing.T) {
	tests := []struct {
		name string
		put  func(*Handler, *gin.Context)
		get  func(*config.Config) (string, int, []config.NativeAPIKeyEntry)
	}{
		{name: "gemini", put: (*Handler).PutGeminiKeys, get: func(cfg *config.Config) (string, int, []config.NativeAPIKeyEntry) {
			entry := cfg.GeminiKey[0]
			return entry.Name, entry.Priority, entry.APIKeyEntries
		}},
		{name: "claude", put: (*Handler).PutClaudeKeys, get: func(cfg *config.Config) (string, int, []config.NativeAPIKeyEntry) {
			entry := cfg.ClaudeKey[0]
			return entry.Name, entry.Priority, entry.APIKeyEntries
		}},
		{name: "codex", put: (*Handler).PutCodexKeys, get: func(cfg *config.Config) (string, int, []config.NativeAPIKeyEntry) {
			entry := cfg.CodexKey[0]
			return entry.Name, entry.Priority, entry.APIKeyEntries
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
			body := `[{
				"name":"native-group",
				"priority":7,
				"base-url":"https://native.example/v1",
				"api-key-entries":[{"api-key":"key-a","priority":9,"proxy-url":"http://proxy.example"}]
			}]`
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/"+tt.name+"-api-key", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			tt.put(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			name, priority, entries := tt.get(h.cfg)
			if name != "native-group" || priority != 7 || len(entries) != 1 || entries[0].APIKey != "key-a" || entries[0].Priority == nil || *entries[0].Priority != 9 || entries[0].ProxyURL != "http://proxy.example" {
				t.Fatalf("grouped entry not preserved: name=%q priority=%d entries=%#v", name, priority, entries)
			}
		})
	}
}

func TestPatchGeminiKeyAcceptsGroupedAndProtocolFields(t *testing.T) {
	h := &Handler{cfg: &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "old", BaseURL: "https://old.example"}}}, configFilePath: writeTestConfigFile(t)}
	body := `{"index":0,"value":{"name":"gemini-group","api-key-entries":[{"api-key":"gemini-key","priority":8,"proxy-url":"http://key-proxy"}],"api-key":"legacy","priority":6,"prefix":"gem","base-url":"https://gemini.example","proxy-url":"http://group-proxy","models":[{"name":"gemini-model","alias":"model"}],"headers":{"X-Test":" value "},"excluded-models":[" excluded "],"disable-cooling":true}}`
	rec := performPatch(t, h.PatchGeminiKey, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.GeminiKey[0]
	if entry.Name != "gemini-group" || entry.Priority != 6 || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "gemini-key" || entry.APIKey != "legacy" || entry.Prefix != "gem" || entry.BaseURL != "https://gemini.example" || entry.ProxyURL != "http://group-proxy" || len(entry.Models) != 1 || entry.Models[0].Name != "gemini-model" || entry.Headers["X-Test"] != "value" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excluded" || !entry.DisableCooling {
		t.Fatalf("patched Gemini entry = %#v", entry)
	}
}

func TestPatchClaudeKeyAcceptsGroupedAndProtocolFields(t *testing.T) {
	h := &Handler{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "old", BaseURL: "https://old.example"}}}, configFilePath: writeTestConfigFile(t)}
	body := `{"index":0,"value":{"name":"claude-group","api-key-entries":[{"api-key":"claude-key","priority":8,"proxy-url":"http://key-proxy"}],"api-key":"legacy","priority":6,"prefix":"claude","base-url":"https://claude.example","proxy-url":"http://group-proxy","models":[{"name":"claude-model","alias":"model"}],"headers":{"X-Test":" value "},"excluded-models":[" excluded "],"rebuild-mid-system-message":true,"disable-cooling":true,"cloak":{"mode":"always","strict-mode":true},"experimental-cch-signing":true}}`
	rec := performPatch(t, h.PatchClaudeKey, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.ClaudeKey[0]
	if entry.Name != "claude-group" || entry.Priority != 6 || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "claude-key" || entry.APIKey != "legacy" || entry.Prefix != "claude" || entry.BaseURL != "https://claude.example" || entry.ProxyURL != "http://group-proxy" || len(entry.Models) != 1 || entry.Models[0].Name != "claude-model" || entry.Headers["X-Test"] != "value" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excluded" || !entry.RebuildMidSystemMessage || !entry.DisableCooling || entry.Cloak == nil || entry.Cloak.Mode != "always" || !entry.Cloak.StrictMode || !entry.ExperimentalCCHSigning {
		t.Fatalf("patched Claude entry = %#v", entry)
	}
}

func TestPatchCodexKeyAcceptsGroupedAndProtocolFields(t *testing.T) {
	h := &Handler{cfg: &config.Config{CodexKey: []config.CodexKey{{APIKey: "old", BaseURL: "https://old.example"}}}, configFilePath: writeTestConfigFile(t)}
	body := `{"index":0,"value":{"name":"codex-group","api-key-entries":[{"api-key":"codex-key","priority":8,"proxy-url":"http://key-proxy"}],"api-key":"legacy","priority":6,"prefix":"codex","base-url":"https://codex.example","websockets":true,"proxy-url":"http://group-proxy","models":[{"name":"codex-model","alias":"model"}],"headers":{"X-Test":" value "},"excluded-models":[" excluded "],"disable-image-generation":true,"disable-cooling":true}}`
	rec := performPatch(t, h.PatchCodexKey, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.CodexKey[0]
	if entry.Name != "codex-group" || entry.Priority != 6 || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "codex-key" || entry.APIKey != "legacy" || entry.Prefix != "codex" || entry.BaseURL != "https://codex.example" || !entry.Websockets || entry.ProxyURL != "http://group-proxy" || len(entry.Models) != 1 || entry.Models[0].Name != "codex-model" || entry.Headers["X-Test"] != "value" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excluded" || !entry.DisableImageGeneration || !entry.DisableCooling {
		t.Fatalf("patched Codex entry = %#v", entry)
	}
}

func performPatch(t *testing.T, patch func(*gin.Context), body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/native-api-key", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	patch(ctx)
	return rec
}

func TestPatchNativeKeyKeepsOnlyGroupsWithEffectiveKeys(t *testing.T) {
	tests := []struct {
		name       string
		newConfig  func() *config.Config
		patch      func(*Handler, *gin.Context)
		entryCount func(*config.Config) int
		entryKeys  func(*config.Config) (string, []config.NativeAPIKeyEntry)
	}{
		{
			name: "gemini",
			newConfig: func() *config.Config {
				return &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "legacy", BaseURL: "https://native.example/v1"}}}
			},
			patch:      func(h *Handler, ctx *gin.Context) { h.PatchGeminiKey(ctx) },
			entryCount: func(cfg *config.Config) int { return len(cfg.GeminiKey) },
			entryKeys: func(cfg *config.Config) (string, []config.NativeAPIKeyEntry) {
				return cfg.GeminiKey[0].APIKey, cfg.GeminiKey[0].APIKeyEntries
			},
		},
		{
			name: "claude",
			newConfig: func() *config.Config {
				return &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "legacy", BaseURL: "https://native.example/v1"}}}
			},
			patch:      func(h *Handler, ctx *gin.Context) { h.PatchClaudeKey(ctx) },
			entryCount: func(cfg *config.Config) int { return len(cfg.ClaudeKey) },
			entryKeys: func(cfg *config.Config) (string, []config.NativeAPIKeyEntry) {
				return cfg.ClaudeKey[0].APIKey, cfg.ClaudeKey[0].APIKeyEntries
			},
		},
		{
			name: "codex",
			newConfig: func() *config.Config {
				return &config.Config{CodexKey: []config.CodexKey{{APIKey: "legacy", BaseURL: "https://native.example/v1"}}}
			},
			patch:      func(h *Handler, ctx *gin.Context) { h.PatchCodexKey(ctx) },
			entryCount: func(cfg *config.Config) int { return len(cfg.CodexKey) },
			entryKeys: func(cfg *config.Config) (string, []config.NativeAPIKeyEntry) {
				return cfg.CodexKey[0].APIKey, cfg.CodexKey[0].APIKeyEntries
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" preserves grouped key", func(t *testing.T) {
			h := &Handler{cfg: tt.newConfig(), configFilePath: writeTestConfigFile(t)}
			rec := performPatch(t, func(ctx *gin.Context) { tt.patch(h, ctx) }, `{"index":0,"value":{"api-key":"","api-key-entries":[{"api-key":"grouped-key"}]}}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := tt.entryCount(h.cfg); got != 1 {
				t.Fatalf("entry count = %d, want 1", got)
			}
			legacy, entries := tt.entryKeys(h.cfg)
			if legacy != "" || len(entries) != 1 || entries[0].APIKey != "grouped-key" {
				t.Fatalf("patched keys = legacy %q entries %#v", legacy, entries)
			}
		})

		t.Run(tt.name+" removes group without effective key", func(t *testing.T) {
			h := &Handler{cfg: tt.newConfig(), configFilePath: writeTestConfigFile(t)}
			rec := performPatch(t, func(ctx *gin.Context) { tt.patch(h, ctx) }, `{"index":0,"value":{"api-key":"","api-key-entries":[{"api-key":"  "}]}}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := tt.entryCount(h.cfg); got != 0 {
				t.Fatalf("entry count = %d, want 0", got)
			}
		})
	}
}

func TestPutNativeKeysRejectsAuthIndexFromAnotherNativeProvider(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	const foreignID = "gemini:apikey:foreign"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: foreignID, Index: "foreign-public-index", Provider: "gemini", Status: coreauth.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{}, writeTestConfigFile(t), manager)
	body := `[{
		"name":"claude-group",
		"base-url":"https://claude.example/v1",
		"api-key-entries":[{"api-key":"claude-key","auth-index":"foreign-public-index"}]
	}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	entries := h.cfg.ClaudeKey[0].APIKeyEntries
	if len(entries) != 1 || entries[0].AuthID == foreignID {
		t.Fatalf("foreign auth identity was accepted: %#v", entries)
	}
}

func TestPutNativeKeysMatchesReorderedGroupsBeforeReusingSharedKeyIdentity(t *testing.T) {
	previous := []config.ClaudeKey{
		{
			Name: "First Relay", BaseURL: "https://first.example/v1",
			APIKeyEntries: []config.NativeAPIKeyEntry{{AuthID: "claude:apikey:first", APIKey: "shared-key"}},
		},
		{
			Name: "Second Relay", BaseURL: "https://second.example/v1",
			APIKeyEntries: []config.NativeAPIKeyEntry{{AuthID: "claude:apikey:second", APIKey: "shared-key"}},
		},
	}
	h := NewHandler(&config.Config{ClaudeKey: previous}, writeTestConfigFile(t), nil)
	body := `[{
		"name":"Second Relay",
		"base-url":"https://second.example/v1",
		"api-key-entries":[{"api-key":"shared-key"}]
	},{
		"name":"First Relay",
		"base-url":"https://first.example/v1",
		"api-key-entries":[{"api-key":"shared-key"}]
	}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].APIKeyEntries[0].AuthID; got != "claude:apikey:second" {
		t.Fatalf("reordered second group auth ID = %q", got)
	}
	if got := h.cfg.ClaudeKey[1].APIKeyEntries[0].AuthID; got != "claude:apikey:first" {
		t.Fatalf("reordered first group auth ID = %q", got)
	}
}

func TestPutNativeKeysUsesAuthIndexWhenProviderNameAndBaseURLChange(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	const stableID = "claude:apikey:stable"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: stableID, Index: "stable-public-index", Provider: "claude", Status: coreauth.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	previous := []config.ClaudeKey{{
		Name: "Old Relay", BaseURL: "https://old.example/v1",
		APIKeyEntries: []config.NativeAPIKeyEntry{{AuthID: stableID, APIKey: "old-key"}},
	}}
	h := NewHandler(&config.Config{ClaudeKey: previous}, writeTestConfigFile(t), manager)
	body := `[{
		"name":"New Relay",
		"base-url":"https://new.example/v2",
		"api-key-entries":[{"api-key":"new-key","auth-index":"stable-public-index"}]
	}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].APIKeyEntries[0].AuthID; got != stableID {
		t.Fatalf("edited provider auth ID = %q", got)
	}
}

func TestPutNativeKeysReorderedGroupsUseAuthIndexesWhenAllFieldsChange(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{ID: "claude:apikey:first", Index: "first-public-index", Provider: "claude", Status: coreauth.StatusActive},
		{ID: "claude:apikey:second", Index: "second-public-index", Provider: "claude", Status: coreauth.StatusActive},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	previous := []config.ClaudeKey{
		{Name: "First Relay", BaseURL: "https://first.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{AuthID: "claude:apikey:first", APIKey: "first-old"}}},
		{Name: "Second Relay", BaseURL: "https://second.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{AuthID: "claude:apikey:second", APIKey: "second-old"}}},
	}
	h := NewHandler(&config.Config{ClaudeKey: previous}, writeTestConfigFile(t), manager)
	body := `[{
		"name":"Second Renamed",
		"base-url":"https://second-new.example/v2",
		"api-key-entries":[{"api-key":"second-new","auth-index":"second-public-index"}]
	},{
		"name":"First Renamed",
		"base-url":"https://first-new.example/v2",
		"api-key-entries":[{"api-key":"first-new","auth-index":"first-public-index"}]
	}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].APIKeyEntries[0].AuthID; got != "claude:apikey:second" {
		t.Fatalf("reordered second group auth ID = %q", got)
	}
	if got := h.cfg.ClaudeKey[1].APIKeyEntries[0].AuthID; got != "claude:apikey:first" {
		t.Fatalf("reordered first group auth ID = %q", got)
	}
}

func TestPutNativeKeysMigratesLegacyGroupedIdentityOnNameEdit(t *testing.T) {
	const apiKey = "legacy-key"
	const baseURL = "https://claude.example/v1"
	legacyID, _ := synthesizer.NewStableIDGenerator().Next("claude:apikey", apiKey, baseURL, "0")
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: legacyID, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": apiKey, "base_url": baseURL},
	}); err != nil {
		t.Fatal(err)
	}
	previous := []config.ClaudeKey{{
		Name: "Old Relay", BaseURL: baseURL,
		APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: apiKey}},
	}}
	h := NewHandler(&config.Config{ClaudeKey: previous}, writeTestConfigFile(t), manager)
	body := `[{
		"name":"Renamed Relay",
		"base-url":"https://claude.example/v1",
		"api-key-entries":[{"api-key":"legacy-key"}]
	}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].APIKeyEntries[0].AuthID; got != legacyID {
		t.Fatalf("legacy grouped auth ID = %q, want %q", got, legacyID)
	}
}

func TestCodexDisableImageGenerationPersistsAcrossGetPutAndPatch(t *testing.T) {
	configPath := writeTestConfigFile(t)
	h := &Handler{cfg: &config.Config{}, configFilePath: configPath}

	putBody := `[{"name":"relay","base-url":"https://relay.example/v1","api-key":"key","disable-image-generation":true}]`
	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/codex-api-key", strings.NewReader(putBody))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutCodexKeys(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}
	if len(h.cfg.CodexKey) != 1 || !h.cfg.CodexKey[0].DisableImageGeneration {
		t.Fatalf("PUT config = %#v, want disable-image-generation true", h.cfg.CodexKey)
	}
	persistedTrue, errLoadTrue := config.LoadConfig(configPath)
	if errLoadTrue != nil {
		t.Fatalf("load persisted true config: %v", errLoadTrue)
	}
	if len(persistedTrue.CodexKey) != 1 || !persistedTrue.CodexKey[0].DisableImageGeneration {
		t.Fatalf("persisted PUT config = %#v, want true", persistedTrue.CodexKey)
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-api-key", nil)
	h.GetCodexKeys(getCtx)
	var getBody struct {
		Items []config.CodexKey `json:"codex-api-key"`
	}
	if errDecode := json.Unmarshal(getRec.Body.Bytes(), &getBody); errDecode != nil {
		t.Fatalf("decode GET response: %v", errDecode)
	}
	if len(getBody.Items) != 1 || !getBody.Items[0].DisableImageGeneration {
		t.Fatalf("GET response = %s, want true", getRec.Body.String())
	}

	patchRec := performPatch(t, h.PatchCodexKey, `{"index":0,"value":{"disable-image-generation":false}}`)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if len(h.cfg.CodexKey) != 1 || h.cfg.CodexKey[0].DisableImageGeneration {
		t.Fatalf("PATCH config = %#v, want disable-image-generation false", h.cfg.CodexKey)
	}
	persistedFalse, errLoadFalse := config.LoadConfig(configPath)
	if errLoadFalse != nil {
		t.Fatalf("load persisted false config: %v", errLoadFalse)
	}
	if len(persistedFalse.CodexKey) != 1 || persistedFalse.CodexKey[0].DisableImageGeneration {
		t.Fatalf("persisted PATCH config = %#v, want false", persistedFalse.CodexKey)
	}
}
