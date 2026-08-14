package management

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestProviderConnectivityTestClaudeUsesExecutorCompatibilityAndPinnedAuth(t *testing.T) {
	var selectedHits atomic.Int32
	var otherHits atomic.Int32
	var seenBody []byte
	var seenHeader http.Header
	var seenURI string

	selectedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedHits.Add(1)
		seenURI = r.URL.RequestURI()
		seenHeader = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_connectivity","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer selectedServer.Close()
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer otherServer.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	selected := &coreauth.Auth{
		ID:       "claude:selected",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "selected-key",
			"base_url": selectedServer.URL,
		},
	}
	other := &coreauth.Auth{
		ID:       "claude:other",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "other-key",
			"base_url": otherServer.URL,
		},
	}
	if _, errRegister := manager.Register(context.Background(), selected); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), other); errRegister != nil {
		t.Fatalf("register other auth: %v", errRegister)
	}

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	baseURL := selectedServer.URL
	proxyURL := ""
	response, status, errTest := h.performProviderConnectivityTest(context.Background(), providerConnectivityTestRequest{
		Provider:  "claude",
		AuthIndex: selected.EnsureIndex(),
		Model:     "claude-test",
		BaseURL:   &baseURL,
		ProxyURL:  &proxyURL,
		Header: map[string]string{
			"X-AgentRouter-Probe": "enabled",
			"Authorization":       "Bearer wrong-key",
			"x-api-key":           "wrong-key",
		},
	})
	if errTest != nil {
		t.Fatalf("performProviderConnectivityTest() error = %v", errTest)
	}
	if status != http.StatusOK || response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d response=%#v", status, response)
	}
	if selectedHits.Load() != 1 || otherHits.Load() != 0 {
		t.Fatalf("selected hits=%d other hits=%d, want 1/0", selectedHits.Load(), otherHits.Load())
	}
	if seenURI != "/v1/messages?beta=true" {
		t.Fatalf("request URI = %q, want /v1/messages?beta=true", seenURI)
	}
	if got := seenHeader.Get("Authorization"); got != "Bearer selected-key" {
		t.Fatalf("Authorization = %q, want selected credential", got)
	}
	if got := seenHeader.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want selected bearer credential only", got)
	}
	if got := seenHeader.Get("User-Agent"); !strings.HasPrefix(got, "claude-cli/2.1.220") {
		t.Fatalf("User-Agent = %q, want Claude Code 2.1.220", got)
	}
	if got := seenHeader.Get("X-App"); got != "cli" {
		t.Fatalf("X-App = %q, want cli", got)
	}
	if got := seenHeader.Get("Anthropic-Beta"); !strings.Contains(got, "claude-code-20250219") {
		t.Fatalf("Anthropic-Beta = %q, want Claude Code beta", got)
	}
	if got := seenHeader.Get("X-Claude-Code-Session-Id"); got == "" {
		t.Fatal("X-Claude-Code-Session-Id is empty")
	}
	if got := seenHeader.Get("X-AgentRouter-Probe"); got != "enabled" {
		t.Fatalf("custom header = %q, want enabled", got)
	}
	if got := gjson.GetBytes(seenBody, "model").String(); got != "claude-test" {
		t.Fatalf("upstream model = %q, want claude-test", got)
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); !strings.Contains(got, "cc_version=2.1.220") {
		t.Fatalf("Claude Code billing identity missing from body: %s", seenBody)
	}
}

func TestProviderConnectivityTestClaudeUsesUnsavedFormOverrides(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_connectivity","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	baseURL := server.URL
	apiKey := "unsaved-key"
	h := &Handler{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:  "unsaved-key",
		BaseURL: server.URL,
		Cloak:   &config.CloakConfig{Mode: "never"},
	}}}}
	response, status, errTest := h.performProviderConnectivityTest(context.Background(), providerConnectivityTestRequest{
		Provider: "claude",
		Model:    "claude-test",
		APIKey:   &apiKey,
		BaseURL:  &baseURL,
		Cloak: &providerConnectivityClaudeCloak{
			Mode:       "always",
			StrictMode: true,
		},
	})
	if errTest != nil || status != http.StatusOK || response.StatusCode != http.StatusOK {
		t.Fatalf("response=%#v status=%d err=%v", response, status, errTest)
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); !strings.Contains(got, "cc_version=2.1.220") {
		t.Fatalf("unsaved cloak override was not applied: %s", seenBody)
	}
}

func TestProviderConnectivityTestClaudeReturnsUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"client restricted"}}`))
	}))
	defer server.Close()

	baseURL := server.URL
	apiKey := "restricted-key"
	h := &Handler{cfg: &config.Config{}}
	response, status, errTest := h.performProviderConnectivityTest(context.Background(), providerConnectivityTestRequest{
		Provider: "claude",
		Model:    "claude-test",
		APIKey:   &apiKey,
		BaseURL:  &baseURL,
	})
	if errTest != nil {
		t.Fatalf("performProviderConnectivityTest() error = %v", errTest)
	}
	if status != http.StatusOK || response.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d response=%#v", status, response)
	}
	if !strings.Contains(response.Body, "client restricted") {
		t.Fatalf("response body = %q, want upstream error", response.Body)
	}
}

func TestProviderConnectivityTestClaudeResolvesConfiguredAliasForPinnedAuth(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_connectivity","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	selected := &coreauth.Auth{
		ID:       "claude:selected-alias",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":      "selected-key",
			"base_url":     server.URL,
			"config_index": "0",
			"source":       "config:claude:test",
		},
	}
	if _, errRegister := manager.Register(context.Background(), selected); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}

	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:  "selected-key",
		BaseURL: server.URL,
		Models:  []config.ClaudeModel{{Name: "claude-upstream", Alias: "public-alias"}},
	}}}
	manager.SetConfig(cfg)
	if got := manager.ResolveExecutionModel(selected, "public-alias"); got != "claude-upstream" {
		t.Fatalf("manager alias resolution = %q, want claude-upstream", got)
	}
	h := &Handler{cfg: cfg, authManager: manager}
	response, status, errTest := h.performProviderConnectivityTest(context.Background(), providerConnectivityTestRequest{
		Provider:  "claude",
		AuthIndex: selected.EnsureIndex(),
		Model:     "public-alias",
	})
	if errTest != nil || status != http.StatusOK || response.StatusCode != http.StatusOK {
		t.Fatalf("response=%#v status=%d err=%v", response, status, errTest)
	}
	if got := gjson.GetBytes(seenBody, "model").String(); got != "claude-upstream" {
		t.Fatalf("upstream model = %q, want claude-upstream", got)
	}
}
