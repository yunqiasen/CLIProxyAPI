package helps

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var agentSignatureRejection = regexp.MustCompile(`^(?:OpenAI Responses bad request: )?The encrypted content for item (rs_[A-Za-z0-9_-]+) could not be verified\. Reason: Encrypted content could not be decrypted or parsed\.(?: \[trace_id=[A-Za-z0-9_-]+\])?$`)

// Agent sometimes masks the resource vendor as "***" in this exact error.
var agentResourceRejection = regexp.MustCompile(`^(?:OpenAI Responses bad request: )?The requested item was created under a different (?:Azure|\*{3}) OpenAI resource\. Use the same resource that created the item to access it\.(?: \[trace_id=[A-Za-z0-9_-]+\])?$`)

type agentSignatureRejectionKind uint8

const (
	agentSignatureRejectionNone agentSignatureRejectionKind = iota
	agentSignatureRejectionEncrypted
	agentSignatureRejectionResource
)

type agentSignatureRejectionMatch struct {
	kind agentSignatureRejectionKind
	path string
}

// classifyAgentSignatureRejection recognizes exact Agent state rejections. A
// named encrypted item must match opaque reasoning. A resource mismatch may be
// carried by portable response item IDs, so it only requires route-bound
// state plus portable-history checks in the shared retry.
func classifyAgentSignatureRejection(body, rejection []byte, endpoint string) agentSignatureRejectionMatch {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "agentrouter.org") || !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/responses") {
		return agentSignatureRejectionMatch{}
	}
	path := "error"
	if !gjson.GetBytes(rejection, path).IsObject() {
		path = "response.error"
	}
	e := gjson.GetBytes(rejection, path)
	if e.Get("type").String() != "invalid_request_error" || e.Get("code").String() != "" || e.Get("param").String() != "" {
		return agentSignatureRejectionMatch{}
	}
	message := e.Get("message").String()
	if match := agentSignatureRejection.FindStringSubmatch(message); len(match) == 2 {
		for _, item := range gjson.GetBytes(body, "input").Array() {
			if item.Get("type").String() == "reasoning" && item.Get("id").String() == match[1] && item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").String() != "" {
				return agentSignatureRejectionMatch{kind: agentSignatureRejectionEncrypted, path: path}
			}
		}
		return agentSignatureRejectionMatch{}
	}
	if !agentResourceRejection.MatchString(message) {
		return agentSignatureRejectionMatch{}
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if responsesRouteBoundItemID(item) {
			return agentSignatureRejectionMatch{kind: agentSignatureRejectionResource, path: path}
		}
		if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").String() != "" {
			return agentSignatureRejectionMatch{kind: agentSignatureRejectionResource, path: path}
		}
	}
	return agentSignatureRejectionMatch{}
}

// normalizeAgentSignatureRejection maps verified Agent state errors into the
// shared route-bound-state recovery code without changing unrelated errors.
func normalizeAgentSignatureRejection(body, rejection []byte, endpoint string) []byte {
	match := classifyAgentSignatureRejection(body, rejection, endpoint)
	if match.kind == agentSignatureRejectionNone {
		return rejection
	}
	normalized, errSet := sjson.SetBytes(rejection, match.path+".code", "invalid_encrypted_content")
	if errSet == nil {
		return normalized
	}
	return rejection
}
