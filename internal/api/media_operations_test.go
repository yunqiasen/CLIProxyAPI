package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func newMediaOperationTestContext(t *testing.T, server *Server, method, path, contentType, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", contentType)
	ctx.Params = gin.Params{
		{Key: "kind", Value: config.MediaKindImage},
		{Key: "operation", Value: "remove-background"},
	}
	return ctx, recorder
}

func registerMediaOperationRuntime(t *testing.T, cfg *config.Config, provider config.MediaProvider, keys ...string) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	providerKey := util.MediaProviderKey(provider.Kind, provider.Name)
	manager.RegisterExecutor(runtimeexecutor.NewMediaExecutor(providerKey, cfg))
	for i, key := range keys {
		priority := 100 - i
		auth := &coreauth.Auth{ID: providerKey + "-" + key, Provider: providerKey, Label: provider.Name, Status: coreauth.StatusActive, Attributes: map[string]string{
			"auth_kind": "api_key", "api_key": key, "base_url": provider.BaseURL,
			"media_kind": provider.Kind, "media_provider_name": provider.Name, "provider_name": provider.Name, "provider_key": providerKey,
			"priority": string(rune('0' + priority)),
		}}
		// Keep the priority parseable without introducing formatting noise in the fixture.
		auth.Attributes["priority"] = "100"
		if i > 0 {
			auth.Attributes["priority"] = "99"
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	return manager
}

func TestMediaOperationHandlerForwardsNoModelOperation(t *testing.T) {
	var gotModel atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotModel.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":{"url":"https://img.example/removed.png"}}`)
	}))
	defer upstream.Close()
	provider := config.MediaProvider{
		Name: "Images", Kind: config.MediaKindImage, BaseURL: upstream.URL,
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "key"}},
		Operations:    []config.MediaOperation{{Name: "remove-background", Capability: config.MediaCapabilityRemoveBackground, Method: http.MethodPost, Path: "/remove", RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponseJSONURL, ResultPath: "output.url"}},
	}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	manager := registerMediaOperationRuntime(t, cfg, provider, "key")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, recorder := newMediaOperationTestContext(t, server, http.MethodPost, "/v1/media/image/remove-background", "application/json", `{"model":"fake","image":"input"}`)
	server.handleMediaOperation(ctx)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "removed.png") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(gotModel.Load().(string), "model") {
		t.Fatalf("model leaked to upstream: %s", gotModel.Load().(string))
	}
}

func TestMediaOperationHandlerRetriesNextKeyAfterUpstreamFailure(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Authorization") == "Bearer first" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"first failed"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	provider := config.MediaProvider{
		Name: "Images", Kind: config.MediaKindImage, BaseURL: upstream.URL,
		DisableCooling: true,
		Operations:     []config.MediaOperation{{Name: "remove-background", Capability: config.MediaCapabilityRemoveBackground, Method: http.MethodPost, Path: "/remove", RequestFormat: config.MediaRequestJSON, ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough}},
	}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	manager := registerMediaOperationRuntime(t, cfg, provider, "first", "second")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, recorder := newMediaOperationTestContext(t, server, http.MethodPost, "/v1/media/image/remove-background", "application/json", `{"image":"input"}`)
	server.handleMediaOperation(ctx)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"ok":true}` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if attempts.Load() != 2 {
		t.Fatalf("upstream attempts = %d, want 2", attempts.Load())
	}
}

func sdkconfigTestBaseHandlers(manager *coreauth.Manager) *handlers.BaseAPIHandler {
	return handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
}

func TestFindMediaOperationPrefersExactNameBeforeCapabilityAlias(t *testing.T) {
	provider := &config.MediaProvider{Operations: []config.MediaOperation{
		{Name: "custom-generate", Capability: config.MediaCapabilityGenerate},
		{Name: config.MediaCapabilityGenerate, Capability: config.MediaCapabilityEdit},
	}}
	operation, ok := findMediaOperation(provider, config.MediaCapabilityGenerate)
	if !ok || operation.Name != config.MediaCapabilityGenerate {
		t.Fatalf("operation = %#v, found=%v; want exact generate operation", operation, ok)
	}
}

func TestMediaOperationAcceptsOptionalOperationWithoutModel(t *testing.T) {
	provider := &config.MediaProvider{Models: nil}
	operation := config.MediaOperation{
		Name:      "remove-background",
		ModelMode: config.MediaModelOptional,
	}
	if !mediaOperationAcceptsModel(provider, operation, "") {
		t.Fatal("optional media operation should accept a request without a model")
	}
}

func TestMediaOperationRejectsModelWithoutOperationCapability(t *testing.T) {
	provider := &config.MediaProvider{Models: []config.MediaModel{
		{Name: "generation-only", Alias: "public-generation", Capabilities: []string{config.MediaCapabilityGenerate}},
	}}
	operation := config.MediaOperation{
		Name:       config.MediaCapabilityUpscale,
		Capability: config.MediaCapabilityUpscale,
		ModelMode:  config.MediaModelRequired,
	}
	if mediaOperationAcceptsModel(provider, operation, "public-generation") {
		t.Fatal("model without upscale capability must not be selected for an upscale operation")
	}
}

func TestMediaOperationAcceptsLegacyModelWithoutDeclaredCapabilities(t *testing.T) {
	provider := &config.MediaProvider{Models: []config.MediaModel{
		{Name: "legacy-upstream", Alias: "legacy-public"},
	}}
	operation := config.MediaOperation{
		Name:       config.MediaCapabilityUpscale,
		Capability: config.MediaCapabilityUpscale,
		ModelMode:  config.MediaModelRequired,
	}
	if !mediaOperationAcceptsModel(provider, operation, "legacy-public") {
		t.Fatal("an empty capability list must retain legacy unrestricted model behavior")
	}
}

func TestMediaOperationRejectsModelWithoutCapabilityWhenOperationUsesKnownName(t *testing.T) {
	provider := &config.MediaProvider{Models: []config.MediaModel{
		{Name: "generation-only", Alias: "public-generation", Capabilities: []string{config.MediaCapabilityGenerate}},
	}}
	operation := config.MediaOperation{
		Name:      config.MediaCapabilityUpscale,
		ModelMode: config.MediaModelRequired,
	}
	if mediaOperationAcceptsModel(provider, operation, "public-generation") {
		t.Fatal("model without upscale capability must not be selected when operation capability is inferred from its name")
	}
}

func TestMediaRequestedModelFallsBackToQueryForMultipartAndGET(t *testing.T) {
	query := url.Values{"model": []string{"query-model"}}
	if got := mediaRequestedModel(nil, "", query); got != "query-model" {
		t.Fatalf("empty-body query model = %q", got)
	}
	if got := mediaRequestedModel([]byte("not-multipart"), "multipart/form-data; boundary=missing", query); got != "query-model" {
		t.Fatalf("multipart query model = %q", got)
	}
}

func TestMediaOperationProvidersPrefersProviderDeclaringRequestedModel(t *testing.T) {
	server := &Server{cfg: &config.Config{MediaProviders: []config.MediaProvider{
		{
			Name:    "model-free-upstream",
			Kind:    config.MediaKindAudio,
			BaseURL: "https://model-free.example.com/v1",
			Operations: []config.MediaOperation{{
				Name:          config.MediaCapabilitySpeech,
				Capability:    config.MediaCapabilitySpeech,
				Method:        http.MethodPost,
				Path:          "/text-to-speech",
				RequestFormat: config.MediaRequestMultipart,
				ModelMode:     config.MediaModelNone,
			}},
		},
		{
			Name:    "declared-model-upstream",
			Kind:    config.MediaKindAudio,
			BaseURL: "https://declared.example.com/v1",
			Models: []config.MediaModel{{
				Name:         "upstream-tts",
				Alias:        "public-tts",
				Capabilities: []string{config.MediaCapabilitySpeech},
			}},
			Operations: []config.MediaOperation{{
				Name:          config.MediaCapabilitySpeech,
				Capability:    config.MediaCapabilitySpeech,
				Method:        http.MethodPost,
				Path:          "/chat/completions",
				RequestFormat: config.MediaRequestJSON,
				ModelMode:     config.MediaModelRequired,
			}},
		},
	}}}

	providers := server.mediaOperationProviders(config.MediaKindAudio, config.MediaCapabilitySpeech, "public-tts")
	if len(providers) != 1 {
		t.Fatalf("providers = %#v; want only the provider declaring the requested model", providers)
	}
	if !strings.Contains(providers[0], "declared-model-upstream") {
		t.Fatalf("providers[0] = %q; want declared-model-upstream", providers[0])
	}

	fallback := server.mediaOperationProviders(config.MediaKindAudio, config.MediaCapabilitySpeech, "")
	if len(fallback) != 1 || !strings.Contains(fallback[0], "model-free-upstream") {
		t.Fatalf("fallback = %#v; want only the model-free provider when no model is requested", fallback)
	}
}
