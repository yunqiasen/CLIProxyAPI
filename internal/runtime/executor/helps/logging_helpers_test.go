package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRecordAPIRequestClonesDeferredBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body := []byte(`{"model":"original"}`)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   body,
	})
	body[10] = 'X'

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	captured := string(requests[0]())
	if !strings.Contains(captured, `{"model":"original"}`) {
		t.Fatalf("captured API request = %q, want original body", captured)
	}
}

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
