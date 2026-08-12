package management

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

func (h *Handler) decodeNativeAuthIndexes(entries []config.NativeAPIKeyEntry, provided ...map[string]string) {
	if h == nil {
		return
	}
	liveIDs := map[string]string(nil)
	if len(provided) > 0 && provided[0] != nil {
		liveIDs = provided[0]
	} else {
		liveIDs = h.liveAuthIDByIndex()
	}
	for i := range entries {
		if stableID := strings.TrimSpace(liveIDs[strings.TrimSpace(entries[i].AuthID)]); stableID != "" {
			entries[i].AuthID = stableID
		}
	}
}

func prepareNativeProviderAuthIDs[T any](
	kind string,
	previous, incoming []T,
	entries func(*T) *[]config.NativeAPIKeyEntry,
	identity func(T) string,
	trusted ...map[string]string,
) []T {
	prepared := append([]T(nil), incoming...)
	usedPrevious := make(map[int]struct{}, len(previous))
	previousByAuthID := make(map[string]int)
	ambiguousAuthIDs := make(map[string]struct{})
	for i := range previous {
		for _, entry := range *entries(&previous[i]) {
			id := strings.TrimSpace(entry.AuthID)
			if id == "" {
				continue
			}
			if _, ambiguous := ambiguousAuthIDs[id]; ambiguous {
				continue
			}
			if owner, exists := previousByAuthID[id]; exists && owner != i {
				delete(previousByAuthID, id)
				ambiguousAuthIDs[id] = struct{}{}
				continue
			}
			previousByAuthID[id] = i
		}
	}
	for i := range prepared {
		match := -1
		for _, entry := range *entries(&prepared[i]) {
			if owner, exists := previousByAuthID[strings.TrimSpace(entry.AuthID)]; exists {
				if _, used := usedPrevious[owner]; !used {
					match = owner
					break
				}
			}
		}
		if match < 0 {
			match = matchLegacyNativeProvider(previous, prepared[i], entries, usedPrevious)
		}
		if match < 0 {
			incomingIdentity := identity(prepared[i])
			if incomingIdentity != "" {
				for j := range previous {
					if _, used := usedPrevious[j]; used {
						continue
					}
					if identity(previous[j]) == incomingIdentity {
						match = j
						break
					}
				}
			}
		}
		var previousEntries []config.NativeAPIKeyEntry
		if match >= 0 {
			previousEntries = materializeLegacyNativeAuthIDs(kind, previous[match], *entries(&previous[match]))
			usedPrevious[match] = struct{}{}
		}
		*entries(&prepared[i]) = prepareNativeAuthIDs(kind, previousEntries, *entries(&prepared[i]), trusted...)
	}
	return prepared
}

func matchLegacyNativeProvider[T any](
	previous []T,
	incoming T,
	entries func(*T) *[]config.NativeAPIKeyEntry,
	used map[int]struct{},
) int {
	incomingKeys := nativeProviderAPIKeys(*entries(&incoming))
	if len(incomingKeys) == 0 {
		return -1
	}
	match := -1
	for i := range previous {
		if _, alreadyUsed := used[i]; alreadyUsed {
			continue
		}
		previousKeys := nativeProviderAPIKeys(*entries(&previous[i]))
		if len(previousKeys) != len(incomingKeys) {
			continue
		}
		equal := true
		for key := range incomingKeys {
			if _, exists := previousKeys[key]; !exists {
				equal = false
				break
			}
		}
		if !equal {
			continue
		}
		if match >= 0 {
			return -1
		}
		match = i
	}
	return match
}

func nativeProviderAPIKeys(entries []config.NativeAPIKeyEntry) map[string]struct{} {
	keys := make(map[string]struct{}, len(entries))
	for i := range entries {
		if key := strings.TrimSpace(entries[i].APIKey); key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys
}

func materializeLegacyNativeAuthIDs[T any](kind string, provider T, entries []config.NativeAPIKeyEntry) []config.NativeAPIKeyEntry {
	out := append([]config.NativeAPIKeyEntry(nil), entries...)
	baseURL := nativeProviderBaseURL(provider)
	idGen := synthesizer.NewStableIDGenerator()
	for i := range out {
		if strings.TrimSpace(out[i].AuthID) != "" || strings.TrimSpace(out[i].APIKey) == "" {
			continue
		}
		id, _ := idGen.Next(kind, out[i].APIKey, baseURL, strconv.Itoa(i))
		out[i].AuthID = id
	}
	return out
}

func nativeProviderBaseURL(provider any) string {
	switch entry := provider.(type) {
	case config.GeminiKey:
		return strings.TrimSpace(entry.BaseURL)
	case config.ClaudeKey:
		return strings.TrimSpace(entry.BaseURL)
	case config.CodexKey:
		return strings.TrimSpace(entry.BaseURL)
	default:
		return ""
	}
}

func prepareNativeAuthIDs(kind string, previous, incoming []config.NativeAPIKeyEntry, trusted ...map[string]string) []config.NativeAPIKeyEntry {
	prepared := append([]config.NativeAPIKeyEntry(nil), incoming...)
	used := make(map[string]struct{}, len(prepared))
	trustedIDs := make(map[string]struct{})
	if len(trusted) > 0 {
		for _, id := range trusted[0] {
			if id = strings.TrimSpace(id); id != "" {
				trustedIDs[id] = struct{}{}
			}
		}
	}
	previousByID := make(map[string]config.NativeAPIKeyEntry, len(previous))
	previousByKey := make(map[string]config.NativeAPIKeyEntry, len(previous))
	for i := range previous {
		entry := previous[i]
		if id := strings.TrimSpace(entry.AuthID); id != "" {
			previousByID[id] = entry
		}
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			previousByKey[key] = entry
		}
	}
	for i := range prepared {
		id := strings.TrimSpace(prepared[i].AuthID)
		_, wasPrevious := previousByID[id]
		_, wasTrusted := trustedIDs[id]
		if id != "" && (wasPrevious || wasTrusted) {
			if _, claimed := used[id]; !claimed {
				prepared[i].AuthID = id
				used[id] = struct{}{}
				continue
			}
		}
		prepared[i].AuthID = ""
		if previousEntry, exists := previousByKey[strings.TrimSpace(prepared[i].APIKey)]; exists {
			if previousID := strings.TrimSpace(previousEntry.AuthID); previousID != "" {
				if _, claimed := used[previousID]; !claimed {
					prepared[i].AuthID = previousID
					used[previousID] = struct{}{}
				}
			}
		}
	}
	for i := range prepared {
		if strings.TrimSpace(prepared[i].AuthID) == "" {
			prepared[i].AuthID = kind + ":" + uuid.NewString()
		}
	}
	return prepared
}

func nativeProviderIdentity(name, baseURL string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	baseURL = strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if name == "" && baseURL == "" {
		return ""
	}
	return name + "\x00" + baseURL
}
