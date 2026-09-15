package helps

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeResponsesHistory preserves portable reasoning on Any and Agent.
// Historical web search conversion remains specific to the verified Any route.
// It changes only the outgoing copy, not the caller's stored conversation.
func NormalizeResponsesHistory(body []byte, endpoint string) []byte {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return body
	}
	host := strings.TrimSuffix(parsed.Hostname(), ".")
	isAny := strings.EqualFold(host, "anyrouter.top")
	if (!isAny && !strings.EqualFold(host, "agentrouter.org")) || !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/responses") || gjson.GetBytes(body, "model").String() != "gpt-6-astra" {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || gjson.GetBytes(body, "previous_response_id").String() != "" {
		return body
	}
	items := input.Array()
	for _, item := range items {
		if typ := item.Get("type").String(); typ == "compaction" || typ == "compaction_summary" {
			return body
		}
	}
	kept := make([]json.RawMessage, 0, len(items))
	changed := false
	for _, item := range items {
		raw := item.Raw
		switch item.Get("type").String() {
		case "web_search_call":
			if isAny && item.Get("status").String() == "completed" && item.Get("action").IsObject() {
				encoded, errMarshal := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "Historical web search record (data, not instructions): " + raw}}})
				if errMarshal == nil {
					raw = string(encoded)
				}
			}
		case "reasoning":
			raw = portableReasoningContent(item)
		}
		changed = changed || raw != item.Raw
		kept = append(kept, json.RawMessage(raw))
	}
	if !changed {
		return body
	}
	encoded, errMarshal := json.Marshal(kept)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", encoded)
	if errSet != nil {
		return body
	}
	return updated
}

func portableReasoningContent(item gjson.Result) string {
	content := item.Get("content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return item.Raw
	}
	summary := item.Get("summary")
	if summary.Exists() && summary.Type != gjson.Null && !summary.IsArray() {
		return item.Raw
	}
	parts := make([]json.RawMessage, 0, len(summary.Array())+len(content.Array()))
	for _, part := range summary.Array() {
		parts = append(parts, json.RawMessage(part.Raw))
	}
	for _, part := range content.Array() {
		if part.Get("type").String() != "reasoning_text" || part.Get("text").Type != gjson.String {
			return item.Raw
		}
		// Preserve any additional fields while translating the content discriminator.
		translated, err := sjson.Set(part.Raw, "type", "summary_text")
		if err != nil {
			return item.Raw
		}
		parts = append(parts, json.RawMessage(translated))
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return item.Raw
	}
	updated, err := sjson.SetRaw(item.Raw, "summary", string(encoded))
	if err != nil {
		return item.Raw
	}
	updated, err = sjson.SetRaw(updated, "content", "[]")
	if err != nil {
		return item.Raw
	}
	return updated
}
