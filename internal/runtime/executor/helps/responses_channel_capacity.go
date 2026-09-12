package helps

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// ResponsesChannelCapacityError keeps AnyRouter channel allocation failures local
// to the attempt. A different session can still succeed with the same credential.
// Call only before downstream output is committed; normal credential rotation
// and request retry limits remain owned by the auth manager.
func ResponsesChannelCapacityError(baseURL, model string, status int, body []byte, cause error) error {
	if cause == nil || model != "gpt-6-astra" || (status != http.StatusInternalServerError && status != http.StatusBadGateway && status != http.StatusServiceUnavailable) {
		return cause
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(endpoint.Hostname(), "anyrouter.top") {
		return cause
	}
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "error.code").String() != "get_channel_failed" {
		return cause
	}
	return &responsesChannelCapacityError{cause: cause}
}

type responsesChannelCapacityError struct{ cause error }

func (e *responsesChannelCapacityError) Error() string            { return e.cause.Error() }
func (e *responsesChannelCapacityError) Unwrap() error            { return e.cause }
func (*responsesChannelCapacityError) IsCredentialFallback() bool { return true }
