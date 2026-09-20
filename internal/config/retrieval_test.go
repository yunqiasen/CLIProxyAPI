package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetrievalConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"chat", "", true},
		{"image", ", image: true", true},
		{"embeddings", ", type: Embeddings", true},
		{"rerank", ", type: rerank", true},
		{"unknown", ", type: embdding", false},
		{"image-conflict", ", type: embeddings, image: true", false},
		{"chat-path", ", upstream-path: /custom", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("openai-compatibility:\n- name: retrieval\n  base-url: https://example.test/v1\n  models: [{name: vector" + tc.fields + "}]\n")
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, parse := range []func() (*Config, error){
				func() (*Config, error) { return ParseConfigBytes(data) },
				func() (*Config, error) { return LoadConfig(path) },
			} {
				cfg, err := parse()
				if (err == nil) != tc.valid {
					t.Fatalf("valid=%t err=%v", tc.valid, err)
				}
				if tc.name == "embeddings" && err == nil && cfg.OpenAICompatibility[0].Models[0].Type != "embeddings" {
					t.Fatal("model kind was not normalized")
				}
			}
		})
	}
}
