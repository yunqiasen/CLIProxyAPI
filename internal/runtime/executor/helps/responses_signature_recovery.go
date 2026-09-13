package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var responsesRejectedInputIndex = regexp.MustCompile(`^input(?:\[([0-9]+)\]|\.([0-9]+))(?:\.(?:encrypted_content|id))?$`)

// PortableResponsesSignatureRetry repairs only explicit rejection of opaque reasoning.
// It leaves the caller's body, portable history, and successful requests unchanged.
func PortableResponsesSignatureRetry(body, rejection []byte, endpoint string) ([]byte, bool) {
	if !json.Valid(body) || !json.Valid(rejection) {
		return body, false
	}
	rejection = normalizeAgentSignatureRejection(body, rejection, endpoint)
	code := gjson.GetBytes(rejection, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(rejection, "response.error.code").String()
	}
	if code != "invalid_encrypted_content" && code != "thinking_signature_invalid" {
		return body, false
	}
	if strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != "" {
		return body, false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	target := -1
	param := gjson.GetBytes(rejection, "error.param").String()
	if param == "" {
		param = gjson.GetBytes(rejection, "response.error.param").String()
	}
	if param != "" {
		match := responsesRejectedInputIndex.FindStringSubmatch(param)
		if len(match) == 0 {
			return body, false
		}
		value := match[1]
		if value == "" {
			value = match[2]
		}
		var err error
		target, err = strconv.Atoi(value)
		if err != nil {
			return body, false
		}
	}
	items := input.Array()
	kept := make([]json.RawMessage, 0, len(items))
	removed := false
	portable := false
	for i, item := range items {
		typ := item.Get("type").String()
		if typ == "compaction" || typ == "compaction_summary" {
			return body, false
		}
		if (target < 0 || target == i) && typ == "reasoning" && item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").String() != "" {
			removed = true
			// Only opaque state is expendable; retain readable summaries/content.
			if responsesReasoningHasText(item) {
				readable, err := sjson.Delete(item.Raw, "encrypted_content")
				if err != nil {
					return body, false
				}
				readable, err = sjson.Delete(readable, "id")
				if err != nil {
					return body, false
				}
				kept = append(kept, json.RawMessage(readable))
			}
			continue
		}
		kept = append(kept, json.RawMessage(item.Raw))
		if typ == "message" || item.Get("role").Exists() || typ == "function_call" || typ == "function_call_output" || typ == "custom_tool_call" || typ == "custom_tool_call_output" {
			portable = true
		}
	}
	if !removed || !portable {
		return body, false
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return body, false
	}
	updated, err := sjson.SetRawBytes(body, "input", encoded)
	if err != nil {
		return body, false
	}
	return updated, true
}

func responsesReasoningHasText(item gjson.Result) bool {
	for _, field := range []string{"summary", "content"} {
		for _, part := range item.Get(field).Array() {
			if part.Get("text").Type == gjson.String && part.Get("text").String() != "" {
				return true
			}
		}
	}
	return false
}

type restoredResponsesBody struct {
	io.Reader
	io.Closer
}

// DoWithResponsesSignatureRecovery retries a rejected HTTP request once, before any
// response is exposed. onRetry records the failed attempt and the actual retry body.
func DoWithResponsesSignatureRecovery(client *http.Client, req *http.Request, onRetry func(*http.Response, []byte, *http.Request, []byte) error) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil || resp == nil || resp.Body == nil || req.GetBody == nil || (resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity) {
		return resp, err
	}
	const maxRejection = 1 << 20
	rejected, errRead := io.ReadAll(io.LimitReader(resp.Body, maxRejection+1))
	originalBody := resp.Body
	resp.Body = &restoredResponsesBody{Reader: io.MultiReader(bytes.NewReader(rejected), originalBody), Closer: originalBody}
	if errRead != nil || len(rejected) > maxRejection {
		return resp, nil
	}
	requestBody, errBody := req.GetBody()
	if errBody != nil {
		return resp, nil
	}
	payload, errPayload := io.ReadAll(requestBody)
	closeResponsesRetryBody(requestBody)
	if errPayload != nil {
		return resp, nil
	}
	repaired, ok := PortableResponsesSignatureRetry(payload, rejected, req.URL.String())
	if !ok {
		return resp, nil
	}
	retry := CloneResponsesRetryRequest(req, repaired)
	if errContext := req.Context().Err(); errContext != nil {
		closeResponsesRetryBody(resp.Body)
		closeResponsesRetryBody(retry.Body)
		return nil, errContext
	}
	if onRetry != nil {
		if errRecord := onRetry(resp, rejected, retry, repaired); errRecord != nil {
			closeResponsesRetryBody(resp.Body)
			closeResponsesRetryBody(retry.Body)
			return nil, errRecord
		}
	}
	closeResponsesRetryBody(resp.Body)
	return client.Do(retry)
}

// CloneResponsesRetryRequest keeps routing/auth headers while replacing only the body.
func CloneResponsesRetryRequest(req *http.Request, payload []byte) *http.Request {
	retry := req.Clone(req.Context())
	retry.Body = io.NopCloser(bytes.NewReader(payload))
	retry.ContentLength = int64(len(payload))
	retry.Header.Del("Content-Length")
	retry.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	return retry
}

func closeResponsesRetryBody(body io.Closer) {
	if err := body.Close(); err != nil {
		log.Errorf("responses signature recovery: close body: %v", err)
	}
}

// ResponsesSignatureRetryRecorder shares retry diagnostics across HTTP execution
// modes while leaving provider-specific replay invalidation in the executor.
func ResponsesSignatureRetryRecorder(ctx context.Context, cfg *config.Config, requestLog UpstreamRequestLog, beforeRetry func(int, []byte) error) func(*http.Response, []byte, *http.Request, []byte) error {
	return func(rejected *http.Response, rejection []byte, retry *http.Request, retryBody []byte) error {
		status := http.StatusBadRequest
		if rejected != nil {
			status = rejected.StatusCode
			RecordAPIResponseMetadata(ctx, cfg, status, rejected.Header.Clone())
			AppendAPIResponseChunk(ctx, cfg, rejection)
		}
		if beforeRetry != nil {
			if err := beforeRetry(status, rejection); err != nil {
				return err
			}
		}
		LogWithRequestID(ctx).Debug("responses executor: retrying rejected opaque reasoning once with portable history")
		entry := requestLog
		entry.Headers = retry.Header.Clone()
		entry.Body = retryBody
		RecordAPIRequest(ctx, cfg, entry)
		return nil
	}
}
