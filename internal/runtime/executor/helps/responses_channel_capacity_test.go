package helps

import (
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type channelCapacityTestError struct{}

func (*channelCapacityTestError) Error() string              { return "original upstream error" }
func (*channelCapacityTestError) StatusCode() int            { return 500 }
func (*channelCapacityTestError) RetryAfter() *time.Duration { d := 2 * time.Second; return &d }

func TestResponsesChannelCapacityErrorScope(t *testing.T) {
	const body = `{"error":{"code":"get_channel_failed","message":"model at capacity"}}`
	cases := []struct {
		name, url, body string
		status          int
		fallback        bool
	}{
		{"any_http_500", "https://anyrouter.top/v1", body, 500, true},
		{"any_http_503", "https://ANYROUTER.TOP/v1", body, 503, true},
		{"other_provider", "https://agentrouter.org/v1", body, 500, false},
		{"host_suffix", "https://anyrouter.top.example.org/v1", body, 500, false},
		{"userinfo", "https://anyrouter.top@example.org/v1", body, 500, false},
		{"invalid_url", ":bad", body, 500, false},
		{"invalid_json", "https://anyrouter.top/v1", body + " invalid", 500, false},
		{"empty_500", "https://anyrouter.top/v1", "", 500, false},
		{"generic_500", "https://anyrouter.top/v1", `{"error":{"code":"server_error"}}`, 500, false},
		{"auth_401", "https://anyrouter.top/v1", body, 401, false},
		{"payment_402", "https://anyrouter.top/v1", body, 402, false},
		{"permission_403", "https://anyrouter.top/v1", body, 403, false},
		{"quota_429", "https://anyrouter.top/v1", body, 429, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := &channelCapacityTestError{}
			got := ResponsesChannelCapacityError(tc.url, "gpt-6-astra", tc.status, []byte(tc.body), cause)
			var fallback cliproxyexecutor.CredentialFallbackError
			marked := errors.As(got, &fallback) && fallback.IsCredentialFallback()
			if marked != tc.fallback {
				t.Fatalf("credential fallback=%v, want %v", marked, tc.fallback)
			}
			if !errors.Is(got, cause) || got.Error() != cause.Error() {
				t.Fatalf("original error lost: %v", got)
			}
			var status cliproxyexecutor.StatusError
			if !errors.As(got, &status) || status.StatusCode() != 500 {
				t.Fatalf("status lost: %v", got)
			}
			var retry interface{ RetryAfter() *time.Duration }
			if !errors.As(got, &retry) || *retry.RetryAfter() != 2*time.Second {
				t.Fatalf("retry hint lost: %v", got)
			}
		})
	}
	if got := ResponsesChannelCapacityError("https://anyrouter.top/v1", "gpt-6-astra", http.StatusInternalServerError, []byte(body), nil); got != nil {
		t.Fatalf("nil error became %v", got)
	}
}

func TestResponsesChannelCapacityErrorOtherModel(t *testing.T) {
	cause := &channelCapacityTestError{}
	got := ResponsesChannelCapacityError("https://anyrouter.top/v1", "another-model", 500, []byte(`{"error":{"code":"get_channel_failed"}}`), cause)
	if got != cause {
		t.Fatalf("other model changed: %v", got)
	}
}
