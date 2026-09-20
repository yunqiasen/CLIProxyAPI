package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestRetrievalHomeRoutingRequiresCapabilityContract(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	_, err := manager.Execute(context.Background(), []string{"home"}, ex.Request{Model: "vector"}, ex.Options{SourceFormat: tr.FromString("openai-embeddings")})
	if err == nil || !strings.Contains(err.Error(), "Home") || !strings.Contains(err.Error(), "local openai-compatibility") {
		t.Fatalf("expected explicit retrieval routing boundary, got %v", err)
	}
}
