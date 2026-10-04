package util

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// IsCodexResponsesLiteRequest recognizes the native header and its websocket metadata mirror.
func IsCodexResponsesLiteRequest(body []byte, headers http.Header) bool {
	for key, values := range headers {
		if strings.EqualFold(key, "X-OpenAI-Internal-Codex-Responses-Lite") {
			for _, value := range values {
				if strings.EqualFold(strings.TrimSpace(value), "true") {
					return true
				}
			}
		}
	}
	value := gjson.GetBytes(body, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite")
	return value.Type == gjson.True || value.Type == gjson.String && strings.EqualFold(strings.TrimSpace(value.String()), "true")
}
