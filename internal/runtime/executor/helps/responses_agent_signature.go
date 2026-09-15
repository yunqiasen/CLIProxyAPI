package helps

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var agentSignatureRejection = regexp.MustCompile(`^(?:OpenAI Responses bad request: )?The encrypted content for item (rs_[A-Za-z0-9_-]+) could not be verified\. Reason: Encrypted content could not be decrypted or parsed\.(?: \[trace_id=[A-Za-z0-9_-]+\])?$`)

var agentResourceRejection = regexp.MustCompile(`^(?:OpenAI Responses bad request: )?The requested item was created under a different Azure OpenAI resource\. Use the same resource that created the item to access it\.(?: \[trace_id=[A-Za-z0-9_-]+\])?$`)

// normalizeAgentSignatureRejection recognizes explicit Agent state rejections.
// A named item must match opaque reasoning. Resource errors have no item ID;
// the shared retry additionally requires portable history and excludes stored
// references/compaction, then removes only opaque reasoning, never other items.
func normalizeAgentSignatureRejection(body, rejection []byte, endpoint string) []byte {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "agentrouter.org") || !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/responses") {
		return rejection
	}
	path := "error"
	if !gjson.GetBytes(rejection, path).IsObject() {
		path = "response.error"
	}
	e := gjson.GetBytes(rejection, path)
	if e.Get("type").String() != "invalid_request_error" || e.Get("code").String() != "" || e.Get("param").String() != "" {
		return rejection
	}
	match := agentSignatureRejection.FindStringSubmatch(e.Get("message").String())
	resourceMismatch := agentResourceRejection.MatchString(e.Get("message").String())
	if len(match) != 2 && !resourceMismatch {
		return rejection
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "reasoning" || (!resourceMismatch && item.Get("id").String() != match[1]) || item.Get("encrypted_content").Type != gjson.String || item.Get("encrypted_content").String() == "" {
			continue
		}
		normalized, errSet := sjson.SetBytes(rejection, path+".code", "invalid_encrypted_content")
		if errSet == nil {
			return normalized
		}
	}
	return rejection
}
