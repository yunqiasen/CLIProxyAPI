package management

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeResponsesCompatibilityLog(t *testing.T, dir, suffix, upstream, response string) requestLogCandidate {
	t.Helper()
	stamp, timestamp := requestLogTestTimestamp(0)
	name := fmt.Sprintf("v1-responses-%s-%s.log", stamp, suffix)
	path := filepath.Join(dir, name)
	if upstream == "" {
		upstream = "=== API REQUEST 1 ===\nUpstream URL: https://relay.example/v1/responses\nAuth: provider=codex, provider_name=FixtureRelay, auth_id=codex:fixture, type=api_key\nBody:\n{\"model\":\"upstream-fixture\",\"input\":\"hello\"}\n"
	}
	contentType := "text/event-stream"
	if strings.HasPrefix(strings.TrimSpace(response), "{") {
		contentType = "application/json"
	}
	content := "=== REQUEST INFO ===\nURL: /v1/responses\nMethod: POST\nTimestamp: " + timestamp + "\n\n=== REQUEST BODY ===\n{\"model\":\"compat-model\",\"input\":\"hello\"}\n\n" + upstream + "\n=== RESPONSE ===\nStatus: 200\nContent-Type: " + contentType + "\n\n" + response + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return requestLogCandidate{name: name, path: path, size: info.Size(), modTime: info.ModTime(), logTime: info.ModTime()}
}

func TestRequestLogResponsesOutcomes(t *testing.T) {
	failed := `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_responses_request","message":"invalid codex request"}}}` + "\n\n"
	completed := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}}` + "\n\n"
	upstreamQuota := "=== API REQUEST 1 ===\nUpstream URL: https://relay.example/v1/responses\nAuth: provider=codex, provider_name=FixtureRelay, auth_id=codex:fixture, type=api_key\nBody:\n{\"model\":\"upstream-fixture\"}\n\n=== API RESPONSE 1 ===\nStatus: 200\nBody:\ndata: {\"type\":\"error\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit exceeded\"}}\n\n"
	tests := []struct {
		name        string
		upstream    string
		response    string
		wantSuccess bool
		wantError   string
		wantOutput  string
	}{
		{"nested SSE failure", "", failed, false, "invalid codex request", "invalid codex request"},
		{"partial output then failure", "", `data: {"type":"response.output_text.delta","delta":"partial answer"}` + "\n\n" + failed, false, "invalid codex request", "partial answer"},
		{"JSON failed response", "", `{"object":"response","status":"failed","error":{"message":"JSON upstream failure","code":"server_error"}}`, false, "JSON upstream failure", "JSON upstream failure"},
		{"nested failure without message", "", `data: {"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content"}}}` + "\n\n", false, "invalid_encrypted_content", "invalid_encrypted_content"},
		{"failure without details", "", `data: {"type":"response.failed","response":{"status":"failed"}}` + "\n\n", false, "response.failed", "response.failed"},
		{"multiline failure", "", "event: response.failed\r\ndata: {\"type\":\"response.failed\",\r\ndata: \"response\":{\"status\":\"failed\",\"error\":{\"message\":\"multiline failure\"}}}\r\n\r\n", false, "multiline failure", "multiline failure"},
		{"upstream-only quota", upstreamQuota, `data: {"type":"response.created","response":{"id":"fixture"}}` + "\n\n", false, "Rate limit exceeded", ""},
		{"completed response beats earlier error", "=== API ERROR RESPONSE ===\nStatus: 429\n\n{\"error\":{\"message\":\"earlier quota\"}}\n\n", completed, true, "", "OK"},
		{"legacy JSON success beats earlier error", "=== API ERROR RESPONSE ===\nStatus: 429\n\n{\"error\":{\"message\":\"earlier quota\"}}\n\n", `{"output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`, true, "", "OK"},
		{"successful response after upstream error", upstreamQuota, completed, true, "", "OK"},
		{"wrapped JSON completion", upstreamQuota, `{"response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}}`, true, "", "OK"},
		{"multiline text", "", "data: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"multiline answer\"}\n\n" + completed, true, "", "multiline answer"},
		{"legacy unseparated data lines", "", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"legacy \"}\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n" + completed, true, "", "legacy answer"},
		{"data before event failure", "", "data: {}\nevent: response.failed\n\n", false, "response.failed", "response.failed"},
		{"data before event text and completion", upstreamQuota, "data: {\"delta\":\"field order answer\"}\r\nevent: response.output_text.delta\r\n\r\ndata: {\"response\":{\"output\":[]}}\r\nevent: response.completed\r\n\r\n", true, "", "field order answer"},
		{"last event field wins", "", "event: response.created\ndata: {}\nevent: response.failed\n\n", false, "response.failed", "response.failed"},
		{"legacy unseparated event fields", upstreamQuota, "event: response.output_text.delta\ndata: {\"delta\":\"legacy event answer\"}\nevent: response.completed\ndata: {\"response\":{\"output\":[]}}\n", true, "", "legacy event answer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := writeResponsesCompatibilityLog(t, t.TempDir(), "outcome", tc.upstream, tc.response)
			got, err := parseRequestLogFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if got.Success != tc.wantSuccess || got.HasError == tc.wantSuccess {
				t.Errorf("success=%t has_error=%t error=%q output=%q", got.Success, got.HasError, got.error, got.output)
			}
			if tc.wantSuccess && got.error != "" {
				t.Errorf("successful request retained a prior attempt error: %q", got.error)
			}
			if !strings.Contains(got.ErrorPreview, tc.wantError) || !strings.Contains(got.error, tc.wantError) {
				t.Errorf("missing error %q in preview=%q detail=%q", tc.wantError, got.ErrorPreview, got.error)
			}
			if !strings.Contains(got.output, tc.wantOutput) {
				t.Errorf("missing output %q in %q", tc.wantOutput, got.output)
			}
		})
	}
}

func TestRequestLogResponsesBuiltInTools(t *testing.T) {
	for _, toolType := range []string{"tool_search_call", "web_search_call", "file_search_call", "image_generation_call", "code_interpreter_call", "computer_call", "mcp_call", "local_shell_call", "shell_call"} {
		t.Run(toolType, func(t *testing.T) {
			response := fmt.Sprintf("data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\",\"output\":[{\"type\":%q,\"status\":\"completed\",\"arguments\":{\"query\":\"private tool args\"},\"text\":\"internal tool text\"}]}}\n\n", toolType)
			candidate := writeResponsesCompatibilityLog(t, t.TempDir(), "tools", "", response)
			got, err := parseRequestLogFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Success || got.HasError || !strings.Contains(got.output, "仅工具调用") {
				t.Fatalf("valid tool turn: success=%t has_error=%t output=%q", got.Success, got.HasError, got.output)
			}
			if len(got.calledTools) != 1 || got.calledTools[0].Type != toolType {
				t.Errorf("called tools=%#v want one %s", got.calledTools, toolType)
			}
			if strings.Contains(got.output, "private tool args") || strings.Contains(got.output, "internal tool text") {
				t.Errorf("tool data appeared as an assistant answer: %q", got.output)
			}
		})
	}
}

func TestRequestLogResponsesRefreshesRevisionTwo(t *testing.T) {
	dir := t.TempDir()
	response := `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}}` + "\n\n"
	candidate := writeResponsesCompatibilityLog(t, dir, "revisiontwo", "", response)
	before, err := os.ReadFile(candidate.path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRequestLogStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	ctx := context.Background()
	if err := syncRequestLogStore(ctx, store, dir); err != nil {
		t.Fatal(err)
	}
	id := requestLogIDFromFilename(candidate.name)
	if _, err := store.db.ExecContext(ctx, `UPDATE request_log_entries SET parser_revision=2, success=1, has_error=0, error_preview='', error='' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := syncRequestLogStore(ctx, store, dir); err != nil {
		t.Fatal(err)
	}
	items, total, err := store.list(ctx, requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Success || !items[0].HasError {
		t.Fatalf("stale parser outcome: total=%d items=%#v", total, items)
	}
	usage, err := store.apiKeyUsageByAuthID(ctx, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := usage["codex:fixture"]; got.Success != 0 || got.Failed != 1 {
		t.Errorf("refreshed provider totals=%#v", got)
	}
	after, err := os.ReadFile(candidate.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("raw log changed during parser refresh")
	}
}

func TestRequestLogResponsesPreservesDeltaWhitespace(t *testing.T) {
	for _, tc := range []struct{ name, pattern string }{
		{"responses", `{"type":"response.output_text.delta","delta":%q}`},
		{"chat", `{"choices":[{"delta":{"content":%q}}]}`},
		{"claude", `{"type":"content_block_delta","delta":{"type":"text_delta","text":%q}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream strings.Builder
			for _, delta := range []string{"hello", " ", "world", "\n", "next line"} {
				fmt.Fprintf(&stream, "data: "+tc.pattern+"\n\n", delta)
			}
			if got := extractTextFromResponseBody(stream.String()); got != "hello world\nnext line" {
				t.Fatalf("delta whitespace changed: %q", got)
			}
		})
	}
}

func TestRequestLogResponsesToolOnlyAcrossProtocols(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"legacy chat", `{"choices":[{"message":{"role":"assistant","function_call":{"name":"fixture_tool","arguments":"{}"}},"finish_reason":"function_call"}]}`},
		{"chat", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"fixture_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{"claude", `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"fixture","name":"fixture_tool","input":{}}],"stop_reason":"tool_use"}`},
		{"gemini", `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"fixture_tool","args":{}}}]},"finishReason":"STOP"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := writeResponsesCompatibilityLog(t, t.TempDir(), "protocol", "", tc.body)
			got, err := parseRequestLogFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Success || !strings.Contains(got.output, "仅工具调用") || len(got.calledTools) != 1 || got.calledTools[0].Name != "fixture_tool" {
				t.Fatalf("tool turn output=%q tools=%#v success=%t", got.output, got.calledTools, got.Success)
			}
		})
	}
}

func TestRequestLogResponsesMatchesFinalAttemptByNumber(t *testing.T) {
	request := func(number int) string {
		return fmt.Sprintf("=== API REQUEST %d ===\nUpstream URL: https://relay.example/v1/responses\nAuth: provider=codex, provider_name=FixtureRelay, auth_id=codex:fixture, type=api_key\nBody:\n{\"model\":\"upstream-fixture\"}\n\n", number)
	}
	response := func(number, message string) string {
		return fmt.Sprintf("=== API RESPONSE%s ===\nStatus: 429\nBody:\n{\"error\":{\"message\":%q}}\n\n", number, message)
	}
	for _, tc := range []struct{ name, upstream, wantError string }{
		{"missing final response", request(1) + request(2) + response("", "unnumbered earlier failure") + response(" 1", "older attempt failure"), ""},
		{"nonconsecutive attempts", request(1) + request(3) + response(" 3", "final attempt failure"), "final attempt failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := writeResponsesCompatibilityLog(t, t.TempDir(), "attempt", tc.upstream, "data: {\"type\":\"response.created\"}\n\n")
			got, err := parseRequestLogFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if got.ErrorPreview != tc.wantError {
				t.Fatalf("final attempt error=%q want=%q", got.ErrorPreview, tc.wantError)
			}
		})
	}
}

func TestRequestLogResponsesRecoversAppendedErrorAnnotation(t *testing.T) {
	// The executor logs the SSE chunk before appending its Error annotation.
	// A chunk without a trailing newline leaves both on the same log line.
	upstream := "=== API REQUEST 1 ===\nUpstream URL: https://relay.example/v1/responses\nAuth: provider=codex, provider_name=FixtureRelay, auth_id=codex:fixture, type=api_key\nBody:\n{}\n\n=== API RESPONSE 1 ===\nStatus: 200\nHeaders:\nContent-Type: text/event-stream\n\nBody:\n"
	failure := `data: {"type":"error","error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}`
	annotation := `Error: {"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}`
	created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"fixture\"}}\n\n"
	for _, tc := range []struct{ name, suffix string }{
		{"no annotation", ""},
		{"separate line", "\n" + annotation},
		{"same line", annotation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := writeResponsesCompatibilityLog(t, t.TempDir(), "annotation", upstream+created+failure+tc.suffix+"\n\n", created)
			got, err := parseRequestLogFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != 200 || got.Success || !got.HasError || got.ErrorPreview != "Rate limit exceeded" {
				t.Fatalf("recorded SSE failure: status=%d success=%t has_error=%t error=%q", got.Status, got.Success, got.HasError, got.ErrorPreview)
			}
			if !strings.Contains(got.error, "rate_limit_exceeded") {
				t.Errorf("error details lost the upstream code: %q", got.error)
			}
			if got.AuthID != "codex:fixture" || got.Provider != "FixtureRelay" {
				t.Errorf("error attribution changed: auth_id=%q provider=%q", got.AuthID, got.Provider)
			}
		})
	}
}

func TestRequestLogResponsesErrorAnnotationBoundaries(t *testing.T) {
	failure := `{"type":"error","error":{"message":"quota"}}`
	literal := `{"type":"response.output_text.delta","delta":"literal Error: keep this"}`
	for _, tc := range []struct{ name, data, want string }{
		{"logged annotation", failure + "Error: quota", failure},
		{"multiline logged annotation", "{\"type\":\"error\",\ndata: \"error\":{\"message\":\"quota\"}}Error: quota", "{\"type\":\"error\",\n\"error\":{\"message\":\"quota\"}}"},
		{"annotation inside JSON string", literal, literal},
		{"unknown trailing data", failure + "unexpected trailer", failure + "unexpected trailer"},
		{"second JSON object", failure + `{}`, failure + `{}`},
		{"malformed JSON", `{"type":"error"Error: quota`, `{"type":"error"Error: quota`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestLogSSEPayloads("data: " + tc.data + "\n\n")
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("payloads=%q want one %q", got, tc.want)
			}
		})
	}
}
