package models

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestRetrievalModelsAreHiddenFromCodexChatPicker(t *testing.T) {
	for _, tc := range []struct{ id, kind string }{
		{"codex-retrieval-vector", "embeddings"},
		{"codex-retrieval-rank", "rerank"},
		{"gpt-5.5", "embeddings"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			r := registry.GetGlobalRegistry()
			client := "retrieval-catalog-" + tc.id
			r.RegisterClient(client, "retrieval", []*registry.ModelInfo{{ID: tc.id, Object: "model", Type: tc.kind}})
			defer r.UnregisterClient(client)
			response := BuildResponse([]map[string]any{{"id": tc.id}}, nil, false)
			entries := response["models"].([]map[string]any)
			if len(entries) != 1 || entries[0]["visibility"] != "hide" {
				t.Fatalf("retrieval model exposed as a chat choice: %#v", entries)
			}
			if _, present := entries[0]["input_modalities"]; present {
				t.Fatalf("retrieval model inherited chat capabilities: %#v", entries[0])
			}
		})
	}
}
