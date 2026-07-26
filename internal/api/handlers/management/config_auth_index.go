package management

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

type nativeAPIKeyEntryWithAuthIndex struct {
	config.NativeAPIKeyEntry
	AuthIndex string `json:"auth-index,omitempty"`
}

type geminiKeyWithAuthIndex struct {
	config.GeminiKey
	APIKeyEntries []nativeAPIKeyEntryWithAuthIndex `json:"api-key-entries,omitempty"`
	AuthIndex     string                           `json:"auth-index,omitempty"`
}

type claudeKeyWithAuthIndex struct {
	config.ClaudeKey
	APIKeyEntries []nativeAPIKeyEntryWithAuthIndex `json:"api-key-entries,omitempty"`
	AuthIndex     string                           `json:"auth-index,omitempty"`
}

type codexKeyWithAuthIndex struct {
	config.CodexKey
	APIKeyEntries []nativeAPIKeyEntryWithAuthIndex `json:"api-key-entries,omitempty"`
	AuthIndex     string                           `json:"auth-index,omitempty"`
}

type xaiKeyWithAuthIndex struct {
	config.XAIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type vertexCompatKeyWithAuthIndex struct {
	config.VertexCompatKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name           string                                   `json:"name"`
	Priority       int                                      `json:"priority,omitempty"`
	Disabled       bool                                     `json:"disabled"`
	Prefix         string                                   `json:"prefix,omitempty"`
	BaseURL        string                                   `json:"base-url"`
	APIKeyEntries  []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models         []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers        map[string]string                        `json:"headers,omitempty"`
	DisableCooling bool                                     `json:"disable-cooling,omitempty"`
	AuthIndex      string                                   `json:"auth-index,omitempty"`
}

func (h *Handler) liveAuthIndexByID() map[string]string {
	out := map[string]string{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return out
	}
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		out[id] = idx
	}
	return out
}

func (h *Handler) liveAuthIDByIndex() map[string]string {
	out := map[string]string{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return out
	}
	for _, auth := range manager.List() {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		index := strings.TrimSpace(auth.Index)
		if index == "" {
			index = auth.EnsureIndex()
		}
		if index != "" {
			out[index] = strings.TrimSpace(auth.ID)
		}
	}
	return out
}

func (h *Handler) geminiKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		response := geminiKeyWithAuthIndex{
			GeminiKey: entry,
			AuthIndex: authIndex,
		}
		if len(entry.APIKeyEntries) > 0 {
			response.APIKeyEntries = make([]nativeAPIKeyEntryWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				keyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next("gemini:apikey", keyEntry.APIKey, entry.BaseURL, strconv.Itoa(j))
				response.APIKeyEntries[j] = nativeAPIKeyEntryWithAuthIndex{
					NativeAPIKeyEntry: keyEntry,
					AuthIndex:         liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}

func (h *Handler) interactionsKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.InteractionsKey))
	for i := range h.cfg.InteractionsKey {
		entry := h.cfg.InteractionsKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini-interactions:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) claudeKeysWithAuthIndex() []claudeKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("claude:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		response := claudeKeyWithAuthIndex{
			ClaudeKey: entry,
			AuthIndex: authIndex,
		}
		if len(entry.APIKeyEntries) > 0 {
			response.APIKeyEntries = make([]nativeAPIKeyEntryWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				keyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next("claude:apikey", keyEntry.APIKey, entry.BaseURL, strconv.Itoa(j))
				response.APIKeyEntries[j] = nativeAPIKeyEntryWithAuthIndex{
					NativeAPIKeyEntry: keyEntry,
					AuthIndex:         liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}

func (h *Handler) codexKeysWithAuthIndex() []codexKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("codex:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		response := codexKeyWithAuthIndex{
			CodexKey:  entry,
			AuthIndex: authIndex,
		}
		if len(entry.APIKeyEntries) > 0 {
			response.APIKeyEntries = make([]nativeAPIKeyEntryWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				keyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next("codex:apikey", keyEntry.APIKey, entry.BaseURL, strconv.Itoa(j))
				response.APIKeyEntries[j] = nativeAPIKeyEntryWithAuthIndex{
					NativeAPIKeyEntry: keyEntry,
					AuthIndex:         liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}

func (h *Handler) xaiKeysWithAuthIndex() []xaiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]xaiKeyWithAuthIndex, len(h.cfg.XAIKey))
	for i := range h.cfg.XAIKey {
		entry := h.cfg.XAIKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("xai:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = xaiKeyWithAuthIndex{
			XAIKey:    entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) vertexCompatKeysWithAuthIndex() []vertexCompatKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		authIndex := liveIndexByID[id]
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: entry,
			AuthIndex:       authIndex,
		}
	}
	return out
}

func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	idGen := synthesizer.NewStableIDGenerator()
	for i := range normalized {
		entry := normalized[i]
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)

		response := openAICompatibilityWithAuthIndex{
			Name:           entry.Name,
			Priority:       entry.Priority,
			Disabled:       entry.Disabled,
			Prefix:         entry.Prefix,
			BaseURL:        entry.BaseURL,
			Models:         entry.Models,
			Headers:        entry.Headers,
			DisableCooling: entry.DisableCooling,
			AuthIndex:      "",
		}
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			response.AuthIndex = liveIndexByID[id]
		} else {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: apiKeyEntry,
					AuthIndex:                 liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}
