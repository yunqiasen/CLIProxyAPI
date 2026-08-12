package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeExecutor is a stateless executor for Anthropic Claude over the messages API.
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type ClaudeExecutor struct {
	cfg                     *config.Config
	requestLogProvider      string
	upstreamModelNormalizer func(string) string
	oauthProfileFetcher     claudeOAuthProfileFetcher
}

type claudeOAuthCancellationError struct {
	cause error
}

func (e *claudeOAuthCancellationError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *claudeOAuthCancellationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *claudeOAuthCancellationError) IsRequestScoped() bool {
	return e != nil
}

func newClaudeOAuthCancellationError(ctx context.Context, oauth bool, err error) error {
	if !oauth {
		return nil
	}
	cause := err
	if ctx != nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	if !errors.Is(cause, context.Canceled) {
		return nil
	}
	return &claudeOAuthCancellationError{cause: cause}
}

func shouldSanitizeClaudeMessagesForUpstream(baseModel string) bool {
	return sigcompat.SignatureProviderFromModelName(baseModel) == sigcompat.SignatureProviderClaude
}

func sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx context.Context, body []byte, baseModel string, preserveEmptyThinkingBlocks ...bool) []byte {
	sanitized := body
	preserveEmpty := len(preserveEmptyThinkingBlocks) > 0 && preserveEmptyThinkingBlocks[0]
	if shouldSanitizeClaudeMessagesForUpstream(baseModel) || preserveEmpty {
		var report sigcompat.SignatureSanitizeReport
		sanitized, report = sigcompat.SanitizeClaudeMessagesForClaudeUpstream(body, baseModel, preserveEmptyThinkingBlocks...)
		logClaudeSignatureSanitizeReport(ctx, baseModel, report)
	}
	return sanitizeClaudeWebSearchDomains(sanitized)
}

// sanitizeClaudeWebSearchDomains removes empty allowed_domains/blocked_domains
// arrays from built-in web_search tools. Some clients (e.g. litellm) emit an
// empty array instead of omitting the field, and Anthropic rejects it with
// "Empty list of domains is ambiguous. Provide at least one domain or null.".
// Deleting the key is equivalent to leaving it unset.
func sanitizeClaudeWebSearchDomains(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body
	}
	tools.ForEach(func(index, tool gjson.Result) bool {
		if !strings.HasPrefix(tool.Get("type").String(), "web_search_") {
			return true
		}
		for _, field := range []string{"allowed_domains", "blocked_domains"} {
			value := tool.Get(field)
			if value.Exists() && value.IsArray() && len(value.Array()) == 0 {
				path := fmt.Sprintf("tools.%d.%s", index.Int(), field)
				if updated, errDelete := sjson.DeleteBytes(body, path); errDelete == nil {
					body = updated
				}
			}
		}
		return true
	})
	return body
}

type claudeUnsupportedServerToolFallback struct {
	toolTypes map[string]bool
	toolNames map[string]bool
}

func claudeUnsupportedServerToolFallbackFromError(statusCode int, body []byte, requestBody []byte) (claudeUnsupportedServerToolFallback, bool) {
	if statusCode < http.StatusBadRequest || statusCode >= http.StatusInternalServerError {
		return claudeUnsupportedServerToolFallback{}, false
	}
	message := gjson.GetBytes(body, "error.message").String()
	if message == "" {
		message = string(body)
	}
	unsupportedType, ok := claudeUnsupportedServerToolTypeFromMessage(message)
	if !ok {
		return claudeUnsupportedServerToolFallback{}, false
	}

	fallback := claudeUnsupportedServerToolFallback{
		toolTypes: make(map[string]bool),
		toolNames: make(map[string]bool),
	}
	tools := gjson.GetBytes(requestBody, "tools")
	if !tools.IsArray() {
		return claudeUnsupportedServerToolFallback{}, false
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if !helps.IsClaudeServerToolType(toolType) || toolType != unsupportedType {
			return true
		}
		fallback.toolTypes[toolType] = true
		if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
			fallback.toolNames[name] = true
		}
		return true
	})
	return fallback, len(fallback.toolTypes) > 0
}

func claudeUnsupportedServerToolTypeFromMessage(message string) (string, bool) {
	if toolType, ok := claudeUnsupportedServerToolTypeFromExactMessage(message); ok {
		return toolType, true
	}

	const bedrockPrefix = "InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: "
	if !strings.HasPrefix(message, bedrockPrefix) {
		return "", false
	}
	wrapped := message[len(bedrockPrefix):]
	const validationMarker = ", ValidationException: "
	validationIndex := strings.Index(wrapped, validationMarker)
	if validationIndex <= 0 || !claudeWrapperOpaqueID(wrapped[:validationIndex]) {
		return "", false
	}
	wrapped = wrapped[validationIndex+len(validationMarker):]

	const requestIDMarker = " (request id: "
	requestIDIndex := strings.Index(wrapped, requestIDMarker)
	if requestIDIndex < 0 {
		return "", false
	}
	requestIDStart := requestIDIndex + len(requestIDMarker)
	requestIDEndOffset := strings.Index(wrapped[requestIDStart:], ")")
	if requestIDEndOffset <= 0 {
		return "", false
	}
	requestIDEnd := requestIDStart + requestIDEndOffset
	if !claudeWrapperOpaqueID(wrapped[requestIDStart:requestIDEnd]) {
		return "", false
	}
	if !claudeBedrockWrapperSuffixValid(wrapped[requestIDEnd+1:]) {
		return "", false
	}
	return claudeUnsupportedServerToolTypeFromExactMessage(wrapped[:requestIDIndex])
}

func claudeBedrockWrapperSuffixValid(suffix string) bool {
	if suffix == "" {
		return true
	}
	const tracePrefix = " [trace_id="
	if !strings.HasPrefix(suffix, tracePrefix) {
		return false
	}
	traceEndOffset := strings.Index(suffix[len(tracePrefix):], "]")
	if traceEndOffset <= 0 {
		return false
	}
	traceEnd := len(tracePrefix) + traceEndOffset
	if !claudeWrapperOpaqueID(suffix[len(tracePrefix):traceEnd]) {
		return false
	}

	const relayPrefix = " (request id: "
	relay := suffix[traceEnd+1:]
	if !strings.HasPrefix(relay, relayPrefix) || !strings.HasSuffix(relay, ")") {
		return false
	}
	return claudeWrapperOpaqueID(relay[len(relayPrefix) : len(relay)-1])
}

func claudeWrapperOpaqueID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune("()[],", r) {
			return false
		}
	}
	return true
}

func claudeUnsupportedServerToolTypeFromExactMessage(message string) (string, bool) {
	const prefix = "tool type '"
	const suffix = "' is not supported for this model"
	if !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, suffix) {
		return "", false
	}
	toolType := message[len(prefix) : len(message)-len(suffix)]
	if toolType == "" || strings.TrimSpace(toolType) != toolType || strings.ContainsAny(toolType, "'\"") {
		return "", false
	}
	return toolType, true
}

func removeClaudeUnsupportedServerTools(body []byte, fallback claudeUnsupportedServerToolFallback) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, false
	}
	filtered := make([]json.RawMessage, 0, len(tools.Array()))
	removed := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if fallback.toolTypes[toolType] {
			removed = true
			return true
		}
		filtered = append(filtered, json.RawMessage(tool.Raw))
		return true
	})
	if !removed {
		return body, false
	}
	if len(filtered) == 0 {
		body, _ = sjson.DeleteBytes(body, "tools")
	} else if rawTools, errMarshal := json.Marshal(filtered); errMarshal == nil {
		body, _ = sjson.SetRawBytes(body, "tools", rawTools)
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.Exists() {
		choiceType := strings.TrimSpace(toolChoice.Get("type").String())
		choiceName := strings.TrimSpace(toolChoice.Get("name").String())
		choiceStillExists := false
		if choiceName != "" {
			for _, tool := range filtered {
				if strings.TrimSpace(gjson.GetBytes(tool, "name").String()) == choiceName {
					choiceStillExists = true
					break
				}
			}
		}
		if len(filtered) == 0 || fallback.toolTypes[choiceType] || (fallback.toolNames[choiceName] && !choiceStillExists) {
			body, _ = sjson.DeleteBytes(body, "tool_choice")
		}
	}
	return body, true
}

func logClaudeSignatureSanitizeReport(ctx context.Context, baseModel string, report sigcompat.SignatureSanitizeReport) {
	if report.DroppedBlocks == 0 && report.DroppedSignatures == 0 && report.ReplacedSignatures == 0 {
		return
	}

	fields := log.Fields{
		"component":           "signature_sanitizer",
		"executor":            "claude",
		"action":              "sanitize_claude_messages",
		"target_provider":     string(report.TargetProvider),
		"target_model":        baseModel,
		"preserved":           report.Preserved,
		"dropped_blocks":      report.DroppedBlocks,
		"dropped_signatures":  report.DroppedSignatures,
		"replaced_signatures": report.ReplacedSignatures,
	}
	if len(report.Decisions) > 0 {
		decision := report.Decisions[0]
		fields["first_block_kind"] = string(decision.BlockKind)
		fields["first_detected_provider"] = string(decision.DetectedProvider)
		fields["first_reason"] = decision.Reason
	}

	helps.LogWithRequestID(ctx).WithFields(fields).Debug("claude executor: sanitized signature history before upstream")
}

// Anthropic-compatible upstreams may reject or even crash when Claude models
// omit max_tokens. Prefer registered model metadata before using a fallback.
const defaultModelMaxTokens = 1024

func NewClaudeExecutor(cfg *config.Config) *ClaudeExecutor { return &ClaudeExecutor{cfg: cfg} }

func (e *ClaudeExecutor) Identifier() string { return "claude" }

func (e *ClaudeExecutor) upstreamRequestLogProvider() string {
	if provider := strings.TrimSpace(e.requestLogProvider); provider != "" {
		return provider
	}
	return e.Identifier()
}

func (e *ClaudeExecutor) upstreamModel(baseModel string) string {
	if e.upstreamModelNormalizer != nil {
		return e.upstreamModelNormalizer(baseModel)
	}
	return baseModel
}

func (e *ClaudeExecutor) restoreResponseModel(payload []byte, model string) []byte {
	if e.upstreamModelNormalizer == nil || strings.TrimSpace(model) == "" {
		return payload
	}
	return restoreClaudeResponseModel(payload, model)
}

func restoreClaudeResponseModel(payload []byte, model string) []byte {
	if updated, changed := setClaudeResponseModel(payload, model); changed {
		return updated
	}

	trimmed := bytes.TrimSpace(payload)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return payload
	}
	dataIndex := bytes.Index(payload, []byte("data:"))
	if dataIndex < 0 {
		return payload
	}
	rawJSON := bytes.TrimSpace(payload[dataIndex+len("data:"):])
	updated, changed := setClaudeResponseModel(rawJSON, model)
	if !changed {
		return payload
	}
	rebuilt := make([]byte, 0, dataIndex+len("data: ")+len(updated))
	rebuilt = append(rebuilt, payload[:dataIndex]...)
	rebuilt = append(rebuilt, []byte("data: ")...)
	rebuilt = append(rebuilt, updated...)
	return rebuilt
}

func setClaudeResponseModel(payload []byte, model string) ([]byte, bool) {
	if !gjson.ValidBytes(payload) {
		return payload, false
	}
	updated := payload
	changed := false
	for _, path := range []string{"model", "message.model"} {
		if !gjson.GetBytes(updated, path).Exists() {
			continue
		}
		next, errSet := sjson.SetBytes(updated, path, model)
		if errSet != nil {
			continue
		}
		updated = next
		changed = true
	}
	return updated, changed
}

// PrepareRequest injects Claude credentials into the outgoing HTTP request.
func (e *ClaudeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := claudeCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	useAPIKey := auth != nil && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""
	isAnthropicBase := isAnthropicUpstreamURL(req.URL)
	if isAnthropicBase && useAPIKey {
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", apiKey)
	} else {
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Claude credentials into the request and executes it.
func (e *ClaudeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("claude executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}
