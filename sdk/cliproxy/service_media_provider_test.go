package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func testMediaProvider() config.MediaProvider {
	return config.MediaProvider{
		Name:    "Image Relay",
		Kind:    config.MediaKindImage,
		BaseURL: "https://images.example/v1",
		Models: []config.MediaModel{
			{Name: "upstream-image", Alias: "public-image", DisplayName: "Public Image", Capabilities: []string{config.MediaCapabilityGenerate, config.MediaCapabilityEdit}},
			{Name: "upstream-upscale", Alias: "public-upscale", Capabilities: []string{config.MediaCapabilityUpscale}},
		},
	}
}

func testMediaAuth(provider config.MediaProvider) *coreauth.Auth {
	providerKey := util.MediaProviderKey(provider.Kind, provider.Name)
	return &coreauth.Auth{
		ID:       "media-image-auth",
		Provider: providerKey,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":           "api_key",
			"api_key":             "key",
			"base_url":            provider.BaseURL,
			"media_kind":          provider.Kind,
			"media_provider_name": provider.Name,
			"provider_key":        providerKey,
		},
	}
}

func TestRegisterExecutorForAuth_MediaProviderUsesMediaExecutor(t *testing.T) {
	provider := testMediaProvider()
	auth := testMediaAuth(provider)
	service := &Service{
		cfg:         &config.Config{MediaProviders: []config.MediaProvider{provider}},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	service.registerExecutorForAuth(auth, false)
	resolved, ok := service.coreManager.Executor(auth.Provider)
	if !ok || resolved == nil {
		t.Fatalf("media executor %q was not registered", auth.Provider)
	}
	if _, okMedia := resolved.(*runtimeexecutor.MediaExecutor); !okMedia {
		t.Fatalf("executor type = %T, want *executor.MediaExecutor", resolved)
	}
}

func TestRegisterModelsForAuth_MediaImageModelsUseImageType(t *testing.T) {
	provider := testMediaProvider()
	auth := testMediaAuth(provider)
	service := &Service{cfg: &config.Config{MediaProviders: []config.MediaProvider{provider}}}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(context.Background(), auth)
	models := modelRegistry.GetModelsForClient(auth.ID)
	if len(models) != 1 {
		t.Fatalf("registered models = %#v, want one standard image model", models)
	}
	model := models[0]
	if model.ID != "public-image" || model.Type != registry.OpenAIImageModelType || model.OwnedBy != provider.Name || model.DisplayName != "Public Image" {
		t.Fatalf("registered model = %#v", model)
	}
	providers := modelRegistry.GetModelProviders("public-image")
	if len(providers) != 1 || providers[0] != auth.Provider {
		t.Fatalf("model providers = %#v, want %q", providers, auth.Provider)
	}
}
