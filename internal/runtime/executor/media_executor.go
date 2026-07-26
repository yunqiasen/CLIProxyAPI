package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const defaultMediaPollInterval = 3 * time.Second

// MediaExecutor forwards image, video, and audio requests to one configured media provider.
type MediaExecutor struct {
	provider string
	cfg      *config.Config
}

// NewMediaExecutor creates an executor bound to a stable media provider key.
func NewMediaExecutor(provider string, cfg *config.Config) *MediaExecutor {
	return &MediaExecutor{provider: strings.ToLower(strings.TrimSpace(provider)), cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *MediaExecutor) Identifier() string {
	if e == nil {
		return ""
	}
	return e.provider
}

// Execute forwards one standard or custom media request.
func (e *MediaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	provider := e.resolveProvider(auth)
	if provider == nil {
		return resp, mediaRequestError{code: http.StatusBadRequest, msg: "media provider config not found"}
	}
	operation, requestedModel, errOperation := resolveMediaOperation(provider, req, opts)
	if errOperation != nil {
		return resp, errOperation
	}
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = strings.TrimSpace(opts.Query.Get("model"))
	}

	upstreamModel := strings.TrimSpace(operation.Model)
	if upstreamModel == "" {
		upstreamModel = resolveMediaModel(provider, requestedModel)
	}
	if operation.ModelMode == config.MediaModelNone {
		upstreamModel = ""
	}
	if operation.ModelMode == config.MediaModelRequired && upstreamModel == "" {
		return resp, mediaRequestError{code: http.StatusBadRequest, msg: fmt.Sprintf("model is required for media operation %s", operation.Name)}
	}
	if errCapability := validateMediaModelCapability(provider, operation, upstreamModel); errCapability != nil {
		return resp, errCapability
	}

	payload, contentType, errPayload := prepareMediaPayload(req.Payload, opts.Headers.Get("Content-Type"), operation, upstreamModel)
	if errPayload != nil {
		return resp, errPayload
	}
	query := prepareMediaQuery(opts.Query, operation, upstreamModel)
	baseURL, apiKey := mediaCredentials(auth, provider)
	if baseURL == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing media provider baseURL"}
	}
	upstreamURL := joinMediaURL(baseURL, operation.Path, query)

	reporter := helps.NewExecutorUsageReporter(ctx, e, upstreamModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)

	body, headers, errSubmit := e.executeHTTPRequest(ctx, httpClient, auth, apiKey, operation.Method, upstreamURL, contentType, payload)
	if errSubmit != nil {
		return resp, errSubmit
	}
	normalizeOperation := operation
	if operation.Async != nil {
		body, headers, err = e.pollAsyncOperation(ctx, httpClient, auth, apiKey, baseURL, operation, body)
		if err != nil {
			return resp, err
		}
		if resultPath := strings.TrimSpace(operation.Async.ResultPath); resultPath != "" {
			normalizeOperation.ResultPath = resultPath
		}
	}
	body, err = normalizeMediaResponse(normalizeOperation, body)
	if err != nil {
		return resp, err
	}
	if normalizeOperation.ResponseFormat == config.MediaResponseJSONURL || normalizeOperation.ResponseFormat == config.MediaResponseJSONBase64 {
		headers.Set("Content-Type", "application/json")
	}
	// Response normalization can change the payload size; never forward the
	// submit/poll Content-Length for the rewritten body.
	headers.Del("Content-Length")
	headers.Set("Content-Length", strconv.Itoa(len(body)))
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: body, Headers: headers}, nil
}

func resolveMediaOperation(provider *config.MediaProvider, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (config.MediaOperation, string, error) {
	customOperation := mediaMetadataString(opts.Metadata, cliproxyexecutor.MediaOperationMetadataKey)
	if customOperation != "" {
		requestedModel := mediaMetadataString(opts.Metadata, cliproxyexecutor.MediaModelMetadataKey)
		if requestedModel == "" {
			requestedModel = req.Model
		}
		normalizedRequestedOperation := normalizeMediaOperationKey(customOperation)
		// Prefer an explicit operation name. This keeps a custom operation named
		// like a capability from being shadowed by another operation.
		for i := range provider.Operations {
			operation := provider.Operations[i]
			if normalizeMediaOperationKey(operation.Name) == normalizedRequestedOperation {
				return operation, requestedModel, nil
			}
		}
		for i := range provider.Operations {
			operation := provider.Operations[i]
			if normalizeMediaOperationKey(operation.Capability) == normalizedRequestedOperation {
				return operation, requestedModel, nil
			}
		}
		return config.MediaOperation{}, "", mediaRequestError{code: http.StatusNotFound, msg: fmt.Sprintf("media operation %s is not configured", customOperation)}
	}

	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return config.MediaOperation{}, "", mediaRequestError{code: http.StatusBadRequest, msg: fmt.Sprintf("unsupported media source format %q", opts.SourceFormat.String())}
	}
	requestPath := helps.PayloadRequestPath(opts)
	capability := config.MediaCapabilityGenerate
	endpointPath := openAICompatImagesGenerationsPath
	requestFormat := config.MediaRequestJSON
	if strings.HasSuffix(requestPath, "/images/edits") {
		capability = config.MediaCapabilityEdit
		endpointPath = openAICompatImagesEditsPath
		requestFormat = config.MediaRequestMultipart
		if json.Valid(req.Payload) {
			requestFormat = config.MediaRequestJSON
		}
	} else if requestPath != "" && !strings.HasSuffix(requestPath, "/images/generations") {
		return config.MediaOperation{}, "", mediaRequestError{code: http.StatusBadRequest, msg: fmt.Sprintf("unsupported image request path %q", requestPath)}
	}
	for i := range provider.Operations {
		operation := provider.Operations[i]
		if normalizeMediaOperationKey(operation.Name) == capability {
			return operation, req.Model, nil
		}
	}
	for i := range provider.Operations {
		operation := provider.Operations[i]
		if normalizeMediaOperationKey(operation.Capability) == capability {
			return operation, req.Model, nil
		}
	}
	return config.MediaOperation{
		Name: capability, Capability: capability, Method: http.MethodPost, Path: endpointPath,
		RequestFormat: requestFormat, ModelMode: config.MediaModelRequired, ResponseFormat: config.MediaResponsePassthrough,
	}, req.Model, nil
}

func normalizeMediaOperationKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(value, "_", "-")
}

func mediaOperationCapability(operation config.MediaOperation) string {
	return config.MediaOperationCapability(operation)
}

func validateMediaModelCapability(provider *config.MediaProvider, operation config.MediaOperation, modelName string) error {
	capability := mediaOperationCapability(operation)
	modelName = strings.TrimSpace(modelName)
	if provider == nil || capability == "" || modelName == "" || operation.ModelMode == config.MediaModelNone {
		return nil
	}
	for i := range provider.Models {
		model := provider.Models[i]
		if !strings.EqualFold(strings.TrimSpace(model.Name), modelName) && !strings.EqualFold(strings.TrimSpace(model.Alias), modelName) {
			continue
		}
		// An empty capability list means the provider has not declared a
		// restriction for this model; retain legacy alias-only behavior.
		if len(model.Capabilities) == 0 || config.HasMediaCapability(model, capability) {
			return nil
		}
		return mediaModelSupportError{code: http.StatusBadRequest, msg: fmt.Sprintf("model is not supported for media capability %s: %s", capability, modelName)}
	}
	return nil
}

func prepareMediaPayload(payload []byte, contentType string, operation config.MediaOperation, model string) ([]byte, string, error) {
	method := strings.ToUpper(strings.TrimSpace(operation.Method))
	noBodyMethod := method == http.MethodGet || method == http.MethodHead
	switch operation.RequestFormat {
	case config.MediaRequestJSON:
		if len(bytes.TrimSpace(payload)) == 0 && noBodyMethod {
			return nil, "", nil
		}
		if !json.Valid(payload) {
			return nil, "", mediaRequestError{code: http.StatusBadRequest, msg: "media request body must be valid JSON"}
		}
		if operation.ModelMode == config.MediaModelNone {
			payload, _ = sjson.DeleteBytes(payload, "model")
		} else if model != "" {
			payload = helps.SetStringIfDifferent(payload, "model", model)
		}
		return payload, "application/json", nil
	case config.MediaRequestMultipart:
		if len(payload) == 0 && noBodyMethod {
			return nil, "", nil
		}
		mediaType, params, errParse := mime.ParseMediaType(strings.TrimSpace(contentType))
		if errParse != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") || strings.TrimSpace(params["boundary"]) == "" {
			return nil, "", mediaRequestError{code: http.StatusBadRequest, msg: "media request must be multipart with a boundary"}
		}
		if operation.ModelMode == config.MediaModelNone {
			model = ""
		}
		out, rewrittenType, errRewrite := rewriteOpenAICompatImagesMultipartPayload(payload, model, params["boundary"], false)
		if errRewrite != nil {
			return nil, "", mediaRequestError{code: http.StatusBadRequest, msg: errRewrite.Error()}
		}
		return out, rewrittenType, nil
	case config.MediaRequestBinary:
		if strings.TrimSpace(contentType) == "" {
			contentType = "application/octet-stream"
		}
		return payload, contentType, nil
	default:
		return nil, "", mediaRequestError{code: http.StatusBadRequest, msg: fmt.Sprintf("unsupported media request format %q", operation.RequestFormat)}
	}
}

func prepareMediaQuery(input url.Values, operation config.MediaOperation, model string) url.Values {
	query := cloneMediaQuery(input)
	removeQueryKey := func(name string) {
		for key := range query {
			if strings.EqualFold(key, name) {
				delete(query, key)
			}
		}
	}
	if operation.ModelMode == config.MediaModelNone {
		removeQueryKey("model")
		return query
	}
	_, hasModelQuery := query["model"]
	if !hasModelQuery {
		for key := range query {
			if strings.EqualFold(key, "model") {
				hasModelQuery = true
				break
			}
		}
	}
	method := strings.ToUpper(strings.TrimSpace(operation.Method))
	if hasModelQuery || method == http.MethodGet || method == http.MethodHead || operation.RequestFormat == config.MediaRequestBinary {
		removeQueryKey("model")
		if strings.TrimSpace(model) != "" {
			query.Set("model", strings.TrimSpace(model))
		}
	}
	return query
}

func cloneMediaQuery(input url.Values) url.Values {
	query := make(url.Values, len(input))
	for key, values := range input {
		query[key] = append([]string(nil), values...)
	}
	return query
}

func (e *MediaExecutor) executeHTTPRequest(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, apiKey, method, upstreamURL, contentType string, payload []byte) ([]byte, http.Header, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodPost
	}
	httpReq, errRequest := http.NewRequestWithContext(ctx, method, upstreamURL, bytes.NewReader(payload))
	if errRequest != nil {
		return nil, nil, errRequest
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-media")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	recordMediaAPIRequest(ctx, e.cfg, httpReq, payload, e.Identifier(), auth)

	httpResp, errDo := client.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, nil, errDo
	}
	headers := httpResp.Header.Clone()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, headers)
	body, errRead := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("media executor: close response body error: %v", errClose)
		if errRead == nil {
			errRead = errClose
		}
	}
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return nil, headers, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, headers, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}
	return body, headers, nil
}

func (e *MediaExecutor) pollAsyncOperation(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, apiKey, baseURL string, operation config.MediaOperation, submitBody []byte) ([]byte, http.Header, error) {
	async := operation.Async
	if async == nil {
		return submitBody, nil, nil
	}
	taskID := strings.TrimSpace(gjson.GetBytes(submitBody, async.TaskIDPath).String())
	if taskID == "" {
		return nil, nil, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("media async response is missing task ID at %s", async.TaskIDPath)}
	}
	interval := defaultMediaPollInterval
	if parsed, errParse := time.ParseDuration(strings.TrimSpace(async.PollInterval)); errParse == nil && parsed > 0 {
		interval = parsed
	}
	pollPath := strings.ReplaceAll(async.PollPath, "{task_id}", url.PathEscape(taskID))
	pollURL := joinMediaURL(baseURL, pollPath, nil)
	for {
		if errContext := ctx.Err(); errContext != nil {
			return nil, nil, errContext
		}
		body, headers, errPoll := e.executeHTTPRequest(ctx, client, auth, apiKey, async.PollMethod, pollURL, "", nil)
		if errPoll != nil {
			return nil, headers, errPoll
		}
		status := strings.TrimSpace(gjson.GetBytes(body, async.StatusPath).String())
		if mediaValueMatches(status, async.SuccessValues) {
			return body, headers, nil
		}
		if mediaValueMatches(status, async.FailureValues) {
			return nil, headers, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("media async operation %s failed with status %s", operation.Name, status)}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func normalizeMediaResponse(operation config.MediaOperation, body []byte) ([]byte, error) {
	switch operation.ResponseFormat {
	case "", config.MediaResponsePassthrough, config.MediaResponseBinary:
		return body, nil
	case config.MediaResponseJSONURL, config.MediaResponseJSONBase64:
		if !json.Valid(body) {
			return nil, statusErr{code: http.StatusBadGateway, msg: "media upstream returned invalid JSON"}
		}
		result := gjson.GetBytes(body, strings.TrimSpace(operation.ResultPath))
		values := mediaResultStrings(result)
		if len(values) == 0 {
			return nil, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("media response is missing result at %s", operation.ResultPath)}
		}
		field := "url"
		if operation.ResponseFormat == config.MediaResponseJSONBase64 {
			field = "b64_json"
		}
		type item struct {
			URL     string `json:"url,omitempty"`
			B64JSON string `json:"b64_json,omitempty"`
		}
		type response struct {
			Data []item `json:"data"`
		}
		out := response{Data: make([]item, 0, len(values))}
		for _, value := range values {
			entry := item{}
			if field == "url" {
				entry.URL = value
			} else {
				entry.B64JSON = value
			}
			out.Data = append(out.Data, entry)
		}
		encoded, errMarshal := json.Marshal(out)
		if errMarshal != nil {
			return nil, errMarshal
		}
		return encoded, nil
	default:
		return nil, mediaRequestError{code: http.StatusBadRequest, msg: fmt.Sprintf("unsupported media response format %q", operation.ResponseFormat)}
	}
}

func mediaResultStrings(result gjson.Result) []string {
	if !result.Exists() {
		return nil
	}
	if result.IsArray() {
		values := make([]string, 0)
		for _, item := range result.Array() {
			if value := strings.TrimSpace(item.String()); value != "" {
				values = append(values, value)
			}
		}
		return values
	}
	if value := strings.TrimSpace(result.String()); value != "" {
		return []string{value}
	}
	return nil
}

func mediaValueMatches(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func mediaMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func joinMediaURL(baseURL, operationPath string, query url.Values) string {
	baseText := strings.TrimSpace(baseURL)
	pathText := strings.TrimSpace(operationPath)
	base, errBase := url.Parse(baseText)
	operation, errOperation := url.Parse(pathText)
	if errBase != nil || base == nil || base.Scheme == "" || base.Host == "" || errOperation != nil || operation == nil {
		joined := strings.TrimRight(baseText, "/") + "/" + strings.TrimLeft(pathText, "/")
		if len(query) > 0 {
			joined += "?" + query.Encode()
		}
		return joined
	}

	base.Path = joinMediaPath(base.Path, operation.Path)
	base.RawPath = ""
	baseQuery := base.Query()
	for key, values := range operation.Query() {
		baseQuery[key] = append([]string(nil), values...)
	}
	for key, values := range query {
		baseQuery[key] = append([]string(nil), values...)
	}
	base.RawQuery = baseQuery.Encode()
	base.Fragment = ""
	return base.String()
}

func joinMediaPath(basePath, operationPath string) string {
	baseParts := splitMediaPath(basePath)
	operationParts := splitMediaPath(operationPath)
	if len(baseParts) == 0 {
		return "/" + strings.Join(operationParts, "/")
	}
	if len(operationParts) == 0 {
		return "/" + strings.Join(baseParts, "/")
	}
	overlap := 0
	maxOverlap := len(baseParts)
	if len(operationParts) < maxOverlap {
		maxOverlap = len(operationParts)
	}
	for size := maxOverlap; size > 0; size-- {
		matched := true
		for index := 0; index < size; index++ {
			if baseParts[len(baseParts)-size+index] != operationParts[index] {
				matched = false
				break
			}
		}
		if matched {
			overlap = size
			break
		}
	}
	return "/" + strings.Join(append(baseParts, operationParts[overlap:]...), "/")
}

func splitMediaPath(value string) []string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (e *MediaExecutor) resolveProvider(auth *cliproxyauth.Auth) *config.MediaProvider {
	if e == nil || e.cfg == nil {
		return nil
	}
	providerKey := e.provider
	providerName := ""
	mediaKind := ""
	if auth != nil {
		if providerKey == "" {
			providerKey = strings.ToLower(strings.TrimSpace(auth.Provider))
		}
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes["provider_key"]); value != "" {
				providerKey = strings.ToLower(value)
			}
			providerName = strings.TrimSpace(auth.Attributes["media_provider_name"])
			mediaKind = strings.ToLower(strings.TrimSpace(auth.Attributes["media_kind"]))
		}
	}
	for i := range e.cfg.MediaProviders {
		provider := &e.cfg.MediaProviders[i]
		if provider.Disabled {
			continue
		}
		key := util.MediaProviderKey(provider.Kind, provider.Name)
		if providerKey != "" && strings.EqualFold(key, providerKey) {
			return provider
		}
		if providerName != "" && strings.EqualFold(strings.TrimSpace(provider.Name), providerName) && (mediaKind == "" || strings.EqualFold(strings.TrimSpace(provider.Kind), mediaKind)) {
			return provider
		}
	}
	return nil
}

func resolveMediaModel(provider *config.MediaProvider, requested string) string {
	requested = thinking.ParseSuffix(strings.TrimSpace(requested)).ModelName
	if provider == nil || requested == "" {
		return requested
	}
	for i := range provider.Models {
		model := provider.Models[i]
		if strings.EqualFold(strings.TrimSpace(model.Alias), requested) || strings.EqualFold(strings.TrimSpace(model.Name), requested) {
			if name := strings.TrimSpace(model.Name); name != "" {
				return name
			}
		}
	}
	return requested
}

func mediaCredentials(auth *cliproxyauth.Auth, provider *config.MediaProvider) (baseURL, apiKey string) {
	if provider != nil {
		baseURL = strings.TrimSpace(provider.BaseURL)
	}
	if auth == nil || auth.Attributes == nil {
		return baseURL, ""
	}
	if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
		baseURL = value
	}
	apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	return baseURL, apiKey
}

func recordMediaAPIRequest(ctx context.Context, cfg *config.Config, req *http.Request, body []byte, provider string, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL: req.URL.String(), Method: req.Method, Headers: req.Header.Clone(), Body: body,
		Provider: provider, ProviderName: helps.RequestLogProviderName(auth), AuthID: authID,
		AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
	})
}

// ExecuteStream executes the media request and emits its response as one raw chunk.
func (e *MediaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	opts.Stream = false
	resp, err := e.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: resp.Headers, Chunks: chunks}, nil
}

// Refresh is a no-op for API-key media providers.
func (e *MediaExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// CountTokens is not applicable to media operations.
func (e *MediaExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, mediaRequestError{code: http.StatusNotImplemented, msg: "media executor does not support token counting"}
}

// HttpRequest applies the selected media credential to an arbitrary request.
func (e *MediaExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("media executor: request is nil")
	}
	provider := e.resolveProvider(auth)
	_, apiKey := mediaCredentials(auth, provider)
	httpReq := req.WithContext(ctx)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

type mediaRequestError struct {
	code int
	msg  string
}

func (e mediaRequestError) Error() string         { return e.msg }
func (e mediaRequestError) StatusCode() int       { return e.code }
func (e mediaRequestError) IsRequestScoped() bool { return true }

// mediaModelSupportError means the selected credential cannot serve the
// requested capability. The auth manager should try another credential or
// provider instead of treating the client request as malformed.
type mediaModelSupportError struct {
	code int
	msg  string
}

func (e mediaModelSupportError) Error() string   { return e.msg }
func (e mediaModelSupportError) StatusCode() int { return e.code }
