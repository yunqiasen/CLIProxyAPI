package management

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// CatalogConfigSnapshot reads the saved inventory configuration without mutation.
func (h *Handler) CatalogConfigSnapshot() *config.Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.CloneForRuntime()
}
