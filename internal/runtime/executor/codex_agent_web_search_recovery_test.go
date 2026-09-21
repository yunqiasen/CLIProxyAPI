package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAgentResourceRecoveryRetainsWebSearchHistory(t *testing.T) {
	runAgentWebSearchRecovery(t, false)
}

func TestAgentEncryptedRecoveryRetainsWebSearchHistory(t *testing.T) {
	runAgentWebSearchRecovery(t, true)
}

func runAgentWebSearchRecovery(t *testing.T, encryptedFirst bool) {
	t.Helper()
	payload := `{"model":"gpt-6-astra","input":[{"type":"message","id":"msg_old","role":"assistant","content":[{"type":"output_text","text":"Earlier step completed"}]},{"type":"web_search_call","id":"ws_old_resource","status":"completed","action":{"type":"search","query":"retained search query"}},{"type":"message","id":"msg_client","role":"user","content":"continue"}]}`
	const rejected = `{"error":{"type":"invalid_request_error","param":"","message":"OpenAI Responses bad request: The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`
	if encryptedFirst {
		payload = strings.Replace(payload, `"input":[`, `"input":[{"type":"reasoning","id":"rs_old","encrypted_content":"`+validCodexReasoningEncryptedContentForTest()+`","summary":[{"type":"summary_text","text":"retained summary"}]},`, 1)
	}
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if r.Header.Get("Authorization") != "Bearer selected-key" {
			t.Error("credential changed during recovery")
		}
		if encryptedFirst && gjson.GetBytes(body, "input.0.encrypted_content").Exists() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","param":"","message":"OpenAI Responses bad request: The encrypted content for item rs_old could not be verified. Reason: Encrypted content could not be decrypted or parsed. [trace_id=fixture]"}}`)
			return
		}
		for _, item := range gjson.GetBytes(body, "input").Array() {
			if (item.Get("type").String() == "message" && item.Get("role").String() == "assistant") || item.Get("type").String() == "web_search_call" {
				if item.Get("id").Exists() {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, rejected)
					return
				}
			}
		}
		if !strings.Contains(string(body), "retained search query") || !strings.Contains(string(body), "Earlier step completed") || !strings.Contains(string(body), "msg_client") {
			t.Error("portable history was lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
	}))
	defer upstream.Close()
	credential := &auth.Auth{ID: "agent-web-search", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"base_url": "http://agentrouter.org/v1", "api_key": "selected-key"}}
	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: []byte(payload)}, ex.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("old conversation failed after %d upstream attempts: %v", calls, err)
	}
	var output strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if calls != 2 || !strings.Contains(output.String(), "response.completed") || !strings.Contains(output.String(), "OK") {
		t.Fatalf("calls=%d output=%s", calls, output.String())
	}
}
