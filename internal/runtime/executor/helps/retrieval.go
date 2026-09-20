package helps

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// RetrievalKind identifies protocols that must never pass through a chat translator.
func RetrievalKind(format string) string {
	switch format {
	case "openai-embeddings":
		return "embeddings"
	case "cohere-rerank":
		return "rerank"
	}
	return ""
}

func RetrievalFormat(kind string) string {
	switch kind {
	case "embeddings":
		return "openai-embeddings"
	case "rerank":
		return "cohere-rerank"
	}
	return ""
}

// ValidateRetrievalRequest validates the envelope without rewriting extension fields.
func ValidateRetrievalRequest(body []byte, kind string) error {
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return fmt.Errorf("invalid JSON request")
	}
	model := gjson.GetBytes(body, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return fmt.Errorf("model is required")
	}
	if stream := gjson.GetBytes(body, "stream"); stream.Exists() && stream.Type != gjson.False {
		return fmt.Errorf("retrieval requests do not support streaming")
	}
	if kind == "embeddings" {
		if retrievalInputCount(gjson.GetBytes(body, "input")) == 0 {
			return fmt.Errorf("input must contain nonempty text or token arrays")
		}
		if dimensions := gjson.GetBytes(body, "dimensions"); dimensions.Exists() && (!retrievalInteger(dimensions) || dimensions.Int() <= 0) {
			return fmt.Errorf("dimensions must be a positive integer")
		}
		if encoding := gjson.GetBytes(body, "encoding_format"); encoding.Exists() && (encoding.Type != gjson.String || (encoding.String() != "float" && encoding.String() != "base64")) {
			return fmt.Errorf("encoding_format must be float or base64")
		}
	} else if kind == "rerank" {
		query, docs := gjson.GetBytes(body, "query"), gjson.GetBytes(body, "documents")
		if query.Type != gjson.String || query.String() == "" || !docs.IsArray() || len(docs.Array()) == 0 {
			return fmt.Errorf("query and a nonempty documents array are required")
		}
		for _, doc := range docs.Array() {
			if doc.Type != gjson.String && !doc.IsObject() {
				return fmt.Errorf("documents must contain text or document objects")
			}
		}
		if n := gjson.GetBytes(body, "top_n"); n.Exists() && (!retrievalInteger(n) || n.Int() <= 0) {
			return fmt.Errorf("top_n must be a positive integer")
		}
	} else {
		return fmt.Errorf("unsupported retrieval protocol")
	}
	return nil
}

// RetrievalURL keeps overrides on the configured provider origin and base path.
func RetrievalURL(base, override, kind string) (string, error) {
	path := strings.TrimSpace(override)
	if path == "" {
		path = "/" + kind
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(path, "\\") {
		return "", fmt.Errorf("invalid retrieval upstream path")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == ".." || part == "." {
			return "", fmt.Errorf("invalid retrieval upstream path")
		}
	}
	target, err := url.Parse(strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/"))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return "", fmt.Errorf("invalid retrieval provider URL")
	}
	return target.String(), nil
}

// ValidateRetrievalResponse rejects empty/chat/error envelopes before publishing
// success. It does not convert vectors, scores, document objects, or usage units.
func ValidateRetrievalResponse(body, request []byte, kind string) error {
	invalid := fmt.Errorf("invalid %s upstream response", kind)
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() || gjson.GetBytes(body, "error").Exists() {
		return invalid
	}
	field := "data"
	limit := retrievalInputCount(gjson.GetBytes(request, "input"))
	if kind == "rerank" {
		field = "results"
		limit = len(gjson.GetBytes(request, "documents").Array())
	}
	results := gjson.GetBytes(body, field)
	if !results.IsArray() || len(results.Array()) == 0 {
		return invalid
	}
	if kind == "embeddings" && len(results.Array()) != limit {
		return invalid
	}
	seen := make(map[int64]bool)
	dimensions := int(gjson.GetBytes(request, "dimensions").Int())
	for _, item := range results.Array() {
		index := item.Get("index")
		if !retrievalInteger(index) || index.Int() < 0 || index.Int() >= int64(limit) || seen[index.Int()] {
			return invalid
		}
		seen[index.Int()] = true
		if kind == "embeddings" {
			vector := item.Get("embedding")
			size := 0
			if vector.Type == gjson.String {
				data, err := base64.StdEncoding.DecodeString(vector.String())
				if err != nil || len(data) == 0 || len(data)%4 != 0 {
					return invalid
				}
				for offset := 0; offset < len(data); offset += 4 {
					value := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4])))
					if math.IsInf(value, 0) || math.IsNaN(value) {
						return invalid
					}
				}
				size = len(data) / 4
			} else {
				if !vector.IsArray() || len(vector.Array()) == 0 {
					return invalid
				}
				size = len(vector.Array())
				for _, value := range vector.Array() {
					if value.Type != gjson.Number || math.IsInf(value.Float(), 0) || math.IsNaN(value.Float()) {
						return invalid
					}
				}
			}
			if dimensions == 0 {
				dimensions = size
			}
			if size != dimensions {
				return invalid
			}
		} else if item.Get("relevance_score").Type != gjson.Number {
			return invalid
		}
	}
	return nil
}

func retrievalInteger(value gjson.Result) bool {
	return value.Type == gjson.Number && value.Float() == float64(value.Int())
}

// Token arrays represent one input; arrays of texts/token arrays represent a batch.
func retrievalInputCount(input gjson.Result) int {
	if input.Type == gjson.String {
		if input.String() != "" {
			return 1
		}
		return 0
	}
	if !input.IsArray() || len(input.Array()) == 0 {
		return 0
	}
	values := input.Array()
	if values[0].Type == gjson.Number {
		if validRetrievalTokens(values) {
			return 1
		}
		return 0
	}
	for _, item := range values {
		if values[0].Type == gjson.String {
			if item.Type != gjson.String || item.String() == "" {
				return 0
			}
		} else if !item.IsArray() || !validRetrievalTokens(item.Array()) {
			return 0
		}
	}
	return len(values)
}

func validRetrievalTokens(values []gjson.Result) bool {
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		if !retrievalInteger(v) || v.Int() < 0 {
			return false
		}
	}
	return true
}

// ParseRetrievalUsage accepts real token counters only. Rerank search units stay
// in the untouched response body and are never relabeled as tokens.
func ParseRetrievalUsage(body []byte, kind string) usage.Detail {
	if kind == "rerank" && !hasOpenAIStyleUsageTokenFields(gjson.GetBytes(body, "usage")) {
		if tokens := gjson.GetBytes(body, "meta.tokens"); hasOpenAIStyleUsageTokenFields(tokens) {
			return parseOpenAIStyleUsageNode(tokens)
		}
	}
	return ParseOpenAIUsage(body)
}

// RetrievalRetryAfter exposes upstream backoff to CPA's existing scheduler.
func RetrievalRetryAfter(headers http.Header, now time.Time) *time.Duration {
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return nil
	}
	if delay, err := time.ParseDuration(value + "s"); err == nil && delay >= 0 {
		return &delay
	}
	if deadline, err := http.ParseTime(value); err == nil {
		delay := max(deadline.Sub(now), 0)
		return &delay
	}
	return nil
}
