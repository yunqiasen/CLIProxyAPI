package synthesizer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ConfigSynthesizer generates Auth entries from configuration API keys.
// It handles Gemini, Interactions, Claude, Codex, xAI, OpenAI-compat, and Vertex-compat providers.
type ConfigSynthesizer struct{}

// NewConfigSynthesizer creates a new ConfigSynthesizer instance.
func NewConfigSynthesizer() *ConfigSynthesizer {
	return &ConfigSynthesizer{}
}

func addWeightToAttrs(weight *int, attrs map[string]string) {
	if weight == nil {
		return
	}
	normalized := *weight
	if normalized <= 0 {
		normalized = 0
	}
	attrs[coreauth.AttributeWeight] = strconv.Itoa(normalized)
}

// Synthesize generates Auth entries from config API keys.
func (s *ConfigSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 32)
	if ctx == nil || ctx.Config == nil {
		return out, nil
	}
	if errValidate := ctx.Config.ValidateCredentialWeights(); errValidate != nil {
		return nil, fmt.Errorf("synthesize config API key auths: %w", errValidate)
	}

	// Gemini API Keys
	out = append(out, s.synthesizeGeminiKeys(ctx)...)
	// Native Interactions API Keys
	out = append(out, s.synthesizeInteractionsKeys(ctx)...)
	// Claude API Keys
	out = append(out, s.synthesizeClaudeKeys(ctx)...)
	// Codex API Keys
	out = append(out, s.synthesizeCodexKeys(ctx)...)
	// xAI API Keys
	out = append(out, s.synthesizeXAIKeys(ctx)...)
	// OpenAI-compat
	out = append(out, s.synthesizeOpenAICompat(ctx)...)
	// Media providers
	out = append(out, s.synthesizeMediaProviders(ctx)...)
	// Vertex-compat
	out = append(out, s.synthesizeVertexCompat(ctx)...)

	return out, nil
}

func nextNativeAuthID(idGen *StableIDGenerator, kind, apiKey, baseURL string, entryIndex int) (string, string) {
	if entryIndex < 0 {
		return idGen.Next(kind, apiKey, baseURL)
	}
	return idGen.Next(kind, apiKey, baseURL, strconv.Itoa(entryIndex))
}

// synthesizeGeminiKeys creates Auth entries for Gemini API keys.
func (s *ConfigSynthesizer) synthesizeGeminiKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeGeminiKeyEntries(ctx, ctx.Config.GeminiKey, "gemini:apikey", "gemini", "gemini-apikey", constant.Gemini)
}

// synthesizeInteractionsKeys creates Auth entries for native Interactions API keys.
func (s *ConfigSynthesizer) synthesizeInteractionsKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeGeminiKeyEntries(ctx, ctx.Config.InteractionsKey, "gemini-interactions:apikey", "interactions", "interactions-apikey", constant.GeminiInteractions)
}

func (s *ConfigSynthesizer) synthesizeGeminiKeyEntries(ctx *SynthesisContext, entries []config.GeminiKey, idKind, sourceName, label, provider string) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		effectiveKeys := config.EffectiveNativeAPIKeys(entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries)
		prefix := strings.TrimSpace(entry.Prefix)
		base := strings.TrimSpace(entry.BaseURL)
		displayLabel := strings.TrimSpace(entry.Name)
		if displayLabel == "" {
			displayLabel = label
		}
		for _, effective := range effectiveKeys {
			id, token := nextNativeAuthID(idGen, idKind, effective.APIKey, base, effective.Index)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", sourceName, token),
				"api_key":      effective.APIKey,
				"config_index": strconv.Itoa(i),
			}
			if entry.Name != "" {
				attrs["provider_name"] = strings.TrimSpace(entry.Name)
			}
			metadata := map[string]any{}
			if entry.DisableCooling {
				metadata["disable_cooling"] = true
			}
			if effective.Priority != 0 || (effective.Index >= 0 && entry.APIKeyEntries[effective.Index].Priority != nil) {
				attrs["priority"] = strconv.Itoa(effective.Priority)
			}
			addWeightToAttrs(entry.Weight, attrs)
			if base != "" {
				attrs["base_url"] = base
			}
			if hash := diff.ComputeGeminiModelsHash(entry.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(entry.Headers, attrs)
			a := &coreauth.Auth{ID: id, Provider: provider, Label: displayLabel, Prefix: prefix, Status: coreauth.StatusActive, ProxyURL: effective.ProxyURL, Attributes: attrs, Metadata: metadata, CreatedAt: now, UpdatedAt: now}
			ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeClaudeKeys creates Auth entries for Claude API keys.
func (s *ConfigSynthesizer) synthesizeClaudeKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.ClaudeKey))
	for i := range cfg.ClaudeKey {
		ck := cfg.ClaudeKey[i]
		effectiveKeys := config.EffectiveNativeAPIKeys(ck.APIKey, ck.Priority, ck.ProxyURL, ck.APIKeyEntries)
		prefix := strings.TrimSpace(ck.Prefix)
		base := strings.TrimSpace(ck.BaseURL)
		label := strings.TrimSpace(ck.Name)
		if label == "" {
			label = "claude-apikey"
		}
		for _, effective := range effectiveKeys {
			id, token := nextNativeAuthID(idGen, "claude:apikey", effective.APIKey, base, effective.Index)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:claude[%s]", token),
				"api_key":      effective.APIKey,
				"config_index": strconv.Itoa(i),
			}
			if ck.Name != "" {
				attrs["provider_name"] = strings.TrimSpace(ck.Name)
			}
			metadata := map[string]any{}
			if ck.DisableCooling {
				metadata["disable_cooling"] = true
			}
			if effective.Priority != 0 || (effective.Index >= 0 && ck.APIKeyEntries[effective.Index].Priority != nil) {
				attrs["priority"] = strconv.Itoa(effective.Priority)
			}
			addWeightToAttrs(ck.Weight, attrs)
			if base != "" {
				attrs["base_url"] = base
			}
			if ck.RebuildMidSystemMessage {
				attrs["rebuild_mid_system_message"] = "true"
			}
			if hash := diff.ComputeClaudeModelsHash(ck.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(ck.Headers, attrs)
			a := &coreauth.Auth{ID: id, Provider: "claude", Label: label, Prefix: prefix, Status: coreauth.StatusActive, ProxyURL: effective.ProxyURL, Attributes: attrs, Metadata: metadata, CreatedAt: now, UpdatedAt: now}
			ApplyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeCodexKeys creates Auth entries for Codex API keys.
func (s *ConfigSynthesizer) synthesizeCodexKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeCodexStyleKeys(ctx, ctx.Config.CodexKey, "codex")
}

// synthesizeXAIKeys creates Auth entries for xAI API keys.
func (s *ConfigSynthesizer) synthesizeXAIKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeCodexStyleKeys(ctx, ctx.Config.XAIKey, "xai")
}

func (s *ConfigSynthesizer) synthesizeCodexStyleKeys(ctx *SynthesisContext, entries []config.CodexKey, provider string) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		effectiveKeys := config.EffectiveNativeAPIKeys(entry.APIKey, entry.Priority, entry.ProxyURL, entry.APIKeyEntries)
		prefix := strings.TrimSpace(entry.Prefix)
		baseURL := strings.TrimSpace(entry.BaseURL)
		label := strings.TrimSpace(entry.Name)
		if label == "" {
			label = provider + "-apikey"
		}
		for _, effective := range effectiveKeys {
			id, token := nextNativeAuthID(idGen, provider+":apikey", effective.APIKey, baseURL, effective.Index)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", provider, token),
				"api_key":      effective.APIKey,
				"config_index": strconv.Itoa(i),
			}
			if entry.Name != "" {
				attrs["provider_name"] = strings.TrimSpace(entry.Name)
			}
			metadata := map[string]any{}
			if entry.DisableCooling {
				metadata["disable_cooling"] = true
			}
			if effective.Priority != 0 || (effective.Index >= 0 && entry.APIKeyEntries[effective.Index].Priority != nil) {
				attrs["priority"] = strconv.Itoa(effective.Priority)
			}
			addWeightToAttrs(entry.Weight, attrs)
			if baseURL != "" {
				attrs["base_url"] = baseURL
			}
			if entry.Websockets {
				attrs["websockets"] = "true"
			}
			if provider == "codex" && entry.AlphaSearch {
				attrs[coreauth.AttributeCodexAlphaSearch] = "true"
			}
			if hash := diff.ComputeCodexModelsHash(entry.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(entry.Headers, attrs)
			a := &coreauth.Auth{ID: id, Provider: provider, Label: label, Prefix: prefix, Status: coreauth.StatusActive, ProxyURL: effective.ProxyURL, Attributes: attrs, Metadata: metadata, CreatedAt: now, UpdatedAt: now}
			ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeOpenAICompat creates Auth entries for OpenAI-compatible providers.
func (s *ConfigSynthesizer) synthesizeOpenAICompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		internalProviderKey := util.OpenAICompatibleProviderKey(providerName)
		base := strings.TrimSpace(compat.BaseURL)
		disableCooling := compat.DisableCooling

		// Handle new APIKeyEntries format (preferred)
		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			addWeightToAttrs(entry.Weight, attrs)
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		// Fallback: create entry without API key if no APIKeyEntries
		if createdEntries == 0 {
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// MaterializeMediaProviderAuthIDs returns a copy with a stable auth ID for every
// credential slot. Existing IDs win; missing IDs use the legacy deterministic
// identity so the first management edit keeps earlier request-log history.
func MaterializeMediaProviderAuthIDs(providers []config.MediaProvider) []config.MediaProvider {
	out := make([]config.MediaProvider, len(providers))
	idGen := NewStableIDGenerator()
	used := make(map[string]struct{})
	reserve := func(preferred, generated string) string {
		candidate := strings.TrimSpace(preferred)
		if candidate == "" {
			candidate = generated
		}
		if _, exists := used[candidate]; exists {
			candidate = generated
			for suffix := 1; ; suffix++ {
				if _, exists := used[candidate]; !exists {
					break
				}
				candidate = fmt.Sprintf("%s-%d", generated, suffix)
			}
		}
		used[candidate] = struct{}{}
		return candidate
	}

	for i := range providers {
		provider := providers[i]
		provider.APIKeyEntries = append([]config.MediaAPIKeyEntry(nil), provider.APIKeyEntries...)
		kind := strings.ToLower(strings.TrimSpace(provider.Kind))
		name := strings.TrimSpace(provider.Name)
		base := strings.TrimSpace(provider.BaseURL)
		idKind := fmt.Sprintf("media-provider:%s:%s", kind, strings.ToLower(name))
		if len(provider.APIKeyEntries) == 0 {
			generated, _ := idGen.Next(idKind, "", base, "")
			provider.AuthID = reserve(provider.AuthID, generated)
		} else {
			for j := range provider.APIKeyEntries {
				entry := provider.APIKeyEntries[j]
				generated, _ := idGen.Next(idKind, entry.APIKey, base, entry.ProxyURL)
				entry.AuthID = reserve(entry.AuthID, generated)
				provider.APIKeyEntries[j] = entry
			}
		}
		out[i] = provider
	}
	return out
}

func mediaProviderAuthSourceToken(authID string) string {
	authID = strings.TrimSpace(authID)
	if separator := strings.LastIndexByte(authID, ':'); separator >= 0 && separator+1 < len(authID) {
		return authID[separator+1:]
	}
	return authID
}

// synthesizeMediaProviders creates one runtime auth per media credential slot.
func (s *ConfigSynthesizer) synthesizeMediaProviders(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	providers := MaterializeMediaProviderAuthIDs(cfg.MediaProviders)
	out := make([]*coreauth.Auth, 0)
	for i := range providers {
		provider := &providers[i]
		if provider.Disabled {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(provider.Kind))
		name := strings.TrimSpace(provider.Name)
		base := strings.TrimSpace(provider.BaseURL)
		providerKey := util.MediaProviderKey(kind, name)
		entries := provider.APIKeyEntries
		if len(entries) == 0 {
			entries = []config.MediaAPIKeyEntry{{AuthID: provider.AuthID}}
		}
		for j := range entries {
			entry := entries[j]
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			id := strings.TrimSpace(entry.AuthID)
			token := mediaProviderAuthSourceToken(id)
			attrs := map[string]string{
				"auth_kind":           "api_key",
				"source":              fmt.Sprintf("config:media-%s[%s]", kind, token),
				"base_url":            base,
				"media_kind":          kind,
				"media_provider_name": name,
				"provider_name":       name,
				"provider_key":        providerKey,
			}
			if key != "" {
				attrs["api_key"] = key
			}
			if header := strings.TrimSpace(provider.APIKeyHeader); header != "" {
				attrs["api_key_header"] = header
			}
			if prefix := strings.TrimSpace(provider.APIKeyPrefix); prefix != "" {
				attrs["api_key_prefix"] = prefix
			}
			priority := provider.Priority
			if entry.Priority != nil {
				priority = *entry.Priority
			}
			if priority != 0 || entry.Priority != nil || provider.Priority != 0 {
				attrs["priority"] = strconv.Itoa(priority)
			}
			addConfigHeadersToAttrs(provider.Headers, attrs)
			metadata := map[string]any{}
			if provider.DisableCooling {
				metadata["disable_cooling"] = true
			}
			auth := &coreauth.Auth{
				ID: id, Provider: providerKey, Label: name, Prefix: strings.TrimSpace(provider.Prefix),
				Status: coreauth.StatusActive, ProxyURL: proxyURL, Attributes: attrs, Metadata: metadata,
				CreatedAt: now, UpdatedAt: now,
			}
			if len(auth.Metadata) == 0 {
				auth.Metadata = nil
			}
			out = append(out, auth)
		}
	}
	return out
}

// synthesizeVertexCompat creates Auth entries for Vertex-compatible providers.
func (s *ConfigSynthesizer) synthesizeVertexCompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.VertexCompatAPIKey))
	for i := range cfg.VertexCompatAPIKey {
		compat := &cfg.VertexCompatAPIKey[i]
		providerName := "vertex"
		base := strings.TrimSpace(compat.BaseURL)

		key := strings.TrimSpace(compat.APIKey)
		prefix := strings.TrimSpace(compat.Prefix)
		proxyURL := strings.TrimSpace(compat.ProxyURL)
		idKind := "vertex:apikey"
		id, token := idGen.Next(idKind, key, base, proxyURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:vertex-apikey[%s]", token),
			"base_url":     base,
			"provider_key": providerName,
			"config_index": strconv.Itoa(i),
		}
		if compat.Priority != 0 {
			attrs["priority"] = strconv.Itoa(compat.Priority)
		}
		addWeightToAttrs(compat.Weight, attrs)
		if key != "" {
			attrs["api_key"] = key
		}
		if hash := diff.ComputeVertexCompatModelsHash(compat.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(compat.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   providerName,
			Label:      "vertex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, compat.ExcludedModels, "apikey")
		out = append(out, a)
	}
	return out
}
