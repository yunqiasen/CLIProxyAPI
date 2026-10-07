package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestModelCatalogConfigValidationAndRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		valid     bool
	}{
		{"absent", `port: 8317`, true},
		{"rules", `client: {model-catalog: {order: DESC, hidden: [" a ", "", "x*"], pinned: [b]}}`, true},
		{"clear", `client: {model-catalog: {order: null, hidden: [], pinned: null}}`, true},
		{"bad order", `client: {model-catalog: {order: random}}`, false},
		{"number order", `client: {model-catalog: {order: 7}}`, false},
		{"number rule", `client: {model-catalog: {hidden: [7]}}`, false},
		{"scalar list", `client: {model-catalog: {hidden: a}}`, false},
		{"object list", `client: {model-catalog: {pinned: [{id: a}]}}`, false},
		{"aliases", `client: {model-catalog: {hidden: &rules [a], pinned: *rules}}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.ParseConfigBytes([]byte(tt.raw))
			if (err == nil) != tt.valid {
				t.Fatalf("parse err=%v valid=%v", err, tt.valid)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.raw), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, loadErr := config.LoadConfig(path)
			if (loadErr == nil) != tt.valid {
				t.Fatalf("load err=%v valid=%v", loadErr, tt.valid)
			}
			if !tt.valid {
				return
			}
			if !reflect.DeepEqual(cfg.Client.ModelCatalog, loaded.Client.ModelCatalog) {
				t.Fatal("load/parse differ")
			}
			encoded, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			round, err := config.ParseConfigBytes(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(round.Client.ModelCatalog, cfg.Client.ModelCatalog) {
				t.Fatalf("round trip differs: %+v vs %+v", round.Client.ModelCatalog, cfg.Client.ModelCatalog)
			}
		})
	}
}
