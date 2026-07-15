package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestServiceResolvesGroupedNativeAPIKeyConfig(t *testing.T) {
	baseURL := "https://native.example/v1"
	service := &Service{cfg: &config.Config{
		GeminiKey:       []config.GeminiKey{{BaseURL: baseURL, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "gemini-key"}}}},
		InteractionsKey: []config.GeminiKey{{BaseURL: baseURL, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "interactions-key"}}}},
		ClaudeKey:       []config.ClaudeKey{{BaseURL: baseURL, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "claude-key"}}}},
		CodexKey:        []config.CodexKey{{BaseURL: baseURL, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "codex-key"}}}},
	}}

	tests := []struct {
		name    string
		key     string
		resolve func(*coreauth.Auth) bool
	}{
		{name: "gemini", key: "gemini-key", resolve: func(auth *coreauth.Auth) bool { return service.resolveConfigGeminiKey(auth) != nil }},
		{name: "interactions", key: "interactions-key", resolve: func(auth *coreauth.Auth) bool { return service.resolveConfigInteractionsKey(auth) != nil }},
		{name: "claude", key: "claude-key", resolve: func(auth *coreauth.Auth) bool { return service.resolveConfigClaudeKey(auth) != nil }},
		{name: "codex", key: "codex-key", resolve: func(auth *coreauth.Auth) bool { return service.resolveConfigCodexKey(auth) != nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &coreauth.Auth{Attributes: map[string]string{"api_key": tt.key, "base_url": baseURL}}
			if !tt.resolve(auth) {
				t.Fatalf("grouped %s config was not resolved", tt.name)
			}
		})
	}
}
