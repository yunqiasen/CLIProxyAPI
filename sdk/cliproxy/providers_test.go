package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPIKeyClientProviderLoadIncludesMediaAuthCounts(t *testing.T) {
	provider := NewAPIKeyClientProvider()
	result, err := provider.Load(context.Background(), &config.Config{MediaProviders: []config.MediaProvider{
		{Kind: config.MediaKindImage, APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "i1"}, {APIKey: "i2"}}},
		{Kind: config.MediaKindVideo},
		{Kind: config.MediaKindAudio, APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "a1"}}},
	}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result.ImageMediaAuthCount != 2 || result.VideoMediaAuthCount != 1 || result.AudioMediaAuthCount != 1 {
		t.Fatalf("media auth counts = image:%d video:%d audio:%d", result.ImageMediaAuthCount, result.VideoMediaAuthCount, result.AudioMediaAuthCount)
	}
}
