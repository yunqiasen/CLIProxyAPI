package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

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

// Execute forwards a non-streaming standard media request.
func (e *MediaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	provider := e.resolveProvider(auth)
	if provider == nil {
		return resp, fmt.Errorf("media executor: provider config not found")
	}

	endpointPath, errPath := standardMediaEndpointPath(opts)
	if errPath != nil {
		return resp, errPath
	}
	upstreamModel := resolveMediaModel(provider, req.Model)
	payload, contentType, errPayload := prepareOpenAICompatImagesPayload(req.Payload, upstreamModel, opts.Headers.Get("Content-Type"), false)
	if errPayload != nil {
		return resp, errPayload
	}
	if contentType == "" {
		contentType = "application/json"
	}

	baseURL, apiKey := mediaCredentials(auth, provider)
	if baseURL == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing media provider baseURL"}
	}
	upstreamURL := strings.TrimRight(baseURL, "/") + endpointPath
	if len(opts.Query) > 0 {
		upstreamURL += "?" + opts.Query.Encode()
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, upstreamModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(payload))
	if errRequest != nil {
		return resp, errRequest
	}
	httpReq.Header.Set("Content-Type", contentType)
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("media executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}, nil
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

func standardMediaEndpointPath(opts cliproxyexecutor.Options) (string, error) {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return "", fmt.Errorf("media executor: unsupported source format %q", opts.SourceFormat.String())
	}
	requestPath := helps.PayloadRequestPath(opts)
	switch {
	case strings.HasSuffix(requestPath, "/images/edits"):
		return openAICompatImagesEditsPath, nil
	case strings.HasSuffix(requestPath, "/images/generations"), strings.TrimSpace(requestPath) == "":
		return openAICompatImagesGenerationsPath, nil
	default:
		return "", fmt.Errorf("media executor: unsupported image request path %q", requestPath)
	}
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
		URL:          req.URL.String(),
		Method:       req.Method,
		Headers:      req.Header.Clone(),
		Body:         body,
		Provider:     provider,
		ProviderName: helps.RequestLogProviderName(auth),
		AuthID:       authID,
		AuthLabel:    authLabel,
		AuthType:     authType,
		AuthValue:    authValue,
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
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "media executor does not support token counting"}
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
