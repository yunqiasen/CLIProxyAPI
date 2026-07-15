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
	body := `{"index":0,"value":{"name":"codex-group","api-key-entries":[{"api-key":"codex-key","priority":8,"proxy-url":"http://key-proxy"}],"api-key":"legacy","priority":6,"prefix":"codex","base-url":"https://codex.example","websockets":true,"proxy-url":"http://group-proxy","models":[{"name":"codex-model","alias":"model"}],"headers":{"X-Test":" value "},"excluded-models":[" excluded "],"disable-cooling":true}}`
	rec := performPatch(t, h.PatchCodexKey, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.CodexKey[0]
	if entry.Name != "codex-group" || entry.Priority != 6 || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "codex-key" || entry.APIKey != "legacy" || entry.Prefix != "codex" || entry.BaseURL != "https://codex.example" || !entry.Websockets || entry.ProxyURL != "http://group-proxy" || len(entry.Models) != 1 || entry.Models[0].Name != "codex-model" || entry.Headers["X-Test"] != "value" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excluded" || !entry.DisableCooling {
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
