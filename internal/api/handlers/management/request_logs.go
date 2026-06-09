package management

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultRequestLogLimit  = 50
	maxRequestLogLimit      = 200
	previewTextLimit        = 96
	requestLogRetentionDays = 7
)

var requestLogSectionHeader = regexp.MustCompile(`^===\s+(.+?)\s+===$`)
var requestLogFilenameTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{6}`)
var requestLogSkillFile = regexp.MustCompile(`\((?:file|path):\s*([^)]+)\)`)

type requestLogListItem struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Size                int64  `json:"size"`
	Modified            int64  `json:"modified"`
	Timestamp           string `json:"timestamp"`
	URL                 string `json:"url"`
	Method              string `json:"method"`
	Model               string `json:"model"`
	IP                  string `json:"ip"`
	IPLocation          string `json:"ip_location"`
	Status              int    `json:"status"`
	Success             bool   `json:"success"`
	PromptPreview       string `json:"prompt_preview"`
	OutputPreview       string `json:"output_preview"`
	ErrorPreview        string `json:"error_preview"`
	ToolPreview         string `json:"tool_preview"`
	SystemPromptPreview string `json:"system_prompt_preview"`
	CalledToolsPreview  string `json:"called_tools_preview"`
	SessionID           string `json:"session_id"`
	ThreadID            string `json:"thread_id"`
	TurnID              string `json:"turn_id"`
	HasError            bool   `json:"has_error"`
}

type requestLogDetail struct {
	requestLogListItem
	Prompt          string                `json:"prompt"`
	Output          string                `json:"output"`
	Error           string                `json:"error"`
	SystemPrompt    string                `json:"system_prompt"`
	AvailableTools  []requestLogToolInfo  `json:"available_tools"`
	MCPs            []requestLogMCPGroup  `json:"mcps"`
	Skills          []requestLogSkillInfo `json:"skills"`
	CalledTools     []requestLogToolInfo  `json:"called_tools"`
	PromptMetadata  requestPromptMetadata `json:"prompt_metadata"`
	RequestMetadata map[string]string     `json:"request_metadata"`
}

type requestLogCandidate struct {
	name    string
	path    string
	size    int64
	modTime time.Time
	logTime time.Time
}

type parsedRequestLog struct {
	requestLogListItem
	prompt          string
	output          string
	error           string
	promptMetadata  requestPromptMetadata
	calledTools     []requestLogToolInfo
	requestMetadata map[string]string
}

type requestLogToolInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Summary     string `json:"summary"`
}

type requestLogMCPGroup struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Tools       []requestLogToolInfo `json:"tools"`
}

type requestLogSkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Prompt      string `json:"prompt"`
}

type requestPromptMetadata struct {
	SystemPrompt        string                `json:"system_prompt"`
	SystemPromptPreview string                `json:"system_prompt_preview"`
	ToolPreview         string                `json:"tool_preview"`
	AvailableTools      []requestLogToolInfo  `json:"available_tools"`
	MCPs                []requestLogMCPGroup  `json:"mcps"`
	Skills              []requestLogSkillInfo `json:"skills"`
	RequestMetadata     map[string]string     `json:"request_metadata"`
}

// GetRequestLogs returns structured request log rows for the management UI.
func (h *Handler) GetRequestLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	limit, errLimit := parseRequestLogLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid limit: %v", errLimit)})
		return
	}
	offset, errOffset := parseRequestLogOffset(c.Query("offset"))
	if errOffset != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid offset: %v", errOffset)})
		return
	}
	query := strings.TrimSpace(c.Query("q"))

	store, errStore := openRequestLogStore(dir)
	if errStore == nil {
		defer store.close()
		if errSync := syncRequestLogStore(c.Request.Context(), store, dir); errSync == nil {
			items, total, errList := store.list(c.Request.Context(), requestLogQueryOptions{Query: query, Limit: limit, Offset: offset})
			if errList == nil {
				c.JSON(http.StatusOK, gin.H{
					"items":          items,
					"total":          total,
					"limit":          limit,
					"offset":         offset,
					"retention_days": requestLogRetentionDays,
					"storage":        "sqlite",
				})
				return
			}
		}
	}

	candidates, errCollect := collectRequestLogCandidates(dir)
	if errCollect != nil {
		if os.IsNotExist(errCollect) {
			c.JSON(http.StatusOK, gin.H{"items": []requestLogListItem{}, "total": 0, "limit": limit, "offset": offset})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list request logs: %v", errCollect)})
		return
	}

	if query != "" {
		filtered := make([]requestLogCandidate, 0, len(candidates))
		lowerQuery := strings.ToLower(query)
		for _, candidate := range candidates {
			parsed, errParse := parseRequestLogFile(candidate)
			if errParse != nil {
				if strings.Contains(strings.ToLower(candidate.name), lowerQuery) || strings.Contains(strings.ToLower(errParse.Error()), lowerQuery) {
					filtered = append(filtered, candidate)
				}
				continue
			}
			if requestLogMatchesQuery(parsed, lowerQuery) {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}

	total := len(candidates)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}

	items := make([]requestLogListItem, 0, end-offset)
	for _, candidate := range candidates[offset:end] {
		parsed, errParse := parseRequestLogFile(candidate)
		if errParse != nil {
			items = append(items, requestLogListItem{
				ID:            requestLogIDFromFilename(candidate.name),
				Name:          candidate.name,
				Size:          candidate.size,
				Modified:      candidate.modTime.Unix(),
				PromptPreview: "",
				OutputPreview: "",
				ErrorPreview:  fmt.Sprintf("failed to parse log: %v", errParse),
				HasError:      true,
			})
			continue
		}
		items = append(items, parsed.requestLogListItem)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":          items,
		"total":          total,
		"limit":          limit,
		"offset":         offset,
		"retention_days": requestLogRetentionDays,
	})
}

// GetRequestLogDetail returns cleaned full prompt/output/error text for a request log.
func (h *Handler) GetRequestLogDetail(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" || strings.ContainsAny(id, "/\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request ID"})
		return
	}

	store, errStore := openRequestLogStore(dir)
	if errStore == nil {
		defer store.close()
		if errSync := syncRequestLogStore(c.Request.Context(), store, dir); errSync == nil {
			detail, errDetail := store.detail(c.Request.Context(), id)
			if errDetail == nil {
				c.JSON(http.StatusOK, detail)
				return
			}
			if errDetail != sql.ErrNoRows {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to load request log: %v", errDetail)})
				return
			}
		}
	}

	candidate, errFind := findRequestLogCandidateByID(dir, id)
	if errFind != nil {
		if os.IsNotExist(errFind) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to find request log: %v", errFind)})
		return
	}

	parsed, errParse := parseRequestLogFile(candidate)
	if errParse != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to parse request log: %v", errParse)})
		return
	}

	c.JSON(http.StatusOK, requestLogDetail{
		requestLogListItem: parsed.requestLogListItem,
		Prompt:             parsed.prompt,
		Output:             parsed.output,
		Error:              parsed.error,
		SystemPrompt:       parsed.promptMetadata.SystemPrompt,
		AvailableTools:     parsed.promptMetadata.AvailableTools,
		MCPs:               parsed.promptMetadata.MCPs,
		Skills:             parsed.promptMetadata.Skills,
		CalledTools:        parsed.calledTools,
		PromptMetadata:     parsed.promptMetadata,
		RequestMetadata:    parsed.requestMetadata,
	})
}

// ExportRequestLogs exports structured request log rows from the local SQLite index.
func (h *Handler) ExportRequestLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}
	limit, errLimit := parseRequestLogLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid limit: %v", errLimit)})
		return
	}
	pagesRaw := strings.TrimSpace(c.Query("pages"))
	if pagesRaw != "" {
		pages, errPages := strconv.Atoi(pagesRaw)
		if errPages != nil || pages < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pages"})
			return
		}
		if pages > 100 {
			pages = 100
		}
		limit *= pages
	}
	offset, errOffset := parseRequestLogOffset(c.Query("offset"))
	if errOffset != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid offset: %v", errOffset)})
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "jsonl" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "format must be csv or jsonl"})
		return
	}
	store, errStore := openRequestLogStore(dir)
	if errStore != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to open request log database: %v", errStore)})
		return
	}
	defer store.close()
	if errSync := syncRequestLogStore(c.Request.Context(), store, dir); errSync != nil && !os.IsNotExist(errSync) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to sync request logs: %v", errSync)})
		return
	}
	filename := fmt.Sprintf("request-logs-%s.%s", time.Now().Format("20060102-150405"), format)
	if format == "jsonl" {
		c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	} else {
		c.Header("Content-Type", "text/csv; charset=utf-8")
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	if errExport := store.export(c.Request.Context(), c.Writer, requestLogQueryOptions{Query: c.Query("q"), Limit: limit, Offset: offset}, format); errExport != nil {
		_ = c.Error(errExport)
		return
	}
}

func parseRequestLogLimit(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultRequestLogLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	if limit > maxRequestLogLimit {
		limit = maxRequestLogLimit
	}
	return limit, nil
}

func parseRequestLogOffset(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return offset, nil
}

func collectRequestLogCandidates(dir string) ([]requestLogCandidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	candidates := make([]requestLogCandidate, 0, len(entries))
	cutoff := time.Now().AddDate(0, 0, -requestLogRetentionDays)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isRequestLogFilename(name) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			return nil, errInfo
		}
		path := filepath.Join(dir, name)
		logTime := requestLogTimeFromFile(path, name, info.ModTime())
		if logTime.Before(cutoff) {
			continue
		}
		candidates = append(candidates, requestLogCandidate{
			name:    name,
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
			logTime: logTime,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].logTime.Equal(candidates[j].logTime) {
			return candidates[i].name > candidates[j].name
		}
		return candidates[i].logTime.After(candidates[j].logTime)
	})
	return candidates, nil
}

func requestLogTimeFromFilename(name string, fallback time.Time) time.Time {
	raw := requestLogFilenameTime.FindString(name)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseInLocation("2006-01-02T150405", raw, time.Local)
	if err != nil {
		return fallback
	}
	return parsed
}

func requestLogTimeFromFile(path, name string, fallback time.Time) time.Time {
	timestamp := requestLogTimestampFromFile(path)
	if timestamp != "" {
		if parsed, errParse := time.Parse(time.RFC3339Nano, timestamp); errParse == nil {
			return parsed
		}
	}
	return requestLogTimeFromFilename(name, fallback)
}

func requestLogTimestampFromFile(path string) string {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return ""
	}
	defer func() {
		_ = file.Close()
	}()

	buffer := make([]byte, 8192)
	n, errRead := file.Read(buffer)
	if errRead != nil && n == 0 {
		return ""
	}
	for _, line := range strings.Split(string(buffer[:n]), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Timestamp:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestLogMatchesQuery(parsed parsedRequestLog, query string) bool {
	item := parsed.requestLogListItem
	values := []string{
		item.ID,
		item.Name,
		item.Timestamp,
		item.URL,
		item.Method,
		item.Model,
		item.IP,
		item.IPLocation,
		strconv.Itoa(item.Status),
		item.PromptPreview,
		item.OutputPreview,
		item.ErrorPreview,
		item.ToolPreview,
		item.SystemPromptPreview,
		item.CalledToolsPreview,
		item.SessionID,
		item.ThreadID,
		item.TurnID,
		parsed.prompt,
		parsed.output,
		parsed.error,
		parsed.promptMetadata.SystemPrompt,
	}
	for _, tool := range parsed.promptMetadata.AvailableTools {
		values = append(values, tool.Name, tool.DisplayName, tool.Description, tool.Summary)
	}
	for _, mcp := range parsed.promptMetadata.MCPs {
		values = append(values, mcp.Name, mcp.Description)
	}
	for _, skill := range parsed.promptMetadata.Skills {
		values = append(values, skill.Name, skill.Description, skill.Path, skill.Prompt)
	}
	for _, tool := range parsed.calledTools {
		values = append(values, tool.Name, tool.DisplayName, tool.Summary)
	}
	for key, value := range parsed.requestMetadata {
		values = append(values, key, value)
	}
	for _, value := range values {
		value = strings.ToLower(value)
		if strings.Contains(value, query) || strings.Contains(normalizeRequestLogSearchText(value), query) {
			return true
		}
	}
	if (query == "成功" || query == "success") && item.Success {
		return true
	}
	if (query == "失败" || query == "fail" || query == "error") && !item.Success {
		return true
	}
	return false
}

func findRequestLogCandidateByID(dir, id string) (requestLogCandidate, error) {
	candidates, err := collectRequestLogCandidates(dir)
	if err != nil {
		return requestLogCandidate{}, err
	}
	suffix := "-" + id + ".log"
	for _, candidate := range candidates {
		if strings.HasSuffix(candidate.name, suffix) {
			return candidate, nil
		}
	}
	return requestLogCandidate{}, os.ErrNotExist
}

func isRequestLogFilename(name string) bool {
	if !strings.HasSuffix(name, ".log") {
		return false
	}
	if name == defaultLogFileName || isRotatedLogFile(name) {
		return false
	}
	if strings.HasPrefix(name, "request-body-") || strings.HasPrefix(name, "response-body-") || strings.HasPrefix(name, "request-log-parts-") {
		return false
	}
	return isAIRequestLogFilename(name)
}

func isAIRequestLogFilename(name string) bool {
	if strings.HasPrefix(name, "error-") {
		name = strings.TrimPrefix(name, "error-")
	}
	allowedPrefixes := []string{
		"v1-responses-",
		"v1-chat-completions-",
		"v1-completions-",
		"v1-messages-",
		"v1beta-models-",
		"api-provider-",
		"openai-",
		"anthropic-",
		"gemini-",
		"codex-",
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func parseRequestLogFile(candidate requestLogCandidate) (parsedRequestLog, error) {
	data, errRead := os.ReadFile(candidate.path)
	if errRead != nil {
		return parsedRequestLog{}, errRead
	}
	sections := splitRequestLogSections(string(data))
	info := parseKeyValueSection(firstSection(sections, "REQUEST INFO"))
	headers := parseKeyValueSection(firstSection(sections, "HEADERS"))
	requestBody := firstSection(sections, "REQUEST BODY")
	response := firstSection(sections, "RESPONSE")
	apiErrors := sections["API ERROR RESPONSE"]

	status := parseResponseStatus(response)
	prompt := extractPromptText(requestBody)
	promptMetadata := extractPromptMetadata(requestBody)
	requestMetadata := mergeRequestMetadata(promptMetadata.RequestMetadata, requestMetadataFromHeaders(headers))
	promptMetadata.RequestMetadata = requestMetadata
	calledTools := extractCalledTools(response)
	output := extractResponseText(response, status)
	errText := extractErrorPreviewText(apiErrors, response, status)
	errFullText := extractErrorFullText(apiErrors, response, status)
	if strings.TrimSpace(errFullText) == "" {
		errFullText = errText
	}
	ip := requestIP(headers)
	location := requestIPLocation(headers, ip)
	id := requestLogIDFromFilename(candidate.name)

	timestamp := strings.TrimSpace(info["Timestamp"])
	if timestamp == "" {
		timestamp = candidate.modTime.Format(time.RFC3339Nano)
	}

	return parsedRequestLog{
		requestLogListItem: requestLogListItem{
			ID:                  id,
			Name:                candidate.name,
			Size:                candidate.size,
			Modified:            candidate.modTime.Unix(),
			Timestamp:           timestamp,
			URL:                 strings.TrimSpace(info["URL"]),
			Method:              strings.TrimSpace(info["Method"]),
			Model:               extractModel(requestBody),
			IP:                  ip,
			IPLocation:          location,
			Status:              status,
			Success:             status >= 200 && status < 400 && strings.TrimSpace(errText) == "",
			PromptPreview:       previewText(prompt),
			OutputPreview:       previewText(output),
			ErrorPreview:        previewText(errText),
			ToolPreview:         promptMetadata.ToolPreview,
			SystemPromptPreview: promptMetadata.SystemPromptPreview,
			CalledToolsPreview:  calledToolsPreview(calledTools),
			SessionID:           requestMetadata["session_id"],
			ThreadID:            requestMetadata["thread_id"],
			TurnID:              requestMetadata["turn_id"],
			HasError:            strings.TrimSpace(errText) != "" || status >= 400,
		},
		prompt:          prompt,
		output:          output,
		error:           errFullText,
		promptMetadata:  promptMetadata,
		calledTools:     calledTools,
		requestMetadata: requestMetadata,
	}, nil
}

func splitRequestLogSections(text string) map[string][]string {
	sections := make(map[string][]string)
	current := ""
	var builder strings.Builder
	flush := func() {
		if current == "" {
			builder.Reset()
			return
		}
		sections[current] = append(sections[current], strings.TrimSpace(builder.String()))
		builder.Reset()
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		matches := requestLogSectionHeader.FindStringSubmatch(trimmed)
		if len(matches) == 2 {
			flush()
			current = normalizeRequestLogSectionName(matches[1])
			continue
		}
		if current != "" {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	flush()
	return sections
}

func normalizeRequestLogSectionName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "API REQUEST ") {
		return "API REQUEST"
	}
	if strings.HasPrefix(name, "API RESPONSE ") {
		return "API RESPONSE"
	}
	return name
}

func firstSection(sections map[string][]string, name string) string {
	values := sections[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func parseKeyValueSection(section string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(section, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func parseResponseStatus(response string) int {
	for _, line := range strings.Split(response, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Status:"); ok {
			status, _ := strconv.Atoi(strings.TrimSpace(value))
			return status
		}
	}
	return 0
}

func extractModel(body string) string {
	var payload any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}
	if object, ok := payload.(map[string]any); ok {
		return stringFromAny(object["model"])
	}
	return ""
}

func extractPromptText(body string) string {
	var payload any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return cleanDisplayText(body)
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return cleanDisplayText(textFromJSONValue(payload))
	}

	var parts []string
	if text := latestRoleText(object["messages"], "user"); text != "" {
		parts = append(parts, text)
	}
	if text := latestResponsesInputText(object["input"], "user"); text != "" {
		parts = append(parts, text)
	}
	if text := latestGeminiContentText(object["contents"], "user"); text != "" {
		parts = append(parts, text)
	}
	if prompt := stringFromAny(object["prompt"]); prompt != "" {
		parts = append(parts, prompt)
	}
	if len(parts) == 0 {
		if text := latestResponsesInputText(object["input"], ""); text != "" {
			parts = append(parts, text)
		}
		if text := latestGeminiContentText(object["contents"], ""); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return cleanDisplayText(textFromJSONValue(payload))
	}
	return cleanDisplayText(strings.Join(uniqueStrings(parts), "\n\n"))
}

func latestRoleText(value any, targetRole string) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringFromAny(object["role"])))
		if targetRole != "" && role != targetRole {
			continue
		}
		if text := textFromJSONValue(object["content"]); text != "" {
			return text
		}
	}
	return ""
}

func latestResponsesInputText(value any, targetRole string) string {
	switch typed := value.(type) {
	case string:
		if targetRole == "" || targetRole == "user" {
			return typed
		}
	case []any:
		for i := len(typed) - 1; i >= 0; i-- {
			item := typed[i]
			object, ok := item.(map[string]any)
			if !ok {
				if targetRole == "" {
					if text := textFromJSONValue(item); text != "" {
						return text
					}
				}
				continue
			}
			role := strings.ToLower(strings.TrimSpace(stringFromAny(object["role"])))
			if targetRole != "" && role != targetRole {
				continue
			}
			if text := textFromJSONValue(object["content"]); text != "" {
				return text
			}
		}
	}
	return ""
}

func latestGeminiContentText(value any, targetRole string) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringFromAny(object["role"])))
		if targetRole != "" && role != "" && role != targetRole {
			continue
		}
		if text := textFromJSONValue(object["parts"]); text != "" {
			return text
		}
	}
	return ""
}

func normalizeRequestLogSearchText(value string) string {
	value = strings.ReplaceAll(value, "T", " ")
	value = strings.ReplaceAll(value, "t", " ")
	return strings.Join(strings.Fields(value), " ")
}

func extractResponseText(response string, status int) string {
	body := responseBody(response)
	if body == "" {
		return "响应体为空（未记录最终输出）"
	}
	if status >= 400 {
		return cleanDisplayText(formatJSONForDisplay(body))
	}
	return cleanDisplayText(extractTextFromResponseBody(body))
}

func extractErrorPreviewText(apiErrors []string, response string, status int) string {
	var parts []string
	for _, section := range apiErrors {
		parts = append(parts, extractTextFromResponseBody(stripStatusAndHeaders(section)))
	}
	if len(nonEmptyStrings(parts)) == 0 {
		responseError := extractErrorFromResponseBody(responseBody(response))
		if responseError != "" {
			parts = append(parts, responseError)
		} else if status >= 400 {
			parts = append(parts, extractTextFromResponseBody(responseBody(response)))
		}
	}
	return cleanDisplayText(strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n\n"))
}

func extractErrorFullText(apiErrors []string, response string, status int) string {
	var parts []string
	for _, section := range apiErrors {
		if text := stripStatusAndHeaders(section); text != "" {
			parts = append(parts, formatJSONForDisplay(text))
		}
	}
	if len(nonEmptyStrings(parts)) == 0 && status >= 400 {
		if body := responseBody(response); body != "" {
			parts = append(parts, formatJSONForDisplay(body))
		}
	}
	return cleanDisplayText(strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n\n"))
}

func responseBody(response string) string {
	lines := strings.Split(response, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return ""
}

func stripStatusAndHeaders(text string) string {
	lines := strings.Split(text, "\n")
	var kept []string
	inBody := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			inBody = true
			continue
		}
		if !inBody {
			if strings.HasPrefix(trimmed, "HTTP Status:") || strings.HasPrefix(trimmed, "Status:") || strings.Contains(trimmed, ":") {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func extractTextFromResponseBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "\ndata:") || strings.HasPrefix(trimmed, "data:") {
		return extractTextFromSSE(trimmed)
	}
	return extractTextFromJSONOrRaw(trimmed)
}

func extractTextFromSSE(text string) string {
	var parts []string
	var deltaBuilder strings.Builder
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if delta := responseDeltaFromJSON(data); delta != "" {
			deltaBuilder.WriteString(delta)
			continue
		}
		parts = append(parts, responseFinalTextFromJSON(data))
	}
	if delta := cleanDisplayText(deltaBuilder.String()); delta != "" {
		return delta
	}
	if text := strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n"); text != "" {
		return text
	}
	return responseSSEFallbackSummary(text)
}

func extractTextFromJSONOrRaw(text string) string {
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return text
	}
	if text := assistantResponseText(payload); text != "" {
		return text
	}
	if message := errorMessageFromJSON(text); message != "" {
		return message
	}
	return responseJSONFallbackSummary(payload)
}

func formatJSONForDisplay(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return trimmed
	}
	formatted, errMarshal := json.MarshalIndent(payload, "", "  ")
	if errMarshal != nil {
		return trimmed
	}
	return string(formatted)
}

func extractErrorFromResponseBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	var parts []string
	if strings.Contains(trimmed, "\ndata:") || strings.HasPrefix(trimmed, "data:") {
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			if message := errorMessageFromJSON(data); message != "" {
				parts = append(parts, message)
			}
		}
		return strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n")
	}
	return errorMessageFromJSON(trimmed)
}

func errorMessageFromJSON(text string) string {
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return ""
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	if errorObject, ok := object["error"].(map[string]any); ok {
		if message := stringFromAny(errorObject["message"]); message != "" {
			return message
		}
	}
	if strings.EqualFold(stringFromAny(object["type"]), "error") {
		return stringFromAny(object["message"])
	}
	return ""
}

func responseDeltaFromJSON(text string) string {
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return ""
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	return responseDeltaFromObject(object)
}

func responseFinalTextFromJSON(text string) string {
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return ""
	}
	return assistantResponseText(payload)
}

func responseSSEFallbackSummary(text string) string {
	var toolNames []string
	var eventTypes []string
	hasCompleted := false
	hasEmptyCompleted := false
	hasData := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		hasData = true
		var payload any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		object, ok := payload.(map[string]any)
		if !ok {
			continue
		}
		eventType := strings.TrimSpace(stringFromAny(object["type"]))
		if eventType != "" {
			eventTypes = append(eventTypes, eventType)
		}
		if strings.EqualFold(eventType, "response.completed") {
			hasCompleted = true
			if responseObject, ok := object["response"].(map[string]any); ok {
				if output, ok := responseObject["output"].([]any); ok && len(output) == 0 {
					hasEmptyCompleted = true
				}
			}
		}
		toolNames = append(toolNames, toolNamesFromResponseObject(object)...)
	}
	if len(toolNames) > 0 {
		return "仅工具调用，无最终文本输出：" + summarizeValues(toolNames)
	}
	if hasEmptyCompleted {
		return "模型响应完成，但 output 为空（未返回最终文本）"
	}
	if hasCompleted {
		return "模型响应完成，但未记录最终文本"
	}
	if hasData {
		return "未解析到最终文本（事件：" + summarizeValues(eventTypes) + "）"
	}
	return "响应体为空（未记录最终输出）"
}

func responseJSONFallbackSummary(value any) string {
	toolNames := toolNamesFromResponseObject(value)
	if len(toolNames) > 0 {
		return "仅工具调用，无最终文本输出：" + summarizeValues(toolNames)
	}
	return "未解析到最终文本"
}

func toolNamesFromResponseObject(value any) []string {
	switch typed := value.(type) {
	case map[string]any:
		eventType := strings.ToLower(strings.TrimSpace(stringFromAny(typed["type"])))
		var names []string
		if isToolResponseType(eventType) || eventType == "response.output_item.added" || eventType == "response.output_item.done" {
			if name := stringFromAny(typed["name"]); name != "" {
				names = append(names, name)
			}
			if item, ok := typed["item"].(map[string]any); ok {
				if name := stringFromAny(item["name"]); name != "" {
					names = append(names, name)
				}
				if itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"]))); isToolResponseType(itemType) {
					if name := stringFromAny(item["name"]); name != "" {
						names = append(names, name)
					}
				}
			}
		}
		for _, key := range []string{"response", "output", "choices"} {
			names = append(names, toolNamesFromResponseObject(typed[key])...)
		}
		return names
	case []any:
		var names []string
		for _, item := range typed {
			names = append(names, toolNamesFromResponseObject(item)...)
		}
		return names
	default:
		return nil
	}
}

func summarizeValues(values []string) string {
	values = uniqueStrings(nonEmptyStrings(values))
	if len(values) == 0 {
		return "未知"
	}
	if len(values) > 4 {
		values = values[:4]
		return strings.Join(values, "、") + " 等"
	}
	return strings.Join(values, "、")
}

func responseDeltaFromObject(object map[string]any) string {
	eventType := strings.ToLower(strings.TrimSpace(stringFromAny(object["type"])))
	if isToolResponseType(eventType) {
		return ""
	}
	switch eventType {
	case "response.output_text.delta":
		return stringFromAny(object["delta"])
	case "content_block_delta":
		if deltaObject, ok := object["delta"].(map[string]any); ok {
			if strings.EqualFold(stringFromAny(deltaObject["type"]), "text_delta") {
				return stringFromAny(deltaObject["text"])
			}
		}
	case "message_delta", "message.delta":
		if deltaObject, ok := object["delta"].(map[string]any); ok {
			if text := contentText(deltaObject["content"]); text != "" {
				return text
			}
			return stringFromAny(deltaObject["text"])
		}
	}
	if choices, ok := object["choices"].([]any); ok {
		var parts []string
		for _, choice := range choices {
			choiceObject, ok := choice.(map[string]any)
			if !ok {
				continue
			}
			if deltaObject, ok := choiceObject["delta"].(map[string]any); ok {
				parts = append(parts, contentText(deltaObject["content"]))
			}
			parts = append(parts, stringFromAny(choiceObject["text"]))
		}
		return strings.Join(nonEmptyStrings(parts), "")
	}
	return ""
}

func assistantResponseText(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if isToolResponseType(strings.ToLower(strings.TrimSpace(stringFromAny(typed["type"])))) {
			return ""
		}
		var parts []string
		if text := stringFromAny(typed["output_text"]); text != "" {
			parts = append(parts, text)
		}
		if text := responseTextFromOutput(typed["output"]); text != "" {
			parts = append(parts, text)
		}
		if text := chatChoicesText(typed["choices"]); text != "" {
			parts = append(parts, text)
		}
		if text := geminiCandidatesText(typed["candidates"]); text != "" {
			parts = append(parts, text)
		}
		if text := messageObjectText(typed["message"]); text != "" {
			parts = append(parts, text)
		}
		if text := assistantResponseText(typed["response"]); text != "" {
			parts = append(parts, text)
		}
		if text := responseDeltaFromObject(typed); text != "" {
			parts = append(parts, text)
		}
		if shouldReadTopLevelContent(typed) {
			if text := contentText(typed["content"]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n")
	case []any:
		var parts []string
		for _, item := range typed {
			parts = append(parts, assistantResponseText(item))
		}
		return strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n")
	}
	return ""
}

func responseTextFromOutput(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(stringFromAny(object["type"])))
		role := strings.ToLower(strings.TrimSpace(stringFromAny(object["role"])))
		if itemType != "" && itemType != "message" {
			continue
		}
		if role != "" && role != "assistant" {
			continue
		}
		parts = append(parts, contentText(object["content"]))
	}
	return strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n")
}

func chatChoicesText(value any) string {
	choices, ok := value.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, choice := range choices {
		object, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		if messageObject, ok := object["message"].(map[string]any); ok {
			parts = append(parts, contentText(messageObject["content"]))
		}
		if deltaObject, ok := object["delta"].(map[string]any); ok {
			parts = append(parts, contentText(deltaObject["content"]))
		}
		parts = append(parts, stringFromAny(object["text"]))
	}
	return strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n")
}

func geminiCandidatesText(value any) string {
	candidates, ok := value.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, candidate := range candidates {
		object, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		if contentObject, ok := object["content"].(map[string]any); ok {
			parts = append(parts, contentText(contentObject["parts"]))
		}
	}
	return strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n")
}

func messageObjectText(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	role := strings.ToLower(strings.TrimSpace(stringFromAny(object["role"])))
	if role != "" && role != "assistant" {
		return ""
	}
	return contentText(object["content"])
}

func contentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		var parts []string
		for _, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				parts = append(parts, contentText(item))
				continue
			}
			itemType := strings.ToLower(strings.TrimSpace(stringFromAny(object["type"])))
			if isToolResponseType(itemType) {
				continue
			}
			if itemType == "" || itemType == "text" || itemType == "output_text" || itemType == "input_text" {
				parts = append(parts, stringFromAny(object["text"]))
			}
		}
		return strings.Join(nonEmptyStrings(parts), "\n")
	case map[string]any:
		if isToolResponseType(strings.ToLower(strings.TrimSpace(stringFromAny(typed["type"])))) {
			return ""
		}
		if text := stringFromAny(typed["text"]); text != "" {
			return text
		}
		return contentText(typed["parts"])
	default:
		return ""
	}
}

func shouldReadTopLevelContent(object map[string]any) bool {
	eventType := strings.ToLower(strings.TrimSpace(stringFromAny(object["type"])))
	if eventType == "" {
		_, hasChoices := object["choices"]
		_, hasOutput := object["output"]
		_, hasCandidates := object["candidates"]
		return !hasChoices && !hasOutput && !hasCandidates
	}
	return eventType == "message" || eventType == "text" || eventType == "output_text" || eventType == "content_block_delta"
}

func isToolResponseType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(value, "function_call") ||
		strings.Contains(value, "tool_call") ||
		strings.Contains(value, "tool_result") ||
		strings.Contains(value, "function_call_output") ||
		value == "function_call" ||
		value == "function_call_output"
}

func extractPromptMetadata(body string) requestPromptMetadata {
	var payload any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return requestPromptMetadata{}
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return requestPromptMetadata{}
	}

	systemPrompt := cleanDisplayText(strings.Join(uniqueStrings(nonEmptyStrings([]string{
		stringFromAny(object["instructions"]),
		systemTextFromMessages(object["messages"]),
		systemTextFromMessages(object["input"]),
	})), "\n\n"))
	tools := extractAvailableTools(object["tools"])
	mcps := groupMCPTools(tools)
	skills := extractSkills(systemPrompt, object["input"])
	requestMetadata := requestMetadataFromObject(object["client_metadata"])

	return requestPromptMetadata{
		SystemPrompt:        systemPrompt,
		SystemPromptPreview: previewText(systemPrompt),
		ToolPreview:         toolPreview(mcps, skills),
		AvailableTools:      tools,
		MCPs:                mcps,
		Skills:              skills,
		RequestMetadata:     requestMetadata,
	}
}

func systemTextFromMessages(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringFromAny(object["role"])))
		if role != "system" && role != "developer" {
			continue
		}
		if text := textFromJSONValue(object["content"]); text != "" {
			parts = append(parts, text)
		}
	}
	return cleanDisplayText(strings.Join(uniqueStrings(nonEmptyStrings(parts)), "\n\n"))
}

func extractAvailableTools(value any) []requestLogToolInfo {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	var tools []requestLogToolInfo
	seen := make(map[string]struct{})
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := stringFromAny(object["name"])
		description := stringFromAny(object["description"])
		toolType := stringFromAny(object["type"])
		if functionObject, ok := object["function"].(map[string]any); ok {
			if name == "" {
				name = stringFromAny(functionObject["name"])
			}
			if description == "" {
				description = stringFromAny(functionObject["description"])
			}
			if toolType == "" {
				toolType = "function"
			}
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, requestLogToolInfo{
			Name:        name,
			DisplayName: displayToolName(name),
			Type:        toolType,
			Description: cleanDisplayText(description),
			Summary:     previewText(description),
		})
	}
	return tools
}

func groupMCPTools(tools []requestLogToolInfo) []requestLogMCPGroup {
	index := make(map[string]int)
	var groups []requestLogMCPGroup
	for _, tool := range tools {
		server := mcpServerName(tool.Name)
		if server == "" {
			continue
		}
		pos, exists := index[server]
		if !exists {
			index[server] = len(groups)
			groups = append(groups, requestLogMCPGroup{Name: server})
			pos = len(groups) - 1
		}
		if groups[pos].Description == "" && tool.Description != "" {
			groups[pos].Description = tool.Description
		}
		groups[pos].Tools = append(groups[pos].Tools, tool)
	}
	return groups
}

func mcpServerName(name string) string {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "mcp__") {
		return ""
	}
	rest := strings.TrimPrefix(name, "mcp__")
	parts := strings.Split(rest, "__")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			return part
		}
	}
	return ""
}

func displayToolName(name string) string {
	if server := mcpServerName(name); server != "" {
		return server
	}
	return strings.TrimSpace(name)
}

func toolPreview(mcps []requestLogMCPGroup, skills []requestLogSkillInfo) string {
	var parts []string
	var mcpNames []string
	for _, mcp := range mcps {
		mcpNames = append(mcpNames, mcp.Name)
	}
	if len(mcpNames) > 0 {
		parts = append(parts, "MCP: "+summarizeValues(mcpNames))
	}
	var skillNames []string
	for _, skill := range skills {
		skillNames = append(skillNames, skill.Name)
	}
	if len(skillNames) > 0 {
		parts = append(parts, "Skill: "+summarizeValues(skillNames))
	}
	return strings.Join(parts, "；")
}

func extractSkills(systemPrompt string, input any) []requestLogSkillInfo {
	var skills []requestLogSkillInfo
	seen := make(map[string]struct{})
	for _, text := range []string{systemPrompt, textFromJSONValue(input)} {
		for _, skill := range parseSkillList(text) {
			if _, exists := seen[skill.Name]; exists {
				continue
			}
			seen[skill.Name] = struct{}{}
			skills = append(skills, skill)
		}
	}
	return skills
}

func parseSkillList(text string) []requestLogSkillInfo {
	text = cleanDisplayText(text)
	if text == "" {
		return nil
	}
	var skills []requestLogSkillInfo
	inSkillsBlock := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "<skills_instructions>") || strings.Contains(lower, "available skills") {
			inSkillsBlock = true
			continue
		}
		if strings.Contains(lower, "</skills_instructions>") || strings.Contains(lower, "how to use skills") {
			if strings.Contains(lower, "</skills_instructions>") {
				inSkillsBlock = false
			}
			continue
		}
		if !inSkillsBlock || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		separator := strings.Index(raw, ": ")
		if separator < 0 {
			continue
		}
		name := strings.TrimSpace(raw[:separator])
		description := strings.TrimSpace(raw[separator+2:])
		if name == "" {
			continue
		}
		path := ""
		if match := requestLogSkillFile.FindStringSubmatch(description); len(match) == 2 {
			path = strings.TrimSpace(match[1])
			description = strings.TrimSpace(requestLogSkillFile.ReplaceAllString(description, ""))
		}
		skills = append(skills, requestLogSkillInfo{
			Name:        name,
			Description: cleanDisplayText(description),
			Path:        path,
			Prompt:      trimmed,
		})
	}
	return skills
}

func requestMetadataFromObject(value any) map[string]string {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string)
	for key, value := range object {
		str := stringFromAny(value)
		if str == "" {
			continue
		}
		normalized := normalizeMetadataKey(key)
		if normalized != "" {
			out[normalized] = str
		}
		out[key] = str
	}
	return out
}

func requestMetadataFromHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string)
	raw := requestLogHeader(headers, "X-Codex-Turn-Metadata")
	if raw != "" {
		var object map[string]any
		if err := json.Unmarshal([]byte(raw), &object); err == nil {
			for key, value := range requestMetadataFromObject(object) {
				out[key] = value
			}
		} else {
			out["x_codex_turn_metadata"] = raw
		}
	}
	for _, key := range []string{"Session-Id", "Thread-Id", "Turn-Id", "X-Session-Id", "X-Thread-Id", "X-Turn-Id"} {
		if value := requestLogHeader(headers, key); value != "" {
			out[normalizeMetadataKey(key)] = value
		}
	}
	return out
}

func normalizeMetadataKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	switch key {
	case "session_id", "thread_id", "turn_id":
		return key
	case "x_session_id":
		return "session_id"
	case "x_thread_id":
		return "thread_id"
	case "x_turn_id":
		return "turn_id"
	default:
		return key
	}
}

func mergeRequestMetadata(primary, secondary map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range primary {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	for key, value := range secondary {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractCalledTools(response string) []requestLogToolInfo {
	body := responseBody(response)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var tools []requestLogToolInfo
	seen := make(map[string]struct{})
	addTool := func(name, typ string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		tools = append(tools, requestLogToolInfo{
			Name:        name,
			DisplayName: displayToolName(name),
			Type:        typ,
			Summary:     toolCallSummary(name),
		})
	}

	if strings.Contains(body, "\ndata:") || strings.HasPrefix(strings.TrimSpace(body), "data:") {
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			var payload any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				continue
			}
			for _, tool := range calledToolsFromObject(payload) {
				addTool(tool.Name, tool.Type)
			}
		}
		return tools
	}

	var payload any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}
	for _, tool := range calledToolsFromObject(payload) {
		addTool(tool.Name, tool.Type)
	}
	return tools
}

func calledToolsFromObject(value any) []requestLogToolInfo {
	switch typed := value.(type) {
	case map[string]any:
		eventType := strings.ToLower(strings.TrimSpace(stringFromAny(typed["type"])))
		var tools []requestLogToolInfo
		if isToolResponseType(eventType) || eventType == "response.output_item.added" || eventType == "response.output_item.done" {
			if name := stringFromAny(typed["name"]); name != "" {
				tools = append(tools, requestLogToolInfo{Name: name, Type: eventType})
			}
			if item, ok := typed["item"].(map[string]any); ok {
				itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
				if isToolResponseType(itemType) || eventType == "response.output_item.added" || eventType == "response.output_item.done" {
					if name := stringFromAny(item["name"]); name != "" {
						tools = append(tools, requestLogToolInfo{Name: name, Type: itemType})
					}
				}
			}
		}
		for _, key := range []string{"response", "output", "choices"} {
			tools = append(tools, calledToolsFromObject(typed[key])...)
		}
		return tools
	case []any:
		var tools []requestLogToolInfo
		for _, item := range typed {
			tools = append(tools, calledToolsFromObject(item)...)
		}
		return tools
	default:
		return nil
	}
}

func toolCallSummary(name string) string {
	if server := mcpServerName(name); server != "" {
		return "MCP: " + server
	}
	return "调用工具: " + name
}

func calledToolsPreview(tools []requestLogToolInfo) string {
	var names []string
	for _, tool := range tools {
		if tool.DisplayName != "" {
			names = append(names, tool.DisplayName)
		} else {
			names = append(names, tool.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return summarizeValues(names)
}

func textFromJSONValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		var parts []string
		for _, item := range typed {
			if text := textFromJSONValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "input_text", "output_text"} {
			if str := stringFromAny(typed[key]); str != "" {
				return str
			}
		}
		if typ := strings.TrimSpace(stringFromAny(typed["type"])); typ != "" {
			switch {
			case strings.Contains(typ, "image"):
				return "[image]"
			case strings.Contains(typ, "audio"):
				return "[audio]"
			case strings.Contains(typ, "file"):
				return "[file]"
			case strings.Contains(typ, "tool"):
				return "[tool]"
			}
		}
		var parts []string
		for _, key := range []string{"content", "parts", "arguments", "result"} {
			if text := textFromJSONValue(typed[key]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func requestIP(headers map[string]string) string {
	for _, key := range []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP", "X-CPA-Client-IP"} {
		value := requestLogHeader(headers, key)
		if value == "" {
			continue
		}
		if key == "X-Forwarded-For" {
			value = strings.TrimSpace(strings.Split(value, ",")[0])
		}
		if net.ParseIP(value) != nil {
			return value
		}
	}
	return ""
}

func requestIPLocation(headers map[string]string, ip string) string {
	var parts []string
	for _, key := range []string{"X-Vercel-IP-Country", "CF-IPCountry", "CloudFront-Viewer-Country"} {
		if value := requestLogHeader(headers, key); value != "" && !strings.EqualFold(value, "XX") {
			parts = append(parts, value)
			break
		}
	}
	for _, key := range []string{"X-Vercel-IP-City", "X-Geo-City"} {
		if value := requestLogHeader(headers, key); value != "" {
			parts = append(parts, value)
			break
		}
	}
	if len(parts) > 0 {
		return strings.Join(uniqueStrings(parts), " ")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() {
		return "本机"
	}
	if parsed.IsPrivate() {
		return "内网"
	}
	return ""
}

func requestLogHeader(headers map[string]string, key string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers[key]); value != "" {
		return value
	}
	for candidate, value := range headers {
		if strings.EqualFold(candidate, key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestLogIDFromFilename(name string) string {
	base := strings.TrimSuffix(name, ".log")
	if idx := strings.LastIndex(base, "-"); idx >= 0 && idx < len(base)-1 {
		return base[idx+1:]
	}
	return base
}

func previewText(text string) string {
	text = cleanDisplayText(text)
	runes := []rune(text)
	if len(runes) <= previewTextLimit {
		return text
	}
	return string(runes[:previewTextLimit]) + "..."
}

func cleanDisplayText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	var kept []string
	previousBlank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		blank := strings.TrimSpace(line) == ""
		if blank && previousBlank {
			continue
		}
		kept = append(kept, line)
		previousBlank = blank
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func stringFromAny(value any) string {
	if str, ok := value.(string); ok {
		return strings.TrimSpace(str)
	}
	return ""
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = cleanDisplayText(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
