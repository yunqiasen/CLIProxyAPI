package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodexKeyDisableImageGenerationYAMLRoundTrip(t *testing.T) {
	var key CodexKey
	if err := yaml.Unmarshal([]byte("name: relay\ndisable-image-generation: true\n"), &key); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if !key.DisableImageGeneration {
		t.Fatal("disable-image-generation was not loaded")
	}

	encoded, err := yaml.Marshal(&key)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), "disable-image-generation: true") {
		t.Fatalf("round-tripped YAML omitted disable-image-generation: %s", encoded)
	}
}
