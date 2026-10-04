package management

import (
	"strconv"

	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const configAPIKeyDisablePattern = "*"

func setConfigAPIKeyExcludedAll(models []string, disable bool) []string {
	if disable {
		for _, item := range models {
			if strings.TrimSpace(item) == configAPIKeyDisablePattern {
				return config.NormalizeExcludedModels(models)
			}
		}
		return config.NormalizeExcludedModels(append(append([]string(nil), models...), configAPIKeyDisablePattern))
	}
	filtered := make([]string, 0, len(models))
	for _, item := range models {
		if strings.TrimSpace(item) == configAPIKeyDisablePattern {
			continue
		}
		filtered = append(filtered, item)
	}
	return config.NormalizeExcludedModels(filtered)
}

func toggleConfigAPIKeyExcludedAll(cfg *config.Config, auth *coreauth.Auth, disable bool) (bool, error) {
	if cfg == nil || auth == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(auth.Attributes["source"])), "config") {
		return false, nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return false, fmt.Errorf("auth id is empty")
	}

	idGen := synthesizer.NewStableIDGenerator()
	matches := func(kind string, entry NativeAPIKeyEntryProvider) bool {
		return matchesNativeConfigAuthID(idGen, authID, kind, entry)
	}
	for i := range cfg.GeminiKey {
		if matches("gemini:apikey", &cfg.GeminiKey[i]) {
			cfg.GeminiKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.GeminiKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.InteractionsKey {
		if matches("gemini-interactions:apikey", &cfg.InteractionsKey[i]) {
			cfg.InteractionsKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.InteractionsKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.ClaudeKey {
		if matches("claude:apikey", &cfg.ClaudeKey[i]) {
			cfg.ClaudeKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.ClaudeKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.CodexKey {
		if matches("codex:apikey", &cfg.CodexKey[i]) {
			cfg.CodexKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.CodexKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.XAIKey {
		if matches("xai:apikey", &cfg.XAIKey[i]) {
			cfg.XAIKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.XAIKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.MetaKey {
		if matches("meta:apikey", &cfg.MetaKey[i]) {
			cfg.MetaKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.MetaKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.VertexCompatAPIKey {
		id, _ := idGen.Next("vertex:apikey", cfg.VertexCompatAPIKey[i].APIKey, cfg.VertexCompatAPIKey[i].BaseURL, cfg.VertexCompatAPIKey[i].ProxyURL)
		if id == authID {
			cfg.VertexCompatAPIKey[i].ExcludedModels = setConfigAPIKeyExcludedAll(cfg.VertexCompatAPIKey[i].ExcludedModels, disable)
			return true, nil
		}
	}
	return false, nil
}

type ExcludedModelsEntry interface {
	NativeAPIKeyEntryProvider
}

type NativeAPIKeyEntryProvider interface {
	GetAPIKey() string
	GetBaseURL() string
	GetEffectiveAPIKeys() []config.EffectiveNativeAPIKey
}

func matchesNativeConfigAuthID(idGen *synthesizer.StableIDGenerator, authID, kind string, entry NativeAPIKeyEntryProvider) bool {
	baseURL := strings.TrimSpace(entry.GetBaseURL())
	for _, effective := range entry.GetEffectiveAPIKeys() {
		if stableID := strings.TrimSpace(effective.AuthID); stableID != "" && stableID == authID {
			return true
		}
		var id string
		if effective.Index < 0 {
			id, _ = idGen.Next(kind, effective.APIKey, baseURL)
		} else {
			id, _ = idGen.Next(kind, effective.APIKey, baseURL, strconv.Itoa(effective.Index))
		}
		if id == authID {
			return true
		}
	}
	return false
}
