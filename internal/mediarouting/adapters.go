package mediarouting

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Adapter chooses an explicit protocol or an exact known first-party host.
// Custom domains remain passthrough unless the operator chooses a protocol.
func Adapter(provider *config.MediaProvider) string {
	if provider == nil {
		return "passthrough"
	}
	if adapter := strings.ToLower(strings.TrimSpace(provider.Adapter)); adapter != "" && adapter != "auto" {
		return adapter
	}
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil {
		return "passthrough"
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.cloudflare.com":
		if provider.Kind == config.MediaKindImage {
			return "cloudflare-workers-ai"
		}
	case "api.xiaomimimo.com":
		if provider.Kind == config.MediaKindAudio {
			return "mimo-audio"
		}
	}
	return "passthrough"
}
