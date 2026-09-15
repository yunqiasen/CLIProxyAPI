package executor

import (
	"context"
	"encoding/base64"
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

func TestCodexRouteStatePreservesSameRouteAndCleansChangedKey(t *testing.T) {
	token := make([]byte, 73)
	token[0] = 0x80
	encrypted := base64.URLEncoding.EncodeToString(token)
	var seen [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_route\",\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"id\":\"rs_fixture\",\"encrypted_content\":\""+encrypted+"\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"readable summary\"}]}]}}\n\n")
	}))
	defer upstream.Close()
	credential := &auth.Auth{ID: "route-a", Provider: "codex", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "key-a"}}
	options := ex.Options{SourceFormat: tr.FormatOpenAIResponse, Metadata: map[string]any{ex.ExecutionSessionMetadataKey: t.Name()}}
	execute := func(payload string) {
		stream, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: []byte(payload)}, options)
		if err != nil {
			t.Fatal(err)
		}
		for c := range stream.Chunks {
			if c.Err != nil {
				t.Fatal(c.Err)
			}
		}
	}
	execute(`{"model":"gpt-6-astra","input":"hello"}`)
	history := `{"model":"gpt-6-astra","input":[{"type":"reasoning","id":"rs_fixture","encrypted_content":"` + encrypted + `","summary":[{"type":"summary_text","text":"readable summary"}]},{"role":"user","content":"keep this"}]}`
	execute(history)
	if !gjson.GetBytes(seen[1], "input.0.encrypted_content").Exists() {
		t.Fatal("same-route state removed")
	}
	credential = &auth.Auth{ID: "route-b", Provider: "codex", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "key-b"}}
	execute(history)
	if gjson.GetBytes(seen[2], "input.0.encrypted_content").Exists() {
		t.Fatal("foreign reasoning still forwarded after key switch")
	}
	if !strings.Contains(string(seen[2]), "readable summary") || !strings.Contains(string(seen[2]), "keep this") {
		t.Fatal("portable history lost")
	}
}

func TestNativeCodexPatchToolsStayNative(t *testing.T) {
	for _, site := range []string{"native", "Any", "Agent"} {
		t.Run(site, func(t *testing.T) {
			var captured []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_patch","status":"completed","output":[{"type":"custom_tool_call","id":"ctc_patch","call_id":"call_patch","name":"apply_patch","input":"PATCH"}]}}`+"\n\n")
			}))
			defer server.Close()
			payload := []byte(`{"model":"gpt-6-astra","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}],"input":[{"type":"custom_tool_call","call_id":"prior","name":"apply_patch","input":"PATCH"},{"type":"custom_tool_call_output","call_id":"prior","output":"OK"}]}`)
			a := &auth.Auth{ID: site, Provider: "codex", Label: site, Attributes: map[string]string{"api_key": "fixture", "base_url": server.URL}}
			resp, err := NewCodexExecutor(&config.Config{}).Execute(context.Background(), a, ex.Request{Model: "gpt-6-astra", Payload: payload}, ex.Options{SourceFormat: tr.FormatOpenAIResponse})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"tools.0", "input.0", "input.1"} {
				if gjson.GetBytes(captured, path).Raw != gjson.GetBytes(payload, path).Raw {
					t.Fatalf("native contract changed at %s: %s", path, captured)
				}
			}
			if gjson.GetBytes(resp.Payload, `output.#(type=="custom_tool_call").input`).String() != "PATCH" {
				t.Fatalf("native output changed: %s", resp.Payload)
			}
		})
	}
}
