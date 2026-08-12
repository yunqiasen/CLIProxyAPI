package api

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestClientUpdateSummaryIncludesMediaAuths(t *testing.T) {
	cfg := &config.Config{
		GeminiKey:          []config.GeminiKey{{APIKey: "gemini"}},
		InteractionsKey:    []config.GeminiKey{{APIKey: "interactions"}},
		ClaudeKey:          []config.ClaudeKey{{APIKey: "claude"}},
		CodexKey:           []config.CodexKey{{APIKey: "codex"}},
		XAIKey:             []config.XAIKey{{APIKey: "xai"}},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vertex"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "first"},
				{APIKey: "second"},
			},
		}},
		MediaProviders: []config.MediaProvider{
			{Kind: config.MediaKindImage, Name: "images", APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "first"}, {APIKey: "second"}}},
			{Kind: config.MediaKindVideo, Name: "video"},
			{Kind: config.MediaKindAudio, Name: "disabled audio", Disabled: true, APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "ignored"}}},
		},
	}

	summary := clientUpdateSummary(cfg, 3)
	for _, expected := range []string{
		"14 clients",
		"2 image media auths",
		"1 video media auths",
		"0 audio media auths",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("clientUpdateSummary() = %q; missing %q", summary, expected)
		}
	}
}
