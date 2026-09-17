package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecuteStreamSanitizesOverlongInputItemIDs(t *testing.T) {
	longReasoningItemID := "rs_" + strings.Repeat("a", 64)
	longCallItemID := strings.Repeat("grok-call-item-", 6)
	longOutputItemID := strings.Repeat("grok-output-item-", 6)
	encryptedContent := validOpenAIResponsesReasoningEncryptedContentForTest()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","stream":true,"input":[` +
			`{"type":"reasoning","id":"` + longReasoningItemID + `","encrypted_content":"` + encryptedContent + `","summary":[]},` +
			`{"type":"function_call","id":"` + longCallItemID + `","call_id":"call-1","name":"lookup","arguments":"{}"},` +
			`{"type":"function_call_output","id":"` + longOutputItemID + `","call_id":"call-1","output":"ok"},` +
			`{"type":"message","id":"item_74ec40c883248ebb4885ec84","role":"user","content":"continue"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}

	if input := gjson.GetBytes(gotBody, "input").Array(); len(input) != 3 {
		t.Fatalf("upstream input length = %d, want 3: %s", len(input), gotBody)
	}
	if gotType := gjson.GetBytes(gotBody, "input.0.type").String(); gotType != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call: %s", gotType, gotBody)
	}

	for index, testCase := range []struct {
		path       string
		originalID string
	}{
		{path: "input.0.id", originalID: longCallItemID},
		{path: "input.1.id", originalID: longOutputItemID},
	} {
		actual := gjson.GetBytes(gotBody, testCase.path).String()
		if len([]rune(actual)) > 64 || actual == testCase.originalID {
			t.Fatalf("input.%d.id was not shortened to at most 64 characters: %q", index, actual)
		}
	}
	if got := gjson.GetBytes(gotBody, "input.0.call_id").String(); got != "call-1" {
		t.Fatalf("function call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(gotBody, "input.1.call_id").String(); got != "call-1" {
		t.Fatalf("function call output call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(gotBody, "input.2.id").String(); got != "msg_item_74ec40c883248ebb4885ec84" {
		t.Fatalf("message input item ID was not normalized: %q", got)
	}
}

func TestCodexExecutorPreservesLongIDReasoningHistory(t *testing.T) {
	for _, host := range []string{"agentrouter.org", "anyrouter.top", "other.example"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", host, stream), func(t *testing.T) {
				requests := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					requests <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
				}))
				defer server.Close()
				payload := []byte(`{"model":"gpt-6-astra","input":[{"type":"reasoning","id":"rs_` + strings.Repeat("a", 62) + `","encrypted_content":"` + validOpenAIResponsesReasoningEncryptedContentForTest() + `","summary":[{"type":"summary_text","text":"summary stays"}],"content":[{"type":"reasoning_text","text":"content stays"}]},{"type":"message","role":"user","content":"continue"},{"type":"function_call_output","call_id":"call_keep","output":"tool stays"}]}`)
				original := string(payload)
				e := NewCodexExecutor(&config.Config{})
				credential := &cliproxyauth.Auth{ID: "history-fixture", Provider: "codex", ProxyURL: server.URL, Attributes: map[string]string{"api_key": "selected", "base_url": "http://" + host + "/v1"}}
				executeAgentAstraFixture(t, e, credential, cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream})
				got := <-requests
				for _, text := range []string{"summary stays", "content stays", "tool stays"} {
					if !strings.Contains(string(got), text) {
						t.Fatalf("%q lost upstream: %s", text, got)
					}
				}
				if gjson.GetBytes(got, "input.0.id").Exists() || gjson.GetBytes(got, "input.0.encrypted_content").Exists() || string(payload) != original {
					t.Fatal("binding remained or caller input changed")
				}
				if host != "other.example" && gjson.GetBytes(got, "input.0.content.#").Int() != 0 {
					t.Fatal("provider reasoning normalization bypassed")
				}
			})
		}
	}
}
