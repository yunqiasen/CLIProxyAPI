package synthesizer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestProviderPriority_SynthesizedSupplierPrecedesKey(t *testing.T) {
	for _, providerPriority := range []int{10, 100} {
		t.Run(fmt.Sprintf("AnyProviderPriority%d", providerPriority), func(t *testing.T) {
			anyKeyPriority, agentKeyPriority := 9, 10
			cfg := &config.Config{CodexKey: []config.CodexKey{
				{Name: "Any", Priority: providerPriority, BaseURL: "https://any.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "fixture-any", Priority: &anyKeyPriority}}},
				{Name: "AgentRouter", Priority: 9, BaseURL: "https://agent.example/v1", APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "fixture-agent", Priority: &agentKeyPriority}}},
			}}
			auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range auths {
				t.Logf("provider=%s runtime_priority=%s", a.Label, a.Attributes["priority"])
			}
			selected, err := (&coreauth.FillFirstSelector{}).Pick(context.Background(), "codex", "cpa-6a", executor.Options{}, auths)
			if err != nil {
				t.Fatal(err)
			}
			if selected.Label != "Any" {
				t.Fatalf("provider-first expectation: Any(provider=%d,key=9); actual=%s(provider=9,key=10)", providerPriority, selected.Label)
			}
		})
	}
}

func TestProviderPriority_AllGroupedFamilies(t *testing.T) {
	for _, family := range []string{"codex", "xai", "meta", "claude", "gemini", "interactions", "media"} {
		t.Run(family, func(t *testing.T) {
			highKey, lowKey := 100, -100
			cfg := &config.Config{}
			for i := 0; i < 2; i++ {
				name, supplierPriority, keyPriority := "backup", 9, &highKey
				if i == 1 {
					name, supplierPriority, keyPriority = "preferred", 10, &lowKey
				}
				keys := []config.NativeAPIKeyEntry{{APIKey: name, Priority: keyPriority}}
				codex := config.CodexKey{Name: name, Priority: supplierPriority, APIKeyEntries: keys}
				gemini := config.GeminiKey{Name: name, Priority: supplierPriority, APIKeyEntries: keys}
				switch family {
				case "codex":
					cfg.CodexKey = append(cfg.CodexKey, codex)
				case "xai":
					cfg.XAIKey = append(cfg.XAIKey, codex)
				case "meta":
					cfg.MetaKey = append(cfg.MetaKey, codex)
				case "claude":
					cfg.ClaudeKey = append(cfg.ClaudeKey, config.ClaudeKey{Name: name, Priority: supplierPriority, APIKeyEntries: keys})
				case "gemini":
					cfg.GeminiKey = append(cfg.GeminiKey, gemini)
				case "interactions":
					cfg.InteractionsKey = append(cfg.InteractionsKey, gemini)
				case "media":
					cfg.MediaProviders = append(cfg.MediaProviders, config.MediaProvider{Name: name, Kind: config.MediaKindImage, Priority: supplierPriority, APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: name, Priority: keyPriority}}})
				}
			}
			auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := (&coreauth.FillFirstSelector{}).Pick(context.Background(), "mixed", "", executor.Options{}, auths)
			if err != nil || selected.Attributes["api_key"] != "preferred" {
				t.Fatalf("preferred supplier not selected: auth=%v err=%v", selected, err)
			}
		})
	}
}
