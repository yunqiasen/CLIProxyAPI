package helps

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesCompactionTrigger identifies an explicit Codex V2 operation, not
// ordinary history containing a previously generated compaction item.
func ResponsesCompactionTrigger(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction_trigger" {
			return true
		}
	}
	return false
}

// ValidateResponsesCompactWindow checks the V1 response envelope without
// pruning, decrypting or reordering the upstream's canonical output window.
func ValidateResponsesCompactWindow(body []byte) error {
	if !json.Valid(body) {
		return fmt.Errorf("upstream compact response is not valid JSON")
	}
	if failure := gjson.GetBytes(body, "error"); failure.Exists() && failure.Type != gjson.Null {
		return fmt.Errorf("upstream compact error: %s", failure.Raw)
	}
	id := gjson.GetBytes(body, "id")
	if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" || !gjson.GetBytes(body, "output").IsArray() {
		return fmt.Errorf("upstream compact response is missing a valid id or output window")
	}
	for i, item := range gjson.GetBytes(body, "output").Array() {
		if !item.IsObject() {
			return fmt.Errorf("upstream compact output item %d is not an object", i)
		}
	}
	return nil
}

// ResponsesCompactionStream tracks only explicit V2 operations. Ordinary chat
// and V1 compact windows retain their own contracts.
type ResponsesCompactionStream struct {
	active bool
	count  int
}

func NewResponsesCompactionStream(request []byte) *ResponsesCompactionStream {
	return &ResponsesCompactionStream{active: ResponsesCompactionTrigger(request)}
}

// Observe rejects a successful V2 terminal unless exactly one complete opaque
// compaction item has been delivered. Upstream failures retain their own error.
func (s *ResponsesCompactionStream) Observe(event []byte) error {
	if s == nil || !s.active {
		return nil
	}
	switch gjson.GetBytes(event, "type").String() {
	case "response.output_item.done":
		item := gjson.GetBytes(event, "item")
		if item.Get("type").String() != "compaction" {
			return nil
		}
		s.count++
		state := item.Get("encrypted_content")
		if state.Type != gjson.String || strings.TrimSpace(state.String()) == "" {
			return fmt.Errorf("upstream V2 compaction item has no valid encrypted_content")
		}
	case "response.completed", "response.done":
		if s.count != 1 {
			return fmt.Errorf("upstream V2 compaction expected exactly one completed compaction item, got %d", s.count)
		}
	}
	return nil
}

// RestorePublicResponsesCompactionFields preserves public API capabilities only
// for the known public endpoint. A third-party Codex route is not assumed to
// accept fields rejected by the native backend.
func RestorePublicResponsesCompactionFields(body, original []byte, baseURL, sourceFormat string) []byte {
	if sourceFormat != "openai-response" {
		return body
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(endpoint.Hostname(), "api.openai.com") || strings.TrimRight(endpoint.Path, "/") != "/v1" {
		return body
	}
	for _, field := range []string{"context_management", "max_output_tokens"} {
		value := gjson.GetBytes(original, field)
		if value.Exists() {
			if updated, errSet := sjson.SetRawBytes(body, field, []byte(value.Raw)); errSet == nil {
				body = updated
			}
		}
	}
	return body
}
