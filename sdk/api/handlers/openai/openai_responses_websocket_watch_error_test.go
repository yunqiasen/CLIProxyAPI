package openai

import (
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"net/http"
	"testing"
)

func TestResponsesWebsocketExposesOnlyExplicitFirstOutputWatch(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		want   bool
	}{
		{`{"error":{"code":"upstream_response_timeout"}}`, http.StatusGatewayTimeout, true},
		{`{"error":{"code":"other_timeout"}}`, http.StatusGatewayTimeout, false},
		{`{"error":{"code":"upstream_response_timeout"}}`, http.StatusBadGateway, false},
	} {
		got := shouldExposeResponsesUpstreamError(&interfaces.ErrorMessage{StatusCode: tc.status, Error: errors.New(tc.body)})
		if got != tc.want {
			t.Fatalf("status=%d body=%s exposed=%t want=%t", tc.status, tc.body, got, tc.want)
		}
	}
}
