package management

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
	log "github.com/sirupsen/logrus"
)

// ObservationSource supplies bounded snapshots without touching inference state.
// Changes returns a bounded, non-destructive, deep-copied snapshot.
// Get returns a single record by observation ID.
type ObservationSource interface {
	Changes(after uint64) requestlog.ChangeSet
	Get(id string) (requestlog.Record, bool)
}

// observationWriterStatus tracks coalescing worker health for the UI.
type observationWriterStatus struct {
	Available            bool
	LastVersion          uint64
	LastWriteAt          time.Time
	LastWriteError       string
	HistoryGap           bool
	MissedObservations   uint64
	InterruptedAtRestart int64
	Syncing              bool
}

// observationWarning returns a human-readable warning string when capture or
// write gaps exist. Empty string means no warning.
func (s observationWriterStatus) warning() string {
	if !s.Available {
		return ""
	}
	var parts []string
	if s.LastWriteError != "" {
		parts = append(parts, fmt.Sprintf("last observation write failed: %s", s.LastWriteError))
	}
	if s.MissedObservations > 0 {
		parts = append(parts, fmt.Sprintf("%d request observation(s) omitted at capacity", s.MissedObservations))
	}
	if s.HistoryGap {
		parts = append(parts, "some observation updates fell outside the in-memory window")
	}
	if s.InterruptedAtRestart > 0 {
		parts = append(parts, fmt.Sprintf("%d active observation(s) marked interrupted at restart", s.InterruptedAtRestart))
	}
	return strings.Join(parts, "; ")
}

// observationWriter coalesces in-memory observation snapshots into the SQLite
// metadata table. It is owned by the index manager lifecycle and never blocks
// the hot request path or influences inference outcomes.
type observationWriter struct {
	source   ObservationSource
	store    *requestLogStore
	now      func() time.Time
	syncLoop time.Duration

	mu          sync.RWMutex
	status      observationWriterStatus
	lastVersion uint64
	initialized bool
	writeMu     sync.Mutex
	startOnce   sync.Once
	trigger     chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newObservationWriter(source ObservationSource, store *requestLogStore, syncLoop time.Duration, now func() time.Time) *observationWriter {
	if now == nil {
		now = time.Now
	}
	if syncLoop <= 0 {
		syncLoop = 250 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &observationWriter{
		source:   source,
		store:    store,
		now:      now,
		syncLoop: syncLoop,
		ctx:      ctx,
		cancel:   cancel,
		trigger:  make(chan struct{}, 1),
		status: observationWriterStatus{
			Available: source != nil,
		},
	}
	return w
}

func (w *observationWriter) Start() {
	if w == nil || w.source == nil {
		return
	}
	w.startOnce.Do(func() {
		w.wg.Add(1)
		go w.run()
	})
}

// TriggerSync schedules a coalesced write; management reads never perform it.
func (w *observationWriter) TriggerSync() {
	if w == nil || w.source == nil {
		return
	}
	select {
	case <-w.ctx.Done():
	case w.trigger <- struct{}{}:
	default:
	}
}

func (w *observationWriter) Close() {
	if w == nil {
		return
	}
	w.cancel()
	w.wg.Wait()
}

func (w *observationWriter) Status() observationWriterStatus {
	if w == nil {
		return observationWriterStatus{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

func (w *observationWriter) run() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.syncLoop)
	defer ticker.Stop()
	w.syncOnce()
	for {
		select {
		case <-w.ctx.Done():
			w.syncWithContext(context.Background())
			return
		case <-w.trigger:
			w.syncOnce()
		case <-ticker.C:
			w.syncOnce()
		}
	}
}

func (w *observationWriter) syncOnce() { w.syncWithContext(w.ctx) }

// A single writer owns the cursor, including explicit test-triggered flushes.
func (w *observationWriter) syncWithContext(ctx context.Context) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.source == nil || w.store == nil {
		return
	}
	w.mu.Lock()
	w.status.Syncing = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.status.Syncing = false
		w.mu.Unlock()
	}()
	if !w.initialized {
		count, err := w.store.markActiveObservationsInterrupted(ctx)
		if err != nil {
			w.setError(fmt.Sprintf("recover persisted observations: %v", err))
			return
		}
		w.initialized = true
		w.mu.Lock()
		w.status.InterruptedAtRestart = count
		w.mu.Unlock()
	}
	changes := w.source.Changes(w.lastVersion)
	w.mu.Lock()
	w.status.MissedObservations = changes.Dropped
	w.status.HistoryGap = w.status.HistoryGap || changes.Reset
	w.mu.Unlock()
	if err := w.store.upsertObservationBatch(ctx, changes.Records); err != nil {
		w.setError(fmt.Sprintf("persist observations: %v", err))
		return
	}
	if changes.Reset {
		ids := make([]string, 0, len(changes.Records))
		for _, record := range changes.Records {
			ids = append(ids, record.ID)
		}
		encoded, _ := json.Marshal(ids)
		if _, err := w.store.interruptObservations(ctx, " AND id NOT IN (SELECT value FROM json_each(?))", string(encoded)); err != nil {
			w.setError(fmt.Sprintf("reconcile observation window: %v", err))
			return
		}
	}
	w.mu.Lock()
	w.lastVersion = changes.Version
	w.status.LastVersion = changes.Version
	if len(changes.Records) > 0 {
		w.status.LastWriteAt = w.now()
	}
	w.status.LastWriteError = ""
	w.mu.Unlock()
}

func (w *observationWriter) setError(msg string) {
	w.mu.Lock()
	w.status.LastWriteError = msg
	w.mu.Unlock()
	log.Errorf("observation writer: %s", msg)
}

// ---- Schema ----

// ensureObservationSchema creates the observation metadata table and adds the
// trace_id column to the raw entries table for joining.
func (s *requestLogStore) ensureObservationSchema(ctx context.Context) error {
	// Add trace_id column to request_log_entries if missing.
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(request_log_entries)`)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, ok := existing["trace_id"]; !ok {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE request_log_entries ADD COLUMN trace_id TEXT`); err != nil {
			return fmt.Errorf("add trace_id column: %w", err)
		}
	}
	_, err = s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_request_log_entries_trace ON request_log_entries(trace_id);
CREATE INDEX IF NOT EXISTS idx_request_log_entries_route_page ON request_log_entries(timestamp_unix DESC, id DESC, trace_id, status, success, has_error);
CREATE INDEX IF NOT EXISTS idx_request_log_entries_route_id ON request_log_entries(id, timestamp_unix, trace_id, status, success, has_error);

CREATE TABLE IF NOT EXISTS request_log_observations (
  id TEXT PRIMARY KEY,
  trace_id TEXT NOT NULL DEFAULT '',
  timestamp TEXT NOT NULL DEFAULT '',
  timestamp_unix INTEGER NOT NULL DEFAULT 0,
  url TEXT,
  method TEXT,
  model TEXT,
  provider TEXT,
  auth_id TEXT,
  upstream_model TEXT,
  ip TEXT,
  session_id TEXT,
  parent_session_id TEXT,
  state TEXT NOT NULL DEFAULT 'active',
  phase TEXT,
  status INTEGER NOT NULL DEFAULT 0,
  error_kind TEXT,
  recovered INTEGER NOT NULL DEFAULT 0,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  attempts_truncated INTEGER NOT NULL DEFAULT 0,
  attempts_json TEXT,
  version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_request_log_observations_trace ON request_log_observations(trace_id);
CREATE INDEX IF NOT EXISTS idx_request_log_observations_state ON request_log_observations(state);
CREATE INDEX IF NOT EXISTS idx_request_log_observations_time ON request_log_observations(timestamp_unix DESC);
`)
	if err != nil {
		return err
	}
	var hasStatus int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('request_log_observations') WHERE name='status'`).Scan(&hasStatus); err != nil {
		return err
	}
	if hasStatus == 0 {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE request_log_observations ADD COLUMN status INTEGER NOT NULL DEFAULT 0`)
	}
	return err
}

// markActiveObservationsInterrupted converts persisted active rows to
// interrupted after a restart. Returns the number of rows updated.
func (s *requestLogStore) markActiveObservationsInterrupted(ctx context.Context) (int64, error) {
	return s.interruptObservations(ctx, "")
}

// Missing active rows have an unknown outcome after restart or observation loss.
// Preserve known failures in completed physical attempts.
func (s *requestLogStore) interruptObservations(ctx context.Context, extraWhere string, extraArgs ...any) (int64, error) {
	args := []any{requestlog.Interrupted, requestlog.Interrupted, time.Now().Unix(), requestlog.Interrupted, requestlog.Interrupted, requestlog.Active}
	args = append(args, extraArgs...)
	result, err := s.db.ExecContext(ctx, `UPDATE request_log_observations SET
state = ?, phase = '', error_kind = ?, updated_at = ?,
attempts_json = CASE WHEN json_array_length(attempts_json) > 0 AND json_extract(attempts_json, '$[#-1].state') = 'active' THEN
json_set(attempts_json, '$[#-1].state', ?, '$[#-1].error_kind', ?) ELSE attempts_json END
WHERE state = ?`+extraWhere, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// pruneObservationsBefore removes observation rows older than the cutoff.
func (s *requestLogStore) pruneObservationsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM request_log_observations WHERE timestamp_unix < ? AND state != 'active'`, cutoff.Unix())
	return err
}

// ---- Upsert ----

func (s *requestLogStore) upsertObservationBatch(ctx context.Context, records []requestlog.Record) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, rec := range records {
		if err := upsertObservationWithExecer(ctx, tx, rec, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertObservationWithExecer(ctx context.Context, execer requestLogExecer, rec requestlog.Record, now int64) error {
	attemptsJSON, _ := json.Marshal(rec.Attempts)
	_, err := execer.ExecContext(ctx, `
INSERT INTO request_log_observations (
  id, trace_id, timestamp, timestamp_unix, url, method, model, provider, auth_id,
  upstream_model, ip, session_id, parent_session_id, state, phase, status, error_kind,
  recovered, attempt_count, attempts_truncated, attempts_json, version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  trace_id=excluded.trace_id,
  timestamp=excluded.timestamp,
  timestamp_unix=excluded.timestamp_unix,
  url=excluded.url,
  method=excluded.method,
  model=excluded.model,
  provider=excluded.provider,
  auth_id=excluded.auth_id,
  upstream_model=excluded.upstream_model,
  ip=excluded.ip,
  session_id=excluded.session_id,
  parent_session_id=excluded.parent_session_id,
  state=excluded.state,
  phase=excluded.phase,
  status=excluded.status,
  error_kind=excluded.error_kind,
  recovered=excluded.recovered,
  attempt_count=excluded.attempt_count,
  attempts_truncated=excluded.attempts_truncated,
  attempts_json=excluded.attempts_json,
  version=excluded.version,
  updated_at=excluded.updated_at
WHERE excluded.version >= request_log_observations.version
`, rec.ID, rec.TraceID, rec.Timestamp.Format(time.RFC3339Nano), rec.Timestamp.Unix(),
		rec.URL, rec.Method, rec.Model, rec.Provider, rec.AuthID,
		rec.UpstreamModel, rec.IP, rec.SessionID, rec.ParentSessionID,
		rec.State, rec.Phase, rec.Status, rec.ErrorKind,
		boolInt(rec.Recovered), observationAttemptCount(rec), rec.AttemptsTruncated,
		string(attemptsJSON), rec.Version, now, now)
	return err
}

// requestLogRouteListItem keeps polling bounded to route metadata and previews.
type requestLogRouteListItem struct {
	requestLogListItem
	State             string `json:"state"`
	Phase             string `json:"phase,omitempty"`
	ErrorKind         string `json:"error_kind,omitempty"`
	Recovered         bool   `json:"recovered"`
	TraceID           string `json:"trace_id,omitempty"`
	RawLogID          string `json:"raw_log_id,omitempty"`
	AttemptCount      int    `json:"attempt_count,omitempty"`
	AttemptsTruncated bool   `json:"attempts_truncated,omitempty"`
}

type requestLogRouteMetadata struct {
	requestLogRouteListItem
	RawLogPending   bool                 `json:"raw_log_pending"`
	ParentSessionID string               `json:"parent_session_id,omitempty"`
	Attempts        []requestlog.Attempt `json:"attempts,omitempty"`
}

type requestLogRouteDetail struct {
	requestLogDetail
	RawLogPending     bool                 `json:"raw_log_pending"`
	State             string               `json:"state"`
	Phase             string               `json:"phase,omitempty"`
	ErrorKind         string               `json:"error_kind,omitempty"`
	Recovered         bool                 `json:"recovered"`
	TraceID           string               `json:"trace_id,omitempty"`
	RawLogID          string               `json:"raw_log_id,omitempty"`
	ParentSessionID   string               `json:"parent_session_id,omitempty"`
	AttemptCount      int                  `json:"attempt_count,omitempty"`
	AttemptsTruncated bool                 `json:"attempts_truncated,omitempty"`
	Attempts          []requestlog.Attempt `json:"attempts,omitempty"`
}

func observationAttemptCount(record requestlog.Record) int {
	count := len(record.Attempts)
	for _, attempt := range record.Attempts {
		count = max(count, attempt.Index)
	}
	return count
}

func validObservationState(state string) bool {
	switch state {
	case "", "all", requestlog.Active, requestlog.Succeeded, requestlog.Failed, requestlog.Canceled, requestlog.Interrupted, "recovered":
		return true
	default:
		return false
	}
}

const legacyObservationStateSQL = `CASE WHEN COALESCE(r.status,0) = 499 THEN 'canceled' WHEN COALESCE(r.success,0) != 0 AND COALESCE(r.has_error,0) = 0 THEN 'succeeded' ELSE 'failed' END`

type routeRowProjection uint8

const (
	routeIdentityProjection routeRowProjection = iota
	routeMetadataProjection
)

// routeRowsQuery uses the same filters for compact identity selection and final
// metadata hydration. Only the latter joins a representative transport file.
func routeRowsQuery(ctx context.Context, reader requestLogReader, query, state string, cutoff *int64, id string, projection routeRowProjection) (string, []any, error) {
	if !validObservationState(state) {
		return "", nil, fmt.Errorf("invalid request state")
	}
	var obsArgs, rawArgs []any
	obsWhere, rawWhere := "", ""
	if state != "" && state != "all" {
		if state == "recovered" {
			obsWhere += " AND o.state = 'succeeded' AND o.recovered = 1"
			rawWhere += " AND 0"
		} else {
			obsWhere += " AND o.state = ?"
			obsArgs = append(obsArgs, state)
			rawWhere += " AND (" + legacyObservationStateSQL + ") = ?"
			rawArgs = append(rawArgs, state)
		}
	}
	if cutoff != nil {
		obsWhere += " AND (o.timestamp_unix >= ? OR o.state = 'active')"
		obsArgs = append(obsArgs, *cutoff)
		rawWhere += " AND r.timestamp_unix >= ?"
		rawArgs = append(rawArgs, *cutoff)
	}
	if id != "" {
		obsWhere += " AND o.id = ?"
		obsArgs = append(obsArgs, id)
		rawWhere += " AND r.id = ?"
		rawArgs = append(rawArgs, id)
	} else {
		rawWhere += ` AND NOT EXISTS (SELECT 1 FROM request_log_observations o WHERE o.trace_id != '' AND o.trace_id = r.trace_id)`
	}
	var prefix string
	var prefixArgs []any
	if strings.TrimSpace(query) != "" {
		matches, args := requestLogIndexedMatchQuery(query)
		ready, err := requestLogSearchReady(ctx, reader)
		if err != nil {
			return "", nil, err
		}
		if !ready {
			where, unindexedArgs := requestLogWhereClause(query)
			matches = `SELECT id FROM (` + matches + `) UNION SELECT id FROM request_log_entries` + where + ` AND id NOT IN (SELECT id FROM request_log_search_entries)`
			args = append(args, unindexedArgs...)
		}
		prefix = `WITH raw_matches AS MATERIALIZED (` + matches + `) `
		prefixArgs = args
		obsWhere += ` AND (EXISTS (SELECT 1 FROM request_log_entries matched JOIN raw_matches m ON m.id = matched.id WHERE o.trace_id != '' AND matched.trace_id = o.trace_id) OR (` + routeMetadataMatch(query, &obsArgs) + `))`
		rawWhere += ` AND r.id IN (SELECT id FROM raw_matches)`
	}
	// Counts and sorting use only covering-index columns. Even previews in the
	// wide raw table may require walking megabytes of preceding body overflow.
	obsQuery := `SELECT o.id, o.timestamp_unix AS ts_unix FROM request_log_observations o WHERE 1=1` + obsWhere
	rawQuery := `SELECT r.id, r.timestamp_unix AS ts_unix FROM request_log_entries r WHERE 1=1` + rawWhere
	if projection == routeMetadataProjection {
		obsQuery = `SELECT
 o.id, o.trace_id, o.timestamp, o.timestamp_unix AS ts_unix, o.url, o.method, o.model,
 o.provider, o.auth_id, o.upstream_model, o.ip, o.session_id,
 o.state, o.phase, o.error_kind, o.recovered, o.attempt_count, o.attempts_truncated,
 COALESCE(r.id,'') AS raw_log_id, COALESCE(r.name,'') AS name, COALESCE(r.size,0) AS size,
 COALESCE(r.modified,o.timestamp_unix) AS modified,
 COALESCE(r.prompt_preview,'') AS prompt_preview, COALESCE(r.output_preview,'') AS output_preview,
 COALESCE(r.error_preview,'') AS error_preview, COALESCE(r.tool_preview,'') AS tool_preview,
 COALESCE(r.system_prompt_preview,'') AS system_prompt_preview, COALESCE(r.called_tools_preview,'') AS called_tools_preview,
 o.status, CASE WHEN o.state='succeeded' THEN 1 ELSE 0 END AS success,
 CASE WHEN o.state IN ('active','succeeded') THEN 0 ELSE 1 END AS has_error,
 COALESCE(r.auth_type,'') AS auth_type, COALESCE(r.upstream_url,'') AS upstream_url,
 COALESCE(r.channel_model,'') AS channel_model, COALESCE(r.ip_location,'') AS ip_location,
 COALESCE(r.thread_id,'') AS thread_id, COALESCE(r.turn_id,'') AS turn_id
 FROM request_log_observations o LEFT JOIN request_log_entries r ON r.id = (
 SELECT chosen.id FROM request_log_entries chosen WHERE o.trace_id != '' AND chosen.trace_id = o.trace_id
 ORDER BY chosen.timestamp_unix DESC, chosen.name DESC, chosen.id DESC LIMIT 1)
 WHERE 1=1` + obsWhere
		rawQuery = `SELECT
 r.id, COALESCE(r.trace_id,''), COALESCE(r.timestamp_text,''), r.timestamp_unix,
 COALESCE(r.url,''), COALESCE(r.method,''), COALESCE(r.model,''),
 COALESCE(NULLIF(TRIM(r.provider_name),''),r.provider,''), COALESCE(r.auth_id,''),
 COALESCE(r.upstream_model,''), COALESCE(r.ip,''), COALESCE(r.session_id,''),
 ` + legacyObservationStateSQL + `, '', '', 0, 0, 0,
 r.id, COALESCE(r.name,''), COALESCE(r.size,0), COALESCE(r.modified,0),
 COALESCE(r.prompt_preview,''), COALESCE(r.output_preview,''), COALESCE(r.error_preview,''),
 COALESCE(r.tool_preview,''), COALESCE(r.system_prompt_preview,''), COALESCE(r.called_tools_preview,''),
 COALESCE(r.status,0), COALESCE(r.success,0), COALESCE(r.has_error,0),
 COALESCE(r.auth_type,''), COALESCE(r.upstream_url,''), COALESCE(r.channel_model,''),
 COALESCE(r.ip_location,''), COALESCE(r.thread_id,''), COALESCE(r.turn_id,'')
 FROM request_log_entries r WHERE 1=1` + rawWhere
	}
	args := append(prefixArgs, obsArgs...)
	args = append(args, rawArgs...)
	return prefix + `SELECT * FROM (` + obsQuery + ` UNION ALL ` + rawQuery + `)`, args, nil
}

func routeMetadataMatch(query string, args *[]any) string {
	fields := []string{"o.id", "o.trace_id", "o.timestamp", "o.url", "o.method", "o.model", "o.provider", "o.auth_id", "o.upstream_model", "o.ip", "o.session_id", "o.state", "o.phase", "o.error_kind"}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, "lower(COALESCE("+field+",'')) LIKE ?")
		*args = append(*args, "%"+strings.TrimSpace(strings.ToLower(query))+"%")
	}
	return strings.Join(parts, " OR ")
}

func scanRouteItem(scanner interface{ Scan(...any) error }, extra ...any) (requestLogRouteListItem, error) {
	var item requestLogRouteListItem
	var timestamp int64
	dest := []any{&item.ID, &item.TraceID, &item.Timestamp, &timestamp, &item.URL, &item.Method, &item.Model,
		&item.Provider, &item.AuthID, &item.UpstreamModel, &item.IP, &item.SessionID,
		&item.State, &item.Phase, &item.ErrorKind, &item.Recovered, &item.AttemptCount, &item.AttemptsTruncated,
		&item.RawLogID, &item.Name, &item.Size, &item.Modified, &item.PromptPreview, &item.OutputPreview, &item.ErrorPreview,
		&item.ToolPreview, &item.SystemPromptPreview, &item.CalledToolsPreview, &item.Status, &item.Success, &item.HasError,
		&item.AuthType, &item.UpstreamURL, &item.ChannelModel, &item.IPLocation, &item.ThreadID, &item.TurnID}
	err := scanner.Scan(append(dest, extra...)...)
	if err != nil {
		return item, err
	}
	if item.State == requestlog.Canceled {
		item.ErrorKind = requestlog.Canceled
	} else if item.State == requestlog.Failed && item.ErrorKind == "" {
		item.ErrorKind = requestlog.Classify(item.Status, nil)
	}
	return item, nil
}

func listRouteRows(ctx context.Context, reader requestLogReader, opts requestLogQueryOptions, state string, cutoff *int64) ([]requestLogRouteListItem, int, error) {
	query, args, err := routeRowsQuery(ctx, reader, opts.Query, state, cutoff, "", routeIdentityProjection)
	if err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), opts.Limit, opts.Offset)
	rows, err := reader.QueryContext(ctx, `SELECT route_page.id, COUNT(*) OVER() FROM (`+query+`) route_page ORDER BY ts_unix DESC, id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	// Count matching identities once, then hydrate only the requested page in
	// the same read transaction. Full-text matching is not repeated per item.
	ids := make([]string, 0, min(opts.Limit, 200))
	total := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &total); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 && opts.Offset > 0 {
		if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+query+`)`, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	items := make([]requestLogRouteListItem, 0, len(ids))
	for _, id := range ids {
		item, err := readRouteListItem(ctx, reader, id, cutoff)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

func (s *requestLogStore) listRoutes(ctx context.Context, opts requestLogQueryOptions, state string, cutoff *int64) ([]requestLogRouteListItem, int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	return listRouteRows(ctx, tx, opts, state, cutoff)
}

func readRouteListItem(ctx context.Context, reader requestLogReader, id string, cutoff *int64) (requestLogRouteListItem, error) {
	query, args, err := routeRowsQuery(ctx, reader, "", "", cutoff, id, routeMetadataProjection)
	if err != nil {
		return requestLogRouteListItem{}, err
	}
	return scanRouteItem(reader.QueryRowContext(ctx, query+` LIMIT 1`, args...))
}

func routeMetadata(ctx context.Context, reader requestLogReader, id string, cutoff *int64) (requestLogRouteMetadata, error) {
	item, err := readRouteListItem(ctx, reader, id, cutoff)
	if err != nil {
		return requestLogRouteMetadata{}, err
	}
	result := requestLogRouteMetadata{requestLogRouteListItem: item}
	var attempts string
	err = reader.QueryRowContext(ctx, `SELECT attempts_json, parent_session_id FROM request_log_observations WHERE id=?`, id).Scan(&attempts, &result.ParentSessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if attempts != "" {
		if err := json.Unmarshal([]byte(attempts), &result.Attempts); err != nil {
			return result, fmt.Errorf("decode route attempts: %w", err)
		}
	}
	return result, nil
}

func (s *requestLogStore) metadataRoute(ctx context.Context, id string, cutoff *int64) (requestLogRouteMetadata, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return requestLogRouteMetadata{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return routeMetadata(ctx, tx, id, cutoff)
}

func observationDetail(meta requestLogRouteMetadata, raw requestLogDetail) requestLogRouteDetail {
	raw.requestLogListItem = meta.requestLogListItem
	return requestLogRouteDetail{requestLogDetail: raw, RawLogPending: meta.RawLogPending, State: meta.State, Phase: meta.Phase, ErrorKind: meta.ErrorKind, Recovered: meta.Recovered,
		TraceID: meta.TraceID, RawLogID: meta.RawLogID, ParentSessionID: meta.ParentSessionID, AttemptCount: meta.AttemptCount, AttemptsTruncated: meta.AttemptsTruncated, Attempts: meta.Attempts}
}

func routeDetail(ctx context.Context, reader requestLogReader, id string, cutoff *int64) (requestLogRouteDetail, error) {
	meta, err := routeMetadata(ctx, reader, id, cutoff)
	if err != nil {
		return requestLogRouteDetail{}, err
	}
	var raw requestLogDetail
	if meta.RawLogID != "" {
		raw, err = readRequestLogDetail(ctx, reader, meta.RawLogID, cutoff)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return requestLogRouteDetail{}, err
		}
	}
	return observationDetail(meta, raw), nil
}

func (s *requestLogStore) detailRoute(ctx context.Context, id string, cutoff *int64) (requestLogRouteDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return requestLogRouteDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return routeDetail(ctx, tx, id, cutoff)
}

func (s *requestLogStore) exportRoutes(ctx context.Context, w io.Writer, opts requestLogQueryOptions, state, format string, cutoff *int64) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	items, _, err := listRouteRows(ctx, tx, opts, state, cutoff)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	csvWriter := csv.NewWriter(w)
	headers := []string{"id", "timestamp", "url", "method", "model", "provider", "upstream_url", "upstream_model", "channel_model", "ip", "ip_location", "status", "success", "prompt", "output", "error", "system_prompt", "called_tools", "session_id", "thread_id", "turn_id", "state", "phase", "error_kind", "recovered", "trace_id", "raw_log_id", "auth_id", "attempt_count", "attempts_truncated", "attempts", "available_tools", "mcps", "skills"}
	if format != "jsonl" {
		if err := csvWriter.Write(headers); err != nil {
			return err
		}
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		detail, err := routeDetail(ctx, tx, item.ID, cutoff)
		if err != nil {
			return err
		}
		if format == "jsonl" {
			if err := encoder.Encode(detail); err != nil {
				return err
			}
			continue
		}
		encoded, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return err
		}
		values := make([]string, len(headers))
		for i, key := range headers {
			raw := fields[key]
			var text string
			if json.Unmarshal(raw, &text) == nil {
				values[i] = text
			} else {
				values[i] = string(raw)
			}
		}
		if err := csvWriter.Write(values); err != nil {
			return err
		}
	}
	if format != "jsonl" {
		csvWriter.Flush()
		return csvWriter.Error()
	}
	return nil
}

func metadataFromObservation(rec requestlog.Record) requestLogRouteMetadata {
	return requestLogRouteMetadata{requestLogRouteListItem: requestLogRouteListItem{requestLogListItem: requestLogListItem{
		ID: rec.ID, Timestamp: rec.Timestamp.Format(time.RFC3339Nano), URL: rec.URL, Method: rec.Method, Model: rec.Model, Provider: rec.Provider,
		AuthID: rec.AuthID, UpstreamModel: rec.UpstreamModel, IP: rec.IP, SessionID: rec.SessionID, Status: rec.Status, Success: rec.State == requestlog.Succeeded,
		HasError: rec.State != requestlog.Active && rec.State != requestlog.Succeeded},
		State: rec.State, Phase: rec.Phase, ErrorKind: rec.ErrorKind, Recovered: rec.Recovered, TraceID: rec.TraceID,
		AttemptCount: observationAttemptCount(rec), AttemptsTruncated: rec.AttemptsTruncated}, ParentSessionID: rec.ParentSessionID, Attempts: rec.Attempts}
}

func (h *Handler) pendingRawRequestLog(state, rawID string) bool {
	return rawID == "" && state != requestlog.Interrupted && h.cfg != nil && !h.cfg.CommercialMode && (h.cfg.RequestLog || state == requestlog.Failed)
}

func observationWithinRetention(rec requestlog.Record, cutoff *int64) bool {
	return cutoff == nil || rec.State == requestlog.Active || rec.Timestamp.Unix() >= *cutoff
}

// liveRouteFallback is intentionally metadata-only when the durable index fails.
func liveRouteFallback(source ObservationSource, opts requestLogQueryOptions, state string, cutoff *int64) ([]requestLogRouteListItem, int) {
	items := []requestLogRouteListItem{}
	if source == nil {
		return items, 0
	}
	matched := []requestlog.Record{}
	query := strings.ToLower(strings.TrimSpace(opts.Query))
	for _, rec := range source.Changes(0).Records {
		if !observationWithinRetention(rec, cutoff) {
			continue
		}
		if state == "recovered" {
			if rec.State != requestlog.Succeeded || !rec.Recovered {
				continue
			}
		} else if state != "" && state != "all" && state != rec.State {
			continue
		}
		if query != "" {
			text := strings.Join([]string{rec.ID, rec.TraceID, rec.Model, rec.Provider, rec.AuthID, rec.UpstreamModel, rec.URL, rec.SessionID, rec.ErrorKind, rec.State, strconv.Itoa(rec.Status)}, " ")
			if !strings.Contains(strings.ToLower(text), query) {
				continue
			}
		}
		matched = append(matched, rec)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Timestamp.Equal(matched[j].Timestamp) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].Timestamp.After(matched[j].Timestamp)
	})
	total := len(matched)
	start := min(opts.Offset, total)
	end := min(start+opts.Limit, total)
	for _, rec := range matched[start:end] {
		items = append(items, metadataFromObservation(rec).requestLogRouteListItem)
	}
	return items, total
}
