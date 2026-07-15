package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConductorResolvesGroupedNativeAPIKeyConfig(t *testing.T) {
	baseURL := "https://native.example/v1"
	cfg := &internalconfig.Config{
		GeminiKey:       []internalconfig.GeminiKey{{BaseURL: baseURL, APIKeyEntries: []internalconfig.NativeAPIKeyEntry{{APIKey: "gemini-key"}}}},
		InteractionsKey: []internalconfig.GeminiKey{{BaseURL: baseURL, APIKeyEntries: []internalconfig.NativeAPIKeyEntry{{APIKey: "interactions-key"}}}},
		ClaudeKey:       []internalconfig.ClaudeKey{{BaseURL: baseURL, APIKeyEntries: []internalconfig.NativeAPIKeyEntry{{APIKey: "claude-key"}}}},
		CodexKey:        []internalconfig.CodexKey{{BaseURL: baseURL, APIKeyEntries: []internalconfig.NativeAPIKeyEntry{{APIKey: "codex-key"}}}},
	}

	tests := []struct {
		name    string
		key     string
		resolve func(*Auth) bool
	}{
		{name: "gemini", key: "gemini-key", resolve: func(auth *Auth) bool { return resolveGeminiAPIKeyConfig(cfg, auth) != nil }},
		{name: "interactions", key: "interactions-key", resolve: func(auth *Auth) bool { return resolveInteractionsAPIKeyConfig(cfg, auth) != nil }},
		{name: "claude", key: "claude-key", resolve: func(auth *Auth) bool { return resolveClaudeAPIKeyConfig(cfg, auth) != nil }},
		{name: "codex", key: "codex-key", resolve: func(auth *Auth) bool { return resolveCodexAPIKeyConfig(cfg, auth) != nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &Auth{Attributes: map[string]string{"api_key": tt.key, "base_url": baseURL}}
			if !tt.resolve(auth) {
				t.Fatalf("grouped %s config was not resolved", tt.name)
			}
		})
	}
}
