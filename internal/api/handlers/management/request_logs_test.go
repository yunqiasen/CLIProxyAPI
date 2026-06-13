package management

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

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
	logPath := filepath.Join(logsDir, "v1-responses-2026-06-12T233555-channelid.log")
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: 2026-06-12T23:35:55+08:00",
		"URL: /v1/responses",
		"Method: POST",
		"",
		"=== REQUEST BODY ===",
		`{"model":"cpa-gpt5","tools":[{"type":"function","name":"mcp__context7__query_docs","description":"Context7 docs lookup."},{"type":"function","name":"mcp__node_repl","description":"Node REPL runner."}],"input":[{"role":"user","content":[{"type":"input_text","text":"用户提示词"}]}]}`,
		"",
		"=== API REQUEST 1 ===",
		"Timestamp: 2026-06-12T23:35:55+08:00",
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
	logPath := filepath.Join(logsDir, "v1-responses-2026-06-09T185805-testid.log")
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: 2026-06-09T18:58:05+08:00",
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
	if items[0].Provider != "英伟达" || items[0].Model != "minimaxai/minimax-m2.7" || items[0].Error != "empty_stream" || items[0].Count != 2 {
		t.Fatalf("first failure detail = %#v", items[0])
	}
	if items[1].Model != "deepseek-ai/deepseek-v3" || items[1].Error != "rate_limit" || items[1].Count != 1 {
		t.Fatalf("second failure detail = %#v", items[1])
	}
}

func TestRequestLogStoreHandlesLegacyNullRowsAndRefreshesThem(t *testing.T) {
	logsDir := t.TempDir()
	logPath := filepath.Join(logsDir, "v1-responses-2026-06-09T185805-legacyid.log")
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Timestamp: 2026-06-09T18:58:05+08:00",
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
	_, err = store.db.ExecContext(context.Background(), `INSERT INTO request_log_entries (id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix, model, status, success, has_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacyid", filepath.Base(logPath), logPath, info.Size(), info.ModTime().Unix(), "2026-06-09T18:58:05+08:00", info.ModTime().Unix(), "gpt-legacy", 200, 1, 0, time.Now().Unix(), time.Now().Unix())
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
	for _, column := range []string{"provider", "auth_id", "auth_type", "upstream_url", "upstream_model", "channel_model"} {
		if !columns[column] {
			t.Fatalf("migrated schema missing column %s", column)
		}
	}
}

func TestRequestLogStoreFailureDetailsDeduplicateByProviderModelAndError(t *testing.T) {
	logsDir := t.TempDir()
	writeFailureLog := func(name string, errMessage string) {
		t.Helper()
		content := strings.Join([]string{
			"=== REQUEST INFO ===",
			"Timestamp: 2026-06-12T23:35:55+08:00",
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
	writeFailureLog("v1-responses-2026-06-12T233555-fail001.log", "quota exceeded")
	writeFailureLog("v1-responses-2026-06-12T233556-fail002.log", "quota exceeded")
	writeFailureLog("v1-responses-2026-06-12T233557-fail003.log", "rate limited")

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
	if details[0].Provider != "英伟达" || details[0].Model != "minimaxai/minimax-m2.7" || details[0].Error != "quota exceeded" || details[0].Count != 2 {
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

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(logsDir)
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
