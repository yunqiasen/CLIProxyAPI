package diff

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// DiffMediaProviders produces redacted, human-readable media provider changes.
func DiffMediaProviders(oldList, newList []config.MediaProvider) []string {
	oldMap := make(map[string]config.MediaProvider, len(oldList))
	newMap := make(map[string]config.MediaProvider, len(newList))
	for i, provider := range oldList {
		oldMap[mediaProviderDiffKey(provider, i)] = provider
	}
	for i, provider := range newList {
		newMap[mediaProviderDiffKey(provider, i)] = provider
	}
	keys := make([]string, 0, len(oldMap)+len(newMap))
	seen := make(map[string]struct{}, len(oldMap)+len(newMap))
	for key := range oldMap {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range newMap {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	changes := make([]string, 0, len(keys))
	for _, key := range keys {
		oldProvider, oldOK := oldMap[key]
		newProvider, newOK := newMap[key]
		label := mediaProviderLabel(newProvider)
		if label == "" {
			label = mediaProviderLabel(oldProvider)
		}
		switch {
		case !oldOK:
			changes = append(changes, fmt.Sprintf("provider added: %s (api-keys=%d, models=%d, operations=%d)", label, mediaKeyCount(newProvider), len(newProvider.Models), len(newProvider.Operations)))
		case !newOK:
			changes = append(changes, fmt.Sprintf("provider removed: %s (api-keys=%d, models=%d, operations=%d)", label, mediaKeyCount(oldProvider), len(oldProvider.Models), len(oldProvider.Operations)))
		case !reflect.DeepEqual(oldProvider, newProvider):
			details := make([]string, 0, 5)
			if oldProvider.BaseURL != newProvider.BaseURL {
				details = append(details, "base-url updated")
			}
			if oldProvider.Disabled != newProvider.Disabled {
				details = append(details, fmt.Sprintf("disabled %t -> %t", oldProvider.Disabled, newProvider.Disabled))
			}
			if mediaKeyCount(oldProvider) != mediaKeyCount(newProvider) {
				details = append(details, fmt.Sprintf("api-keys %d -> %d", mediaKeyCount(oldProvider), mediaKeyCount(newProvider)))
			}
			if len(oldProvider.Models) != len(newProvider.Models) {
				details = append(details, fmt.Sprintf("models %d -> %d", len(oldProvider.Models), len(newProvider.Models)))
			}
			if len(oldProvider.Operations) != len(newProvider.Operations) {
				details = append(details, fmt.Sprintf("operations %d -> %d", len(oldProvider.Operations), len(newProvider.Operations)))
			}
			if len(details) == 0 {
				details = append(details, "settings updated")
			}
			changes = append(changes, fmt.Sprintf("provider updated: %s (%s)", label, strings.Join(details, ", ")))
		}
	}
	return changes
}

func mediaProviderDiffKey(provider config.MediaProvider, index int) string {
	kind := strings.ToLower(strings.TrimSpace(provider.Kind))
	name := strings.ToLower(strings.TrimSpace(provider.Name))
	if name != "" {
		return kind + "/" + name
	}
	return fmt.Sprintf("%s/#%d", kind, index)
}

func mediaProviderLabel(provider config.MediaProvider) string {
	name := strings.TrimSpace(provider.Name)
	kind := strings.ToLower(strings.TrimSpace(provider.Kind))
	if name == "" && kind == "" {
		return ""
	}
	return kind + "/" + name
}

func mediaKeyCount(provider config.MediaProvider) int {
	count := 0
	for _, entry := range provider.APIKeyEntries {
		if strings.TrimSpace(entry.APIKey) != "" {
			count++
		}
	}
	if count == 0 && len(provider.APIKeyEntries) == 0 {
		return 1
	}
	return count
}
