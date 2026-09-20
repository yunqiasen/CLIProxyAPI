package helps

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var anyHistoricalItemRejection = regexp.MustCompile(`^bad response status code 400 \(request id: [A-Za-z0-9_-]+\)$`)

// Any hides the underlying item error behind this exact envelope. A same-key
// live comparison verified it rejects an Agent assistant item ID while accepting
// identical readable history without that ID. This is a bounded repair candidate,
// not a general classification of all HTTP 400s as resource mismatches.
func anyResponsesHistoricalItemRejection(body, rejection []byte, endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "anyrouter.top") || !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/responses") || gjson.GetBytes(body, "model").String() != "gpt-6-astra" {
		return false
	}
	e := gjson.GetBytes(rejection, "error")
	if !e.IsObject() {
		e = gjson.GetBytes(rejection, "response.error")
	}
	if e.Get("type").String() != "invalid_request_error" || e.Get("code").String() != "" || e.Get("param").String() != "" || !anyHistoricalItemRejection.MatchString(e.Get("message").String()) {
		return false
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		// Bare reasoning and client-owned message IDs are not sufficient evidence.
		if item.Get("type").String() != "reasoning" && item.Get("id").Type == gjson.String && item.Get("id").String() != "" && responsesRouteBoundItemID(item) {
			return true
		}
	}
	return false
}
