package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func (h *Handler) performRetrievalConnectivityTest(ctx context.Context, body providerConnectivityTestRequest, requested string) (providerConnectivityTestResponse, int, error) {
	selected, cfg, err := h.retrievalConnectivityAuth(body)
	if err != nil {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, err
	}
	model := auth.ResolveConfiguredAPIKeyModel(cfg, selected, requested)
	request := auth.WithConfiguredAPIKeyModelInfo(cfg, selected, ex.Request{Model: model}, requested)
	info, ok := auth.ResolvedAPIKeyModelInfo(request)
	if !ok || helps.RetrievalFormat(info.Type) == "" {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, fmt.Errorf("probe requires a configured embeddings or rerank model")
	}
	payload := map[string]any{"model": requested, "input": "Hi"}
	if info.Type == "rerank" {
		payload = map[string]any{"model": requested, "query": "Hi", "documents": []string{"Hi"}, "top_n": 1}
	}
	request.Payload, _ = json.Marshal(payload)
	opts := ex.Options{SourceFormat: tr.FromString(helps.RetrievalFormat(info.Type)), OriginalRequest: request.Payload, Headers: connectivityHeaders(body.Header), Metadata: map[string]any{ex.RequestedModelMetadataKey: requested, ex.RequestPathMetadataKey: "/v1/" + info.Type}}
	// Validate the selected draft's model pool, without mutating the saved scheduler
	// or exposing its credential pool to a probe.
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	if _, err = manager.Register(ctx, selected); err != nil {
		return providerConnectivityTestResponse{}, http.StatusBadRequest, err
	}
	if err = manager.ValidateRetrievalRouting([]string{selected.Provider}, ex.Request{Model: requested}, opts); err != nil {
		return providerConnectivityErrorResponse(err), http.StatusOK, nil
	}
	response, err := runtimeexecutor.NewOpenAICompatExecutor(selected.Provider, cfg).Execute(ctx, selected, request, opts)
	if err != nil {
		return providerConnectivityErrorResponse(err), http.StatusOK, nil
	}
	return providerConnectivityResponse(response), http.StatusOK, nil
}

func (h *Handler) retrievalConnectivityAuth(body providerConnectivityTestRequest) (*auth.Auth, *config.Config, error) {
	cfg := &config.Config{}
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			cfg = h.cfg.CloneForRuntime()
		}
		h.mu.Unlock()
	}
	var selected *auth.Auth
	entry := config.OpenAICompatibility{Name: "retrieval-probe"}
	key, proxy := "", ""
	if strings.TrimSpace(body.AuthIndex) != "" {
		selected = h.authByIndex(strings.TrimSpace(body.AuthIndex))
		if selected == nil {
			return nil, nil, fmt.Errorf("auth not found")
		}
		if selected.Attributes["compat_name"] == "" {
			return nil, nil, fmt.Errorf("auth provider mismatch")
		}
		for _, p := range cfg.OpenAICompatibility {
			if p.Name == selected.Attributes["compat_name"] && strings.TrimSpace(p.BaseURL) == selected.Attributes["base_url"] {
				entry = p
				break
			}
		}
		key = selected.Attributes["api_key"]
		proxy = selected.ProxyURL
	}
	if len(body.OpenAIConfig) > 0 {
		saved, _ := json.Marshal(entry)
		var fields, draft map[string]json.RawMessage
		_ = json.Unmarshal(saved, &fields)
		if err := json.Unmarshal(body.OpenAIConfig, &draft); err != nil || draft == nil {
			return nil, nil, fmt.Errorf("invalid openai_config object")
		}
		for k, v := range draft {
			fields[k] = v
		}
		merged, _ := json.Marshal(fields)
		entry = config.OpenAICompatibility{}
		if err := json.Unmarshal(merged, &entry); err != nil {
			return nil, nil, fmt.Errorf("invalid openai_config: %w", err)
		}
	}
	if body.APIKey != nil {
		key = strings.TrimSpace(*body.APIKey)
	}
	if body.ProxyURL != nil {
		proxy = strings.TrimSpace(*body.ProxyURL)
	}
	if body.BaseURL != nil {
		entry.BaseURL = strings.TrimSpace(*body.BaseURL)
	}
	if body.Header != nil {
		entry.Headers = body.Header
	}
	if strings.TrimSpace(entry.Name) == "" {
		entry.Name = "retrieval-probe"
	}
	if strings.TrimSpace(entry.BaseURL) == "" || (key == "" && selected == nil && body.APIKey == nil) {
		return nil, nil, fmt.Errorf("selected credential and provider base URL are required")
	}
	// Credential rows in a draft are deliberately ignored.
	entry.APIKeyEntries = []config.OpenAICompatibilityAPIKey{{APIKey: key, ProxyURL: proxy}}
	cfg.OpenAICompatibility = []config.OpenAICompatibility{entry}
	if errValidate := config.ValidateOpenAICompatibilityModels(cfg.OpenAICompatibility); errValidate != nil {
		return nil, nil, errValidate
	}
	cfg.SanitizeOpenAICompatibility()
	generated, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: &config.Config{OpenAICompatibility: cfg.OpenAICompatibility}, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		return nil, nil, err
	}
	if len(generated) != 1 {
		return nil, nil, fmt.Errorf("provider is disabled or has no selected credential")
	}
	return generated[0], cfg, nil
}
