package auth

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/mediarouting"
)

// mediaOperationRoute resolves a catalog-independent operation against the
// selected provider, including fixed-model and model-free operations.
func (m *Manager) mediaOperationRoute(auth *Auth, route string) (model, state string, supported bool) {
	operationName, requested, ok := mediarouting.ParseSelectionKey(route)
	if !ok || auth == nil {
		return "", "", false
	}
	provider := resolveMediaProviderConfig(m.loadAPIKeyModelRouting().config, auth)
	if provider == nil || provider.Disabled {
		return "", "", false
	}
	operation, ok := mediarouting.FindOperation(provider, operationName)
	if !ok || !mediarouting.AcceptsModel(provider, operation, requested) {
		return "", "", false
	}
	model = mediarouting.UpstreamModel(provider, operation, requested)
	state = model
	if state == "" {
		state = mediarouting.SelectionKey(operation.Name, "")
	}
	return model, state, true
}
