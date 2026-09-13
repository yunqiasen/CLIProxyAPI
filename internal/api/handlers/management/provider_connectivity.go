package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	apiHandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	openaiHandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type providerConnectivityClaudeCloak struct {
	Mode           string   `json:"mode"`
	StrictMode     bool     `json:"strict_mode"`
	SensitiveWords []string `json:"sensitive_words"`
	CacheUserID    bool     `json:"cache_user_id"`
}

type providerConnectivityTestRequest struct {
	CodexConfig             json.RawMessage                  `json:"codex_config"`
	Provider                string                           `json:"provider"`
	AuthIndex               string                           `json:"auth_index"`
	Model                   string                           `json:"model"`
	APIKey                  *string                          `json:"api_key"`
	BaseURL                 *string                          `json:"base_url"`
	ProxyURL                *string                          `json:"proxy_url"`
	Header                  map[string]string                `json:"header"`
	Cloak                   *providerConnectivityClaudeCloak `json:"cloak"`
	RebuildMidSystemMessage *bool                            `json:"rebuild_mid_system_message"`
	DisableImageGeneration  *bool                            `json:"disable_image_generation"`
}

type providerConnectivityTestResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
}

// ProviderConnectivityTest runs a provider probe through the production executor
// while pinning the exact credential selected in the management panel.
func (h *Handler) ProviderConnectivityTest(c *gin.Context) {
	var body providerConnectivityTestRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	response, status, errTest := h.performProviderConnectivityTest(c.Request.Context(), body)
	if errTest != nil {
		c.JSON(status, gin.H{"error": errTest.Error()})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) performProviderConnectivityTest(ctx context.Context, body providerConnectivityTestRequest) (providerConnectivityTestResponse, int, error) {
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	requestedModel := strings.TrimSpace(body.Model)
	if requestedModel == "" {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("missing model")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	switch provider {
	case "claude":
		return h.performClaudeConnectivityTest(ctx, body, requestedModel)
	case "codex":
		return h.performCodexConnectivityTest(ctx, body, requestedModel)
	default:
		return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("unsupported provider")
	}
}

func (h *Handler) performClaudeConnectivityTest(ctx context.Context, body providerConnectivityTestRequest, requestedModel string) (providerConnectivityTestResponse, int, error) {
	auth, cfg, errAuth := h.claudeConnectivityAuth(body)
	if errAuth != nil {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, errAuth
	}
	model := resolveClaudeConnectivityModel(cfg, auth, requestedModel)

	payload, errMarshal := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 8,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "Hi",
		}},
	})
	if errMarshal != nil {
		return providerConnectivityTestResponse{}, http.StatusInternalServerError, fmt.Errorf("build probe payload: %w", errMarshal)
	}

	headers := connectivityHeaders(body.Header)
	executor := runtimeexecutor.NewClaudeExecutor(cfg)
	response, errExecute := executor.Execute(ctx, auth, coreexecutor.Request{
		Model:   model,
		Payload: payload,
	}, coreexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		Headers:         headers,
		Metadata: map[string]any{
			coreexecutor.RequestedModelMetadataKey: requestedModel,
		},
	})
	if errExecute != nil {
		return providerConnectivityErrorResponse(errExecute), http.StatusOK, nil
	}
	return providerConnectivityResponse(response), http.StatusOK, nil
}

func (h *Handler) performCodexConnectivityTest(ctx context.Context, body providerConnectivityTestRequest, requestedModel string) (providerConnectivityTestResponse, int, error) {
	auth, cfg, errAuth := h.codexConnectivityAuth(body)
	if errAuth != nil {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, errAuth
	}
	model := resolveCodexConnectivityModel(cfg, auth, requestedModel)
	recovery := h.codexRecoverySnapshot(body, cfg)

	probeSessionID := uuid.NewString()
	probeThreadID := uuid.NewString()
	probeTurnID := uuid.NewString()
	probeInstallationID := uuid.NewString()
	payload, errMarshal := json.Marshal(map[string]any{
		"instructions":        "You are a test assistant.",
		"model":               model,
		"include":             []string{"reasoning.encrypted_content"},
		"reasoning":           map[string]any{"effort": "low", "summary": "auto"},
		"text":                map[string]any{"verbosity": "low"},
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
		"stream":              true,
		"store":               false,
		"max_output_tokens":   256,
		"prompt_cache_key":    probeSessionID,
		"client_metadata": map[string]string{
			"session_id":              probeSessionID,
			"thread_id":               probeThreadID,
			"turn_id":                 probeTurnID,
			"x-codex-installation-id": probeInstallationID,
			"x-codex-window-id":       probeSessionID + ":0",
		},
		"input": []map[string]any{{
			"type": "message",
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": "Hi",
			}},
		}},
		"tools": []map[string]any{
			{"type": "image_generation"},
			{
				"type":        "function",
				"name":        "image_gen.imagegen",
				"description": "Generate an image",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
	})
	if errMarshal != nil {
		return providerConnectivityTestResponse{}, http.StatusInternalServerError, fmt.Errorf("build probe payload: %w", errMarshal)
	}

	headers := connectivityHeaders(body.Header)
	executor := runtimeexecutor.NewCodexAutoExecutor(cfg)
	request := coreauth.WithConfiguredAPIKeyModelInfo(cfg, auth, coreexecutor.Request{
		Model:   model,
		Payload: payload,
		Metadata: map[string]any{
			coreexecutor.DerivedSessionIDMetadataKey: probeSessionID,
		},
	}, requestedModel)
	stream, errExecute := executor.ExecuteStream(ctx, auth, request, coreexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Headers:         headers,
		Stream:          true,
		Metadata: map[string]any{
			coreexecutor.RequestedModelMetadataKey: requestedModel,
			coreexecutor.RequestPathMetadataKey:    "/v1/responses",
		},
	})
	if errExecute != nil {
		return providerConnectivityErrorResponse(errExecute), http.StatusOK, nil
	}
	response, errCollect := collectProviderConnectivityStream(ctx, stream)
	if errCollect != nil {
		return providerConnectivityErrorResponse(errCollect), http.StatusOK, nil
	}
	h.recoverCodexProbe(ctx, body, cfg, recovery, model, response.Payload)
	return providerConnectivityResponse(response), http.StatusOK, nil
}

func connectivityHeaders(values map[string]string) http.Header {
	headers := make(http.Header, len(values))
	for key, value := range values {
		if key = strings.TrimSpace(key); key != "" {
			headers.Set(key, value)
		}
	}
	return headers
}

func collectProviderConnectivityStream(ctx context.Context, stream *coreexecutor.StreamResult) (coreexecutor.Response, error) {
	if stream == nil {
		return coreexecutor.Response{}, fmt.Errorf("connectivity probe returned no stream")
	}
	response := coreexecutor.Response{Headers: stream.Headers}
	var payload bytes.Buffer
	framer := openaiHandlers.NewResponsesStreamFramer()
	for {
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		case chunk, ok := <-stream.Chunks:
			if !ok {
				framer.Flush(&payload)
				response.Payload = payload.Bytes()
				return response, nil
			}
			if chunk.Err != nil {
				return response, chunk.Err
			}
			framer.WriteChunk(&payload, chunk.Payload)
		}
	}
}

func providerConnectivityResponse(response coreexecutor.Response) providerConnectivityTestResponse {
	return providerConnectivityTestResponse{
		StatusCode: http.StatusOK,
		Header:     headerValues(response.Headers),
		Body:       string(response.Payload),
	}
}

func (h *Handler) claudeConnectivityAuth(body providerConnectivityTestRequest) (*coreauth.Auth, *config.Config, error) {
	var auth *coreauth.Auth
	if authIndex := strings.TrimSpace(body.AuthIndex); authIndex != "" {
		auth = h.authByIndex(authIndex)
		if auth == nil && body.APIKey == nil {
			return nil, nil, fmt.Errorf("auth not found")
		}
		if auth != nil && !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			return nil, nil, fmt.Errorf("auth provider mismatch")
		}
	}
	if auth == nil {
		auth = &coreauth.Auth{
			ID:         "management:claude-connectivity-test",
			Provider:   "claude",
			Attributes: map[string]string{},
		}
	} else {
		auth = auth.Clone()
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
	}

	cfg := &config.Config{}
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			cfg = h.cfg.CloneForRuntime()
		}
		h.mu.Unlock()
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	testKey := claudeConfigForConnectivity(cfg, auth)
	if body.APIKey != nil {
		apiKey := strings.TrimSpace(*body.APIKey)
		if apiKey == "" {
			delete(auth.Attributes, "api_key")
		} else {
			auth.Attributes["api_key"] = apiKey
		}
	}
	if strings.TrimSpace(auth.Attributes["api_key"]) == "" {
		return nil, nil, fmt.Errorf("api key required")
	}
	if body.BaseURL != nil {
		baseURL := strings.TrimSpace(*body.BaseURL)
		if baseURL == "" {
			delete(auth.Attributes, "base_url")
		} else {
			auth.Attributes["base_url"] = baseURL
		}
	}
	if body.ProxyURL != nil {
		auth.ProxyURL = strings.TrimSpace(*body.ProxyURL)
	}
	if body.Header != nil {
		removeConnectivityHeaderAttrs(auth.Attributes)
		for key, value := range body.Header {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key != "" && value != "" && !isConnectivityCredentialHeader(key) {
				auth.Attributes["header:"+key] = value
			}
		}
	}

	testKey.APIKey = strings.TrimSpace(auth.Attributes["api_key"])
	testKey.APIKeyEntries = nil
	testKey.BaseURL = strings.TrimSpace(auth.Attributes["base_url"])
	testKey.ProxyURL = auth.ProxyURL
	if body.Header != nil {
		testKey.Headers = cloneConnectivityHeaders(body.Header)
	}
	if body.Cloak != nil {
		cacheUserID := body.Cloak.CacheUserID
		testKey.Cloak = &config.CloakConfig{
			Mode:           strings.TrimSpace(body.Cloak.Mode),
			StrictMode:     body.Cloak.StrictMode,
			SensitiveWords: append([]string(nil), body.Cloak.SensitiveWords...),
			CacheUserID:    &cacheUserID,
		}
	}
	if body.RebuildMidSystemMessage != nil {
		testKey.RebuildMidSystemMessage = *body.RebuildMidSystemMessage
		if *body.RebuildMidSystemMessage {
			auth.Attributes["rebuild_mid_system_message"] = "true"
		} else {
			delete(auth.Attributes, "rebuild_mid_system_message")
		}
	}
	cfg.ClaudeKey = []config.ClaudeKey{testKey}
	return auth, cfg, nil
}

func (h *Handler) codexConnectivityAuth(body providerConnectivityTestRequest) (*coreauth.Auth, *config.Config, error) {
	var auth *coreauth.Auth
	if authIndex := strings.TrimSpace(body.AuthIndex); authIndex != "" {
		auth = h.authByIndex(authIndex)
		if auth == nil && body.APIKey == nil {
			return nil, nil, fmt.Errorf("auth not found")
		}
		if auth != nil && !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			return nil, nil, fmt.Errorf("auth provider mismatch")
		}
	}
	if auth == nil {
		auth = &coreauth.Auth{
			ID:         "management:codex-connectivity-test",
			Provider:   "codex",
			Attributes: map[string]string{},
		}
	} else {
		auth = auth.Clone()
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
	}

	cfg := &config.Config{}
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			cfg = h.cfg.CloneForRuntime()
		}
		h.mu.Unlock()
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	testKey := codexConfigForConnectivity(cfg, auth)
	// Runtime-only header credentials remain valid when no draft replaces them.
	for name, value := range auth.Attributes {
		if strings.HasPrefix(strings.ToLower(name), "header:") {
			name = strings.TrimSpace(name[len("header:"):])
			if name != "" {
				if testKey.Headers == nil {
					testKey.Headers = make(map[string]string)
				}
				testKey.Headers[name] = value
			}
		}
	}
	// Runtime values include selected-entry proxy overrides; draft fields win next.
	testKey.APIKey = strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
	testKey.BaseURL = strings.TrimSpace(auth.Attributes["base_url"])
	testKey.ProxyURL = auth.ProxyURL
	if len(body.CodexConfig) > 0 {
		// Decode over a field map so object fields replace, rather than merge maps.
		saved, _ := json.Marshal(testKey)
		var fields, draft map[string]json.RawMessage
		_ = json.Unmarshal(saved, &fields)
		if err := json.Unmarshal(body.CodexConfig, &draft); err != nil || draft == nil {
			return nil, nil, fmt.Errorf("invalid codex_config object")
		}
		for key, value := range draft {
			fields[key] = value
		}
		merged, _ := json.Marshal(fields)
		testKey = config.CodexKey{}
		if err := json.Unmarshal(merged, &testKey); err != nil {
			return nil, nil, fmt.Errorf("invalid codex_config: %w", err)
		}
	}
	// A provider draft never selects credentials; the selected row alone owns them.
	testKey.APIKey = strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
	testKey.APIKeyEntries = nil
	if body.APIKey != nil {
		testKey.APIKey = strings.TrimSpace(*body.APIKey)
	}
	if body.BaseURL != nil {
		testKey.BaseURL = strings.TrimSpace(*body.BaseURL)
	}
	if body.ProxyURL != nil {
		testKey.ProxyURL = strings.TrimSpace(*body.ProxyURL)
	}
	if body.Header != nil {
		testKey.Headers = body.Header
	}
	if body.DisableImageGeneration != nil {
		testKey.DisableImageGeneration = *body.DisableImageGeneration
	}
	headers := cloneConnectivityHeaders(testKey.Headers)
	if headers == nil {
		headers = make(map[string]string)
	}
	if testKey.APIKey == "" {
		for key, value := range testKey.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "Authorization") && isUsableConnectivityAuthorization(value) {
				headers["Authorization"] = strings.TrimSpace(value)
			}
		}
		if headers["Authorization"] == "" {
			return nil, nil, fmt.Errorf("api key required")
		}
	}
	testKey.Headers = headers
	cfg.CodexKey = []config.CodexKey{testKey}
	synthesisConfig := &config.Config{CodexKey: cfg.CodexKey}
	if err := synthesisConfig.ValidateCredentialWeights(); err != nil {
		return nil, nil, fmt.Errorf("invalid probe config: %w", err)
	}
	generated := synthesizer.SynthesizeCodexAuth(&synthesizer.SynthesisContext{
		Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
	}, testKey, config.EffectiveNativeAPIKey{
		APIKey: testKey.APIKey, ProxyURL: testKey.ProxyURL, Priority: testKey.Priority, Index: -1,
	}, 0)
	if !strings.HasPrefix(auth.ID, "management:") && body.APIKey == nil {
		generated.ID = auth.ID
	}
	return generated, cfg, nil
}

func resolveCodexConnectivityModel(cfg *config.Config, auth *coreauth.Auth, requestedModel string) string {
	return coreauth.ResolveConfiguredAPIKeyModel(cfg, auth, requestedModel)
}

func codexConfigForConnectivity(cfg *config.Config, auth *coreauth.Auth) config.CodexKey {
	if cfg == nil || auth == nil {
		return config.CodexKey{}
	}
	if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex])); errIndex == nil && index >= 0 && index < len(cfg.CodexKey) {
		return cfg.CodexKey[index]
	}
	apiKey := strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if entry, _ := config.ResolveNativeAPIKeyConfig(cfg.CodexKey, apiKey, baseURL); entry != nil {
		return *entry
	}
	return config.CodexKey{}
}

func resolveClaudeConnectivityModel(cfg *config.Config, auth *coreauth.Auth, requestedModel string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	entry := claudeConfigForConnectivity(cfg, auth)
	for i := range entry.Models {
		alias := strings.TrimSpace(entry.Models[i].Alias)
		name := strings.TrimSpace(entry.Models[i].Name)
		if alias != "" && strings.EqualFold(alias, requestedModel) {
			if name != "" {
				return name
			}
			return alias
		}
		if name != "" && strings.EqualFold(name, requestedModel) {
			return name
		}
	}
	return requestedModel
}

func claudeConfigForConnectivity(cfg *config.Config, auth *coreauth.Auth) config.ClaudeKey {
	if cfg == nil || auth == nil {
		return config.ClaudeKey{}
	}
	if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes["config_index"])); errIndex == nil && index >= 0 && index < len(cfg.ClaudeKey) {
		return cfg.ClaudeKey[index]
	}
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if entry, _ := config.ResolveNativeAPIKeyConfig(cfg.ClaudeKey, apiKey, baseURL); entry != nil {
		return *entry
	}
	return config.ClaudeKey{}
}

func removeConnectivityHeaderAttrs(attrs map[string]string) {
	for key := range attrs {
		if strings.HasPrefix(strings.ToLower(key), "header:") {
			delete(attrs, key)
		}
	}
}

func isConnectivityCredentialHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "x-api-key":
		return true
	default:
		return false
	}
}

func isUsableConnectivityAuthorization(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.Contains(value, "$TOKEN$")
}

func cloneConnectivityHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		if key = strings.TrimSpace(key); key != "" && !isConnectivityCredentialHeader(key) {
			cloned[key] = value
		}
	}
	return cloned
}

func providerConnectivityErrorResponse(err error) providerConnectivityTestResponse {
	status := clienterror.HTTPStatusFromErrorOr(err, http.StatusBadGateway)
	headers := make(http.Header)
	body := []byte(nil)
	var terminated *coreexecutor.RequestTerminatedError
	if errors.As(err, &terminated) && terminated != nil {
		status = terminated.StatusCode()
		headers = terminated.ResponseHeaders()
		body = terminated.ResponseBody()
	}
	if len(body) == 0 {
		message := strings.TrimSpace(err.Error())
		if json.Valid([]byte(message)) {
			body = []byte(message)
		} else {
			body = apiHandlers.BuildErrorResponseBody(status, message)
		}
	}
	return providerConnectivityTestResponse{
		StatusCode: status,
		Header:     headerValues(headers),
		Body:       string(body),
	}
}

func headerValues(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(headers))
	for key, values := range headers {
		out[key] = append([]string(nil), values...)
	}
	return out
}
