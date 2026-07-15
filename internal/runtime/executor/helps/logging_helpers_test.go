package helps

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestRequestLogProviderNamePrefersAttributeAndSkipsGenericNativeLabel(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "provider attribute",
			auth: &cliproxyauth.Auth{Provider: "claude", Label: "claude-apikey", Attributes: map[string]string{"provider_name": "relay-a"}},
			want: "relay-a",
		},
		{
			name: "custom label fallback",
			auth: &cliproxyauth.Auth{Provider: "codex", Label: "relay-b"},
			want: "relay-b",
		},
		{
			name: "generic native label",
			auth: &cliproxyauth.Auth{Provider: "gemini", Label: "gemini-apikey"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequestLogProviderName(tt.auth); got != tt.want {
				t.Fatalf("RequestLogProviderName() = %q, want %q", got, tt.want)
			}
		})
	}

	line := formatAuthInfo(UpstreamRequestLog{Provider: "claude", ProviderName: "relay-a", AuthID: "auth-1"})
	if !strings.Contains(line, "provider=claude") || !strings.Contains(line, "provider_name=relay-a") {
		t.Fatalf("formatted auth metadata = %q", line)
	}
}

func TestFormatAuthInfoURLEncodesDelimitedFields(t *testing.T) {
	providerName := "relay,a=b+c%2C d/中文"
	line := formatAuthInfo(UpstreamRequestLog{Provider: "claude", ProviderName: providerName, AuthID: "auth,1", AuthType: "api_key"})
	if !strings.Contains(line, "encoding=url") {
		t.Fatalf("formatted auth metadata missing encoding marker: %q", line)
	}
	if strings.Contains(line, "provider_name="+providerName) || strings.Contains(line, "auth_id=auth,1") {
		t.Fatalf("formatted auth metadata contains unescaped delimiters: %q", line)
	}
}
