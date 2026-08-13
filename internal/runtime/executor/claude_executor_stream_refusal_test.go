package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func claudeRefusalTestSSE(stopReason string) string {
	stopDetails := "null"
	if stopReason == "refusal" {
		stopDetails = `{"type":"refusal","category":"cyber","explanation":"blocked by upstream policy"}`
	}
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_refusal","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"PARTIAL_SHOULD_NOT_LEAK"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q,"stop_details":%s},"usage":{"output_tokens":1}}`, stopReason, stopDetails),
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func executeClaudeRefusalTestStream(t *testing.T, responseFormat sdktranslator.Format, upstream string) (*cliproxyexecutor.StreamResult, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(upstream))
	}))
	t.Cleanup(server.Close)

	payload := []byte(`{"model":"opus5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"test"}]}]}`)
	sourceFormat := sdktranslator.FormatOpenAIResponse
	if responseFormat == sdktranslator.FormatClaude {
		sourceFormat = sdktranslator.FormatClaude
		payload = []byte(`{"model":"opus5","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"test"}]}`)
	}
	return NewClaudeExecutor(&config.Config{}).ExecuteStream(
		context.Background(),
		&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}},
		cliproxyexecutor.Request{Model: "opus5", Payload: payload},
		cliproxyexecutor.Options{
			SourceFormat:    sourceFormat,
			ResponseFormat:  responseFormat,
			OriginalRequest: payload,
			Stream:          true,
		},
	)
}

func TestClaudeExecutorOpenAIResponsesRefusalBuffersAndReturnsCredentialFallback(t *testing.T) {
	result, errExecute := executeClaudeRefusalTestStream(t, sdktranslator.FormatOpenAIResponse, claudeRefusalTestSSE("refusal"))
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var payload []byte
	var streamErr error
	for chunk := range result.Chunks {
		payload = append(payload, chunk.Payload...)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if len(payload) != 0 {
		t.Fatalf("refusal leaked translated payload: %s", payload)
	}
	if streamErr == nil {
		t.Fatal("stream error = nil, want typed credential fallback")
	}
	var fallback interface{ IsCredentialFallback() bool }
	if !errors.As(streamErr, &fallback) || fallback == nil || !fallback.IsCredentialFallback() {
		t.Fatalf("stream error = %T %v, want credential fallback", streamErr, streamErr)
	}
	var status interface{ StatusCode() int }
	if !errors.As(streamErr, &status) || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream status = %v, want %d", status, http.StatusBadGateway)
	}
	if got := streamErr.Error(); !strings.Contains(got, `"code":"cyber_policy"`) || strings.Contains(got, "PARTIAL_SHOULD_NOT_LEAK") {
		t.Fatalf("stream error = %q, want cyber policy without partial output", got)
	}
}

func TestClaudeExecutorOpenAIResponsesSuccessReleasesBufferedStream(t *testing.T) {
	result, errExecute := executeClaudeRefusalTestStream(t, sdktranslator.FormatOpenAIResponse, claudeRefusalTestSSE("end_turn"))
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	text := string(payload)
	if !strings.Contains(text, "PARTIAL_SHOULD_NOT_LEAK") || !strings.Contains(text, `"type":"response.completed"`) {
		t.Fatalf("translated success stream incomplete: %s", text)
	}
}

func TestClaudeExecutorOpenAIResponsesNonStreamRefusalReturnsCredentialFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeRefusalTestSSE("refusal")))
	}))
	defer server.Close()

	payload := []byte(`{"model":"opus5","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"test"}]}]}`)
	response, errExecute := NewClaudeExecutor(&config.Config{}).Execute(
		context.Background(),
		&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}},
		cliproxyexecutor.Request{Model: "opus5", Payload: payload},
		cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			OriginalRequest: payload,
		},
	)
	if len(response.Payload) != 0 {
		t.Fatalf("refusal returned translated response: %s", response.Payload)
	}
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want credential fallback")
	}
	var fallback interface{ IsCredentialFallback() bool }
	if !errors.As(errExecute, &fallback) || fallback == nil || !fallback.IsCredentialFallback() {
		t.Fatalf("Execute() error = %T %v, want credential fallback", errExecute, errExecute)
	}
}

func TestClaudeExecutorNativeClaudeRefusalKeepsNativeEvents(t *testing.T) {
	result, errExecute := executeClaudeRefusalTestStream(t, sdktranslator.FormatClaude, claudeRefusalTestSSE("refusal"))
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("native Claude stream error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	text := string(payload)
	if !strings.Contains(text, `"stop_reason":"refusal"`) || !strings.Contains(text, "PARTIAL_SHOULD_NOT_LEAK") {
		t.Fatalf("native Claude refusal was not preserved: %s", text)
	}
}

func TestClaudeExecutorOpenAIResponsesManagerRotatesAfterRefusal(t *testing.T) {
	var mu sync.Mutex
	calls := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if apiKey == "" {
			apiKey = r.Header.Get("X-Api-Key")
		}
		mu.Lock()
		calls = append(calls, apiKey)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if apiKey == "refused-key" {
			_, _ = w.Write([]byte(claudeRefusalTestSSE("refusal")))
			return
		}
		success := strings.ReplaceAll(claudeRefusalTestSSE("end_turn"), "PARTIAL_SHOULD_NOT_LEAK", "SUCCESS_FROM_SECOND_KEY")
		_, _ = w.Write([]byte(success))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
	model := "opus5"
	auths := []*cliproxyauth.Auth{
		{ID: "aa-refused-auth", Provider: "claude", Attributes: map[string]string{"api_key": "refused-key", "base_url": server.URL}},
		{ID: "bb-good-auth", Provider: "claude", Attributes: map[string]string{"api_key": "good-key", "base_url": server.URL}},
	}
	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	}

	payload := []byte(`{"model":"opus5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"test"}]}]}`)
	result, errExecute := manager.ExecuteStream(
		context.Background(),
		[]string{"claude"},
		cliproxyexecutor.Request{Model: model, Payload: payload},
		cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			OriginalRequest: payload,
			Stream:          true,
		},
	)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var output []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("fallback stream error = %v", chunk.Err)
		}
		output = append(output, chunk.Payload...)
	}
	text := string(output)
	if strings.Contains(text, "PARTIAL_SHOULD_NOT_LEAK") || !strings.Contains(text, "SUCCESS_FROM_SECOND_KEY") || !strings.Contains(text, `"type":"response.completed"`) {
		t.Fatalf("fallback output = %s, want only the successful credential response", text)
	}
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	if len(gotCalls) != 2 || gotCalls[0] != "refused-key" || gotCalls[1] != "good-key" {
		t.Fatalf("upstream keys = %v, want [refused-key good-key]", gotCalls)
	}
	refused, _ := manager.GetByID(auths[0].ID)
	if refused.Failed != 1 || refused.Unavailable || !refused.NextRetryAfter.IsZero() || refused.ModelStates[model] != nil {
		t.Fatalf("refused auth state = %#v, want failure count without cooldown", refused)
	}
	good, _ := manager.GetByID(auths[1].ID)
	if good.Success != 1 || good.Failed != 0 {
		t.Fatalf("good auth totals = success=%d failed=%d, want 1/0", good.Success, good.Failed)
	}
}

func TestClaudeExecutorOpenAIResponsesAllCredentialsRefusedReturnsOriginalFallbackError(t *testing.T) {
	var mu sync.Mutex
	calls := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if apiKey == "" {
			apiKey = r.Header.Get("X-Api-Key")
		}
		mu.Lock()
		calls = append(calls, apiKey)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeRefusalTestSSE("refusal")))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
	model := "opus5"
	auths := []*cliproxyauth.Auth{
		{ID: "aa-refused-auth", Provider: "claude", Attributes: map[string]string{"api_key": "refused-key-a", "base_url": server.URL}},
		{ID: "bb-refused-auth", Provider: "claude", Attributes: map[string]string{"api_key": "refused-key-b", "base_url": server.URL}},
	}
	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	}

	payload := []byte(`{"model":"opus5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"test"}]}]}`)
	result, errExecute := manager.ExecuteStream(
		context.Background(),
		[]string{"claude"},
		cliproxyexecutor.Request{Model: model, Payload: payload},
		cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			OriginalRequest: payload,
			Stream:          true,
		},
	)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v, want terminal stream error", errExecute)
	}
	var output []byte
	var streamErr error
	for chunk := range result.Chunks {
		output = append(output, chunk.Payload...)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if len(output) != 0 {
		t.Fatalf("all-refused output leaked payload: %s", output)
	}
	if streamErr == nil {
		t.Fatal("terminal stream error = nil")
	}
	var fallback interface{ IsCredentialFallback() bool }
	if !errors.As(streamErr, &fallback) || fallback == nil || !fallback.IsCredentialFallback() {
		t.Fatalf("terminal error = %T %v, want original credential fallback", streamErr, streamErr)
	}
	if got := streamErr.Error(); !strings.Contains(got, `"code":"cyber_policy"`) {
		t.Fatalf("terminal error = %q, want cyber_policy", got)
	}
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	if len(gotCalls) != 2 || gotCalls[0] != "refused-key-a" || gotCalls[1] != "refused-key-b" {
		t.Fatalf("upstream keys = %v, want [refused-key-a refused-key-b]", gotCalls)
	}
}

func TestShouldBufferClaudeTranslatedStream(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat sdktranslator.Format
		baseURL        string
		want           bool
	}{
		{name: "third party responses relay", responseFormat: sdktranslator.FormatOpenAIResponse, baseURL: "https://agentrouter.org", want: true},
		{name: "official Anthropic responses", responseFormat: sdktranslator.FormatOpenAIResponse, baseURL: "https://api.anthropic.com", want: false},
		{name: "native Claude relay", responseFormat: sdktranslator.FormatClaude, baseURL: "https://agentrouter.org", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldBufferClaudeTranslatedStream(tc.responseFormat, tc.baseURL); got != tc.want {
				t.Fatalf("shouldBufferClaudeTranslatedStream(%q, %q) = %t, want %t", tc.responseFormat, tc.baseURL, got, tc.want)
			}
		})
	}
}
