package management

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func requestLogTestTimestamp(offset time.Duration) (string, string) {
	ts := time.Now().Add(-time.Hour + offset).In(time.FixedZone("UTC+8", 8*60*60))
	return ts.Format("2006-01-02T150405"), ts.Format(time.RFC3339)
}

func writeMultipartImageEditRequestLog(t *testing.T, dir, suffix string) string {
	t.Helper()
	stamp, timestamp := requestLogTestTimestamp(0)
	path := filepath.Join(dir, fmt.Sprintf("v1-images-edits-%s-%s.log", stamp, suffix))
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp,
		"URL: /v1/images/edits",
		"Method: POST",
		"",
		"=== REQUEST BODY ===",
		"--fixture",
		`Content-Disposition: form-data; name="model"`,
		"",
		"public-image",
		"--fixture--",
		"",
		"=== RESPONSE ===",
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"data":[{"url":"https://fixture.example/image.png"}]}`,
		"",
	}, "\r\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write multipart image log: %v", err)
	}
	return path
}

func TestExtractModelReadsMultipartModelField(t *testing.T) {
	body := strings.Join([]string{
		"--fixture",
		`Content-Disposition: form-data; name="model"`,
		"",
		"public-image",
		"--fixture--",
	}, "\r\n")
	if got := extractModel(body); got != "public-image" {
		t.Fatalf("extractModel(multipart) = %q, want public-image", got)
	}
}

func TestExtractRequestModelReadsQueryModel(t *testing.T) {
	if got := extractRequestModel("AUDIO-IN", "/v1/media/audio/speech?model=public-speech"); got != "public-speech" {
		t.Fatalf("extractRequestModel(query) = %q, want public-speech", got)
	}
}

func TestIsAIRequestLogFilenameIncludesMediaRoutes(t *testing.T) {
	for _, name := range []string{
		"v1-images-generations-2026-07-26T134302-image.log",
		"v1-images-remove-background-2026-07-26T134302-image-op.log",
		"v1-media-video-text-to-video-2026-07-26T134302-video.log",
		"v1-media-audio-speech-2026-07-26T134302-audio.log",
		"v1-videos-text-to-video-2026-07-26T134302-video-alias.log",
		"v1-audio-speech-2026-07-26T134302-audio-alias.log",
	} {
		if !isAIRequestLogFilename(name) {
			t.Errorf("isAIRequestLogFilename(%q) = false, want true", name)
		}
	}
}

func TestExtractResponseTextFiltersResponsesToolDeltas(t *testing.T) {
	response := strings.Join([]string{
		"Status: 200",
		"Content-Type: text/event-stream",
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"自然语言输出"}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"cmd\":\"noise\"}"}`,
		"",
		"event: response.output_text.done",
		`data: {"type":"response.output_text.done","text":"自然语言输出"}`,
		"",
	}, "\n")

	got := extractResponseText(response, 200)
	if got != "自然语言输出" {
		t.Fatalf("extractResponseText() = %q, want natural language text only", got)
	}
	if strings.Contains(got, "cmd") || strings.Contains(got, "noise") {
		t.Fatalf("extractResponseText() leaked tool arguments: %q", got)
	}
}

func TestExtractResponseTextReadsResponsesOutputMessage(t *testing.T) {
	response := strings.Join([]string{
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"最终输出"}]},{"type":"function_call","arguments":"{\"cmd\":\"noise\"}"}]}`,
	}, "\n")

	got := extractResponseText(response, 200)
	if got != "最终输出" {
		t.Fatalf("extractResponseText() = %q, want final assistant output", got)
	}
}

func TestExtractResponseTextReadsChatCompletions(t *testing.T) {
	response := strings.Join([]string{
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"choices":[{"message":{"role":"assistant","content":"chat 输出"}}]}`,
	}, "\n")

	got := extractResponseText(response, 200)
	if got != "chat 输出" {
		t.Fatalf("extractResponseText() = %q, want chat completion content", got)
	}
}

func TestExtractResponseTextSummarizesToolOnlyResponses(t *testing.T) {
	response := strings.Join([]string{
		"Status: 200",
		"Content-Type: text/event-stream",
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"exec_command","arguments":""}}`,
		"",
		"event: response.function_call_arguments.done",
		`data: {"type":"response.function_call_arguments.done","arguments":"{\"cmd\":\"secret command\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed","output":[]}}`,
		"",
	}, "\n")

	got := extractResponseText(response, 200)
	want := "仅工具调用，无最终文本输出：exec_command"
	if got != want {
		t.Fatalf("extractResponseText() = %q, want %q", got, want)
	}
	if strings.Contains(got, "secret command") || strings.Contains(got, "cmd") {
		t.Fatalf("extractResponseText() leaked tool arguments: %q", got)
	}
}

func TestExtractResponseTextShowsEmptyBodySummary(t *testing.T) {
	response := strings.Join([]string{
		"Status: 200",
		"Content-Type: text/event-stream",
		"",
	}, "\n")

	got := extractResponseText(response, 200)
	want := "响应体为空（未记录最终输出）"
	if got != want {
		t.Fatalf("extractResponseText() = %q, want %q", got, want)
	}
}

func TestRequestIPPrefersForwardedHeaders(t *testing.T) {
	headers := map[string]string{
		"X-CPA-Client-IP":  "172.17.0.1",
		"X-Forwarded-For":  "8.8.8.8, 172.17.0.1",
		"CF-Connecting-IP": "1.1.1.1",
	}

	got := requestIP(headers)
	if got != "1.1.1.1" {
		t.Fatalf("requestIP() = %q, want CF-Connecting-IP", got)
	}
}

func TestExtractPromptMetadataFromCodexBody(t *testing.T) {
	body := `{"model":"gpt-5.5","instructions":"<skills_instructions>\n### Available skills\n- frontend-design: Create polished frontend interfaces. (file: /skills/frontend-design/SKILL.md)\n- browser-use:control-in-app-browser: Control the browser. (file: /skills/browser/SKILL.md)\n### How to use skills\nUse them.\n</skills_instructions>\n\n# System\nKeep it short.","tools":[{"type":"function","name":"mcp__context7__query_docs","description":"Context7 docs lookup."},{"type":"function","name":"mcp__node_repl","description":"Node REPL runner."},{"type":"function","name":"exec_command","description":"Run shell."}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"用户问题"}]}]}`

	meta := extractPromptMetadata(body)
	if meta.SystemPrompt == "" || !strings.Contains(meta.SystemPrompt, "Keep it short") {
		t.Fatalf("system prompt missing: %#v", meta.SystemPrompt)
	}
	if len(meta.MCPs) != 2 || meta.MCPs[0].Name != "context7" || meta.MCPs[1].Name != "node_repl" {
		t.Fatalf("MCPs = %#v", meta.MCPs)
	}
	if len(meta.Skills) != 2 || meta.Skills[0].Name != "frontend-design" || meta.Skills[1].Name != "browser-use:control-in-app-browser" {
		t.Fatalf("Skills = %#v", meta.Skills)
	}
	if meta.ToolPreview != "MCP: context7、node_repl；Skill: frontend-design、browser-use:control-in-app-browser" {
		t.Fatalf("ToolPreview = %q", meta.ToolPreview)
	}
}

func TestExtractCalledToolsFromResponsesSSE(t *testing.T) {
	response := strings.Join([]string{
		"Status: 200",
		"Content-Type: text/event-stream",
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"exec_command","arguments":""}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","name":"mcp__context7__query_docs"}}`,
		"",
		"event: response.function_call_arguments.done",
		`data: {"type":"response.function_call_arguments.done","arguments":"{\"cmd\":\"secret\"}"}`,
	}, "\n")

	got := extractCalledTools(response)
	if len(got) != 2 || got[0].Name != "exec_command" || got[1].Name != "mcp__context7__query_docs" {
		t.Fatalf("called tools = %#v", got)
	}
	if strings.Contains(got[0].Summary, "secret") {
		t.Fatalf("called tool summary leaked noisy args: %#v", got[0])
	}
}

func TestParseRequestLogExtractsUpstreamChannelAndCalledToolsOnly(t *testing.T) {
	logsDir := t.TempDir()
	stamp, timestamp := requestLogTestTimestamp(0)
	logPath := filepath.Join(logsDir, fmt.Sprintf("v1-responses-%s-channelid.log", stamp))
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp,
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== REQUEST BODY ===",
		`{"model":"cpa-gpt5","tools":[{"type":"function","name":"mcp__context7__query_docs","description":"Context7 docs lookup."},{"type":"function","name":"mcp__node_repl","description":"Node REPL runner."}],"input":[{"role":"user","content":[{"type":"input_text","text":"用户提示词"}]}]}`,
		"",
		"=== API REQUEST 1 ===",
		"Timestamp: " + timestamp,
		"Upstream URL: https://integrate.api.nvidia.com/v1/chat/completions",
		"HTTP Method: POST",
		"Auth: provider=英伟达, auth_id=openai-compatibility:英伟达:abc123, label=英伟达, type=api_key value=nvap...test",
		"",
		"Headers:",
		"Content-Type: application/json",
		"",
		"Body:",
		`{"model":"minimaxai/minimax-m2.7","messages":[{"role":"user","content":"用户提示词"}],"stream":true}`,
		"",
		"=== RESPONSE ===",
		"Status: 200",
		"Content-Type: text/event-stream",
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"mcp__context7__query_docs"}}`,
		"",
		"event: response.output_text.done",
		`data: {"type":"response.output_text.done","text":"最终输出"}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}

	parsed, err := parseRequestLogFile(requestLogCandidate{name: filepath.Base(logPath), path: logPath, size: info.Size(), modTime: info.ModTime(), logTime: info.ModTime()})
	if err != nil {
		t.Fatalf("parse log: %v", err)
	}
	if parsed.Model != "cpa-gpt5" {
		t.Fatalf("Model = %q, want client model", parsed.Model)
	}
	if parsed.Provider != "英伟达" || parsed.UpstreamModel != "minimaxai/minimax-m2.7" || parsed.ChannelModel != "英伟达 / minimaxai/minimax-m2.7" {
		t.Fatalf("channel fields = provider:%q upstream:%q channel:%q", parsed.Provider, parsed.UpstreamModel, parsed.ChannelModel)
	}
	if parsed.ToolPreview == "" {
		t.Fatalf("available tool preview should remain available in detail metadata")
	}
	if parsed.CalledToolsPreview != "context7" {
		t.Fatalf("CalledToolsPreview = %q, want actual called MCP only", parsed.CalledToolsPreview)
	}
	if len(parsed.calledTools) != 1 || parsed.calledTools[0].Name != "mcp__context7__query_docs" || parsed.calledTools[0].Description == "" {
		t.Fatalf("called tools were not enriched: %#v", parsed.calledTools)
	}
}

func TestRequestLogStoreListDetailAndExport(t *testing.T) {
	logsDir := t.TempDir()
	stamp, timestamp := requestLogTestTimestamp(0)
	logPath := filepath.Join(logsDir, fmt.Sprintf("v1-responses-%s-testid.log", stamp))
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp,
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== HEADERS ===",
		"X-Forwarded-For: 8.8.8.8",
		"",
		"=== REQUEST BODY ===",
		`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"用户提示词"}]}],"instructions":"系统提示词"}`,
		"",
		"=== RESPONSE ===",
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"最终输出"}]}]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()
	if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
		t.Fatalf("sync store: %v", err)
	}
	items, total, err := store.list(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total/items = %d/%d", total, len(items))
	}
	if items[0].Model != "gpt-test" || items[0].IP != "8.8.8.8" || !items[0].Success {
		t.Fatalf("unexpected item: %#v", items[0])
	}
	detail, err := store.detail(context.Background(), items[0].ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Prompt != "用户提示词" || detail.Output != "最终输出" || detail.SystemPrompt != "系统提示词" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	var out strings.Builder
	if err := store.export(context.Background(), &out, requestLogQueryOptions{Limit: 10}, "csv"); err != nil {
		t.Fatalf("export: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "用户提示词") || !strings.Contains(got, "最终输出") {
		t.Fatalf("export missing content: %s", got)
	}
}

func TestOpenRequestLogStoreConfiguresBusyTimeoutOnEveryConnection(t *testing.T) {
	store, err := openRequestLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()

	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		connection, errConn := store.db.Conn(context.Background())
		if errConn != nil {
			t.Fatalf("open connection %d: %v", i, errConn)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	for i, connection := range connections {
		var timeout int
		if errQuery := connection.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&timeout); errQuery != nil {
			t.Fatalf("query connection %d busy timeout: %v", i, errQuery)
		}
		if timeout != 5000 {
			t.Fatalf("connection %d busy timeout = %d, want 5000", i, timeout)
		}
	}
}

func TestRequestLogStoreFailureDetailsDeduplicatesByProviderModelAndError(t *testing.T) {
	logsDir := t.TempDir()
	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()

	candidate := requestLogCandidate{name: "v1-responses-2026-06-12T230000-fail.log", path: filepath.Join(logsDir, "v1-responses-2026-06-12T230000-fail.log"), size: 10, modTime: time.Now(), logTime: time.Now()}
	parsed := parsedRequestLog{requestLogListItem: requestLogListItem{ID: "fail-1", Name: candidate.name, Size: candidate.size, Modified: candidate.modTime.Unix(), Timestamp: candidate.logTime.Format(time.RFC3339Nano), URL: "/v1/responses", Method: "POST", Model: "cpa-gpt", Provider: "英伟达", UpstreamModel: "minimaxai/minimax-m2.7", ChannelModel: "英伟达 / minimaxai/minimax-m2.7", Status: 500, Success: false, ErrorPreview: "empty_stream", HasError: true}, error: "empty_stream"}
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	parsed.ID = "fail-2"
	parsed.Name = "v1-responses-2026-06-12T230001-fail.log"
	candidate.name = parsed.Name
	candidate.path = filepath.Join(logsDir, candidate.name)
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	parsed.ID = "fail-3"
	parsed.Name = "v1-responses-2026-06-12T230002-fail.log"
	parsed.UpstreamModel = "deepseek-ai/deepseek-v3"
	parsed.ChannelModel = "英伟达 / deepseek-ai/deepseek-v3"
	parsed.ErrorPreview = "rate_limit"
	parsed.error = "rate_limit"
	candidate.name = parsed.Name
	candidate.path = filepath.Join(logsDir, candidate.name)
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatalf("upsert third: %v", err)
	}

	items, err := store.failureDetails(context.Background(), "英伟达", 10)
	if err != nil {
		t.Fatalf("failure details: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("failure details len = %d, want 2: %#v", len(items), items)
	}
	if items[0].Provider != "英伟达" || items[0].Model != "cpa-gpt" || items[0].Error != "empty_stream" || items[0].Count != 2 {
		t.Fatalf("first failure detail = %#v", items[0])
	}
	if items[1].Model != "cpa-gpt" || items[1].Error != "rate_limit" || items[1].Count != 1 {
		t.Fatalf("second failure detail = %#v", items[1])
	}
}

func TestRequestLogStoreFailureDetailsPreferClientModel(t *testing.T) {
	logsDir := t.TempDir()
	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()

	now := time.Now()
	candidate := requestLogCandidate{name: "v1-images-generations-2026-07-26T130000-alias.log", path: filepath.Join(logsDir, "v1-images-generations-2026-07-26T130000-alias.log"), size: 10, modTime: now, logTime: now}
	parsed := parsedRequestLog{requestLogListItem: requestLogListItem{
		ID: "alias", Name: candidate.name, Size: candidate.size, Modified: now.Unix(), Timestamp: now.Format(time.RFC3339Nano),
		URL: "/v1/images/generations", Method: "POST", Model: "public-image", Provider: "image-relay",
		UpstreamModel: "vendor-image-v2", Status: 500, Success: false, ErrorPreview: "fixture failure", HasError: true,
	}, error: "fixture failure"}
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatalf("upsert failure: %v", err)
	}

	items, err := store.failureDetails(context.Background(), "image-relay", 10)
	if err != nil {
		t.Fatalf("failure details: %v", err)
	}
	if len(items) != 1 || items[0].Model != "public-image" {
		t.Fatalf("failure details = %#v, want client model public-image", items)
	}
}

func TestRequestLogStoreHandlesLegacyNullRowsAndRefreshesThem(t *testing.T) {
	logsDir := t.TempDir()
	stamp, timestamp := requestLogTestTimestamp(0)
	logPath := filepath.Join(logsDir, fmt.Sprintf("v1-responses-%s-legacyid.log", stamp))
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp,
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== REQUEST BODY ===",
		`{"model":"gpt-legacy","input":[{"role":"user","content":[{"type":"input_text","text":"用户提示词"}]}],"instructions":"系统提示词"}`,
		"",
		"=== RESPONSE ===",
		"Status: 200",
		"Content-Type: application/json",
		"",
		`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"最终输出"}]}]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}

	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, model, status, success, has_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacyid", filepath.Base(logPath), logPath, info.Size(), info.ModTime().Unix(), timestamp, info.ModTime().Unix(), "gpt-legacy", 200, 1, 0, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	items, total, err := store.list(context.Background(), requestLogQueryOptions{Query: "legacyid", Limit: 10})
	if err != nil {
		t.Fatalf("list legacy null row: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("legacy total/items = %d/%d", total, len(items))
	}

	if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
		t.Fatalf("sync store: %v", err)
	}
	items, total, err = store.list(context.Background(), requestLogQueryOptions{Query: "用户提示词", Limit: 10})
	if err != nil {
		t.Fatalf("list refreshed row: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].PromptPreview == "" || items[0].OutputPreview == "" {
		t.Fatalf("row was not refreshed: total=%d items=%#v", total, items)
	}
}

func TestRequestLogStoreBackfillsLegacyMediaModelOnlyOnce(t *testing.T) {
	logsDir := t.TempDir()
	logPath := writeMultipartImageEditRequestLog(t, logsDir, "legacy-media")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat multipart image log: %v", err)
	}

	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()

	id := requestLogIDFromFilename(filepath.Base(logPath))
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, url, method, model, status, success, has_error, parser_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, filepath.Base(logPath), logPath, info.Size(), info.ModTime().Unix(), info.ModTime().Format(time.RFC3339Nano), info.ModTime().Unix(), "/v1/images/edits", "POST", "", 200, 1, 0, 0, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatalf("insert legacy media row: %v", err)
	}

	if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	var model string
	var parserRevision int
	if err := store.db.QueryRowContext(context.Background(), `SELECT model, parser_revision FROM request_log_entries WHERE id = ?`, id).Scan(&model, &parserRevision); err != nil {
		t.Fatalf("query refreshed media row: %v", err)
	}
	if model != "public-image" || parserRevision <= 0 {
		t.Fatalf("refreshed media row model/revision = %q/%d, want public-image/current", model, parserRevision)
	}

	if _, err := store.db.ExecContext(context.Background(), `UPDATE request_log_entries SET updated_at = 42 WHERE id = ?`, id); err != nil {
		t.Fatalf("set refresh sentinel: %v", err)
	}
	if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	var updatedAt int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT updated_at FROM request_log_entries WHERE id = ?`, id).Scan(&updatedAt); err != nil {
		t.Fatalf("query refresh sentinel: %v", err)
	}
	if updatedAt != 42 {
		t.Fatalf("updated_at = %d, want 42 after unchanged second sync", updatedAt)
	}
}

func TestRequestLogStoreMigratesLegacySchemaBeforeNewIndexes(t *testing.T) {
	logsDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(logsDir, requestLogDBFilename))
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `
CREATE TABLE request_log_entries (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  raw_log_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  modified INTEGER NOT NULL,
  timestamp_text TEXT NOT NULL,
  timestamp_unix INTEGER NOT NULL,
  url TEXT,
  method TEXT,
  model TEXT,
  ip TEXT,
  ip_location TEXT,
  status INTEGER,
  success INTEGER NOT NULL,
  prompt TEXT,
  output TEXT,
  error TEXT,
  system_prompt TEXT,
  available_tools_json TEXT,
  mcps_json TEXT,
  skills_json TEXT,
  called_tools_json TEXT,
  prompt_metadata_json TEXT,
  request_metadata_json TEXT,
  prompt_preview TEXT,
  output_preview TEXT,
  error_preview TEXT,
  tool_preview TEXT,
  system_prompt_preview TEXT,
  called_tools_preview TEXT,
  session_id TEXT,
  thread_id TEXT,
  turn_id TEXT,
  has_error INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
`)
	if errClose := db.Close(); errClose != nil {
		t.Fatalf("close legacy sqlite: %v", errClose)
	}
	if err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.close()

	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(request_log_entries)`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table info rows: %v", err)
	}
	for _, column := range []string{"provider", "provider_name", "auth_id", "auth_type", "upstream_url", "upstream_model", "channel_model", "parser_revision"} {
		if !columns[column] {
			t.Fatalf("migrated schema missing column %s", column)
		}
	}
}

func TestRequestLogStoreFailureDetailsDeduplicateByProviderModelAndError(t *testing.T) {
	logsDir := t.TempDir()
	writeFailureLog := func(name string, timestamp string, errMessage string) {
		t.Helper()
		content := strings.Join([]string{
			"=== REQUEST INFO ===",
			"Timestamp: " + timestamp,
			"URL: /v1/responses",
			"Method: POST",
			"",
			"=== REQUEST BODY ===",
			`{"model":"cpa-gpt5","input":[{"role":"user","content":[{"type":"input_text","text":"用户提示词"}]}]}`,
			"",
			"=== API REQUEST 1 ===",
			"Upstream URL: https://integrate.api.nvidia.com/v1/chat/completions",
			"HTTP Method: POST",
			"Auth: provider=英伟达, auth_id=openai-compatibility:英伟达:abc123, label=英伟达, type=api_key value=nvap...test",
			"",
			"Body:",
			`{"model":"minimaxai/minimax-m2.7","messages":[{"role":"user","content":"用户提示词"}]}`,
			"",
			"=== API ERROR RESPONSE 1 ===",
			"HTTP Status: 429",
			"",
			fmt.Sprintf(`{"error":{"message":%q}}`, errMessage),
			"",
			"=== RESPONSE ===",
			"Status: 500",
			"Content-Type: application/json",
			"",
			fmt.Sprintf(`{"error":{"message":%q}}`, errMessage),
			"",
		}, "\n")
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}
	stamp1, timestamp1 := requestLogTestTimestamp(0)
	stamp2, timestamp2 := requestLogTestTimestamp(time.Second)
	stamp3, timestamp3 := requestLogTestTimestamp(2 * time.Second)
	writeFailureLog(fmt.Sprintf("v1-responses-%s-fail001.log", stamp1), timestamp1, "quota exceeded")
	writeFailureLog(fmt.Sprintf("v1-responses-%s-fail002.log", stamp2), timestamp2, "quota exceeded")
	writeFailureLog(fmt.Sprintf("v1-responses-%s-fail003.log", stamp3), timestamp3, "rate limited")

	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()
	if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
		t.Fatalf("sync store: %v", err)
	}
	details, err := store.failureDetails(context.Background(), "英伟达", 10)
	if err != nil {
		t.Fatalf("failureDetails: %v", err)
	}
	if len(details) != 2 {
		t.Fatalf("failureDetails len = %d, want 2: %#v", len(details), details)
	}
	if details[0].Provider != "英伟达" || details[0].Model != "cpa-gpt5" || details[0].Error != "quota exceeded" || details[0].Count != 2 {
		t.Fatalf("first detail = %#v", details[0])
	}
}

func TestExportRequestLogsHonorsPages(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	baseTime := time.Now().Add(-time.Hour).UTC()
	for i := 0; i < 3; i++ {
		ts := baseTime.Add(time.Duration(i) * time.Second)
		logPath := filepath.Join(logsDir, fmt.Sprintf("v1-responses-%s-page-%d.log", ts.Format("2006-01-02T150405"), i))
		content := strings.Join([]string{
			"=== REQUEST INFO ===",
			"Timestamp: " + ts.Format(time.RFC3339),
			"URL: /v1/responses",
			"Method: POST",
			"",
			"=== REQUEST BODY ===",
			fmt.Sprintf(`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"用户提示词-%d"}]}]}`, i),
			"",
			"=== RESPONSE ===",
			"Status: 200",
			"Content-Type: application/json",
			"",
			fmt.Sprintf(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"最终输出-%d"}]}]}`, i),
			"",
		}, "\n")
		if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(logsDir)
	if err := h.StartRequestLogIndex(); err != nil {
		t.Fatalf("start request log index: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	manager, _ := h.requestLogIndexSnapshot()
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs/export?limit=1&pages=2&format=csv", nil)
	h.ExportRequestLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("csv row count = %d, want header + 2 rows", len(records))
	}
}

func TestRequestLogProviderNameParserStoreAndFallback(t *testing.T) {
	logsDir := t.TempDir()
	stamp, timestamp := requestLogTestTimestamp(0)
	writeLog := func(suffix, authLine string) string {
		t.Helper()
		path := filepath.Join(logsDir, fmt.Sprintf("v1-responses-%s-%s.log", stamp, suffix))
		content := strings.Join([]string{
			"=== REQUEST INFO ===",
			"Timestamp: " + timestamp,
			"URL: /v1/responses",
			"Method: POST",
			"",
			"=== REQUEST BODY ===",
			`{"model":"claude-client","input":"hello"}`,
			"",
			"=== API REQUEST 1 ===",
			"Upstream URL: https://api.anthropic.com/v1/messages",
			"HTTP Method: POST",
			"Auth: " + authLine,
			"",
			"Body:",
			`{"model":"claude-upstream","messages":[{"role":"user","content":"hello"}]}`,
			"",
			"=== API ERROR RESPONSE 1 ===",
			"HTTP Status: 500",
			"",
			`{"error":{"message":"upstream failed"}}`,
			"",
			"=== RESPONSE ===",
			"Status: 500",
			"Content-Type: application/json",
			"",
			`{"error":{"message":"upstream failed"}}`,
		}, "\n")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
		return path
	}

	namedPath := writeLog("provider-name", "provider=claude, provider_name=relay-a, auth_id=claude:apikey:named, label=relay-a, type=api_key value=sk...named")
	legacyPath := writeLog("provider-legacy", "provider=claude, auth_id=claude:apikey:legacy, label=claude-apikey, type=api_key value=sk...legacy")

	parse := func(path string) parsedRequestLog {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat log: %v", err)
		}
		parsed, err := parseRequestLogFile(requestLogCandidate{name: filepath.Base(path), path: path, size: info.Size(), modTime: info.ModTime(), logTime: info.ModTime()})
		if err != nil {
			t.Fatalf("parse log: %v", err)
		}
		return parsed
	}

	named := parse(namedPath)
	if named.Provider != "relay-a" || named.ProtocolProvider != "claude" {
		t.Fatalf("named providers = display:%q protocol:%q", named.Provider, named.ProtocolProvider)
	}
	legacy := parse(legacyPath)
	if legacy.Provider != "claude" || legacy.ProtocolProvider != "claude" {
		t.Fatalf("legacy providers = display:%q protocol:%q", legacy.Provider, legacy.ProtocolProvider)
	}

	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()
	if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
		t.Fatalf("sync store: %v", err)
	}

	items, total, err := store.list(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total/items = %d/%d", total, len(items))
	}
	providers := map[string]string{}
	for _, item := range items {
		providers[item.AuthID] = item.Provider
	}
	if providers["claude:apikey:named"] != "relay-a" || providers["claude:apikey:legacy"] != "claude" {
		t.Fatalf("list providers = %#v", providers)
	}

	detail, err := store.detail(context.Background(), named.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Provider != "relay-a" {
		t.Fatalf("detail provider = %q, want relay-a", detail.Provider)
	}

	var exported strings.Builder
	if err := store.export(context.Background(), &exported, requestLogQueryOptions{Limit: 10}, "csv"); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(exported.String(), "relay-a") || !strings.Contains(exported.String(), "claude") {
		t.Fatalf("export providers missing: %s", exported.String())
	}

	failures, err := store.failureDetails(context.Background(), "relay-a", 10)
	if err != nil {
		t.Fatalf("failure details: %v", err)
	}
	if len(failures) != 1 || failures[0].Provider != "relay-a" {
		t.Fatalf("failure providers = %#v", failures)
	}

	var protocol, providerName string
	if err := store.db.QueryRowContext(context.Background(), `SELECT provider, provider_name FROM request_log_entries WHERE auth_id = ?`, "claude:apikey:named").Scan(&protocol, &providerName); err != nil {
		t.Fatalf("query stored providers: %v", err)
	}
	if protocol != "claude" || providerName != "relay-a" {
		t.Fatalf("stored providers = protocol:%q display:%q", protocol, providerName)
	}
}

func TestRequestLogProviderNameMigrationAddsColumnAndCompositeIndexes(t *testing.T) {
	logsDir := t.TempDir()
	store, err := openRequestLogStore(logsDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.close()

	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(request_log_entries)`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close table info: %v", err)
	}
	if !columns["provider_name"] {
		t.Fatal("provider_name column missing")
	}

	indexRows, err := store.db.QueryContext(context.Background(), `PRAGMA index_list(request_log_entries)`)
	if err != nil {
		t.Fatalf("index list: %v", err)
	}
	indexes := map[string]bool{}
	for indexRows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := indexRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index list: %v", err)
		}
		indexes[name] = true
	}
	if err := indexRows.Close(); err != nil {
		t.Fatalf("close index list: %v", err)
	}
	for _, name := range []string{"idx_request_log_entries_auth_time", "idx_request_log_entries_provider_time"} {
		if !indexes[name] {
			t.Fatalf("index %s missing: %#v", name, indexes)
		}
	}
}

func writeSnapshotRequestLog(t *testing.T, dir, suffix, authID string, timestamp time.Time, status int) string {
	t.Helper()
	name := fmt.Sprintf("v1-responses-%s-%s.log", timestamp.Format("2006-01-02T150405"), suffix)
	path := filepath.Join(dir, name)
	responseBody := `{"output_text":"ok"}`
	if status >= 400 {
		responseBody = fmt.Sprintf(`{"error":{"message":"status %d"}}`, status)
	}
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: " + timestamp.Format(time.RFC3339),
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== REQUEST BODY ===",
		`{"model":"client-model","input":"hello"}`,
		"",
		"=== API REQUEST 1 ===",
		"Upstream URL: https://api.example.com/v1/responses",
		"HTTP Method: POST",
		"Auth: provider=claude, provider_name=relay-a, auth_id=" + authID + ", type=api_key",
		"",
		"Body:",
		`{"model":"upstream-model","input":"hello"}`,
		"",
		"=== RESPONSE ===",
		fmt.Sprintf("Status: %d", status),
		"Content-Type: application/json",
		"",
		responseBody,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write snapshot log: %v", err)
	}
	return path
}

func newBlockedSnapshotHandler(t *testing.T, dir string) (*Handler, *requestLogIndexManager, func()) {
	t.Helper()
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{
		RetentionDays: func() int { return 7 },
		ScanHook: func(ctx context.Context) error {
			if !block.Load() {
				return nil
			}
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager
	startBlocked := func() {
		block.Store(true)
		manager.TriggerSync()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("blocked snapshot scan did not start")
		}
	}
	releaseBlocked := func() {
		block.Store(false)
		select {
		case <-release:
		default:
			close(release)
		}
	}
	t.Cleanup(releaseBlocked)
	return h, manager, func() {
		startBlocked()
	}
}

func TestRequestLogsListSnapshotDoesNotWaitForBlockedScan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeSnapshotRequestLog(t, dir, "baseline", "auth-baseline", time.Now().Add(-time.Minute), 200)
	h, _, block := newBlockedSnapshotHandler(t, dir)
	writeSnapshotRequestLog(t, dir, "pending", "auth-pending", time.Now(), 200)
	block()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs?limit=10", nil)
	started := time.Now()
	h.GetRequestLogs(ctx)
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("snapshot list waited %s for blocked scan", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items         []requestLogListItem `json:"items"`
		Total         int                  `json:"total"`
		Syncing       bool                 `json:"syncing"`
		LastSyncedAt  string               `json:"last_synced_at"`
		LastSyncError string               `json:"last_sync_error"`
		RetentionDays int                  `json:"retention_days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 || !payload.Syncing || payload.LastSyncedAt == "" || payload.RetentionDays != 7 {
		t.Fatalf("snapshot list payload = %#v", payload)
	}
}

func TestRequestLogsFailureSnapshotDoesNotWaitForBlockedScan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeSnapshotRequestLog(t, dir, "baseline-failure001", "auth-baseline", time.Now().Add(-time.Minute), 500)
	h, _, block := newBlockedSnapshotHandler(t, dir)
	writeSnapshotRequestLog(t, dir, "pending-failure002", "auth-pending", time.Now(), 500)
	block()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs/failures?provider=relay-a", nil)
	h.GetRequestLogFailureDetails(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items   []requestLogFailureDetail `json:"items"`
		Syncing bool                      `json:"syncing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode failures: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Count != 1 || !payload.Syncing {
		t.Fatalf("failure snapshot payload = %#v", payload)
	}
}

func TestRequestLogsExportSnapshotDoesNotWaitForBlockedScan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeSnapshotRequestLog(t, dir, "baseline-export", "auth-baseline", time.Now().Add(-time.Minute), 200)
	h, _, block := newBlockedSnapshotHandler(t, dir)
	writeSnapshotRequestLog(t, dir, "pending-export", "auth-pending", time.Now(), 200)
	block()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs/export?limit=10&format=csv", nil)
	h.ExportRequestLogs(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("export row count = %d, want header + snapshot row", len(records))
	}
	if rec.Header().Get("X-Request-Log-Syncing") != "true" || rec.Header().Get("X-Request-Log-Last-Synced-At") == "" {
		t.Fatalf("export sync headers = %#v", rec.Header())
	}
}

func TestRequestLogDetailBackfillIndexesOnlyRequestedFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	manager := newTestRequestLogIndexManager(t, dir, requestLogIndexManagerOptions{RetentionDays: func() int { return 7 }})
	waitForRequestLogManager(t, manager, func(status requestLogSyncStatus) bool {
		return !status.Syncing && !status.LastSyncedAt.IsZero()
	})
	requestedPath := writeSnapshotRequestLog(t, dir, "requested-detail001", "auth-requested", time.Now().Add(-time.Minute), 200)
	writeSnapshotRequestLog(t, dir, "unrelated-detail002", "auth-unrelated", time.Now(), 200)
	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(dir)
	h.requestLogIndex = manager

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "id", Value: requestLogIDFromFilename(filepath.Base(requestedPath))}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-logs/detail", nil)
	h.GetRequestLogDetail(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	_, total, err := manager.List(context.Background(), requestLogQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list after backfill: %v", err)
	}
	if total != 1 {
		t.Fatalf("detail backfill indexed %d rows, want only requested file", total)
	}
}

func TestParseRequestLogAuthLineDecodesURLFieldsAndKeepsLegacyFormat(t *testing.T) {
	providerName := "relay,a=b+c%2C d/中文"
	encoded := "encoding=url, provider=claude, provider_name=" + url.PathEscape(providerName) + ", auth_id=" + url.PathEscape("auth,1") + ", type=api_key%20value=masked"
	parsed := parseRequestLogAuthLine(encoded)
	if parsed["provider_name"] != providerName || parsed["auth_id"] != "auth,1" || parsed["type"] != "api_key" {
		t.Fatalf("parsed URL auth fields = %#v", parsed)
	}

	legacy := parseRequestLogAuthLine("provider=claude, provider_name=relay-a, auth_id=auth-1, type=api_key value=masked")
	if legacy["provider_name"] != "relay-a" || legacy["auth_id"] != "auth-1" || legacy["type"] != "api_key" {
		t.Fatalf("parsed legacy auth fields = %#v", legacy)
	}
}
