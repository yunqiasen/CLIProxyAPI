package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestRetrievalRateLimitInformsSharedScheduler(t *testing.T) {
	for _, value := range []string{"17", time.Now().Add(17 * time.Second).UTC().Format(http.TimeFormat)} {
		t.Run(value, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", value)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer upstream.Close()
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "retrieval", BaseURL: upstream.URL, Models: []config.OpenAICompatibilityModel{{Name: "vector", Type: "embeddings"}}}}}
			selected := &auth.Auth{ID: "selected", Provider: "retrieval", Attributes: map[string]string{"api_key": "key", "base_url": upstream.URL, "compat_name": "retrieval"}}
			req := auth.WithConfiguredAPIKeyModelInfo(cfg, selected, ex.Request{Model: "vector", Payload: []byte(`{"model":"vector","input":"x"}`)}, "vector")
			_, err := NewOpenAICompatExecutor("retrieval", cfg).Execute(context.Background(), selected, req, ex.Options{SourceFormat: tr.FromString("openai-embeddings")})
			retry, ok := err.(interface{ RetryAfter() *time.Duration })
			if !ok || retry.RetryAfter() == nil || *retry.RetryAfter() < 15*time.Second || *retry.RetryAfter() > 17*time.Second {
				t.Fatalf("upstream Retry-After was not delivered to the scheduler: %v", err)
			}
		})
	}
}
