package management

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type requestLogReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Keep short per-request fields apart from the full bodies. Identical long texts
// are searched once and mapped back to all matching requests, without truncation.
var requestLogSearchFields = []string{
	"id", "name", "timestamp_text", "url", "method", "model", "provider", "provider_name",
	"auth_id", "auth_type", "upstream_url", "upstream_model", "channel_model", "ip", "ip_location",
	"called_tools_preview", "session_id", "thread_id", "turn_id",
}

func (s *requestLogStore) ensureSearchSchema(ctx context.Context) error {
	columns := strings.Join(requestLogSearchFields, ", ")
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS request_log_search_entries AS
SELECT `+columns+`, timestamp_unix, status, success FROM request_log_entries WHERE 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_log_search_id ON request_log_search_entries(id);
CREATE INDEX IF NOT EXISTS idx_request_log_search_time ON request_log_search_entries(timestamp_unix DESC, name DESC);
CREATE TABLE IF NOT EXISTS request_log_search_texts (
  id INTEGER PRIMARY KEY,
  digest BLOB NOT NULL UNIQUE,
  content TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS request_log_search_refs (
  entry_id TEXT NOT NULL,
  text_id INTEGER NOT NULL,
  PRIMARY KEY(entry_id, text_id)
);
CREATE INDEX IF NOT EXISTS idx_request_log_search_text_refs ON request_log_search_refs(text_id, entry_id);
CREATE TABLE IF NOT EXISTS request_log_search_state (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  ready INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO request_log_search_state(id, ready) VALUES(1, 0);
CREATE TRIGGER IF NOT EXISTS request_log_search_insert AFTER INSERT ON request_log_entries BEGIN
  UPDATE request_log_search_state SET ready = 0 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS request_log_search_update AFTER UPDATE ON request_log_entries BEGIN
  DELETE FROM request_log_search_refs WHERE entry_id = OLD.id;
  DELETE FROM request_log_search_entries WHERE id = OLD.id;
  UPDATE request_log_search_state SET ready = 0 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS request_log_search_delete AFTER DELETE ON request_log_entries BEGIN
  DELETE FROM request_log_search_refs WHERE entry_id = OLD.id;
  DELETE FROM request_log_search_entries WHERE id = OLD.id;
END;
`)
	if err != nil {
		return fmt.Errorf("create request log search index: %w", err)
	}
	return updateRequestLogSearchReady(ctx, s.db)
}

func updateRequestLogSearchReady(ctx context.Context, execer requestLogExecer) error {
	_, err := execer.ExecContext(ctx, `UPDATE request_log_search_state SET ready = NOT EXISTS (
SELECT 1 FROM request_log_entries AS entry
LEFT JOIN request_log_search_entries AS indexed ON indexed.id = entry.id
WHERE indexed.id IS NULL LIMIT 1) WHERE id = 1`)
	return err
}

func requestLogSearchReady(ctx context.Context, reader requestLogReader) (bool, error) {
	var ready bool
	err := reader.QueryRowContext(ctx, `SELECT ready FROM request_log_search_state WHERE id = 1`).Scan(&ready)
	return ready, err
}

func upsertRequestLogSearch(ctx context.Context, tx *sql.Tx, id string, texts []string) error {
	columns := strings.Join(requestLogSearchFields, ", ") + ", timestamp_unix, status, success"
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO request_log_search_entries (`+columns+`)
SELECT `+columns+` FROM request_log_entries WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM request_log_search_refs WHERE entry_id = ?`, id); err != nil {
		return err
	}
	for _, text := range texts {
		if text == "" {
			continue
		}
		digest := sha256.Sum256([]byte(text))
		var textID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM request_log_search_texts WHERE digest = ?`, digest[:]).Scan(&textID)
		if errors.Is(err, sql.ErrNoRows) {
			// Use SQLite's own lower() to preserve the established LIKE semantics,
			// including non-ASCII text and logs containing invalid UTF-8 bytes.
			err = tx.QueryRowContext(ctx, `INSERT INTO request_log_search_texts(digest, content) VALUES(?, lower(?)) RETURNING id`, digest[:], text).Scan(&textID)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO request_log_search_refs(entry_id, text_id) VALUES(?, ?)`, id, textID); err != nil {
			return err
		}
	}
	return nil
}

// Backfill existing structured records in bounded transactions. Raw files may
// already have rotated away; existing content remains searchable during migration.
func (s *requestLogStore) backfillSearch(ctx context.Context, batchSize int) error {
	if batchSize <= 0 {
		batchSize = defaultRequestLogBatchSize
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := requestLogSearchReady(ctx, s.db)
		if err != nil || ready {
			return err
		}
		if err := s.backfillSearchBatch(ctx, batchSize); err != nil {
			return err
		}
	}
}

func (s *requestLogStore) backfillSearchBatch(ctx context.Context, batchSize int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT entry.id, COALESCE(entry.prompt, ''), COALESCE(entry.output, ''), COALESCE(entry.error, ''), COALESCE(entry.system_prompt, '')
FROM request_log_entries AS entry
LEFT JOIN request_log_search_entries AS indexed ON indexed.id = entry.id
WHERE indexed.id IS NULL LIMIT ?`, batchSize)
	if err != nil {
		return err
	}
	type pendingEntry struct {
		id    string
		texts [4]string
	}
	pending := make([]pendingEntry, 0, batchSize)
	for rows.Next() {
		var entry pendingEntry
		if err := rows.Scan(&entry.id, &entry.texts[0], &entry.texts[1], &entry.texts[2], &entry.texts[3]); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entry := range pending {
		if err := upsertRequestLogSearch(ctx, tx, entry.id, entry.texts[:]); err != nil {
			return err
		}
	}
	if err := updateRequestLogSearchReady(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *requestLogStore) pruneSearchTexts(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM request_log_search_texts
WHERE NOT EXISTS (SELECT 1 FROM request_log_search_refs WHERE text_id = request_log_search_texts.id)`)
	return err
}

func requestLogIndexedMatchQuery(query string) (string, []any) {
	like := "%" + strings.TrimSpace(strings.ToLower(query)) + "%"
	parts := make([]string, 0, len(requestLogSearchFields)+2)
	args := make([]any, 0, len(requestLogSearchFields)+3)
	for _, field := range requestLogSearchFields {
		parts = append(parts, "lower(COALESCE("+field+", '')) LIKE ?")
		args = append(args, like)
	}
	parts = append(parts, "CAST(COALESCE(status, 0) AS TEXT) LIKE ?")
	args = append(args, like)
	parts = append(parts, "lower(CASE WHEN COALESCE(success, 0) != 0 THEN 'success 成功' ELSE 'fail failed failure error 失败 错误' END) LIKE ?")
	args = append(args, like)
	// If short fields already match every entry (for example "%" or a common
	// letter), LIMIT 0 skips the full-text scan. An AND predicate may be reordered
	// by SQLite after the text lookup and would still read the bodies.
	// Bind the shared-body predicate after all short-field predicates.
	args = append(args, like)
	return `WITH short_matches AS MATERIALIZED (
SELECT id FROM request_log_search_entries WHERE ` + strings.Join(parts, " OR ") + `)
SELECT id FROM short_matches
UNION SELECT entry_id FROM request_log_search_refs WHERE text_id IN (
SELECT id FROM request_log_search_texts WHERE content LIKE ?
LIMIT CASE WHEN (SELECT COUNT(*) FROM short_matches) < (SELECT COUNT(*) FROM request_log_search_entries) THEN -1 ELSE 0 END)`, args
}

// Count and page the matching identifiers without re-scanning full text for each
// half of the list response. Only the requested page reads wide detail records.
func requestLogSearchPage(ctx context.Context, reader requestLogReader, opts requestLogQueryOptions) ([]string, int, error) {
	matches, args := requestLogIndexedMatchQuery(opts.Query)
	rows, err := reader.QueryContext(ctx, `SELECT id FROM request_log_search_entries WHERE id IN (`+matches+`)
ORDER BY timestamp_unix DESC, name DESC`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	total := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		if total >= opts.Offset && len(ids) < opts.Limit {
			ids = append(ids, id)
		}
		total++
	}
	return ids, total, rows.Err()
}
