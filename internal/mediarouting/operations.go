// Package mediarouting shares media operation and model identity resolution.
package mediarouting

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

const routePrefix = "@cpa-media/"

// SelectionKey separates operation dispatch from the client model catalog.
// The scheduler resolves it per provider before reading or writing cooldowns.
func SelectionKey(operation, model string) string {
	return routePrefix + url.PathEscape(NormalizeOperation(operation)) + "?" + url.Values{"model": {strings.TrimSpace(model)}}.Encode()
}

func ParseSelectionKey(key string) (operation, model string, ok bool) {
	if !strings.HasPrefix(key, routePrefix) {
		return "", "", false
	}
	path, query, _ := strings.Cut(strings.TrimPrefix(key, routePrefix), "?")
	operation, err := url.PathUnescape(path)
	if err != nil || operation == "" {
		return "", "", false
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", "", false
	}
	return operation, values.Get("model"), true
}

func NormalizeOperation(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "_", "-")
}

func FindOperation(provider *config.MediaProvider, requested string) (config.MediaOperation, bool) {
	if provider == nil {
		return config.MediaOperation{}, false
	}
	requested = NormalizeOperation(requested)
	for _, op := range provider.Operations {
		if NormalizeOperation(op.Name) == requested {
			return op, true
		}
	}
	for _, op := range provider.Operations {
		if NormalizeOperation(op.Capability) == requested {
			return op, true
		}
	}
	return config.MediaOperation{}, false
}

func UnprefixModel(provider *config.MediaProvider, requested string) string {
	requested = strings.TrimSpace(requested)
	if provider != nil && strings.TrimSpace(provider.Prefix) != "" {
		prefix := strings.Trim(strings.TrimSpace(provider.Prefix), "/") + "/"
		requested = strings.TrimPrefix(requested, prefix)
	}
	return thinking.ParseSuffix(requested).ModelName
}

func ResolveModel(provider *config.MediaProvider, requested string) string {
	requested = UnprefixModel(provider, requested)
	if provider != nil {
		for _, model := range provider.Models {
			if strings.EqualFold(model.Name, requested) || strings.EqualFold(model.Alias, requested) {
				return strings.TrimSpace(model.Name)
			}
		}
	}
	return requested
}

func DeclaresModel(provider *config.MediaProvider, requested string) bool {
	if provider == nil {
		return false
	}
	requested = UnprefixModel(provider, requested)
	for _, model := range provider.Models {
		if strings.EqualFold(strings.TrimSpace(model.Name), requested) || (strings.TrimSpace(model.Alias) != "" && strings.EqualFold(strings.TrimSpace(model.Alias), requested)) {
			return true
		}
	}
	return false
}

func AcceptsModel(provider *config.MediaProvider, operation config.MediaOperation, requested string) bool {
	if provider == nil {
		return false
	}
	requested = UnprefixModel(provider, requested)
	if operation.ModelMode == config.MediaModelNone {
		return requested == "" || len(provider.Models) == 0 || DeclaresModel(provider, requested)
	}
	if requested == "" {
		return operation.ModelMode != config.MediaModelRequired || strings.TrimSpace(operation.Model) != ""
	}
	if strings.TrimSpace(operation.Model) != "" || len(provider.Models) == 0 {
		return true
	}
	capability := config.MediaOperationCapability(operation)
	for _, model := range provider.Models {
		if !strings.EqualFold(strings.TrimSpace(model.Name), requested) && !strings.EqualFold(strings.TrimSpace(model.Alias), requested) {
			continue
		}
		return capability == "" || len(model.Capabilities) == 0 || config.HasMediaCapability(model, capability)
	}
	return false
}

// UpstreamModel preserves the explicit operation-level model override.
func UpstreamModel(provider *config.MediaProvider, operation config.MediaOperation, requested string) string {
	if operation.ModelMode == config.MediaModelNone {
		return ""
	}
	if fixed := strings.TrimSpace(operation.Model); fixed != "" {
		return fixed
	}
	return ResolveModel(provider, requested)
}
