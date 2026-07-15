package management

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

func matchesNativeConfigAuthID(idGen *synthesizer.StableIDGenerator, authID, kind, legacyKey string, priority int, proxyURL string, entries []config.NativeAPIKeyEntry, baseURL string) bool {
	for _, effective := range config.EffectiveNativeAPIKeys(legacyKey, priority, proxyURL, entries) {
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

func toggleConfigAPIKeyExcludedAll(cfg *config.Config, auth *coreauth.Auth, disable bool) (bool, error) {
	if cfg == nil || auth == nil || !coreauth.IsConfigAPIKeyAuth(auth) {
		return false, nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return false, fmt.Errorf("auth id is empty")
	}

	idGen := synthesizer.NewStableIDGenerator()

	for i := range cfg.GeminiKey {
		entry := &cfg.GeminiKey[i]
		if matchesNativeConfigAuthID(idGen, authID, "gemini:apikey", entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries, entry.BaseURL) {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.InteractionsKey {
		entry := &cfg.InteractionsKey[i]
		if matchesNativeConfigAuthID(idGen, authID, "gemini-interactions:apikey", entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries, entry.BaseURL) {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		if matchesNativeConfigAuthID(idGen, authID, "claude:apikey", entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries, entry.BaseURL) {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.CodexKey {
		entry := &cfg.CodexKey[i]
		if matchesNativeConfigAuthID(idGen, authID, "codex:apikey", entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries, entry.BaseURL) {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.XAIKey {
		entry := &cfg.XAIKey[i]
		if matchesNativeConfigAuthID(idGen, authID, "xai:apikey", entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries, entry.BaseURL) {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.VertexCompatAPIKey {
		entry := &cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		if id == authID {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}

	return false, nil
}
