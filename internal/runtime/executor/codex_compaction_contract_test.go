package executor

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gorilla/websocket"
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

func TestCodexCompactResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		valid       bool
	}{
		{"whole-window", `{"id":"cmp_1","object":"response.compaction","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"retained context"}]},{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}],"usage":{"input_tokens":5,"output_tokens":2}}`, true},
		{"html", `<html>proxy error</html>`, false},
		{"error-object", `{"error":{"code":"bad_request","message":"compact rejected"}}`, false},
		{"missing-window", `{"id":"cmp_1","object":"response.compaction"}`, false},
		{"scalar-window-items", `{"id":"cmp_bad","output":[null,42]}`, false},
		{"malformed-window", `{"id":"cmp_1","object":"response.compaction","output":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses/compact" {
					t.Errorf("wrong path %s", r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(raw, "stream").Exists() {
					t.Error("compact sent stream flag")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer srv.Close()
			credential := &auth.Auth{Provider: "codex", Attributes: map[string]string{"base_url": srv.URL, "api_key": "fixture"}}
			result, err := NewCodexExecutor(&config.Config{}).Execute(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":"history"}`)}, ex.Options{SourceFormat: tr.FormatOpenAIResponse, Alt: "responses/compact"})
			if !tc.valid {
				if err == nil {
					t.Fatalf("invalid compact response accepted: %s", result.Payload)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Payload) != tc.reply {
				t.Errorf("whole window changed: %s", result.Payload)
			}
		})
	}
}

func TestCodexV2CompactionDoesNotInjectImageTool(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cmp\",\"status\":\"completed\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}]}}\n\n")
	}))
	defer srv.Close()
	credential := &auth.Auth{Provider: "codex", Attributes: map[string]string{"base_url": srv.URL, "api_key": "fixture"}}
	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`)}, ex.Options{SourceFormat: tr.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	count, completed := 0, false
	for c := range result.Chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(c.Payload, []byte("data:")))
		if gjson.GetBytes(data, "type").String() == "response.output_item.done" {
			count++
		}
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			completed = true
		}
	}
	if count != 1 || !completed {
		t.Fatalf("compaction events: items=%d completed=%v", count, completed)
	}
	if gjson.GetBytes(captured, `tools.#(type=="image_generation")`).Exists() {
		t.Errorf("compaction injected image tool: %s", captured)
	}
	if !gjson.GetBytes(captured, `input.#(type=="compaction_trigger")`).Exists() {
		t.Error("compaction trigger removed")
	}
}

func TestCodexSummaryToolChoiceNoneDoesNotInjectImageTool(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_summary\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer srv.Close()
	credential := &auth.Auth{Provider: "codex", Attributes: map[string]string{"base_url": srv.URL, "api_key": "fixture"}}
	stream, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":"Summarize history","tools":[],"tool_choice":"none","parallel_tool_calls":false,"max_output_tokens":8000}`)}, ex.Options{SourceFormat: tr.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	for c := range stream.Chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
	}
	if gjson.GetBytes(captured, `tools.#(type=="image_generation")`).Exists() {
		t.Fatalf("tool-disabled request injected image tool: %s", captured)
	}
	if gjson.GetBytes(captured, "max_output_tokens").Exists() {
		t.Error("unsupported native Codex budget forwarded")
	}
}

func TestCodexV2RejectsCompletionWithoutCompactionItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_invalid\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer srv.Close()
	credential := &auth.Auth{Provider: "codex", Attributes: map[string]string{"base_url": srv.URL, "api_key": "fixture"}}
	stream, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[{"type":"compaction_trigger"}]}`)}, ex.Options{SourceFormat: tr.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for c := range stream.Chunks {
		if c.Err != nil {
			streamErr = c.Err
		}
	}
	if streamErr == nil {
		t.Fatal("V2 completed without a compaction item was accepted")
	}
}

func TestCodexPublicResponsesCompactionFields(t *testing.T) {
	var captured []byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_public\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer proxy.Close()
	credential := &auth.Auth{Provider: "codex", ProxyURL: proxy.URL, Attributes: map[string]string{"base_url": "http://api.openai.com/v1", "api_key": "fixture"}}
	raw := []byte(`{"model":"gpt-6-astra","input":"history","context_management":[{"type":"compaction","compact_threshold":1000}],"max_output_tokens":8000,"tools":[],"tool_choice":"none"}`)
	stream, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), credential, ex.Request{Model: "gpt-6-astra", Payload: raw}, ex.Options{SourceFormat: tr.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	for c := range stream.Chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
	}
	if gjson.GetBytes(captured, "context_management.0.compact_threshold").Int() != 1000 || gjson.GetBytes(captured, "max_output_tokens").Int() != 8000 {
		t.Fatalf("public Responses fields discarded: %s", captured)
	}
}

func TestCodexV2CompactionContractAcrossTransports(t *testing.T) {
	for _, transport := range []string{"http", "http-stream", "ws", "ws-stream"} {
		for _, tc := range []struct {
			name               string
			count              int
			missingState, fail bool
			extraMessage       bool
		}{
			{name: "valid", count: 1}, {name: "valid-extra-message", count: 1, extraMessage: true}, {name: "missing-item"}, {name: "duplicate", count: 2}, {name: "missing-state", count: 1, missingState: true}, {name: "upstream-failure", fail: true},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				var events [][]byte
				if tc.extraMessage {
					events = append(events, []byte(`{"type":"response.output_item.done","output_index":9,"item":{"type":"message","role":"assistant","content":[]}}`))
				}
				for i := 0; i < tc.count; i++ {
					state := "opaque"
					if tc.missingState {
						state = ""
					}
					events = append(events, []byte(fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"compaction","encrypted_content":%q}}`, i, state)))
				}
				if tc.fail {
					events = append(events, []byte(`{"type":"response.failed","response":{"id":"resp_cmp","status":"failed","error":{"code":"compact_denied","message":"fixture compact denied"}}}`))
				} else {
					events = append(events, []byte(`{"type":"response.completed","response":{"id":"resp_cmp","status":"completed","output":[]}}`))
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if websocket.IsWebSocketUpgrade(r) {
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						if _, _, err = conn.ReadMessage(); err != nil {
							t.Error(err)
							return
						}
						for _, event := range events {
							if err = conn.WriteMessage(websocket.TextMessage, event); err != nil {
								return
							}
						}
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range events {
						_, _ = w.Write(append(append([]byte("data: "), event...), '\n', '\n'))
					}
				}))
				defer upstream.Close()
				credential := &auth.Auth{ID: "compact-fixture", Provider: "codex", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "fixture"}}
				req := ex.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[{"type":"compaction_trigger"}]}`)}
				opts := ex.Options{SourceFormat: tr.FormatOpenAIResponse}
				cfg := &config.Config{}
				var err error
				if strings.HasSuffix(transport, "stream") {
					var result *ex.StreamResult
					if strings.HasPrefix(transport, "ws") {
						result, err = NewCodexWebsocketsExecutor(cfg).ExecuteStream(context.Background(), credential, req, opts)
					} else {
						result, err = NewCodexExecutor(cfg).ExecuteStream(context.Background(), credential, req, opts)
					}
					if err == nil {
						for c := range result.Chunks {
							if c.Err != nil {
								err = c.Err
							}
						}
					}
				} else if transport == "ws" {
					_, err = NewCodexWebsocketsExecutor(cfg).Execute(context.Background(), credential, req, opts)
				} else {
					_, err = NewCodexExecutor(cfg).Execute(context.Background(), credential, req, opts)
				}
				if strings.HasPrefix(tc.name, "valid") {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if err == nil {
						t.Fatal("invalid compaction accepted")
					}
					if tc.fail && !strings.Contains(err.Error(), "fixture compact denied") {
						t.Errorf("upstream error lost: %v", err)
					}
				}
			})
		}
	}
}
