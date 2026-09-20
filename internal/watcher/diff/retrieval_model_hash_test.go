package diff

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"testing"
)

func TestRetrievalSettingsTriggerModelReload(t *testing.T) {
	original := []config.OpenAICompatibilityModel{{Name: "m"}}
	before := ComputeOpenAICompatModelsHash(original)
	original[0].Type = "embeddings"
	after := ComputeOpenAICompatModelsHash(original)
	if before == after {
		t.Fatal("endpoint type change does not trigger model reload")
	}
	original[0].UpstreamPath = "/custom"
	if after == ComputeOpenAICompatModelsHash(original) {
		t.Fatal("path change does not trigger model reload")
	}
}
