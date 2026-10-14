package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestMediaReviewAsyncSubmitNotRepeatedAfterPollFailure(t *testing.T) {
	var submits, polls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			n := submits.Add(1)
			fmt.Fprintf(w, `{"task_id":"task-%d"}`, n)
			return
		}
		polls.Add(1)
		w.WriteHeader(502)
		io.WriteString(w, `{"error":"poll temporarily failed"}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "ReviewAsync", Kind: "image", BaseURL: upstream.URL, DisableCooling: true,
		Operations: []config.MediaOperation{{Name: "remove-background", Capability: "remove-background", Method: "POST", Path: "/submit", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough", Async: &config.MediaAsyncOperation{TaskIDPath: "task_id", PollMethod: "GET", PollPath: "/tasks/{task_id}", StatusPath: "status", SuccessValues: []string{"completed"}, FailureValues: []string{"failed"}}}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "first", "second")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/media/image/remove-background", "application/json", `{"image":"synthetic"}`)
	server.handleMediaOperation(ctx)
	t.Logf("submit_count=%d poll_count=%d final_status=%d", submits.Load(), polls.Load(), rec.Code)
	if submits.Load() != 1 {
		t.Errorf("expected one committed submit; got %d (second key creates another task)", submits.Load())
	}
}

func TestMediaReviewCharacterizesDocumentedFixedModelOverride(t *testing.T) {
	var calls atomic.Int32
	var model string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		model = string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"url":"fixture"}]}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "ReviewAudio", Kind: "audio", BaseURL: upstream.URL, Models: []config.MediaModel{{Name: "tts", Alias: "review-tts", Capabilities: []string{"speech"}}, {Name: "asr", Alias: "review-asr", Capabilities: []string{"transcribe"}}}, Operations: []config.MediaOperation{{Name: "speech", Capability: "speech", Method: "POST", Path: "/speech", RequestFormat: "json", ModelMode: "required", Model: "tts", ResponseFormat: "passthrough"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "key")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/audio/speech", "application/json", `{"model":"review-asr","input":"synthetic"}`)
	server.handleMediaOperationFor(ctx, "audio", "speech")
	t.Logf("client_model=review-asr upstream_body=%s status=%d calls=%d", model, rec.Code, calls.Load())
	if calls.Load() != 1 || rec.Code != 200 || !strings.Contains(model, `"model":"tts"`) {
		t.Errorf("documented fixed model override changed unexpectedly")
	}
}

func TestMediaReviewGenericOperationPreservesPrefixAlias(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"url":"fixture"}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "ReviewPrefix", Kind: "image", BaseURL: upstream.URL, Prefix: "vendor", Models: []config.MediaModel{{Name: "upstream", Alias: "public", Capabilities: []string{"remove-background"}}}, Operations: []config.MediaOperation{{Name: "remove-background", Capability: "remove-background", Method: "POST", Path: "/remove", RequestFormat: "json", ModelMode: "required", ResponseFormat: "passthrough"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "key")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/media/image/remove-background", "application/json", `{"model":"vendor/public","image":"synthetic"}`)
	server.handleMediaOperation(ctx)
	t.Logf("prefixed_alias status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
	if rec.Code != 200 || calls.Load() != 1 {
		t.Errorf("configured prefix alias did not route")
	}
}

func TestMediaReviewCooldownIsScopedToRequestedModel(t *testing.T) {
	var speechCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/asr") {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"asr model rate limit","code":"rate_limit_exceeded"}}`)
			return
		}
		speechCalls.Add(1)
		io.WriteString(w, `{"url":"fixture"}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "ReviewCooldown", Kind: "audio", BaseURL: upstream.URL, Models: []config.MediaModel{{Name: "tts", Alias: "review-tts", Capabilities: []string{"speech"}}, {Name: "asr", Alias: "review-asr", Capabilities: []string{"transcribe"}}}, Operations: []config.MediaOperation{{Name: "speech", Capability: "speech", Method: "POST", Path: "/speech", RequestFormat: "json", ModelMode: "required", ResponseFormat: "passthrough"}, {Name: "transcribe", Capability: "transcribe", Method: "POST", Path: "/asr", RequestFormat: "json", ModelMode: "required", ResponseFormat: "passthrough"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "key")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx1, rec1 := newMediaOperationTestContext(t, server, "POST", "/v1/audio/transcriptions", "application/json", `{"model":"review-asr"}`)
	server.handleMediaOperationFor(ctx1, "audio", "transcribe")
	ctx2, rec2 := newMediaOperationTestContext(t, server, "POST", "/v1/audio/speech", "application/json", `{"model":"review-tts"}`)
	server.handleMediaOperationFor(ctx2, "audio", "speech")
	t.Logf("asr_status=%d speech_status=%d speech_upstream_calls=%d body=%s", rec1.Code, rec2.Code, speechCalls.Load(), rec2.Body.String())
	if rec2.Code != 200 || speechCalls.Load() != 1 {
		t.Errorf("one model cooldown blocked a different model")
	}
}

func TestMediaOperationModelFreeCooldownDoesNotBlockOtherOperation(t *testing.T) {
	var speechCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/clone" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":"clone rate limit"}`)
			return
		}
		speechCalls.Add(1)
		io.WriteString(w, `{"url":"https://example.test/speech.wav"}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "model-free", Kind: "audio", BaseURL: upstream.URL, Operations: []config.MediaOperation{
		{Name: "clone", Method: "POST", Path: "/clone", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough"},
		{Name: "speech", Method: "POST", Path: "/speech", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough"},
	}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "key")
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	for _, op := range []string{"clone", "clone", "speech"} {
		ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/audio/"+op, "application/json", `{}`)
		server.handleMediaOperationFor(ctx, "audio", op)
		if op == "speech" && rec.Code != 200 {
			t.Fatalf("unrelated speech blocked: %d %s", rec.Code, rec.Body)
		}
		if op == "clone" && rec.Code == 200 {
			t.Fatal("failed clone was accepted")
		}
	}
	if speechCalls.Load() != 1 {
		t.Fatalf("speech calls=%d", speechCalls.Load())
	}
}

func TestMediaOperationWeightAffectsRealKeySelection(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.Header.Get("Authorization")]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"url":"https://example.test/result.wav"}`)
	}))
	defer upstream.Close()
	high, zero, three, one := 50, 0, 3, 1
	p := config.MediaProvider{Name: "weighted", Kind: "audio", BaseURL: upstream.URL, APIKeyEntries: []config.MediaAPIKeyEntry{
		{APIKey: "zero", Weight: &zero, Priority: &high}, {APIKey: "three", Weight: &three}, {APIKey: "one", Weight: &one},
	}, Operations: []config.MediaOperation{{Name: "speech", Method: "POST", Path: "/speech", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := coreauth.NewManager(nil, &coreauth.WeightedRoundRobinSelector{}, nil)
	manager.SetConfig(cfg)
	generated, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range generated {
		manager.RegisterExecutor(runtimeexecutor.NewMediaExecutor(a.Provider, cfg))
		if _, err := manager.Register(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	for range 8 {
		ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/audio/speech", "application/json", `{}`)
		server.handleMediaOperationFor(ctx, "audio", "speech")
		if rec.Code != 200 {
			t.Fatalf("request failed: %d %s", rec.Code, rec.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["Bearer zero"] != 0 || counts["Bearer three"] != 6 || counts["Bearer one"] != 2 {
		t.Fatalf("weight not applied: %v", counts)
	}
}

func TestMediaCommittedPollFailuresStopEveryRetryRound(t *testing.T) {
	for _, status := range []int{401, 429, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var submits, polls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "POST" {
					submits.Add(1)
					io.WriteString(w, `{"task_id":"accepted"}`)
					return
				}
				polls.Add(1)
				w.WriteHeader(status)
				io.WriteString(w, `{"error":"poll failed"}`)
			}))
			defer upstream.Close()
			p := config.MediaProvider{Name: "no-replay", Kind: "image", BaseURL: upstream.URL, DisableCooling: true, Operations: []config.MediaOperation{{Name: "remove-background", Method: "POST", Path: "/submit", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough", Async: &config.MediaAsyncOperation{TaskIDPath: "task_id", PollMethod: "GET", PollPath: "/tasks/{task_id}", StatusPath: "status", SuccessValues: []string{"completed"}}}}}
			cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
			manager := registerMediaOperationRuntime(t, cfg, p, "first", "second")
			manager.SetRetryConfig(3, 0, 0)
			server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
			ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/media/image/remove-background", "application/json", `{}`)
			server.handleMediaOperation(ctx)
			if rec.Code != status || submits.Load() != 1 || polls.Load() != 1 {
				t.Fatalf("status=%d submits=%d polls=%d", rec.Code, submits.Load(), polls.Load())
			}
			if !strings.Contains(rec.Body.String(), "accepted") {
				t.Fatal("accepted task identity lost")
			}
			for _, a := range manager.List() {
				if a.Unavailable || len(a.ModelStates) > 0 {
					t.Fatalf("poll failure cooled credential: %+v", a.ModelStates)
				}
			}
		})
	}
}

func TestMediaAcceptedTruncatedSubmitDoesNotReplay(t *testing.T) {
	var submits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, `{"task_id":"accepted"}`)
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "truncated", Kind: "image", BaseURL: upstream.URL, DisableCooling: true, Operations: []config.MediaOperation{{Name: "remove-background", Method: "POST", Path: "/submit", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough"}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "first", "second")
	manager.SetRetryConfig(2, 0, 0)
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/media/image/remove-background", "application/json", `{}`)
	server.handleMediaOperation(ctx)
	if rec.Code == 200 || submits.Load() != 1 {
		t.Fatalf("accepted response replayed: status=%d submits=%d", rec.Code, submits.Load())
	}
}

func TestMediaCallerCancellationNeverReplaysAcceptedTask(t *testing.T) {
	var submits, polls atomic.Int32
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			submits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"task_id":"accepted"}`)
			return
		}
		if polls.Add(1) == 1 {
			close(entered)
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	p := config.MediaProvider{Name: "cancel", Kind: "image", BaseURL: upstream.URL, DisableCooling: true, Operations: []config.MediaOperation{{Name: "remove-background", Method: "POST", Path: "/submit", RequestFormat: "json", ModelMode: "none", ResponseFormat: "passthrough", Async: &config.MediaAsyncOperation{TaskIDPath: "task_id", PollMethod: "GET", PollPath: "/tasks/{task_id}", StatusPath: "status", SuccessValues: []string{"completed"}}}}}
	cfg := &config.Config{MediaProviders: []config.MediaProvider{p}}
	manager := registerMediaOperationRuntime(t, cfg, p, "first", "second")
	manager.SetRetryConfig(3, 0, 0)
	server := &Server{cfg: cfg, handlers: sdkconfigTestBaseHandlers(manager)}
	ctx, rec := newMediaOperationTestContext(t, server, "POST", "/v1/media/image/remove-background", "application/json", `{}`)
	requestCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx.Request = ctx.Request.WithContext(requestCtx)
	done := make(chan struct{})
	go func() { server.handleMediaOperation(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("poll never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("caller cancellation did not stop request")
	}
	if submits.Load() != 1 || polls.Load() != 1 || rec.Code == 200 {
		t.Fatalf("status=%d submits=%d polls=%d", rec.Code, submits.Load(), polls.Load())
	}
	for _, a := range manager.List() {
		if a.Unavailable || len(a.ModelStates) > 0 {
			t.Fatal("cancellation cooled a credential")
		}
	}
}
