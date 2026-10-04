package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexUserAgent             = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	codexOriginator            = "codex-tui"
	codexDefaultImageToolModel = "gpt-image-2"
	codexResponsesLiteHeader   = "X-OpenAI-Internal-Codex-Responses-Lite"
	codexResponsesLiteMetadata = "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite"
)

var dataTag = []byte("data:")

func translateCodexRequestPair(from, to sdktranslator.Format, model string, originalPayload, payload []byte, stream bool, preserveEmptyThinkingBlocks ...bool) ([]byte, []byte) {
	original, body, _ := translateCodexRequestPairWithUpdateIntent(from, to, model, originalPayload, payload, stream, preserveEmptyThinkingBlocks...)
	return original, body
}

func translateCodexRequestPairWithUpdateIntent(from, to sdktranslator.Format, model string, originalPayload, payload []byte, stream bool, preserveEmptyThinkingBlocks ...bool) ([]byte, []byte, bool) {
	isCompat := len(preserveEmptyThinkingBlocks) > 0 && preserveEmptyThinkingBlocks[0]
	ctx := context.Background()
	translate := func(raw []byte) ([]byte, bool) {
		if isCompat && from == sdktranslator.FormatClaude && to == sdktranslator.FormatCodex {
			return helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, nil, from, to, model, raw, stream, true), false
		}
		translated := sdktranslator.TranslateRequestEnvelope(ctx, from, to, sdktranslator.RequestEnvelope{Format: from, Model: model, Stream: stream, Body: raw})
		return translated.Body, translated.ConfigurationUpdatesChanged
	}
	if bytes.Equal(originalPayload, payload) {
		body, changed := translate(payload)
		return body, body, changed
	}
	originalTranslated, _ := translate(originalPayload)
	body, changed := translate(payload)
	return originalTranslated, body, changed
}

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	} else {
		req.Header.Del("Authorization")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
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

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte, headerSets ...http.Header) (*http.Request, []byte, error) {
	var headers http.Header
	if len(headerSets) > 0 {
		headers = headerSets[0]
	}
	var cache helps.CodexCache
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		modelName := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
		if modelName == "" {
			modelName = thinking.ParseSuffix(req.Model).ModelName
		}
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, headers)
		if errCache != nil {
			return nil, nil, errCache
		}
		if ok {
			cache = cached
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAI) {
		if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
			cache.ID = strings.TrimSpace(promptCacheKey.String())
		}
		if cache.ID == "" {
			cache.ID = helps.ProviderSessionUUID("codex", req.Metadata)
		}
		if cache.ID == "" {
			if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
				cache.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
			}
		}
	}
	if cache.ID == "" {
		cache.ID = helps.ProviderSessionUUID("codex", req.Metadata)
	}

	if cache.ID != "" {
		rawJSON = helps.SetStringIfDifferent(rawJSON, "prompt_cache_key", cache.ID)
	}
	rawJSON = helps.SanitizeCodexInputItemIDs(rawJSON)
	if sourceFormatEqual(from, sdktranslator.FormatCodex) || sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		rawJSON = helps.NormalizeResponsesHistory(rawJSON, url)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, nil, err
	}
	if cache.ID != "" {
		httpReq.Header.Set("Session-Id", cache.ID)
	}
	return httpReq, rawJSON, nil
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config, clientHeaders ...http.Header) {
	var ginHeaders http.Header
	if len(clientHeaders) > 0 && clientHeaders[0] != nil {
		ginHeaders = clientHeaders[0]
	} else if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}
	applyCodexHeadersFromSources(r, auth, token, stream, cfg, ginHeaders)
}

// applyModelHeaderOverrides forces models.json config.override_header onto upstream headers.
func applyModelHeaderOverrides(headers http.Header, modelName string) {
	if headers == nil {
		return
	}
	overrides := registry.ModelOverrideHeaders(modelName)
	if len(overrides) == 0 {
		return
	}
	for key, value := range overrides {
		headers.Set(key, value)
	}
	if strings.Contains(headers.Get("User-Agent"), "Mac OS") && codexSessionHeaderValue(headers) == "" {
		headers.Set("Session_id", uuid.NewString())
	}
}

// applyCodexDirectImageHeaders sets Codex upstream headers for direct /images/* calls.
// Downstream client User-Agent values are not forwarded to reduce Cloudflare 1010 blocks.
func applyCodexDirectImageHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config, clientHeaders ...http.Header) {
	var ginHeaders http.Header
	if len(clientHeaders) > 0 && clientHeaders[0] != nil {
		ginHeaders = clientHeaders[0].Clone()
		ginHeaders.Del("User-Agent")
	} else if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header.Clone()
		ginHeaders.Del("User-Agent")
	}
	applyCodexHeadersFromSources(r, auth, token, stream, cfg, ginHeaders)
}

func applyCodexHeadersFromSources(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config, ginHeaders http.Header) {
	cacheSession := r.Header.Get("Session-Id")
	defer func() {
		if cacheSession != "" {
			r.Header.Set("Session_id", cacheSession)
		}
	}()
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	} else {
		r.Header.Del("Authorization")
	}

	if ginHeaders != nil && ginHeaders.Get("X-Codex-Beta-Features") != "" {
		r.Header.Set("X-Codex-Beta-Features", ginHeaders.Get("X-Codex-Beta-Features"))
	}
	misc.EnsureHeader(r.Header, ginHeaders, "Version", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Turn-Metadata", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Turn-State", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Client-Request-Id", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Window-Id", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Thread-Id", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Session-Id", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Openai-Internal-Codex-Responses-Lite", "")

	cfgUserAgent, _ := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithConfigPrecedence(r.Header, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := codexAuthUsesAPIKey(auth)
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		r.Header.Set("Originator", originator)
	} else if !isAPIKey {
		r.Header.Set("Originator", codexOriginator)
	}
	if !isAPIKey {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				r.Header.Set("Chatgpt-Account-Id", accountID)
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs, ginHeaders)
	applyCodexCloakingHeaders(r.Header, cfg, auth)
}

const codexRoutingHintHeader = "X-Codex-Routing-Hint"

// applyCodexRoutingHint sends the routing hint native Codex attaches to every
// ChatGPT-backend Responses request: "model=<slug>" plus ";tier=<service_tier>"
// when the body requests a tier (openai/codex rust-v0.155.0,
// codex-rs/core/src/client.rs build_routing_hint_header). Without it, a
// translated request carries service_tier=priority only in the body. Whether
// the backend needs the header to grant priority is undocumented.
//
// The model is the resolved model written to the upstream body, while the tier
// is read from the final body so payload rules cannot make the hint stale. A
// hint forwarded by a native client names its original model and is replaced.
// Operator configuration keeps precedence: when an auth "header:" rule for the
// hint resolves to a value (static, or a "$Header" reference the request
// carries), that value is sent, and callers apply models.json override_header
// afterwards. A rule that resolves to nothing falls back to the derived hint.
// API-key requests are not touched, matching native Codex, which sends no hint
// to API-key providers.
func applyCodexRoutingHint(ctx context.Context, headers http.Header, auth *cliproxyauth.Auth, baseModel string, upstreamBody []byte, clientHeaders http.Header) {
	if codexAuthUsesAPIKey(auth) {
		return
	}
	deleteHeaderCaseInsensitive(headers, codexRoutingHintHeader)
	if operatorHint := codexOperatorHeaderValue(ctx, auth, clientHeaders, codexRoutingHintHeader); operatorHint != "" {
		headers.Set(codexRoutingHintHeader, operatorHint)
		return
	}
	model := strings.TrimSpace(baseModel)
	if model == "" {
		return
	}
	hint := "model=" + model
	if tier := gjson.GetBytes(upstreamBody, "service_tier"); tier.Type == gjson.String {
		if value := strings.TrimSpace(tier.String()); value != "" {
			hint += ";tier=" + value
		}
	}
	headers.Set(codexRoutingHintHeader, hint)
}

// codexOperatorHeaderValue returns the value the auth's "header:" rules
// resolve to for name, using the same resolver that applied them to the
// request, so dynamic references that resolve to nothing report "".
func codexOperatorHeaderValue(ctx context.Context, auth *cliproxyauth.Auth, clientHeaders http.Header, name string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	resolved := (&http.Request{Header: http.Header{}}).WithContext(ctx)
	util.ApplyCustomHeadersFromAttrs(resolved, auth.Attributes, clientHeaders)
	return strings.TrimSpace(resolved.Header.Get(name))
}

func isCodexCloakingDisabled(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	if auth != nil && auth.AuthKind() == cliproxyauth.AuthKindAPIKey {
		cfg = cfg.ForAPIKey()
	}
	if auth != nil && len(auth.Attributes) > 0 {
		if val, ok := auth.Attributes[cliproxyauth.AttributeCodexDisableCloaking]; ok {
			if parsed, errParse := strconv.ParseBool(strings.TrimSpace(val)); errParse == nil {
				return parsed
			}
		}
	}
	if entry := resolveCodexKeyConfig(cfg, auth); entry != nil && entry.DisableCodexCloaking != nil {
		return *entry.DisableCodexCloaking
	}
	if cfg != nil && cfg.Codex.DisableCodexCloaking {
		return true
	}
	return false
}

func applyCodexCloakingHeaders(headers http.Header, cfg *config.Config, auth *cliproxyauth.Auth) {
	if headers == nil || cfg == nil || isCodexCloakingDisabled(cfg, auth) {
		return
	}
	headers.Set("User-Agent", codexUserAgent)
	headers.Set("Originator", codexOriginator)
}

func normalizeCodexInstructions(body []byte, nativeRequest ...bool) []byte {
	if len(nativeRequest) > 0 && nativeRequest[0] {
		return body
	}
	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() || instructions.Type == gjson.Null {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}
	return body
}

var imageGenToolJSON = []byte(`{"type":"image_generation","output_format":"png"}`)
var imageGenToolArrayJSON = []byte(`[{"type":"image_generation","output_format":"png"}]`)

func isCodexFreePlanAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func isImageGenerationFunctionTool(tool gjson.Result) bool {
	switch tool.Get("type").String() {
	case "function":
		return tool.Get("name").String() == "image_gen.imagegen"
	case "namespace":
		if tool.Get("name").String() != "image_gen" {
			return false
		}
		tools := tool.Get("tools")
		if !tools.IsArray() {
			return false
		}
		for _, nestedTool := range tools.Array() {
			if nestedTool.Get("type").String() == "function" && nestedTool.Get("name").String() == "imagegen" {
				return true
			}
		}
	}
	return false
}

func codexAuthDisablesImageGeneration(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeCodexDisableImageGeneration]), "true")
}

func codexFunctionToolName(tool gjson.Result) string {
	name := strings.TrimSpace(tool.Get("name").String())
	if name == "" {
		name = strings.TrimSpace(tool.Get("function.name").String())
	}
	return strings.ToLower(name)
}

func isCodexImageGenerationFunctionReference(tool gjson.Result) bool {
	if !strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "function") {
		return false
	}
	name := codexFunctionToolName(tool)
	if name == "image_gen.imagegen" {
		return true
	}
	namespace := strings.TrimSpace(tool.Get("namespace").String())
	if namespace == "" {
		namespace = strings.TrimSpace(tool.Get("function.namespace").String())
	}
	return strings.EqualFold(namespace, "image_gen") && name == "imagegen"
}

func isCodexImageGenerationToolChoice(choice gjson.Result) bool {
	if !choice.Exists() {
		return false
	}
	if choice.Type == gjson.String {
		value := strings.ToLower(strings.TrimSpace(choice.String()))
		return value == "image_generation" || value == "image_gen.imagegen"
	}
	if !choice.IsObject() {
		return false
	}
	choiceType := strings.ToLower(strings.TrimSpace(choice.Get("type").String()))
	name := strings.ToLower(strings.TrimSpace(choice.Get("name").String()))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(choice.Get("function.name").String()))
	}
	if choiceType == "image_generation" {
		return true
	}
	if choiceType == "function" {
		return name == "image_gen.imagegen" || (name == "imagegen" && strings.EqualFold(strings.TrimSpace(choice.Get("namespace").String()), "image_gen"))
	}
	return false
}

func stripCodexImageGenerationTool(raw []byte) ([]byte, bool) {
	tool := gjson.ParseBytes(raw)
	toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
	if toolType == "image_generation" || isCodexImageGenerationFunctionReference(tool) {
		return nil, true
	}

	if toolType != "namespace" && toolType != "additional_tools" && toolType != "allowed_tools" {
		return raw, false
	}

	nested := tool.Get("tools")
	if !nested.IsArray() {
		return raw, false
	}

	filtered := make([][]byte, 0, len(nested.Array()))
	changed := false
	imageNamespace := strings.EqualFold(strings.TrimSpace(tool.Get("name").String()), "image_gen")
	for _, nestedTool := range nested.Array() {
		if imageNamespace && strings.EqualFold(strings.TrimSpace(nestedTool.Get("type").String()), "function") && codexFunctionToolName(nestedTool) == "imagegen" {
			changed = true
			continue
		}
		filteredTool, nestedChanged := stripCodexImageGenerationTool([]byte(nestedTool.Raw))
		changed = changed || nestedChanged
		if len(filteredTool) > 0 {
			filtered = append(filtered, filteredTool)
		}
	}
	if len(filtered) == 0 {
		return nil, true
	}
	if !changed {
		return raw, false
	}
	updated, errSet := sjson.SetRaw(tool.Raw, "tools", string(helps.JoinRawJSONArray(filtered)))
	if errSet != nil {
		return raw, false
	}
	return []byte(updated), true
}

func stripCodexImageGenerationToolsAtPath(body []byte, path string) []byte {
	tools := gjson.GetBytes(body, path)
	if !tools.Exists() || !tools.IsArray() {
		return body
	}
	filtered := make([][]byte, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		filteredTool, toolChanged := stripCodexImageGenerationTool([]byte(tool.Raw))
		changed = changed || toolChanged
		if len(filteredTool) > 0 {
			filtered = append(filtered, filteredTool)
		}
	}
	if !changed {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, path, helps.JoinRawJSONArray(filtered))
	if errSet != nil {
		return body
	}
	return updated
}

func codexRootToolContainerState(body []byte) (exists bool, hasTools bool) {
	for _, path := range []string{"tools", "additional_tools"} {
		tools := gjson.GetBytes(body, path)
		if !tools.Exists() || !tools.IsArray() {
			continue
		}
		exists = true
		if len(tools.Array()) > 0 {
			hasTools = true
		}
	}
	return exists, hasTools
}

func codexToolContainerState(body []byte) (exists bool, hasTools bool) {
	inspect := func(path string) {
		tools := gjson.GetBytes(body, path)
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		exists = true
		if len(tools.Array()) > 0 {
			hasTools = true
		}
	}
	inspect("tools")
	inspect("additional_tools")
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for index := range input.Array() {
			inspect(fmt.Sprintf("input.%d.tools", index))
			inspect(fmt.Sprintf("input.%d.additional_tools", index))
		}
	}
	return exists, hasTools
}

func stripCodexImageGenerationTools(body []byte) []byte {
	hadRootToolContainers, _ := codexRootToolContainerState(body)
	hadToolContainers, _ := codexToolContainerState(body)
	body = stripCodexImageGenerationToolsAtPath(body, "tools")
	body = stripCodexImageGenerationToolsAtPath(body, "additional_tools")

	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for index := range input.Array() {
			body = stripCodexImageGenerationToolsAtPath(body, fmt.Sprintf("input.%d.tools", index))
			body = stripCodexImageGenerationToolsAtPath(body, fmt.Sprintf("input.%d.additional_tools", index))
		}
	}

	choice := gjson.GetBytes(body, "tool_choice")
	if isCodexImageGenerationToolChoice(choice) {
		if updated, errDelete := sjson.DeleteBytes(body, "tool_choice"); errDelete == nil {
			body = updated
		}
	} else if strings.EqualFold(strings.TrimSpace(choice.Get("type").String()), "allowed_tools") {
		allowedTools := choice.Get("tools")
		if allowedTools.IsArray() {
			filtered := make([][]byte, 0, len(allowedTools.Array()))
			for _, allowedTool := range allowedTools.Array() {
				filteredTool, _ := stripCodexImageGenerationTool([]byte(allowedTool.Raw))
				if len(filteredTool) > 0 {
					filtered = append(filtered, filteredTool)
				}
			}
			if len(filtered) == 0 {
				if updated, errDelete := sjson.DeleteBytes(body, "tool_choice"); errDelete == nil {
					body = updated
				}
			} else if updated, errSet := sjson.SetRawBytes(body, "tool_choice.tools", helps.JoinRawJSONArray(filtered)); errSet == nil {
				body = updated
			}
		}
	}
	_, hasRootTools := codexRootToolContainerState(body)
	_, hasTools := codexToolContainerState(body)
	if (hadRootToolContainers && !hasRootTools) || (!hadRootToolContainers && hadToolContainers && !hasTools) {
		if updated, errDelete := sjson.DeleteBytes(body, "tool_choice"); errDelete == nil {
			body = updated
		}
	}
	return normalizeCodexParallelToolCallsForTools(body)
}

func ensureImageGenerationTool(body []byte, baseModel string, auth *cliproxyauth.Auth, headers http.Header) []byte {
	if codexAuthDisablesImageGeneration(auth) {
		return stripCodexImageGenerationTools(body)
	}
	if isCodexResponsesLiteRequest(body, headers) || helps.ResponsesCompactionTrigger(body) || gjson.GetBytes(body, "tool_choice").String() == "none" {
		return body
	}
	if strings.HasSuffix(baseModel, "spark") {
		return body
	}
	if isCodexFreePlanAuth(auth) {
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tools", imageGenToolArrayJSON)
		return body
	}
	for _, t := range tools.Array() {
		if t.Get("type").String() == "image_generation" || isImageGenerationFunctionTool(t) {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tools.-1", imageGenToolJSON)
	return body
}

func normalizeCodexParallelToolCalls(body []byte, headers http.Header) []byte {
	if isCodexResponsesLiteRequest(body, headers) {
		body = helps.SetBoolIfDifferent(body, "parallel_tool_calls", false)
		return body
	}
	return normalizeCodexParallelToolCallsForTools(body)
}

// isCodexResponsesLiteRequest resolves the native header and websocket metadata
// mirror through the shared policy sources, including the optional Gin fallback.
func isCodexResponsesLiteRequest(body []byte, headers http.Header) bool {
	if strings.EqualFold(strings.TrimSpace(headers.Get(codexResponsesLiteHeader)), "true") || strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, codexResponsesLiteMetadata).String()), "true") {
		return true
	}
	return util.IsCodexResponsesLiteRequest(body, headers)
}

func normalizeCodexParallelToolCallsForTools(body []byte) []byte {
	if !gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		return body
	}

	_, hasTools := codexToolContainerState(body)
	if hasTools {
		return body
	}

	body, _ = sjson.DeleteBytes(body, "parallel_tool_calls")
	return body
}

func publishCodexImageToolUsage(ctx context.Context, reporter *helps.UsageReporter, body []byte, completedData []byte) {
	detail, ok := helps.ParseCodexImageToolUsage(completedData)
	if !ok {
		return
	}
	reporter.EnsurePublished(ctx)
	reporter.PublishAdditionalModel(ctx, codexImageGenerationToolModel(body), detail)
}

func codexImageGenerationToolModel(body []byte) string {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() != "image_generation" {
				continue
			}
			if model := strings.TrimSpace(tool.Get("model").String()); model != "" {
				return model
			}
			break
		}
	}
	return codexDefaultImageToolModel
}

func applyCodexIdentityConfuseBody(cfg *config.Config, auth *cliproxyauth.Auth, userPayload []byte, rawJSON []byte, autoProviderIdentity ...bool) ([]byte, codexIdentityConfuseState) {
	autoProviderScoped := len(autoProviderIdentity) > 0 && autoProviderIdentity[0]
	if !codexIdentityConfuseEnabled(cfg, auth, autoProviderScoped) || auth == nil || strings.TrimSpace(auth.ID) == "" || len(rawJSON) == 0 {
		return rawJSON, codexIdentityConfuseState{}
	}

	state := codexIdentityConfuseState{enabled: true, authID: strings.TrimSpace(auth.ID)}
	promptCacheKey := strings.TrimSpace(gjson.GetBytes(userPayload, "prompt_cache_key").String())
	if promptCacheKey == "" {
		promptCacheKey = strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_key").String())
	}
	if promptCacheKey != "" {
		state.originalPromptCacheKey = promptCacheKey
		state.promptCacheKey = codexIdentityConfuseUUID(auth.ID, "prompt-cache", promptCacheKey)
		rawJSON = helps.SetStringIfDifferent(rawJSON, "prompt_cache_key", state.promptCacheKey)
	}
	installationID := strings.TrimSpace(gjson.GetBytes(userPayload, "client_metadata.x-codex-installation-id").String())
	if installationID == "" {
		installationID = strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.x-codex-installation-id").String())
	}
	if installationID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-installation-id", state.confuseIdentityKind("installation", installationID))
	}
	for _, field := range []struct {
		path string
		kind string
	}{
		{path: "client_metadata.session_id", kind: "session"},
		{path: "client_metadata.thread_id", kind: "thread"},
	} {
		if value := strings.TrimSpace(gjson.GetBytes(rawJSON, field.path).String()); value != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, field.path, state.confuseIdentityKind(field.kind, value))
		}
	}
	if turnID := strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.turn_id").String()); turnID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.turn_id", state.confuseTurnID(turnID))
	}
	if turnMetadata := strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.x-codex-turn-metadata").String()); turnMetadata != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-turn-metadata", applyCodexTurnMetadataIdentityConfuse(turnMetadata, &state))
	}
	if state.promptCacheKey != "" {
		if windowID := strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.x-codex-window-id").String()); windowID != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-window-id", state.confuseWindowID(windowID))
		}
	}

	return rawJSON, state
}

func applyCodexIdentityConfuseHeaders(headers http.Header, state *codexIdentityConfuseState) {
	if headers == nil {
		return
	}
	if state == nil || !state.enabled {
		return
	}

	if rawTurnMetadata := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata")); rawTurnMetadata != "" {
		headers.Set("X-Codex-Turn-Metadata", applyCodexTurnMetadataIdentityConfuse(rawTurnMetadata, state))
	}
	if state.promptCacheKey == "" {
		return
	}

	setCodexSessionHeaderCasePreserved(headers, "Session_id", state.promptCacheKey)
	if headerValueCaseInsensitive(headers, "Conversation_id") != "" {
		setHeaderCasePreserved(headers, "Conversation_id", state.promptCacheKey)
	}
	headers.Set("X-Client-Request-Id", state.promptCacheKey)
	headers.Set("Thread-Id", state.promptCacheKey)
	windowID := headerValueCaseInsensitive(headers, "X-Codex-Window-Id")
	if windowID == "" {
		windowID = state.promptCacheKey + ":0"
	} else {
		windowID = state.confuseWindowID(windowID)
	}
	headers.Set("X-Codex-Window-Id", windowID)
}

func applyCodexTurnMetadataIdentityConfuse(rawTurnMetadata string, state *codexIdentityConfuseState) string {
	updatedTurnMetadata := rawTurnMetadata
	if state == nil || !state.enabled {
		return updatedTurnMetadata
	}
	if state.promptCacheKey != "" && gjson.Get(rawTurnMetadata, "prompt_cache_key").Exists() {
		updatedTurnMetadata, _ = sjson.Set(updatedTurnMetadata, "prompt_cache_key", state.promptCacheKey)
	} else if state.promptCacheKey != "" && state.originalPromptCacheKey != "" {
		updatedTurnMetadata = strings.ReplaceAll(updatedTurnMetadata, state.originalPromptCacheKey, state.promptCacheKey)
	}
	if turnID := strings.TrimSpace(gjson.Get(rawTurnMetadata, "turn_id").String()); turnID != "" {
		updatedTurnMetadata, _ = sjson.Set(updatedTurnMetadata, "turn_id", state.confuseTurnID(turnID))
	}
	if windowID := strings.TrimSpace(gjson.Get(rawTurnMetadata, "window_id").String()); windowID != "" {
		updatedTurnMetadata, _ = sjson.Set(updatedTurnMetadata, "window_id", state.confuseWindowID(windowID))
	}
	return updatedTurnMetadata
}

func applyCodexIdentityConfuseResponsePayload(payload []byte, state codexIdentityConfuseState) []byte {
	for _, replacement := range state.identityReplacements {
		payload = replaceCodexIdentityResponsePayload(payload, replacement.original, replacement.confused)
	}
	return replaceCodexIdentityResponsePayload(payload, state.originalPromptCacheKey, state.promptCacheKey)
}

func applyCodexIdentityExposeResponsePayload(payload []byte, state codexIdentityConfuseState) []byte {
	for i := len(state.identityReplacements) - 1; i >= 0; i-- {
		replacement := state.identityReplacements[i]
		payload = replaceCodexIdentityResponsePayload(payload, replacement.confused, replacement.original)
	}
	return replaceCodexIdentityResponsePayload(payload, state.promptCacheKey, state.originalPromptCacheKey)
}

func (state *codexIdentityConfuseState) confuseIdentityKind(kind string, value string) string {
	value = strings.TrimSpace(value)
	if state == nil || !state.enabled || strings.TrimSpace(state.authID) == "" || value == "" {
		return value
	}
	if state.originalPromptCacheKey != "" && value == state.originalPromptCacheKey && state.promptCacheKey != "" {
		return state.promptCacheKey
	}
	for _, replacement := range state.identityReplacements {
		if replacement.original == value || replacement.confused == value {
			return replacement.confused
		}
	}
	confused := codexIdentityConfuseUUID(state.authID, kind, value)
	state.identityReplacements = append(state.identityReplacements, codexIdentityReplacement{original: value, confused: confused})
	return confused
}

func (state *codexIdentityConfuseState) confuseTurnID(turnID string) string {
	return state.confuseIdentityKind("turn", turnID)
}

func (state *codexIdentityConfuseState) confuseWindowID(windowID string) string {
	windowID = strings.TrimSpace(windowID)
	if state == nil || !state.enabled || windowID == "" || state.promptCacheKey == "" {
		return windowID
	}
	for _, replacement := range state.identityReplacements {
		if replacement.original == windowID || replacement.confused == windowID {
			return replacement.confused
		}
	}
	confused := state.promptCacheKey + ":0"
	state.identityReplacements = append(state.identityReplacements, codexIdentityReplacement{original: windowID, confused: confused})
	return confused
}

func replaceCodexIdentityResponsePayload(payload []byte, from string, to string) []byte {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if len(payload) == 0 || from == "" || to == "" || from == to || !bytes.Contains(payload, []byte(from)) {
		return payload
	}
	return bytes.ReplaceAll(payload, []byte(from), []byte(to))
}

func codexIdentityConfuseEnabled(cfg *config.Config, auth *cliproxyauth.Auth, autoProviderScoped bool) bool {
	if autoProviderScoped && codexIdentityIsolationRequiredForResponsesHost(auth) {
		return true
	}
	if cfg == nil || !cfg.Codex.IdentityConfuse {
		return false
	}
	strategy := strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy))
	return cfg.Routing.SessionAffinity || strategy == "fill-first" || strategy == "fillfirst" || strategy == "ff"
}

func codexIdentityIsolationRequiredForResponsesHost(auth *cliproxyauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.Attributes == nil {
		return false
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		return false
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	switch strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".") {
	case "anyrouter.top", "agentrouter.org":
		return true
	default:
		return false
	}
}

func codexResponsesIdentityIsolationEligible(from sdktranslator.Format, rawURL string) bool {
	if sourceFormatEqual(from, sdktranslator.FromString(codexOpenAIImageSourceFormat)) {
		return false
	}
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(endpoint.Path, "/"), "/responses")
}

func codexIdentityConfuseUUID(authID string, kind string, value string) string {
	name := strings.Join([]string{"cli-proxy-api", "codex", "identity-confuse", kind, strings.TrimSpace(authID), strings.TrimSpace(value)}, ":")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

type codexIdentityConfuseState struct {
	enabled                bool
	authID                 string
	originalPromptCacheKey string
	promptCacheKey         string
	identityReplacements   []codexIdentityReplacement
}

type codexIdentityReplacement struct {
	original string
	confused string
}

// cacheHelperForAuth keeps the upstream cache-key synthesis and adds the fork's
// credential-scoped identity only after the unmodified client body is retained.
func (e *CodexExecutor) cacheHelperForAuth(ctx context.Context, from sdktranslator.Format, url string, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, userPayload, rawJSON []byte, headerSets ...http.Header) (*http.Request, []byte, codexIdentityConfuseState, error) {
	httpReq, body, err := e.cacheHelper(ctx, from, url, req, rawJSON, headerSets...)
	if err != nil {
		return nil, nil, codexIdentityConfuseState{}, err
	}
	body, state := applyCodexIdentityConfuseBody(e.cfg, auth, userPayload, body, codexResponsesIdentityIsolationEligible(from, url))
	httpReq.Body = io.NopCloser(bytes.NewReader(body))
	httpReq.ContentLength = int64(len(body))
	httpReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	applyCodexIdentityConfuseHeaders(httpReq.Header, &state)
	return httpReq, body, state, nil
}
