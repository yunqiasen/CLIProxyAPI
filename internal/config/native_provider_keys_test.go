package config

import "testing"

func intPointer(value int) *int { return &value }

func TestParseConfigBytesNativeProviderGroups(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
request-log-retention-days: 0
claude-api-key:
  - name: relay-a
    api-key: legacy-key
    priority: 8
    proxy-url: http://provider-proxy
    api-key-entries:
      - auth-id: claude:apikey:persisted-a
        api-key: key-a
        priority: 0
      - api-key: key-b
        priority: 20
        proxy-url: http://key-proxy
    rebuild-mid-system-message: true
    experimental-cch-signing: true
codex-api-key:
  - api-key: legacy-codex
    base-url: https://codex.example.com
    websockets: true
gemini-api-key:
  - name: gemini-relay
    api-key-entries:
      - api-key: gemini-a
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.RequestLogRetentionDays != 0 {
		t.Fatalf("RequestLogRetentionDays = %d, want 0", cfg.RequestLogRetentionDays)
	}
	claude := cfg.ClaudeKey[0]
	if claude.Name != "relay-a" || len(claude.APIKeyEntries) != 2 {
		t.Fatalf("Claude group = %#v", claude)
	}
	if claude.APIKeyEntries[0].Priority == nil || *claude.APIKeyEntries[0].Priority != 0 {
		t.Fatalf("explicit zero priority lost: %#v", claude.APIKeyEntries[0].Priority)
	}
	if claude.APIKeyEntries[0].AuthID != "claude:apikey:persisted-a" {
		t.Fatalf("stable auth ID lost: %#v", claude.APIKeyEntries[0])
	}
	if !claude.RebuildMidSystemMessage || !claude.ExperimentalCCHSigning {
		t.Fatalf("Claude-specific settings lost: %#v", claude)
	}
	if len(cfg.CodexKey) != 1 || !cfg.CodexKey[0].Websockets {
		t.Fatalf("legacy Codex settings lost: %#v", cfg.CodexKey)
	}
	if len(cfg.GeminiKey) != 1 || cfg.GeminiKey[0].Name != "gemini-relay" || len(cfg.GeminiKey[0].APIKeyEntries) != 1 {
		t.Fatalf("Gemini group = %#v", cfg.GeminiKey)
	}
}

func TestEffectiveNativeAPIKeysPrefersGroupedEntriesAndInheritsDefaults(t *testing.T) {
	entries := []NativeAPIKeyEntry{
		{APIKey: " key-a ", Priority: intPointer(0)},
		{APIKey: "key-b", Priority: intPointer(20), ProxyURL: " http://key-proxy "},
		{APIKey: "key-a", Priority: intPointer(99)},
		{APIKey: "  "},
	}
	got := EffectiveNativeAPIKeys("legacy", 8, "http://provider-proxy", entries)
	if len(got) != 2 {
		t.Fatalf("len(EffectiveNativeAPIKeys()) = %d, want 2: %#v", len(got), got)
	}
	if got[0].APIKey != "key-a" || got[0].Priority != 0 || got[0].ProxyURL != "http://provider-proxy" || got[0].Index != 0 {
		t.Fatalf("first effective key = %#v", got[0])
	}
	if got[1].APIKey != "key-b" || got[1].Priority != 20 || got[1].ProxyURL != "http://key-proxy" || got[1].Index != 1 {
		t.Fatalf("second effective key = %#v", got[1])
	}
}

func TestEffectiveNativeAPIKeysPreservesStableAuthID(t *testing.T) {
	got := EffectiveNativeAPIKeys("", 0, "", []NativeAPIKeyEntry{{AuthID: "claude:apikey:persisted", APIKey: "changed-key"}})
	if len(got) != 1 || got[0].AuthID != "claude:apikey:persisted" {
		t.Fatalf("effective key identity = %#v", got)
	}
}

func TestEffectiveNativeAPIKeysFallsBackToLegacyKey(t *testing.T) {
	got := EffectiveNativeAPIKeys(" legacy ", 7, " proxy ", nil)
	if len(got) != 1 || got[0].APIKey != "legacy" || got[0].Priority != 7 || got[0].ProxyURL != "proxy" || got[0].Index != -1 {
		t.Fatalf("legacy effective key = %#v", got)
	}
}

func TestRequestLogRetentionDefaultsAndNegativeNormalization(t *testing.T) {
	cfgDefault, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfgDefault.RequestLogRetentionDays != 7 {
		t.Fatalf("default retention = %d, want 7", cfgDefault.RequestLogRetentionDays)
	}
	cfgNegative, err := ParseConfigBytes([]byte("request-log-retention-days: -3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfgNegative.RequestLogRetentionDays != 7 {
		t.Fatalf("negative retention = %d, want 7", cfgNegative.RequestLogRetentionDays)
	}
}

func TestCloneForRuntimeDeepCopiesNativeAPIKeyEntries(t *testing.T) {
	cfg := &Config{ClaudeKey: []ClaudeKey{{APIKeyEntries: []NativeAPIKeyEntry{{APIKey: "a", Priority: intPointer(1)}}}}}
	cloned := cfg.CloneForRuntime()
	*cloned.ClaudeKey[0].APIKeyEntries[0].Priority = 9
	cloned.ClaudeKey[0].APIKeyEntries[0].APIKey = "changed"
	if cfg.ClaudeKey[0].APIKeyEntries[0].APIKey != "a" || *cfg.ClaudeKey[0].APIKeyEntries[0].Priority != 1 {
		t.Fatalf("CloneForRuntime shared grouped key storage: %#v", cfg.ClaudeKey[0].APIKeyEntries)
	}
}

func TestSanitizeGeminiKeysDeduplicatesByEffectiveKeyIdentity(t *testing.T) {
	cfg := &Config{GeminiKey: []GeminiKey{
		{Name: "shared-name", BaseURL: "https://gemini.example/v1", APIKeyEntries: []NativeAPIKeyEntry{{APIKey: "key-a"}}},
		{Name: "shared-name", BaseURL: "https://gemini.example/v1", APIKeyEntries: []NativeAPIKeyEntry{{APIKey: "key-b"}}},
		{Name: "other-name", BaseURL: "https://gemini.example/v1", APIKeyEntries: []NativeAPIKeyEntry{{APIKey: "key-a"}}},
	}}

	cfg.SanitizeGeminiKeys()

	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("len(GeminiKey) = %d, want 2: %#v", len(cfg.GeminiKey), cfg.GeminiKey)
	}
	seen := make(map[string]int)
	for _, group := range cfg.GeminiKey {
		keys := EffectiveNativeAPIKeys(group.APIKey, group.Priority, group.ProxyURL, group.APIKeyEntries)
		if len(keys) != 1 {
			t.Fatalf("effective keys = %#v, want one key", keys)
		}
		seen[keys[0].APIKey]++
	}
	if seen["key-a"] != 1 || seen["key-b"] != 1 {
		t.Fatalf("sanitized key identities = %#v, want key-a and key-b once", seen)
	}
}
