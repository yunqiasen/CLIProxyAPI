package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExamplePanelRepositoryMatchesForkDefault(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Management struct {
			Repository string `yaml:"panel-github-repository"`
		} `yaml:"management"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Management.Repository != DefaultPanelGitHubRepository {
		t.Fatalf("example panel repository = %q, want %q", doc.Management.Repository, DefaultPanelGitHubRepository)
	}
}
