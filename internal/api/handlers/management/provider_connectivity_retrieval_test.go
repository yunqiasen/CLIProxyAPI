package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestRetrievalProbeUsesSelectedKeyAndDraft(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"embeddings", "rerank"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail_%t", kind, fail), func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path != "/v1/custom" || r.Header.Get("Authorization") != "Bearer selected" || r.Header.Get("X-Draft") != "yes" || r.Header.Get("X-Stale") != "" {
						t.Error("probe bypassed selected-key draft settings")
					}
					if gjson.GetBytes(body, "model").String() != "real-model" || gjson.GetBytes(body, "messages").Exists() {
						t.Errorf("wrong upstream payload: %s", body)
					}
					if fail {
						w.WriteHeader(429)
						_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
						return
					}
					if kind == "embeddings" {
						_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)
					} else {
						_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.9}]}`)
					}
				}))
				defer upstream.Close()
				cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "saved-provider", BaseURL: "http://127.0.0.1:1", Headers: map[string]string{"X-Stale": "old"}, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}, {APIKey: "other-paid-key"}}, Models: []config.OpenAICompatibilityModel{{Name: "old", Alias: "old"}}}}}
				entries, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil {
					t.Fatal(err)
				}
				manager := auth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				for _, a := range entries {
					if _, err = manager.Register(context.Background(), a); err != nil {
						t.Fatal(err)
					}
				}
				h := &Handler{cfg: cfg, authManager: manager}
				raw, _ := json.Marshal(map[string]any{"provider": "openai-compatibility", "auth_index": entries[0].EnsureIndex(), "model": "public-model", "proxy_url": "direct", "openai_config": map[string]any{"name": "saved-provider", "base-url": upstream.URL + "/v1", "headers": map[string]string{"X-Draft": "yes"}, "api-key-entries": []map[string]string{{"api-key": "ignored-pool-key"}}, "models": []map[string]string{{"name": "real-model", "alias": "public-model", "type": kind, "upstream-path": "/custom"}}}})
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest("POST", "/v0/management/provider-connectivity-test", strings.NewReader(string(raw)))
				h.ProviderConnectivityTest(c)
				var result providerConnectivityTestResponse
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				want := 200
				if fail {
					want = 429
				}
				if w.Code != 200 || result.StatusCode != want || calls != 1 {
					t.Fatalf("http=%d status=%d calls=%d body=%s", w.Code, result.StatusCode, calls, w.Body.String())
				}
				if cfg.OpenAICompatibility[0].BaseURL != "http://127.0.0.1:1" || cfg.OpenAICompatibility[0].Models[0].Name != "old" {
					t.Fatal("saved provider mutated")
				}
			})
		}
	}
}

func TestRetrievalProbeRejectsInvalidModelKinds(t *testing.T) {
	for _, model := range []string{
		`{"name":"vector","type":"embdding"}`,
		`{"name":"vector","type":"embeddings","image":true}`,
	} {
		t.Run(model, func(t *testing.T) {
			h := &Handler{cfg: &config.Config{}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-connectivity-test", strings.NewReader(`{"provider":"openai-compatibility","model":"vector","api_key":"selected","openai_config":{"name":"retrieval","base-url":"http://127.0.0.1:1","models":[`+model+`]}}`))
			h.ProviderConnectivityTest(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
