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
	if len(models) != 2 {
		t.Fatalf("registered models = %#v, want standard and custom image models", models)
	}
	model := models[0]
	if model.ID != "public-image" || model.Type != registry.OpenAIImageModelType || model.OwnedBy != provider.Name || model.DisplayName != "Public Image" {
		t.Fatalf("registered model = %#v", model)
	}
	if models[1].ID != "public-upscale" || models[1].Type != "media-image" {
		t.Fatalf("custom image model = %#v", models[1])
	}
	providers := modelRegistry.GetModelProviders("public-image")
	if len(providers) != 1 || providers[0] != auth.Provider {
		t.Fatalf("model providers = %#v, want %q", providers, auth.Provider)
	}
}

func TestBuildMediaProviderConfigModelsIncludesCustomMediaModels(t *testing.T) {
	tests := []struct {
		kind       string
		capability string
		wantType   string
	}{
		{kind: config.MediaKindImage, capability: config.MediaCapabilityUpscale, wantType: "media-image"},
		{kind: config.MediaKindVideo, capability: config.MediaCapabilityTextToVideo, wantType: "media-video"},
		{kind: config.MediaKindAudio, capability: config.MediaCapabilitySpeech, wantType: "media-audio"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			provider := &config.MediaProvider{
				Name: "Media Relay", Kind: tt.kind,
				Models: []config.MediaModel{{Name: "upstream-model", Alias: "public-model", Capabilities: []string{tt.capability}}},
			}
			models := buildMediaProviderConfigModels(provider)
			if len(models) != 1 || models[0].ID != "public-model" || models[0].Type != tt.wantType {
				t.Fatalf("registered models = %#v, want public-model type %q", models, tt.wantType)
			}
		})
	}
}

func TestApplyWatcherConfigUpdateSynchronizesMediaAuthBeforeReturn(t *testing.T) {
	const authID = "media-hot-reload-auth"
	oldProvider := testMediaProvider()
	oldProvider.Name = "Image Relay Before"
	oldProvider.BaseURL = "https://before.example/v1"
	oldProvider.Models = []config.MediaModel{{
		Name: "before-upstream", Alias: "before-image", Capabilities: []string{config.MediaCapabilityGenerate},
	}}
	oldProvider.APIKeyEntries = []config.MediaAPIKeyEntry{{AuthID: authID, APIKey: "before-key"}}
	oldConfig := &config.Config{MediaProviders: []config.MediaProvider{oldProvider}}
	service := &Service{cfg: oldConfig, coreManager: coreauth.NewManager(nil, nil, nil)}
	service.registerConfigAPIKeyAuths(context.Background(), oldConfig)

	modelRegistry := registry.GetGlobalRegistry()
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
	if _, ok := service.coreManager.GetByID(authID); !ok {
		t.Fatal("initial media auth was not registered")
	}

	newProvider := oldProvider
	newProvider.Name = "Image Relay After"
	newProvider.BaseURL = "https://after.example/v1"
	newProvider.Models = []config.MediaModel{{
		Name: "after-upstream", Alias: "after-image", Capabilities: []string{config.MediaCapabilityGenerate},
	}}
	newProvider.APIKeyEntries = []config.MediaAPIKeyEntry{{AuthID: authID, APIKey: "after-key"}}
	service.applyWatcherConfigUpdate(&config.Config{MediaProviders: []config.MediaProvider{newProvider}})

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("updated media auth is unavailable after watcher callback returned")
	}
	if updated.Label != newProvider.Name || updated.Provider != util.MediaProviderKey(newProvider.Kind, newProvider.Name) {
		t.Fatalf("updated media auth identity = label:%q provider:%q", updated.Label, updated.Provider)
	}
	if got := updated.Attributes["api_key"]; got != "after-key" {
		t.Fatalf("updated api_key = %q, want after-key", got)
	}
	if got := updated.Attributes["base_url"]; got != newProvider.BaseURL {
		t.Fatalf("updated base_url = %q, want %q", got, newProvider.BaseURL)
	}
	if resolved, exists := service.coreManager.Executor(updated.Provider); !exists || resolved == nil {
		t.Fatalf("updated media executor %q is unavailable", updated.Provider)
	} else if _, okMedia := resolved.(*runtimeexecutor.MediaExecutor); !okMedia {
		t.Fatalf("updated executor type = %T, want *executor.MediaExecutor", resolved)
	}
	models := modelRegistry.GetModelsForClient(authID)
	if len(models) != 1 || models[0].ID != "after-image" {
		t.Fatalf("updated registered models = %#v, want after-image", models)
	}
}

func TestApplyWatcherConfigUpdateRegistersKeylessMediaProvider(t *testing.T) {
	const authID = "media-keyless-hot-reload-auth"
	provider := testMediaProvider()
	provider.Name = "Keyless Image Relay"
	provider.AuthID = authID
	provider.APIKeyEntries = nil
	cfg := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}

	modelRegistry := registry.GetGlobalRegistry()
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
	service.applyWatcherConfigUpdate(cfg)

	auth, ok := service.coreManager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatal("keyless media auth is unavailable after watcher callback returned")
	}
	if _, hasAPIKey := auth.Attributes["api_key"]; hasAPIKey {
		t.Fatalf("keyless media auth leaked api_key attribute: %#v", auth.Attributes)
	}
	if models := modelRegistry.GetModelsForClient(authID); len(models) != len(provider.Models) {
		t.Fatalf("keyless media models = %#v, want %d models", models, len(provider.Models))
	}
}

func TestApplyWatcherConfigUpdateRemovesDeletedMediaAuthBeforeReturn(t *testing.T) {
	const authID = "media-deleted-hot-reload-auth"
	provider := testMediaProvider()
	provider.APIKeyEntries = []config.MediaAPIKeyEntry{{AuthID: authID, APIKey: "deleted-key"}}
	oldConfig := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	service := &Service{cfg: oldConfig, coreManager: coreauth.NewManager(nil, nil, nil)}
	service.registerConfigAPIKeyAuths(context.Background(), oldConfig)

	modelRegistry := registry.GetGlobalRegistry()
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
	if _, ok := service.coreManager.GetByID(authID); !ok {
		t.Fatal("initial media auth was not registered")
	}

	service.applyWatcherConfigUpdate(&config.Config{})
	if stale, ok := service.coreManager.GetByID(authID); ok {
		t.Fatalf("deleted media auth remains after watcher callback returned: %#v", stale)
	}
	if models := modelRegistry.GetModelsForClient(authID); len(models) != 0 {
		t.Fatalf("deleted media models remain registered: %#v", models)
	}
}
