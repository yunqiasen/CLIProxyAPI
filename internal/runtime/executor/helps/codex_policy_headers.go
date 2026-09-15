package helps

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CodexPolicyHeaders resolves the header sources used by payload policy before
// transport headers are built. It mirrors client < credential < model precedence
// without modifying the caller's headers or generating transport identity fields.
func CodexPolicyHeaders(ctx context.Context, client http.Header, auth *cliproxyauth.Auth, model string) http.Header {
	client = CodexClientHeaders(ctx, client)
	headers := client.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(&http.Request{Header: headers}, auth.Attributes)
	}
	ApplyCodexModelHeaderOverrides(headers, model)
	return headers
}

// CodexClientHeaders uses the same optional Gin fallback for policy and transport.
func CodexClientHeaders(ctx context.Context, client http.Header) http.Header {
	if client == nil && ctx != nil {
		if c, ok := ctx.Value("gin").(*gin.Context); ok && c != nil && c.Request != nil {
			client = c.Request.Header
		}
	}
	return client
}

// ApplyCodexModelHeaderOverrides applies model headers after credential headers.
func ApplyCodexModelHeaderOverrides(headers http.Header, model string) bool {
	if headers == nil {
		return false
	}
	overrides := registry.ModelOverrideHeaders(model)
	for key, value := range overrides {
		headers.Set(key, value)
	}
	return len(overrides) > 0
}
