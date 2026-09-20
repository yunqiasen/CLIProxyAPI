package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func newRetrievalServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg.APIKeys = []string{"retrieval-client"}
	cfg.AuthDir = t.TempDir()
	cfg.RemoteManagement.DisableControlPanel = true
	manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	entries, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range entries {
		manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(a.Provider, cfg))
		if _, err = manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		var models []*registry.ModelInfo
		for _, p := range cfg.OpenAICompatibility {
			if p.Name != a.Attributes["compat_name"] {
				continue
			}
			for _, m := range p.Models {
				alias := m.Alias
				if alias == "" {
					alias = m.Name
				}
				if a.Prefix == "" || !cfg.ForceModelPrefix {
					models = append(models, &registry.ModelInfo{ID: alias, Object: "model", OwnedBy: a.Provider, Type: m.Type})
				}
				if a.Prefix != "" {
					models = append(models, &registry.ModelInfo{ID: a.Prefix + "/" + alias, Object: "model", OwnedBy: a.Provider, Type: m.Type})
				}
			}
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, models)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	}
	server := NewServer(cfg, manager, access.NewManager(), filepath.Join(t.TempDir(), "config.yaml"))
	t.Cleanup(func() {
		if errStop := server.Stop(context.Background()); errStop != nil {
			t.Error(errStop)
		}
	})
	return server
}

func TestRetrievalNativeEndpoints(t *testing.T) {
	for _, tc := range []struct{ kind, input, output string }{
		{"embeddings", `"input":["first","second"],"dimensions":2,"encoding_format":"float","extension":9007199254740993`, `{"object":"list","model":"vector-v1","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`},
		{"rerank", `"query":"first","documents":["first","second"],"top_n":1,"return_documents":true,"extension":9007199254740993`, `{"id":"rank-ok","results":[{"index":0,"relevance_score":0.9,"document":{"text":"first"}}],"meta":{"billed_units":{"search_units":1}}}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/"+tc.kind {
					t.Errorf("upstream path=%s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get("X-Custom") != "kept" {
					t.Error("upstream credential/headers lost")
				}
				if gjson.GetBytes(body, "model").String() != "vector-v1" || gjson.GetBytes(body, "extension").Raw != "9007199254740993" {
					t.Errorf("model or extension changed: %s", body)
				}
				if gjson.GetBytes(body, "messages").Exists() || gjson.GetBytes(body, "reasoning_effort").Exists() {
					t.Error("chat preparation used for retrieval")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.output)
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			err := yaml.Unmarshal([]byte(fmt.Sprintf(`openai-compatibility:
- name: retrieval-fixture
  base-url: %s/v1
  headers: {X-Custom: kept}
  api-key-entries: [{api-key: upstream-key}]
  models: [{name: vector-v1, alias: public-retrieval, type: %s}]
`, upstream.URL, tc.kind)), cfg)
			if err != nil {
				t.Fatal(err)
			}
			server := newRetrievalServer(t, cfg)
			request := httptest.NewRequest(http.MethodPost, "/v1/"+tc.kind, strings.NewReader(`{"model":"public-retrieval",`+tc.input+`}`))
			request.Header.Set("Authorization", "Bearer retrieval-client")
			request.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()
			server.engine.ServeHTTP(result, request)
			if result.Code != 200 || result.Body.String() != tc.output || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", result.Code, calls, result.Body.String())
			}
		})
	}
}

func TestRetrievalRoutingAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"key-failover", "model-conflict", "wrong-endpoint", "bad-response", "upstream-400", "custom-path", "unauthenticated"} {
		t.Run(mode, func(t *testing.T) {
			var keys []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Authorization"))
				if mode == "key-failover" && len(keys) == 1 {
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
					return
				}
				if mode == "upstream-400" {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"message":"bad input"}}`)
					return
				}
				if mode == "bad-response" {
					_, _ = io.WriteString(w, `{}`)
					return
				}
				if mode == "custom-path" && r.URL.Path != "/api/vectorize" {
					t.Errorf("path=%s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0.5,0.5]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
			}))
			defer upstream.Close()
			model := config.OpenAICompatibilityModel{Name: "vector-v1", Alias: "vector-public", Type: "embeddings"}
			if mode == "custom-path" {
				model.UpstreamPath = "/vectorize"
			}
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "retrieval-boundary", BaseURL: upstream.URL + "/api", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "one"}, {APIKey: "two"}}, Models: []config.OpenAICompatibilityModel{model}}}}
			if mode == "model-conflict" {
				other := model
				other.Name = "other-vector-space"
				cfg.OpenAICompatibility[0].Models = append(cfg.OpenAICompatibility[0].Models, other)
			}
			server := newRetrievalServer(t, cfg)
			path := "/v1/embeddings"
			body := `{"model":"vector-public","input":"test"}`
			if mode == "wrong-endpoint" {
				path = "/v1/chat/completions"
				body = `{"model":"vector-public","messages":[{"role":"user","content":"test"}]}`
			}
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if mode != "unauthenticated" {
				req.Header.Set("Authorization", "Bearer retrieval-client")
			}
			result := httptest.NewRecorder()
			server.engine.ServeHTTP(result, req)
			wantStatus, wantCalls := 200, 1
			switch mode {
			case "key-failover":
				wantCalls = 2
			case "model-conflict", "wrong-endpoint":
				wantStatus, wantCalls = 400, 0
			case "bad-response":
				wantStatus, wantCalls = 502, 2
			case "upstream-400":
				wantStatus = 400
			case "unauthenticated":
				wantStatus, wantCalls = 401, 0
			}
			if result.Code != wantStatus || len(keys) != wantCalls {
				t.Fatalf("status=%d want=%d calls=%d want=%d body=%s", result.Code, wantStatus, len(keys), wantCalls, result.Body.String())
			}
			if mode == "key-failover" && keys[0] == keys[1] {
				t.Fatal("did not switch credentials")
			}
		})
	}
}

func TestRetrievalEnvelopeIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name, input, output string
		want                int
	}{
		{"mixed-input", `{"input":["text",true]}`, ``, 400},
		{"negative-tokens", `{"input":[1,-1]}`, ``, 400},
		{"empty-text", `{"input":""}`, ``, 400},
		{"bad-dimensions", `{"input":"x","dimensions":1.5}`, ``, 400},
		{"streaming", `{"input":"x","stream":true}`, ``, 400},
		{"missing-vector", `{"input":["a","b"]}`, `{"data":[{"index":0,"embedding":[1]}]}`, 502},
		{"wrong-dimensions", `{"input":"x","dimensions":2}`, `{"data":[{"index":0,"embedding":[1]}]}`, 502},
		{"bad-base64", `{"input":"x","encoding_format":"base64"}`, `{"data":[{"index":0,"embedding":"not-base64"}]}`, 502},
		{"infinite-base64", `{"input":"x","dimensions":1,"encoding_format":"base64"}`, `{"data":[{"index":0,"embedding":"AACAfw=="}]}`, 502},
		{"nan-base64", `{"input":"x","dimensions":1,"encoding_format":"base64"}`, `{"data":[{"index":0,"embedding":"AADAfw=="}]}`, 502},
		{"token-input", `{"input":[1,2]}`, `{"data":[{"index":0,"embedding":[1]}]}`, 200},
		{"batch-tokens", `{"input":[[1,2],[3]]}`, `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2]}]}`, 200},
		{"base64", `{"input":"x","dimensions":2,"encoding_format":"base64"}`, `{"data":[{"index":0,"embedding":"AACAPwAAAEA="}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = io.WriteString(w, tc.output) }))
			defer upstream.Close()
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "integrity", BaseURL: upstream.URL, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "one"}}, Models: []config.OpenAICompatibilityModel{{Name: "vector", Type: "embeddings"}}}}}
			server := newRetrievalServer(t, cfg)
			req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"vector",`+tc.input[1:]))
			req.Header.Set("Authorization", "Bearer retrieval-client")
			out := httptest.NewRecorder()
			server.engine.ServeHTTP(out, req)
			wantCalls := 1
			if tc.want == 400 {
				wantCalls = 0
			}
			if out.Code != tc.want || calls != wantCalls {
				t.Fatalf("status=%d want=%d calls=%d want=%d body=%s", out.Code, tc.want, calls, wantCalls, out.Body.String())
			}
		})
	}
}

func TestRetrievalPreservesUpstreamErrorHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.Header().Set("X-Request-Id", "retrieval-rate-limit")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"busy","code":"rate_limit"}}`)
	}))
	defer upstream.Close()
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "retrieval-rate-limit", BaseURL: upstream.URL,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}},
		Models:        []config.OpenAICompatibilityModel{{Name: "vector", Type: "embeddings"}},
	}}}
	cfg.PassthroughHeaders = true
	server := newRetrievalServer(t, cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"vector","input":"test"}`))
	req.Header.Set("Authorization", "Bearer retrieval-client")
	out := httptest.NewRecorder()
	server.engine.ServeHTTP(out, req)
	if out.Code != http.StatusTooManyRequests || out.Header().Get("Retry-After") != "17" || out.Header().Get("X-Request-Id") != "retrieval-rate-limit" {
		t.Fatalf("status=%d headers=%v body=%s", out.Code, out.Header(), out.Body.String())
	}
	if gjson.Get(out.Body.String(), "error.code").String() != "rate_limit" {
		t.Fatalf("upstream error changed: %s", out.Body.String())
	}
}

type retrievalUsageCapture struct {
	model   string
	records chan usage.Record
}

func (p *retrievalUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Model != p.model {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func TestRetrievalUsageKeepsAliasCredentialAndTokenUnits(t *testing.T) {
	for _, tc := range []struct {
		name, kind, output string
		input, total       int64
	}{
		{"openai", "embeddings", `{"data":[{"index":0,"embedding":[1]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`, 7, 7},
		{"cohere", "rerank", `{"results":[{"index":0,"relevance_score":0.9}],"meta":{"tokens":{"input_tokens":11,"output_tokens":2},"billed_units":{"search_units":1}}}`, 11, 13},
		{"units-only", "rerank", `{"results":[{"index":0,"relevance_score":0.9}],"meta":{"billed_units":{"search_units":3}}}`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.output) }))
			defer upstream.Close()
			model := "retrieval-usage-" + tc.name
			capture := &retrievalUsageCapture{model: model, records: make(chan usage.Record, 2)}
			usage.RegisterNamedPlugin(model, capture)
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: model, BaseURL: upstream.URL, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}}, Models: []config.OpenAICompatibilityModel{{Name: model, Alias: "public-usage", Type: tc.kind}}}}}
			server := newRetrievalServer(t, cfg)
			body := `{"model":"public-usage","input":"test"}`
			if tc.kind == "rerank" {
				body = `{"model":"public-usage","query":"test","documents":["test"]}`
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/"+tc.kind, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer retrieval-client")
			out := httptest.NewRecorder()
			server.engine.ServeHTTP(out, req)
			if out.Code != 200 || out.Body.String() != tc.output {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			select {
			case record := <-capture.records:
				if record.Failed || record.Alias != "public-usage" || record.AuthID == "" || record.AuthIndex == "" || record.APIKey != "retrieval-client" || record.Detail.InputTokens != tc.input || record.Detail.TotalTokens != tc.total {
					t.Fatalf("unexpected usage record: %+v", record)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("missing retrieval usage record")
			}
		})
	}
}

func TestRetrievalAcrossProvidersAndPrefixes(t *testing.T) {
	for _, mode := range []string{"failover", "different-vector-spaces", "prefix"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(body, "model").String() != "vector-v1" {
					t.Errorf("wrong routed model: %s", body)
				}
				if mode == "failover" && attempt == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			for i := 0; i < 2; i++ {
				model := "vector-v1"
				if i == 1 && mode != "failover" {
					model = "other-vector-space"
				}
				cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, config.OpenAICompatibility{
					Name: fmt.Sprintf("retrieval-site-%d", i), BaseURL: upstream.URL,
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: fmt.Sprintf("selected-%d", i)}},
					Models:        []config.OpenAICompatibilityModel{{Name: model, Alias: "vectors", Type: "embeddings"}},
				})
			}
			requested := "vectors"
			if mode == "prefix" {
				cfg.ForceModelPrefix = true
				cfg.OpenAICompatibility[0].Prefix = "site-a"
				requested = "site-a/vectors"
			}
			server := newRetrievalServer(t, cfg)
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"`+requested+`","input":"test"}`))
			req.Header.Set("Authorization", "Bearer retrieval-client")
			out := httptest.NewRecorder()
			server.engine.ServeHTTP(out, req)
			wantStatus, wantCalls := 200, int32(2)
			if mode == "different-vector-spaces" {
				wantStatus, wantCalls = 400, 0
			}
			if mode == "prefix" {
				wantCalls = 1
			}
			if out.Code != wantStatus || calls.Load() != wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", out.Code, calls.Load(), out.Body.String())
			}
		})
	}
}

func TestRetrievalCallerCancellationStopsUpstream(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			entered, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if headers {
					_, _ = io.WriteString(w, `{"data":[`)
					w.(http.Flusher).Flush()
				}
				close(entered)
				select {
				case <-r.Context().Done():
					close(stopped)
				case <-release:
				}
			}))
			defer upstream.Close()
			defer close(release)
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "retrieval-cancel", BaseURL: upstream.URL, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}}, Models: []config.OpenAICompatibilityModel{{Name: "vector", Type: "embeddings"}}}}}
			server := newRetrievalServer(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"vector","input":"test"}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer retrieval-client")
			done := make(chan struct{})
			go func() { defer close(done); server.engine.ServeHTTP(httptest.NewRecorder(), req) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream not called")
			}
			cancel()
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream ignored caller cancellation")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("request handler remained active")
			}
		})
	}
}

func TestRetrievalProductionAndProbePayloadParity(t *testing.T) {
	for _, kind := range []string{"embeddings", "rerank"} {
		for _, prefix := range []string{"", "team"} {
			t.Run(kind+"/"+prefix, func(t *testing.T) {
				var bodies [][]byte
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					bodies = append(bodies, body)
					if r.URL.Path != "/v1/custom" || r.Header.Get("X-Settings") != "saved" {
						t.Error("provider settings bypassed")
					}
					if kind == "embeddings" {
						_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
					} else {
						_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.8}]}`)
					}
				}))
				defer upstream.Close()
				cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "retrieval-parity", Prefix: prefix, BaseURL: upstream.URL + "/v1", Headers: map[string]string{"X-Settings": "saved"}, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}, {APIKey: "not-selected"}}, Models: []config.OpenAICompatibilityModel{{Name: "real-model", Alias: "public-parity", Type: kind, UpstreamPath: "/custom"}}}}}
				cfg.ForceModelPrefix = true
				requested := "public-parity"
				if prefix != "" {
					requested = prefix + "/" + requested
				}
				protocol := "openai-embeddings"
				body := fmt.Sprintf(`{"model":%q,"input":"Hi"}`, requested)
				if kind == "rerank" {
					protocol = "cohere-rerank"
					body = fmt.Sprintf(`{"model":%q,"query":"Hi","documents":["Hi"],"top_n":1}`, requested)
				}
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: requested, Protocol: protocol}}, Params: map[string]any{"extension": "shared", "model": "not-the-routed-model"}}}
				server := newRetrievalServer(t, cfg)
				req := httptest.NewRequest(http.MethodPost, "/v1/"+kind, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer retrieval-client")
				out := httptest.NewRecorder()
				server.engine.ServeHTTP(out, req)
				if out.Code != 200 {
					t.Fatalf("production status=%d body=%s", out.Code, out.Body.String())
				}
				probeBody, _ := json.Marshal(map[string]any{"provider": "openai-compatibility", "model": requested, "api_key": "selected", "openai_config": cfg.OpenAICompatibility[0]})
				probe := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(probe)
				c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-connectivity-test", strings.NewReader(string(probeBody)))
				server.mgmt.ProviderConnectivityTest(c)
				if probe.Code != 200 || gjson.Get(probe.Body.String(), "status_code").Int() != 200 || len(bodies) != 2 {
					t.Fatalf("probe status=%d body=%s calls=%d", probe.Code, probe.Body.String(), len(bodies))
				}
				var expected, actual any
				_ = json.Unmarshal(bodies[0], &expected)
				_ = json.Unmarshal(bodies[1], &actual)
				expectedJSON, _ := json.Marshal(expected)
				actualJSON, _ := json.Marshal(actual)
				if string(expectedJSON) != string(actualJSON) || gjson.GetBytes(bodies[0], "extension").String() != "shared" || gjson.GetBytes(bodies[0], "model").String() != "real-model" {
					t.Fatalf("production=%s probe=%s", bodies[0], bodies[1])
				}
			})
		}
	}
}

func TestRetrievalBaseURLOnlyProviderAndProbe(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected empty bearer credential"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
	}))
	defer upstream.Close()
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "retrieval-base-only", BaseURL: upstream.URL,
		Models: []config.OpenAICompatibilityModel{{Name: "vector", Type: "embeddings"}},
	}}}
	server := newRetrievalServer(t, cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"vector","input":"Hi"}`))
	req.Header.Set("Authorization", "Bearer retrieval-client")
	out := httptest.NewRecorder()
	server.engine.ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Errorf("production status=%d body=%s", out.Code, out.Body.String())
	}
	index := server.handlers.AuthManager.List()[0].EnsureIndex()
	probe := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(probe)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-connectivity-test", strings.NewReader(`{"provider":"openai-compatibility","model":"vector","auth_index":"`+index+`"}`))
	server.mgmt.ProviderConnectivityTest(c)
	if probe.Code != http.StatusOK || gjson.Get(probe.Body.String(), "status_code").Int() != http.StatusOK || calls != 2 {
		t.Fatalf("probe status=%d calls=%d body=%s", probe.Code, calls, probe.Body.String())
	}
}

func TestRetrievalRejectsAliasesSharedWithOAuth(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"unexpected upstream dispatch"}}`)
	}))
	defer upstream.Close()
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "retrieval-oauth-conflict", BaseURL: upstream.URL,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}},
		Models:        []config.OpenAICompatibilityModel{{Name: "vector-v1", Alias: "shared-retrieval", Type: "embeddings"}},
	}}}
	server := newRetrievalServer(t, cfg)
	oauth := &auth.Auth{
		ID: "retrieval-oauth-chat", Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{"base_url": upstream.URL, "priority": "100"},
		Metadata:   map[string]any{"access_token": "fixture-oauth-token"},
	}
	if oauth.AuthKind() != auth.AuthKindOAuth {
		t.Fatal("fixture must use OAuth rather than an API-key model capability")
	}
	manager := server.handlers.AuthManager
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	if _, err := manager.Register(context.Background(), oauth); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(oauth.ID, "codex", []*registry.ModelInfo{{ID: "shared-retrieval", Type: "codex"}})
	t.Cleanup(func() { reg.UnregisterClient(oauth.ID) })
	manager.RefreshSchedulerEntry(oauth.ID)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"shared-retrieval","input":"Hi"}`))
	req.Header.Set("Authorization", "Bearer retrieval-client")
	out := httptest.NewRecorder()
	server.engine.ServeHTTP(out, req)
	if out.Code != http.StatusBadRequest || calls.Load() != 0 || !strings.Contains(out.Body.String(), "endpoint type") {
		t.Fatalf("status=%d upstream_calls=%d body=%s", out.Code, calls.Load(), out.Body.String())
	}
}

func TestRetrievalModelsDoNotReplaceChatAutoSelection(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || gjson.GetBytes(body, "model").String() != "chat-auto-v1" {
			t.Errorf("unexpected chat auto route: path=%s body=%s", r.URL.Path, body)
		}
		_, _ = io.WriteString(w, `{"id":"chat-auto","choices":[{"index":0,"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "retrieval-chat-auto", BaseURL: upstream.URL + "/v1",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "selected"}},
		Models: []config.OpenAICompatibilityModel{
			{Name: "chat-auto-v1", Alias: "chat-auto"},
			{Name: "vector-auto-v1", Alias: "vector-auto", Type: "embeddings"},
			{Name: "rank-auto-v1", Alias: "rank-auto", Type: "rerank"},
		},
	}}}
	server := newRetrievalServer(t, cfg)
	entry := server.handlers.AuthManager.List()[0]
	registry.GetGlobalRegistry().RegisterClient(entry.ID, entry.Provider, []*registry.ModelInfo{
		{ID: "chat-auto", Type: "openai-compatibility", Created: time.Now().Unix()},
		{ID: "vector-auto", Type: "embeddings", Created: time.Now().Unix() + 100},
		{ID: "rank-auto", Type: "rerank", Created: time.Now().Unix() + 200},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"Hi"}]}`))
	req.Header.Set("Authorization", "Bearer retrieval-client")
	out := httptest.NewRecorder()
	server.engine.ServeHTTP(out, req)
	if out.Code != http.StatusOK || calls.Load() != 1 || gjson.Get(out.Body.String(), "choices.0.message.content").String() != "ready" {
		t.Fatalf("status=%d upstream_calls=%d body=%s", out.Code, calls.Load(), out.Body.String())
	}
}
