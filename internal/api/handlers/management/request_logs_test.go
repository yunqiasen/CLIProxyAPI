package management

import (
	"context"
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
