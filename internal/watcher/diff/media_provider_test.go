package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDiffMediaProvidersRedactsKeysAndReportsStructure(t *testing.T) {
	oldList := []config.MediaProvider{{Name: "images", Kind: config.MediaKindImage, BaseURL: "https://old.example", APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "secret-a"}}}}
	newList := []config.MediaProvider{{Name: "images", Kind: config.MediaKindImage, BaseURL: "https://new.example", APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "secret-b"}, {APIKey: "secret-c"}}, Models: []config.MediaModel{{Name: "model", Capabilities: []string{config.MediaCapabilityGenerate}}}}}
	changes := DiffMediaProviders(oldList, newList)
	joined := strings.Join(changes, "\n")
	if !strings.Contains(joined, "provider updated: image/images") || !strings.Contains(joined, "api-keys 1 -> 2") || !strings.Contains(joined, "models 0 -> 1") {
		t.Fatalf("changes = %q", joined)
	}
	if strings.Contains(joined, "secret-") {
		t.Fatalf("changes leaked API key: %q", joined)
	}
}

func TestBuildConfigChangeDetailsIncludesMediaProviders(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{MediaProviders: []config.MediaProvider{{Name: "images", Kind: config.MediaKindImage, BaseURL: "https://image.example"}}}
	joined := strings.Join(BuildConfigChangeDetails(oldCfg, newCfg), "\n")
	if !strings.Contains(joined, "media-providers:") || !strings.Contains(joined, "provider added: image/images") {
		t.Fatalf("details = %q", joined)
	}
}
