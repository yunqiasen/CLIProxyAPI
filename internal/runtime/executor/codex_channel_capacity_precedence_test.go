package executor

import (
	"net/http"
	"testing"
)

func TestCodexChannelCodePrecedesGenericCapacityMessage(t *testing.T) {
	channel := []byte(`{"error":{"code":"get_channel_failed","message":"model is at capacity"}}`)
	if got := newCodexStatusErrWithCooling(http.StatusBadGateway, channel, false); got.StatusCode() != http.StatusBadGateway || got.credentialScoped {
		t.Fatalf("channel allocation became credential quota: %#v", got)
	}
	capacity := []byte(`{"error":{"message":"selected model is at capacity"}}`)
	if got := newCodexStatusErrWithCooling(http.StatusBadGateway, capacity, false).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("ordinary capacity classification changed: %d", got)
	}
}
