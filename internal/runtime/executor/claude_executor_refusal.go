package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// claudeCredentialFallbackError preserves the upstream request-policy response
// while allowing the auth manager to try another credential for the same request.
type claudeCredentialFallbackError struct {
	body []byte
}

func (e *claudeCredentialFallbackError) Error() string {
	if e == nil {
		return ""
	}
	return string(e.body)
}

func (e *claudeCredentialFallbackError) StatusCode() int {
	return http.StatusBadGateway
}

func (*claudeCredentialFallbackError) IsCredentialFallback() bool {
	return true
}

var _ cliproxyexecutor.CredentialFallbackError = (*claudeCredentialFallbackError)(nil)

func newClaudeCredentialFallbackError(model string, stopDetails gjson.Result) error {
	category := strings.TrimSpace(stopDetails.Get("category").String())
	code := "policy_refusal"
	if category == "cyber" {
		code = "cyber_policy"
	}
	message := strings.TrimSpace(stopDetails.Get("explanation").String())
	if message == "" {
		message = "Claude upstream refused this request"
	}
	payload := map[string]any{
		"error": map[string]any{
			"type":     "invalid_request",
			"code":     code,
			"message":  message,
			"model":    model,
			"category": category,
			"param":    nil,
		},
	}
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return &claudeCredentialFallbackError{body: []byte(fmt.Sprintf(`{"error":{"type":"invalid_request","code":%q,"message":%q}}`, code, message))}
	}
	return &claudeCredentialFallbackError{body: body}
}

func claudeStreamRefusalError(model string, line []byte) (error, bool) {
	trimmedLine := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmedLine, "data:") {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "data:"))
	if payload == "" || payload == "[DONE]" || !gjson.Valid(payload) {
		return nil, false
	}
	root := gjson.Parse(payload)
	if root.Get("type").String() != "message_delta" || root.Get("delta.stop_reason").String() != "refusal" {
		return nil, false
	}
	return newClaudeCredentialFallbackError(model, root.Get("delta.stop_details")), true
}

func claudeSSERefusalError(model string, data []byte) (error, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if errRefusal, ok := claudeStreamRefusalError(model, []byte(line)); ok {
			return errRefusal, true
		}
	}
	return nil, false
}

func shouldBufferClaudeTranslatedStream(responseFormat sdktranslator.Format, baseURL string) bool {
	return responseFormat == sdktranslator.FormatOpenAIResponse && !isAnthropicUpstreamBase(baseURL)
}
