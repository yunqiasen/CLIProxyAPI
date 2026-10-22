package helps

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Agent appends relay request metadata to the upstream message. Keep both the
// message and diagnostic suffix anchored rather than accepting arbitrary prose.
const agentRejectionMetadataSuffix = `(?: \(request id: [A-Za-z0-9_-]+\))?(?: \[trace_id=[A-Za-z0-9_-]+\]| trace_id: [A-Za-z0-9_-]+)?$`

const agentRejectionDecodePrefix = "failed to decode error response: json: cannot unmarshal number into Go struct field ResponseError.error.code of type string, raw: "

var agentRejectionRelayMetadata = regexp.MustCompile(`^` + agentRejectionMetadataSuffix)

var agentSignatureRejection = regexp.MustCompile(`^(?:OpenAI Responses bad request: )?The encrypted content for item (rs_[A-Za-z0-9_-]+) could not be verified\. Reason: Encrypted content could not be decrypted or parsed\.` + agentRejectionMetadataSuffix)

// Agent sometimes masks the resource vendor as "***" in this exact error.
var agentResourceRejection = regexp.MustCompile(`^(?:OpenAI Responses bad request: )?The requested item was created under a different (?:Azure|\*{3}) OpenAI resource\. Use the same resource that created the item to access it\.` + agentRejectionMetadataSuffix)

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
	message := agentSignatureRejectionMessage(gjson.GetBytes(rejection, path))
	if message == "" {
		return agentSignatureRejectionMatch{}
	}
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

// agentSignatureRejectionMessage unwraps only the observed relay decoder error.
// Parse one JSON value and validate both envelopes plus the remaining metadata;
// quoted errors, arbitrary trailers and recursive wrappers are not evidence.
func agentSignatureRejectionMessage(e gjson.Result) string {
	if !isAgentSignatureRejectionEnvelope(e) {
		return ""
	}
	message := e.Get("message").String()
	if !strings.HasPrefix(message, agentRejectionDecodePrefix) {
		return message
	}
	encoded := strings.TrimPrefix(message, agentRejectionDecodePrefix)
	decoder := json.NewDecoder(strings.NewReader(encoded))
	var raw json.RawMessage
	if errDecode := decoder.Decode(&raw); errDecode != nil || !agentRejectionRelayMetadata.MatchString(encoded[decoder.InputOffset():]) {
		return ""
	}
	inner := gjson.GetBytes(raw, "error")
	if !isAgentSignatureRejectionEnvelope(inner) || inner.Get("code").Type != gjson.Number || inner.Get("code").Raw != "0" {
		return ""
	}
	return inner.Get("message").String()
}

func isAgentSignatureRejectionEnvelope(e gjson.Result) bool {
	code := e.Get("code")
	return e.IsObject() && e.Get("type").String() == "invalid_request_error" && e.Get("param").String() == "" &&
		(code.String() == "" || (code.Type == gjson.Number && code.Raw == "0"))
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
