package config

import "strings"

// NativeAPIKeyEntry is one credential in a native provider group.
type NativeAPIKeyEntry struct {
	APIKey   string `yaml:"api-key" json:"api-key"`
	Priority *int   `yaml:"priority,omitempty" json:"priority,omitempty"`
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// EffectiveNativeAPIKey contains the resolved per-key routing values used at runtime.
type EffectiveNativeAPIKey struct {
	APIKey   string
	Priority int
	ProxyURL string
	Index    int
}

// EffectiveNativeAPIKeys resolves grouped credentials, falling back to the legacy key.
func EffectiveNativeAPIKeys(legacyKey string, defaultPriority int, defaultProxy string, entries []NativeAPIKeyEntry) []EffectiveNativeAPIKey {
	out := make([]EffectiveNativeAPIKey, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	defaultProxy = strings.TrimSpace(defaultProxy)
	for index := range entries {
		key := strings.TrimSpace(entries[index].APIKey)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		priority := defaultPriority
		if entries[index].Priority != nil {
			priority = *entries[index].Priority
		}
		proxyURL := strings.TrimSpace(entries[index].ProxyURL)
		if proxyURL == "" {
			proxyURL = defaultProxy
		}
		out = append(out, EffectiveNativeAPIKey{APIKey: key, Priority: priority, ProxyURL: proxyURL, Index: index})
	}
	if len(out) > 0 {
		return out
	}
	legacyKey = strings.TrimSpace(legacyKey)
	if legacyKey == "" {
		return nil
	}
	return []EffectiveNativeAPIKey{{APIKey: legacyKey, Priority: defaultPriority, ProxyURL: defaultProxy, Index: -1}}
}

// NativeAPIKeyConfigEntry exposes the shared identity fields of grouped native providers.
type NativeAPIKeyConfigEntry interface {
	GetBaseURL() string
	GetEffectiveAPIKeys() []EffectiveNativeAPIKey
}

// ResolveNativeAPIKeyConfig finds a native provider group and its matching effective key.
func ResolveNativeAPIKeyConfig[T NativeAPIKeyConfigEntry](entries []T, apiKey, baseURL string) (*T, *EffectiveNativeAPIKey) {
	apiKey = strings.TrimSpace(apiKey)
	baseURL = strings.TrimSpace(baseURL)
	for i := range entries {
		entry := &entries[i]
		entryBaseURL := strings.TrimSpace((*entry).GetBaseURL())
		effectiveKeys := (*entry).GetEffectiveAPIKeys()
		if apiKey != "" {
			for keyIndex := range effectiveKeys {
				if !strings.EqualFold(effectiveKeys[keyIndex].APIKey, apiKey) {
					continue
				}
				if baseURL != "" {
					if strings.EqualFold(entryBaseURL, baseURL) {
						return entry, &effectiveKeys[keyIndex]
					}
					continue
				}
				if entryBaseURL == "" {
					return entry, &effectiveKeys[keyIndex]
				}
			}
			continue
		}
		if baseURL != "" && strings.EqualFold(entryBaseURL, baseURL) && len(effectiveKeys) > 0 {
			return entry, &effectiveKeys[0]
		}
	}
	if apiKey != "" {
		for i := range entries {
			entry := &entries[i]
			effectiveKeys := (*entry).GetEffectiveAPIKeys()
			for keyIndex := range effectiveKeys {
				if strings.EqualFold(effectiveKeys[keyIndex].APIKey, apiKey) {
					return entry, &effectiveKeys[keyIndex]
				}
			}
		}
	}
	return nil, nil
}
