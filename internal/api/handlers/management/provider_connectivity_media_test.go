package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func mediaProbeCall(t *testing.T, h *Handler, payload map[string]any) (int, providerConnectivityTestResponse) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v8/management/provider-connectivity-test", strings.NewReader(string(raw)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ProviderConnectivityTest(c)
	var result providerConnectivityTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v %s", err, rec.Body.String())
	}
	return rec.Code, result
}

func TestMediaProbeUsesExecutorDraftAndOneSelectedKey(t *testing.T) {
	var requests []string
	var requestsMu sync.Mutex
	var proxyCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("X-Media-Key")+" "+r.Header.Get("X-Draft")+" "+string(body))
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":{"image":"https://example.test/completed.png"}}`)
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		r.RequestURI = ""
		r.Header.Del("Proxy-Connection")
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	provider := config.MediaProvider{Name: "fixture", Kind: "image", BaseURL: upstream.URL + "/api/v2?base=one", Prefix: "vendor", APIKeyHeader: "X-Media-Key", APIKeyPrefix: "-", Headers: map[string]string{"X-Draft": "draft"}, Models: []config.MediaModel{{Name: "raw", Alias: "public", Capabilities: []string{"generate"}}}, Operations: []config.MediaOperation{{Name: "generate", Method: "POST", Path: "/api/v2/run?op=two", RequestFormat: "json", ModelMode: "required", ResponseFormat: "json-url", ResultPath: "result.image"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	cfg.MediaProviders[0].APIKeyEntries = []config.MediaAPIKeyEntry{{APIKey: "selected", ProxyURL: "direct"}, {APIKey: "do-not-use"}}
	generated, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	selected, err := manager.Register(t.Context(), generated[0])
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(cfg)
	draft := provider
	draft.APIKeyEntries = []config.MediaAPIKeyEntry{{APIKey: "pool-must-be-ignored"}}
	body := `{"model":"vendor/public","prompt":"draw"}`
	code, result := mediaProbeCall(t, NewHandlerWithoutConfigFilePath(cfg, manager), map[string]any{"provider": "media", "auth_index": selected.Index, "model": "vendor/public", "proxy_url": proxy.URL, "media_config": draft, "media_operation": "generate", "media_request": map[string]string{"data": body, "content_type": "application/json"}})
	if code != 200 || result.StatusCode != 200 {
		t.Fatalf("probe = %d %#v", code, result)
	}
	requestsMu.Lock()
	first := append([]string(nil), requests...)
	requestsMu.Unlock()
	if proxyCalls.Load() != 1 || len(first) != 1 {
		t.Fatalf("probe proxy=%d calls=%d", proxyCalls.Load(), len(first))
	}
	selected.ProxyURL = proxy.URL
	executor := runtimeexecutor.NewMediaExecutor(selected.Provider, cfg)
	_, err = executor.Execute(context.Background(), selected, coreexecutor.Request{Model: "vendor/public", Payload: []byte(body)}, coreexecutor.Options{SourceFormat: tr.FromString("media"), Headers: http.Header{"Content-Type": {"application/json"}}, Metadata: map[string]any{coreexecutor.MediaOperationMetadataKey: "generate", coreexecutor.MediaModelMetadataKey: "vendor/public"}})
	if err != nil {
		t.Fatal(err)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("probe/production diverged: %v", requests)
	}
	if !strings.Contains(requests[0], "/api/v2/run?base=one&op=two selected draft") || !strings.Contains(requests[0], `"model":"raw"`) {
		t.Fatalf("bad outbound: %s", requests[0])
	}
	after, _ := json.Marshal(cfg)
	if string(before) != string(after) {
		t.Fatal("probe changed saved config")
	}
}

func TestMediaProbeRequiresCompletedValidResult(t *testing.T) {
	cases := []struct {
		name, body, contentType, format string
		want                            int
	}{
		{"failed-state-with-url", `{"state":"failed","url":"https://example.test/partial.wav"}`, "application/json", "passthrough", 502},
		{"empty-json", `{}`, "application/json", "passthrough", 502},
		{"pending", `{"task_id":"pending-task","status":"pending"}`, "application/json", "passthrough", 502},
		{"empty-audio", "", "audio/wav", "binary", 502},
		{"invalid-base64", `{"data":[{"b64_json":"not-valid-base64!"}]}`, "application/json", "passthrough", 502},
		{"false-result", `{"data":[{"b64_json":"aGVsbG8="}]}`, "application/json", "passthrough", 502},
		{"error-no-message", `{"error":{"code":"rejected"}}`, "application/json", "passthrough", 502},
		{"completed", `{"data":[{"url":"https://example.test/result.wav"}]}`, "application/json", "passthrough", 200},
		{"completed-url-with-empty-base64-and-task-id", `{"task_id":"provenance","data":[{"url":"https://example.test/result.wav","b64_json":""}]}`, "application/json", "passthrough", 200},
		{"pending-with-preview", `{"task_id":"pending","status":"processing","data":[{"url":"https://example.test/preview.wav","b64_json":""}]}`, "application/json", "passthrough", 502},
		{"empty-base64-without-result", `{"data":[{"b64_json":""}]}`, "application/json", "passthrough", 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			p := config.MediaProvider{Name: "fixture", Kind: "audio", BaseURL: upstream.URL, Operations: []config.MediaOperation{{Name: "speech", Method: "POST", Path: "/speech", RequestFormat: "json", ModelMode: "none", ResponseFormat: tc.format}}}
			code, res := mediaProbeCall(t, NewHandlerWithoutConfigFilePath(&config.Config{}, nil), map[string]any{"provider": "media", "api_key": "selected", "proxy_url": "direct", "media_config": p, "media_operation": "speech", "media_request": map[string]string{"data": "{}", "content_type": "application/json"}})
			if code != 200 || res.StatusCode != tc.want || calls.Load() != 1 {
				t.Fatalf("HTTP=%d result=%#v calls=%d", code, res, calls.Load())
			}
		})
	}
}

func TestMediaProbeStandardImageDefaultsMatchProduction(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"url":"https://example.test/result.png"}]}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "default-images", Kind: "image", BaseURL: upstream.URL + "/v1", Models: []config.MediaModel{{Name: "raw", Alias: "public"}}}
	code, result := mediaProbeCall(t, NewHandlerWithoutConfigFilePath(&config.Config{}, nil), map[string]any{"provider": "media", "api_key": "selected", "proxy_url": "direct", "media_config": p, "media_operation": "generate", "model": "public", "media_request": map[string]string{"data": `{"model":"public","prompt":"draw"}`, "content_type": "application/json"}})
	if code != 200 || result.StatusCode != 200 || calls.Load() != 1 {
		t.Fatalf("HTTP=%d result=%#v calls=%d", code, result, calls.Load())
	}
}

func TestMediaProbePreservesPendingStateAcrossResponseNormalization(t *testing.T) {
	for _, pendingField := range []string{"status", "state"} {
		t.Run(pendingField, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					pendingField: "processing", "task_id": "accepted-job",
					"result": map[string]string{"image": "https://example.test/preview.png"},
				})
			}))
			defer upstream.Close()
			provider := config.MediaProvider{
				Name: "normalized-preview", Kind: "image", BaseURL: upstream.URL,
				Operations: []config.MediaOperation{{
					Name: "generate", Method: "POST", Path: "/generate", RequestFormat: "json",
					ModelMode: "none", ResponseFormat: "json-url", ResultPath: "result.image",
				}},
			}
			code, result := mediaProbeCall(t, NewHandlerWithoutConfigFilePath(&config.Config{}, nil), map[string]any{
				"provider": "media", "api_key": "fixture-key", "proxy_url": "direct", "media_config": provider,
				"media_operation": "generate", "media_request": map[string]string{"data": "{}", "content_type": "application/json"},
			})
			if code != http.StatusOK || result.StatusCode != http.StatusBadGateway || calls.Load() != 1 {
				t.Fatalf("pending preview must not be completed: HTTP=%d result=%#v calls=%d", code, result, calls.Load())
			}
		})
	}
}

func TestMediaProbeNormalizedEnvelopeRetainsCompletionAndFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, envelope string
		wantStatus     int
	}{
		{"completed-task-provenance", `{"task_id":"finished","result":{"image":"https://example.test/result.png"}}`, http.StatusOK},
		{"nested-pending-preview", `{"task_id":"accepted","result":{"status":"processing","image":"https://example.test/preview.png"}}`, http.StatusBadGateway},
		{"nested-failed-preview", `{"status":"completed","result":{"state":"failed","image":"https://example.test/preview.png"}}`, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.envelope)
			}))
			defer upstream.Close()
			provider := config.MediaProvider{
				Name: "normalized-envelope", Kind: "image", BaseURL: upstream.URL,
				Operations: []config.MediaOperation{{
					Name: "generate", Method: "POST", Path: "/generate", RequestFormat: "json",
					ModelMode: "none", ResponseFormat: "json-url", ResultPath: "result.image",
				}},
			}
			code, result := mediaProbeCall(t, NewHandlerWithoutConfigFilePath(&config.Config{}, nil), map[string]any{
				"provider": "media", "api_key": "fixture-key", "proxy_url": "direct", "media_config": provider,
				"media_operation": "generate", "media_request": map[string]string{"data": "{}", "content_type": "application/json"},
			})
			if code != http.StatusOK || result.StatusCode != tc.wantStatus || calls.Load() != 1 {
				t.Fatalf("envelope evidence changed: HTTP=%d result=%#v calls=%d", code, result, calls.Load())
			}
		})
	}
}
