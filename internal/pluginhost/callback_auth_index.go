package pluginhost

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// httpClientForCallback uses a host-bound client first. Unbound background calls
// may select one of the calling plugin's credentials, without supplying a proxy URL.
func (h *Host) httpClientForCallback(operation hostHTTPOperationHandle, request []byte) (pluginapi.HostHTTPClient, error) {
	if client, ok := operation.ctx.Value(callbackHTTPClientKey{}).(pluginapi.HostHTTPClient); ok && client != nil {
		return client, nil
	}
	var req rpcHostHTTPRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return h.newHTTPClient(nil), nil
	}
	if operation.pluginID == "" || operation.instance == nil || operation.instance.closed.Load() {
		return nil, fmt.Errorf("auth_index requires an active plugin instance")
	}
	auth, err := h.authByIndex(authIndex)
	if err != nil {
		return nil, err
	}
	if auth.Disabled {
		return nil, fmt.Errorf("selected credential is disabled")
	}
	if !h.callbackOwnsAuthProvider(operation.pluginID, operation.instance, auth.Provider) {
		return nil, fmt.Errorf("selected credential provider does not belong to the calling plugin")
	}
	return h.newHTTPClient(auth), nil
}

func (h *Host) callbackOwnsAuthProvider(pluginID string, instance *hostCallbackInstance, provider string) bool {
	provider = normalizeProviderID(provider)
	if provider == "" || h.isPluginFused(pluginID) {
		return false
	}
	h.mu.Lock()
	loaded := h.loaded[pluginID]
	matchesInstance := loaded == nil || loaded.callbackInstance == instance
	h.mu.Unlock()
	if !matchesInstance {
		return false
	}
	for _, record := range h.activeRecords() {
		if record.id != pluginID {
			continue
		}
		if authProvider := record.plugin.Capabilities.AuthProvider; authProvider != nil {
			if id, ok := h.callAuthProviderIdentifier(pluginID, authProvider); ok && id == provider {
				return true
			}
		}
		if normalizeProviderID(h.modelProvider(pluginID)) == provider {
			return true
		}
		if executor := record.plugin.Capabilities.Executor; executor != nil {
			if id, ok := h.executorProvider(record, executor); ok && normalizeProviderID(id) == provider {
				return true
			}
		}
	}
	return false
}
