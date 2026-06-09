package management

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const requestLogDBFilename = "request_logs.db"

type requestLogStore struct {
	path string
	db   *sql.DB
}

func openRequestLogStore(logDir string) (*requestLogStore, error) {
	if strings.TrimSpace(logDir) == "" {
		return nil, fmt.Errorf("log directory not configured")
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(logDir, requestLogDBFilename)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &requestLogStore{path: path, db: db}
	if err := store.ensureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *requestLogStore) close() {
	if s != nil && s.db != nil {
		_ = s.db.Close()
	}
}

func (s *requestLogStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS request_log_entries (
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
CREATE INDEX IF NOT EXISTS idx_request_log_entries_timestamp ON request_log_entries(timestamp_unix DESC);
CREATE INDEX IF NOT EXISTS idx_request_log_entries_model ON request_log_entries(model);
CREATE INDEX IF NOT EXISTS idx_request_log_entries_status ON request_log_entries(status);
CREATE INDEX IF NOT EXISTS idx_request_log_entries_success ON request_log_entries(success);
`)
	return err
}

func (s *requestLogStore) pruneBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM request_log_entries WHERE timestamp_unix < ?`, cutoff.Unix())
	return err
}

func (s *requestLogStore) upsertParsed(ctx context.Context, parsed parsedRequestLog, candidate requestLogCandidate) error {
	now := time.Now().Unix()
	item := parsed.requestLogListItem
	availableTools, _ := json.Marshal(parsed.promptMetadata.AvailableTools)
	mcps, _ := json.Marshal(parsed.promptMetadata.MCPs)
	skills, _ := json.Marshal(parsed.promptMetadata.Skills)
	calledTools, _ := json.Marshal(parsed.calledTools)
	promptMetadata, _ := json.Marshal(parsed.promptMetadata)
	requestMetadata, _ := json.Marshal(parsed.requestMetadata)
	success := boolInt(item.Success)
	hasError := boolInt(item.HasError)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO request_log_entries (
  id, name, raw_log_path, size, modified, timestamp_text, timestamp_unix,
  url, method, model, ip, ip_location, status, success,
  prompt, output, error, system_prompt,
  available_tools_json, mcps_json, skills_json, called_tools_json, prompt_metadata_json, request_metadata_json,
  prompt_preview, output_preview, error_preview, tool_preview, system_prompt_preview, called_tools_preview,
  session_id, thread_id, turn_id, has_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name,
  raw_log_path=excluded.raw_log_path,
  size=excluded.size,
  modified=excluded.modified,
  timestamp_text=excluded.timestamp_text,
  timestamp_unix=excluded.timestamp_unix,
  url=excluded.url,
  method=excluded.method,
  model=excluded.model,
  ip=excluded.ip,
  ip_location=excluded.ip_location,
  status=excluded.status,
  success=excluded.success,
  prompt=excluded.prompt,
  output=excluded.output,
  error=excluded.error,
  system_prompt=excluded.system_prompt,
  available_tools_json=excluded.available_tools_json,
  mcps_json=excluded.mcps_json,
  skills_json=excluded.skills_json,
  called_tools_json=excluded.called_tools_json,
  prompt_metadata_json=excluded.prompt_metadata_json,
  request_metadata_json=excluded.request_metadata_json,
  prompt_preview=excluded.prompt_preview,
  output_preview=excluded.output_preview,
  error_preview=excluded.error_preview,
  tool_preview=excluded.tool_preview,
  system_prompt_preview=excluded.system_prompt_preview,
  called_tools_preview=excluded.called_tools_preview,
  session_id=excluded.session_id,
  thread_id=excluded.thread_id,
  turn_id=excluded.turn_id,
  has_error=excluded.has_error,
  updated_at=excluded.updated_at
`, item.ID, item.Name, candidate.path, item.Size, item.Modified, item.Timestamp, candidate.logTime.Unix(), item.URL, item.Method, item.Model, item.IP, item.IPLocation, item.Status, success, parsed.prompt, parsed.output, parsed.error, parsed.promptMetadata.SystemPrompt, string(availableTools), string(mcps), string(skills), string(calledTools), string(promptMetadata), string(requestMetadata), item.PromptPreview, item.OutputPreview, item.ErrorPreview, item.ToolPreview, item.SystemPromptPreview, item.CalledToolsPreview, item.SessionID, item.ThreadID, item.TurnID, hasError, now, now)
	return err
}

func syncRequestLogStore(ctx context.Context, store *requestLogStore, dir string) error {
	candidates, err := collectRequestLogCandidates(dir)
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -requestLogRetentionDays)
	if err := store.pruneBefore(ctx, cutoff); err != nil {
		return err
	}
	for _, candidate := range candidates {
		parsed, errParse := parseRequestLogFile(candidate)
		if errParse != nil {
			continue
		}
		if err := store.upsertParsed(ctx, parsed, candidate); err != nil {
			return err
		}
	}
	return nil
}

type requestLogQueryOptions struct {
	Query  string
	Limit  int
	Offset int
}

func (s *requestLogStore) list(ctx context.Context, opts requestLogQueryOptions) ([]requestLogListItem, int, error) {
	where, args := requestLogWhereClause(opts.Query)
	countQuery := "SELECT COUNT(1) FROM request_log_entries" + where
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, opts.Limit, opts.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, size, modified, timestamp_text, url, method, model, ip, ip_location, status, success, prompt_preview, output_preview, error_preview, tool_preview, system_prompt_preview, called_tools_preview, session_id, thread_id, turn_id, has_error FROM request_log_entries`+where+` ORDER BY timestamp_unix DESC, name DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]requestLogListItem, 0, opts.Limit)
	for rows.Next() {
		var item requestLogListItem
		var success, hasError int
		if err := rows.Scan(&item.ID, &item.Name, &item.Size, &item.Modified, &item.Timestamp, &item.URL, &item.Method, &item.Model, &item.IP, &item.IPLocation, &item.Status, &success, &item.PromptPreview, &item.OutputPreview, &item.ErrorPreview, &item.ToolPreview, &item.SystemPromptPreview, &item.CalledToolsPreview, &item.SessionID, &item.ThreadID, &item.TurnID, &hasError); err != nil {
			return nil, 0, err
		}
		item.Success = success != 0
		item.HasError = hasError != 0
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *requestLogStore) detail(ctx context.Context, id string) (requestLogDetail, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, size, modified, timestamp_text, url, method, model, ip, ip_location, status, success, prompt_preview, output_preview, error_preview, tool_preview, system_prompt_preview, called_tools_preview, session_id, thread_id, turn_id, has_error, prompt, output, error, system_prompt, available_tools_json, mcps_json, skills_json, called_tools_json, prompt_metadata_json, request_metadata_json FROM request_log_entries WHERE id = ?`, id)
	var detail requestLogDetail
	var success, hasError int
	var availableToolsJSON, mcpsJSON, skillsJSON, calledToolsJSON, promptMetadataJSON, requestMetadataJSON string
	if err := row.Scan(&detail.ID, &detail.Name, &detail.Size, &detail.Modified, &detail.Timestamp, &detail.URL, &detail.Method, &detail.Model, &detail.IP, &detail.IPLocation, &detail.Status, &success, &detail.PromptPreview, &detail.OutputPreview, &detail.ErrorPreview, &detail.ToolPreview, &detail.SystemPromptPreview, &detail.CalledToolsPreview, &detail.SessionID, &detail.ThreadID, &detail.TurnID, &hasError, &detail.Prompt, &detail.Output, &detail.Error, &detail.SystemPrompt, &availableToolsJSON, &mcpsJSON, &skillsJSON, &calledToolsJSON, &promptMetadataJSON, &requestMetadataJSON); err != nil {
		return requestLogDetail{}, err
	}
	detail.Success = success != 0
	detail.HasError = hasError != 0
	_ = json.Unmarshal([]byte(availableToolsJSON), &detail.AvailableTools)
	_ = json.Unmarshal([]byte(mcpsJSON), &detail.MCPs)
	_ = json.Unmarshal([]byte(skillsJSON), &detail.Skills)
	_ = json.Unmarshal([]byte(calledToolsJSON), &detail.CalledTools)
	_ = json.Unmarshal([]byte(promptMetadataJSON), &detail.PromptMetadata)
	_ = json.Unmarshal([]byte(requestMetadataJSON), &detail.RequestMetadata)
	return detail, nil
}

func (s *requestLogStore) export(ctx context.Context, w io.Writer, opts requestLogQueryOptions, format string) error {
	where, args := requestLogWhereClause(opts.Query)
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, opts.Limit, opts.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id, timestamp_text, url, method, model, ip, ip_location, status, success, prompt, output, error, system_prompt, called_tools_preview, session_id, thread_id, turn_id FROM request_log_entries`+where+` ORDER BY timestamp_unix DESC, name DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if strings.EqualFold(format, "jsonl") {
		enc := json.NewEncoder(w)
		for rows.Next() {
			row, err := scanRequestLogExportRow(rows)
			if err != nil {
				return err
			}
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "timestamp", "url", "method", "model", "ip", "ip_location", "status", "success", "prompt", "output", "error", "system_prompt", "called_tools", "session_id", "thread_id", "turn_id"}); err != nil {
		return err
	}
	for rows.Next() {
		row, err := scanRequestLogExportRow(rows)
		if err != nil {
			return err
		}
		if err := cw.Write([]string{row["id"], row["timestamp"], row["url"], row["method"], row["model"], row["ip"], row["ip_location"], row["status"], row["success"], row["prompt"], row["output"], row["error"], row["system_prompt"], row["called_tools"], row["session_id"], row["thread_id"], row["turn_id"]}); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	return rows.Err()
}

func scanRequestLogExportRow(rows *sql.Rows) (map[string]string, error) {
	var id, timestamp, url, method, model, ip, ipLocation, prompt, output, errorText, systemPrompt, calledTools, sessionID, threadID, turnID string
	var status int
	var success int
	if err := rows.Scan(&id, &timestamp, &url, &method, &model, &ip, &ipLocation, &status, &success, &prompt, &output, &errorText, &systemPrompt, &calledTools, &sessionID, &threadID, &turnID); err != nil {
		return nil, err
	}
	return map[string]string{
		"id": id, "timestamp": timestamp, "url": url, "method": method, "model": model, "ip": ip, "ip_location": ipLocation,
		"status": fmt.Sprintf("%d", status), "success": fmt.Sprintf("%t", success != 0), "prompt": prompt, "output": output,
		"error": errorText, "system_prompt": systemPrompt, "called_tools": calledTools, "session_id": sessionID, "thread_id": threadID, "turn_id": turnID,
	}, nil
}

func requestLogWhereClause(query string) (string, []any) {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return "", nil
	}
	like := "%" + query + "%"
	return ` WHERE lower(id) LIKE ? OR lower(name) LIKE ? OR lower(url) LIKE ? OR lower(method) LIKE ? OR lower(model) LIKE ? OR lower(ip) LIKE ? OR lower(ip_location) LIKE ? OR lower(prompt) LIKE ? OR lower(output) LIKE ? OR lower(error) LIKE ? OR lower(system_prompt) LIKE ? OR lower(called_tools_preview) LIKE ? OR lower(session_id) LIKE ? OR lower(thread_id) LIKE ? OR lower(turn_id) LIKE ?`, []any{like, like, like, like, like, like, like, like, like, like, like, like, like, like, like}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
