package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestProviderConnectivityCodexDraftUsesProductionSynthesis(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer selected-key" {
			t.Error("selected credential replaced")
		}
		if strings.Contains(string(b), "max_output_tokens") {
			t.Error("probe bypassed production Responses request translation")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_probe\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()
	cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "selected-key", BaseURL: upstream.URL, ResponsesFirstOutputTimeoutSeconds: 120, Models: []config.CodexModel{{Name: "raw-model", Alias: "alias-model"}}}}}
	m := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: "selected", Provider: "codex", Attributes: map[string]string{"api_key": "selected-key", "base_url": upstream.URL, "config_index": "0", coreauth.AttributeResponsesFirstOutputTimeoutSeconds: "120"}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, authManager: m}
	// The API payload deliberately carries an unrecognized draft field on the baseline.
	input := map[string]any{"provider": "codex", "auth_index": a.EnsureIndex(), "model": "alias-model", "codex_config": map[string]any{"responses-first-output-timeout-seconds": 0}}
	raw, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v0/management/provider-connectivity-test", strings.NewReader(string(raw)))
	h.ProviderConnectivityTest(c)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var req providerConnectivityTestRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	auth, _, err := h.codexConnectivityAuth(req)
	if err != nil {
		t.Fatal(err)
	}
	if auth.Attributes[coreauth.AttributeResponsesFirstOutputTimeoutSeconds] != "" {
		t.Fatal("unsaved first-output setting not applied by production synthesis")
	}
	if got, _ := m.GetByID(a.ID); got.Attributes[coreauth.AttributeResponsesFirstOutputTimeoutSeconds] != "120" {
		t.Fatal("probe mutated saved auth")
	}
}

func TestProviderConnectivityCodexDraftOverridesRoutingWithoutChangingSavedState(t *testing.T) {
	for _, headerOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("header_only_%t", headerOnly), func(t *testing.T) {
			var calls int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				b, _ := io.ReadAll(r.Body)
				if r.Header.Get("X-Draft") != "new" || r.Header.Get("X-Stale") != "" {
					t.Errorf("draft headers not applied")
				}
				if gjson.GetBytes(b, "model").String() != "gpt-6-astra" || gjson.GetBytes(b, "reasoning.effort").String() != "high" {
					t.Errorf("draft alias/thinking mismatch: model=%s effort=%s", gjson.GetBytes(b, "model"), gjson.GetBytes(b, "reasoning.effort"))
				}
				if r.Header.Get("Authorization") != "Bearer selected-key" {
					t.Error("unselected key used")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_probe\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer upstream.Close()
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "selected-key", BaseURL: "http://127.0.0.1:1", ProxyURL: "http://127.0.0.1:1", Headers: map[string]string{"X-Stale": "old"}, ResponsesFirstOutputTimeoutSeconds: 120}}}
			m := coreauth.NewManager(nil, nil, nil)
			a := &coreauth.Auth{ID: "selected-draft", Provider: "codex", ProxyURL: cfg.CodexKey[0].ProxyURL, Attributes: map[string]string{"api_key": "selected-key", "base_url": cfg.CodexKey[0].BaseURL, "config_index": "0"}}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, authManager: m}
			draft := map[string]any{"base-url": upstream.URL, "proxy-url": "direct", "headers": map[string]string{"X-Draft": "new"}, "responses-first-output-timeout-seconds": 0, "models": []map[string]any{{"name": "gpt-6-astra", "alias": "cpa-6a", "thinking": map[string]any{"levels": []string{"high"}}}}, "api-key-entries": []map[string]string{{"api-key": "wrong-key"}}}
			req := providerConnectivityTestRequest{Provider: "codex", AuthIndex: a.EnsureIndex(), Model: "cpa-6a(high)"}
			if headerOnly {
				req.AuthIndex = ""
				draft["headers"] = map[string]string{"X-Draft": "new", "Authorization": "Bearer selected-key"}
			}
			req.CodexConfig, _ = json.Marshal(draft)
			response, _, err := h.performProviderConnectivityTest(context.Background(), req)
			if err != nil || response.StatusCode != 200 || calls != 1 {
				t.Fatalf("calls=%d status=%d err=%v", calls, response.StatusCode, err)
			}
			if cfg.CodexKey[0].ResponsesFirstOutputTimeoutSeconds != 120 || cfg.CodexKey[0].BaseURL == upstream.URL {
				t.Error("draft mutated saved provider")
			}
		})
	}
}

func TestProviderConnectivityAndProductionShareAnyAgentPipeline(t *testing.T) {
	for _, scenario := range []struct {
		host     string
		resource bool
		masked   bool
		anyItems bool
	}{
		{host: "anyrouter.top"},
		{host: "anyrouter.top", anyItems: true},
		{host: "agentrouter.org"},
		{host: "agentrouter.org", resource: true},
		{host: "agentrouter.org", resource: true, masked: true},
	} {
		host := scenario.host
		t.Run(fmt.Sprintf("%s/resource_%t/masked_%t/items_%t", host, scenario.resource, scenario.masked, scenario.anyItems), func(t *testing.T) {
			var requests [][]byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				requests = append(requests, b)
				if scenario.resource && (gjson.GetBytes(b, "input.0.content.#").Int() != 0 || gjson.GetBytes(b, "input.0.summary.0.text").String() != "retained reasoning") {
					t.Error("Agent reasoning compatibility missing from production/probe")
				}
				if r.Header.Get("Authorization") != "Bearer selected-key" {
					t.Error("wrong key")
				}
				if gjson.GetBytes(b, "model").String() != "gpt-6-astra" || gjson.GetBytes(b, "reasoning.effort").String() != "high" || gjson.GetBytes(b, "max_output_tokens").Exists() {
					t.Error("probe/production preparation differs")
				}
				if scenario.anyItems {
					if gjson.GetBytes(b, "input.0.id").Exists() {
						w.WriteHeader(400)
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","param":"","message":"bad response status code 400 (request id: fixture)"}}`)
						return
					}
					if gjson.GetBytes(b, "input.0.content").String() != "Keep prior answer" || gjson.GetBytes(b, "input.2.output").String() != "Keep result" || gjson.GetBytes(b, "input.1.call_id").String() != "keep_call" {
						t.Error("Any history lost")
					}
				} else if host == "anyrouter.top" {
					if gjson.GetBytes(b, "input.0.content.0.text").String() == "" || gjson.GetBytes(b, "input.0.type").String() != "message" || gjson.GetBytes(b, "input.1.summary.0.text").String() != "preserved thinking" {
						t.Error("Any history repair missing from entrypoint")
					}
				} else if gjson.GetBytes(b, "input.0.encrypted_content").Exists() {
					w.WriteHeader(400)
					message := "OpenAI Responses bad request: The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed. [trace_id=parity]"
					if scenario.resource {
						message = "The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. [trace_id=parity]"
					}
					if scenario.masked {
						message = "OpenAI Responses bad request: " + strings.Replace(message, "Azure", "***", 1)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": nil, "param": "", "message": message}})
					return
				}
				if host == "agentrouter.org" {
					found := false
					for _, item := range gjson.GetBytes(b, "input").Array() {
						if item.Get("type").String() == "web_search_call" {
							t.Error("production/probe retained a native search bound to the rejected resource")
						}
						found = found || strings.Contains(item.Get("content.0.text").String(), "retained parity search")
					}
					if !found {
						t.Error("production/probe dropped readable search history")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ok\",\"status\":\"in_progress\"}}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			defer upstream.Close()
			history := `[ {"type":"web_search_call","status":"completed","action":{"type":"search","query":"fixture"}}, {"type":"reasoning","content":[{"type":"reasoning_text","text":"preserved thinking"}]}, {"role":"user","content":"reply OK"}]`
			if host == "agentrouter.org" {
				token := make([]byte, 73)
				token[0] = 0x80
				history = `[{"type":"reasoning","id":"rs_foreign","encrypted_content":"` + base64.URLEncoding.EncodeToString(token) + `"},{"role":"user","content":"reply OK"}]`
			}
			if scenario.resource {
				history = strings.Replace(history, `"id":"rs_foreign"`, `"id":"rs_foreign","content":[{"type":"reasoning_text","text":"retained reasoning"}]`, 1)
			}
			if host == "agentrouter.org" {
				history = strings.TrimSuffix(history, "]") + `,{"type":"web_search_call","id":"ws_parity","status":"completed","action":{"type":"open_page","url":"https://example.test/retained-parity-search"},"results":[{"text":"retained parity search"}]}]`
			}
			if scenario.anyItems {
				history = `[{"type":"message","id":"msg_agent","role":"assistant","content":"Keep prior answer"},{"type":"function_call","id":"fc_agent","name":"fixture","call_id":"keep_call","arguments":"{}"},{"type":"function_call_output","id":"fco_old","call_id":"keep_call","output":"Keep result"},{"role":"user","content":"Continue"}]`
			}
			cfg := &config.Config{CodexKey: []config.CodexKey{{Name: "fixture", APIKey: "selected-key", BaseURL: "http://" + host + "/v1", ProxyURL: upstream.URL, DisableImageGeneration: true, Models: []config.CodexModel{{Name: "gpt-6-astra", Alias: "cpa-6a", Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}}}}, Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "cpa-6a", Protocol: "codex", FromProtocol: sdktranslator.FormatOpenAIResponse.String()}}, Params: map[string]any{"input": history}}}}}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			a := auths[0]
			m := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			m.SetConfig(cfg)
			m.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			if _, err = m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "cpa-6a"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			payload := []byte(`{"model":"cpa-6a","input":"Hi","max_output_tokens":256,"stream":true,"store":false,"reasoning":{"effort":"low"}}`)
			stream, err := m.ExecuteStream(context.Background(), []string{"codex"}, coreexecutor.Request{Model: "cpa-6a(high)", Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: true, Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "cpa-6a(high)", coreexecutor.RequestPathMetadataKey: "/v1/responses"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = collectProviderConnectivityStream(context.Background(), stream); err != nil {
				t.Fatal(err)
			}
			productionCalls := len(requests)
			h := &Handler{cfg: cfg, authManager: m}
			raw, _ := json.Marshal(map[string]any{"provider": "codex", "auth_index": a.EnsureIndex(), "model": "cpa-6a(high)"})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v0/management/provider-connectivity-test", bytes.NewReader(raw))
			h.ProviderConnectivityTest(c)
			var response providerConnectivityTestResponse
			if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || response.StatusCode != 200 || !strings.Contains(response.Body, "response.completed") {
				t.Fatalf("probe status=%d upstream=%d", w.Code, response.StatusCode)
			}
			frames := strings.Split(strings.TrimSpace(response.Body), "\n\n")
			if len(frames) != 2 {
				t.Fatalf("probe merged SSE events without framing: got %d frames", len(frames))
			}
			for _, frame := range frames {
				var eventData string
				for _, line := range strings.Split(frame, "\n") {
					if strings.HasPrefix(line, "data: ") {
						eventData += strings.TrimPrefix(line, "data: ")
					}
				}
				if !json.Valid([]byte(eventData)) {
					t.Fatalf("invalid SSE event JSON: %s", frame)
				}
			}
			want := 1
			if host == "agentrouter.org" || scenario.anyItems {
				want = 2
			}
			if productionCalls != want || len(requests) != 2*want {
				t.Fatalf("production/probe calls=%d/%d want=%d", productionCalls, len(requests)-productionCalls, want)
			}
			if !reflect.DeepEqual(gjson.GetBytes(requests[want-1], "input").Value(), gjson.GetBytes(requests[len(requests)-1], "input").Value()) {
				t.Error("probe and production outgoing histories diverged")
			}
		})
	}
}

func TestCodexProbeRecoversOnlyUnchangedSavedPaymentCooldown(t *testing.T) {
	for _, scenario := range []string{"completed", "saved_ui_draft", "failed", "edited_draft", "changed_during_probe", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := coreauth.NewManager(nil, nil, nil)
			var cfg *config.Config
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "changed_during_probe" {
					cfg.CodexKey[0].Headers = map[string]string{"X-Changed": "true"}
				}
				if scenario == "canceled" {
					cancel()
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if scenario == "failed" {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"budget exhausted\"}}}\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"probe\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer upstream.Close()
			cfg = &config.Config{CodexKey: []config.CodexKey{{APIKey: "selected-key", BaseURL: upstream.URL, Models: []config.CodexModel{{Name: "upstream", Alias: "public"}}}}}
			a := &coreauth.Auth{ID: "recovery-selected", Provider: "codex", Attributes: map[string]string{"api_key": "selected-key", "base_url": upstream.URL, "config_index": "0"}}
			for _, id := range []string{a.ID, "other-key"} {
				clone := a.Clone()
				clone.ID = id
				if id != a.ID {
					clone.Attributes["api_key"] = "other-key"
				}
				if _, err := m.Register(ctx, clone); err != nil {
					t.Fatal(err)
				}
				m.MarkResult(ctx, coreauth.Result{AuthID: id, Model: "public", Error: &coreauth.Error{HTTPStatus: 402, Message: "budget exhausted"}})
			}
			m.SetConfig(cfg)
			h := &Handler{cfg: cfg, authManager: m}
			req := providerConnectivityTestRequest{Provider: "codex", AuthIndex: a.EnsureIndex(), Model: "public"}
			if scenario == "saved_ui_draft" {
				req.CodexConfig = json.RawMessage(`{"name":null,"priority":null,"weight":null,"prefix":null,"base-url":"` + upstream.URL + `","proxy-url":null,"headers":null,"models":[{"name":"upstream","alias":"public"}],"excluded-models":null,"disable-cooling":null,"websockets":null,"disable-image-generation":null,"responses-first-output-timeout-seconds":null}`)
			}
			if scenario == "edited_draft" {
				req.CodexConfig = json.RawMessage(`{"headers":{"X-Draft":"unsaved"}}`)
			}
			_, _, _ = h.performProviderConnectivityTest(ctx, req)
			got, _ := m.GetByID(a.ID)
			if cleared := !got.ModelStates["public"].Unavailable; cleared != (scenario == "completed" || scenario == "saved_ui_draft") {
				t.Fatalf("cooldown cleared=%v", cleared)
			}
			other, _ := m.GetByID("other-key")
			if !other.ModelStates["public"].Unavailable {
				t.Fatal("other credential was resumed")
			}
		})
	}
}

func TestCompletedCodexProbeRequiresValidTerminalEvent(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"complete", `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`, true},
		{"event_name", "event: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"done", "data: [DONE]\n\n", false},
		{"partial", `data: {"type":"response.output_text.delta","delta":"hi"}`, false},
		{"no_status", `data: {"type":"response.completed","response":{}}`, false},
		{"error_after_complete", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\ndata: {\"type\":\"error\",\"error\":{\"message\":\"fail\"}}\n\n", false},
		{"incomplete", `data: {"type":"response.completed","response":{"status":"incomplete"}}`, false},
		{"error_event_with_completed_type", "event: error\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", false},
		{"malformed", "data: {\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := completedCodexProbe([]byte(tc.payload)); got != tc.want {
				t.Fatalf("completed=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestCodexConnectivityCancellationReachesUpstream(t *testing.T) {
	received := make(chan struct{})
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"probe\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		close(received)
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	h := &Handler{cfg: &config.Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := "selected"
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = h.performProviderConnectivityTest(ctx, providerConnectivityTestRequest{Provider: "codex", Model: "m", APIKey: &key, BaseURL: &upstream.URL})
	}()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("probe not received")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive cancellation")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("probe handler did not finish")
	}
}
