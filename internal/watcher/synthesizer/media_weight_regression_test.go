package synthesizer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestMediaKeyWeightSurvivesConfigAndSynthesis(t *testing.T) {
	for _, weight := range []string{"37", "0", "-1", "1000001"} {
		t.Run(weight, func(t *testing.T) {
			var cfg config.Config
			if err := json.Unmarshal([]byte(`{"media-providers":[{"name":"fixture","kind":"audio","base-url":"https://example.test","api-key-entries":[{"api-key":"fixture","priority":12,"weight":`+weight+`}]}]}`), &cfg); err != nil {
				t.Fatal(err)
			}
			generated, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{Config: &cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
			if weight == "1000001" {
				if err == nil {
					t.Fatal("out-of-range weight accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := weight
			if want == "-1" {
				want = "0"
			}
			if len(generated) != 1 || generated[0].Attributes["weight"] != want || generated[0].Attributes["priority"] != "12" {
				t.Fatalf("weight not wired for %s: %#v", weight, generated)
			}
		})
	}
}
