package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/mediarouting"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type mediaConnectivityRequest struct {
	Data        string `json:"data"`
	DataBase64  string `json:"data_base64"`
	ContentType string `json:"content_type"`
}

func (h *Handler) performMediaConnectivityTest(ctx context.Context, body providerConnectivityTestRequest) (providerConnectivityTestResponse, int, error) {
	selected, cfg, err := h.mediaConnectivityAuth(body)
	if err != nil {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, err
	}
	provider := &cfg.MediaProviders[0]
	operationName := mediarouting.NormalizeOperation(body.MediaOperation)
	standardImage := provider.Kind == config.MediaKindImage && (operationName == config.MediaCapabilityGenerate || operationName == config.MediaCapabilityEdit)
	operation, ok := mediarouting.FindOperation(provider, operationName)
	if (!ok && !standardImage) || (ok && !mediarouting.AcceptsModel(provider, operation, body.Model)) {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("media operation or model is not configured")
	}
	if body.MediaRequest == nil {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("missing media_request")
	}
	payload := []byte(body.MediaRequest.Data)
	if body.MediaRequest.DataBase64 != "" {
		if len(payload) > 0 {
			return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("send only one media request body")
		}
		payload, err = base64.StdEncoding.Strict().DecodeString(body.MediaRequest.DataBase64)
		if err != nil {
			return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("invalid media request base64")
		}
	}
	if len(payload) > 128<<20 {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("media request exceeds size limit")
	}
	opts := ex.Options{
		SourceFormat: tr.FromString("media"), ResponseFormat: tr.FromString("media"),
		OriginalRequest: payload, Headers: http.Header{"Content-Type": {body.MediaRequest.ContentType}},
		Metadata: map[string]any{
			ex.MediaKindMetadataKey: provider.Kind, ex.MediaOperationMetadataKey: operationName,
			ex.MediaModelMetadataKey: body.Model, ex.RequestedModelMetadataKey: body.Model,
			ex.RequestPathMetadataKey: "/v1/media/" + provider.Kind + "/" + operationName,
		},
	}
	if standardImage {
		// Use the same default-operation resolution as the standard image handlers.
		opts.SourceFormat = tr.FromString("openai-image")
		delete(opts.Metadata, ex.MediaOperationMetadataKey)
		path := "/v1/images/generations"
		if operationName == config.MediaCapabilityEdit {
			path = "/v1/images/edits"
		}
		opts.Metadata[ex.RequestPathMetadataKey] = path
	}
	response, err := runtimeexecutor.NewMediaExecutor(selected.Provider, cfg).Execute(ctx, selected, ex.Request{Model: body.Model, Payload: payload}, opts)
	if err != nil {
		return providerConnectivityErrorResponse(err), http.StatusOK, nil
	}
	if state, _ := response.Metadata[ex.MediaResultMetadataKey].(string); state != helps.MediaResultCompleted {
		return providerConnectivityErrorResponse(fmt.Errorf("media probe has no verified completed result (%s)", state)), http.StatusOK, nil
	}
	// Return evidence rather than embedding megabytes of media in a probe envelope.
	summary, _ := json.Marshal(map[string]any{"status": "completed", "bytes": len(response.Payload)})
	response.Payload = summary
	response.Headers.Del("Content-Length")
	return providerConnectivityResponse(response), http.StatusOK, nil
}

// The draft is a complete save-shaped snapshot. Credential pools are ignored:
// only the selected key and its explicit proxy override enter synthesis.
func (h *Handler) mediaConnectivityAuth(body providerConnectivityTestRequest) (*coreauth.Auth, *config.Config, error) {
	cfg := &config.Config{}
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			cfg = h.cfg.CloneForRuntime()
		}
		h.mu.Unlock()
	}
	entry := config.MediaProvider{}
	key, proxy := "", ""
	if index := strings.TrimSpace(body.AuthIndex); index != "" {
		selected := h.authByIndex(index)
		if selected == nil {
			return nil, nil, fmt.Errorf("auth not found")
		}
		kind, name := selected.Attributes["media_kind"], selected.Attributes["media_provider_name"]
		saved := cfg.FindMediaProvider(kind, name)
		if saved == nil {
			return nil, nil, fmt.Errorf("auth provider mismatch")
		}
		entry = *saved
		key = selected.Attributes["api_key"]
		proxy = selected.ProxyURL
	}
	if len(body.MediaConfig) > 0 {
		entry = config.MediaProvider{}
		if err := json.Unmarshal(body.MediaConfig, &entry); err != nil {
			return nil, nil, fmt.Errorf("invalid media_config")
		}
	}
	if body.APIKey != nil {
		key = strings.TrimSpace(*body.APIKey)
	}
	if body.ProxyURL != nil {
		proxy = strings.TrimSpace(*body.ProxyURL)
	}
	entry.AuthID = ""
	entry.APIKeyEntries = nil
	if key != "" {
		entry.APIKeyEntries = []config.MediaAPIKeyEntry{{APIKey: key, ProxyURL: proxy}}
	}
	if err := validateMediaProviders([]config.MediaProvider{entry}); err != nil {
		return nil, nil, err
	}
	cfg.MediaProviders = []config.MediaProvider{entry}
	cfg.SanitizeMediaProviders()
	generated, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: &config.Config{MediaProviders: cfg.MediaProviders}, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		return nil, nil, err
	}
	if len(generated) != 1 {
		return nil, nil, fmt.Errorf("media provider is disabled or has no selected credential")
	}
	generated[0].ProxyURL = proxy
	return generated[0], cfg, nil
}
