package management

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
)

func observationHandlerFixture(t *testing.T) (*Handler, *requestLogStore) {
	t.Helper()
	store := openRouteTestStore(t)
	h := NewHandlerWithoutConfigFilePath(&config.Config{RequestLogRetentionDays: 7}, nil)
	h.SetLogDirectory(t.TempDir())
	h.requestLogIndex = &requestLogIndexManager{ctx: context.Background(), trigger: make(chan struct{}, 1), store: store, now: time.Now, retentionDays: func() int { return 7 }}
	return h, store
}

func observationHTTP(t *testing.T, h *Handler, target, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.GetRequestLogDetail(c)
	} else if strings.Contains(target, "/export?") {
		h.ExportRequestLogs(c)
	} else {
		h.GetRequestLogs(c)
	}
	return rec
}

func insertObservationRaw(t *testing.T, store *requestLogStore, id, trace, prompt string, ts time.Time, status int, success bool) {
	t.Helper()
	candidate := requestLogCandidate{name: id + ".log", path: "/fixture/" + id + ".log", logTime: ts, modTime: ts, size: 123}
	parsed := parsedRequestLog{requestLogListItem: requestLogListItem{ID: id, Name: candidate.name, Timestamp: ts.Format(time.RFC3339Nano), Model: "alias", Method: "POST", URL: "/v1/responses", Status: status, Success: success, HasError: !success}, traceID: trace, prompt: prompt, output: "fixture-output"}
	if err := store.upsertParsed(context.Background(), parsed, candidate); err != nil {
		t.Fatal(err)
	}
}

func decodeObservationList(t *testing.T, rec *httptest.ResponseRecorder) ([]requestLogRouteListItem, int) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Items []requestLogRouteListItem `json:"items"`
		Total int                       `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Items, response.Total
}

func TestObservationIntegrityFullTextSearchPaginatesOnce(t *testing.T) {
	h, store := observationHandlerFixture(t)
	stamp := time.Now().Add(-time.Minute)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("raw-%d", i)
		trace := fmt.Sprintf("trace-%d", i)
		ts := stamp.Add(time.Duration(i) * time.Second)
		insertObservationRaw(t, store, id, trace, strings.Repeat("long body ", 1024)+"深处目标", ts, 200, true)
		if i != 5 {
			record := makeRecord(fmt.Sprintf("call-%d", i), trace, requestlog.Succeeded, ts)
			record.Status = 200
			insertTestObservation(t, store, record)
		}
	}
	for page, want := range [][]string{{"raw-5", "call-4"}, {"call-3", "call-2"}, {"call-1"}} {
		rows, total := decodeObservationList(t, observationHTTP(t, h, fmt.Sprintf("/v8/management/request-logs?view=routes&q=深处目标&limit=2&offset=%d", page*2), ""))
		if total != 5 || len(rows) != len(want) {
			t.Fatalf("page %d total=%d rows=%+v", page, total, rows)
		}
		for i, id := range want {
			if rows[i].ID != id {
				t.Fatalf("page %d item %d=%q want %q", page, i, rows[i].ID, id)
			}
		}
	}
}

func TestObservationIntegrityStateFiltersIncludeLegacyOutcomes(t *testing.T) {
	h, store := observationHandlerFixture(t)
	stamp := time.Now()
	insertObservationRaw(t, store, "legacy-ok", "", "ok", stamp, 200, true)
	insertObservationRaw(t, store, "legacy-fail", "", "bad", stamp, 429, false)
	insertObservationRaw(t, store, "legacy-cancel", "", "canceled", stamp, 499, false)
	insertTestObservation(t, store, makeRecord("in-flight", "trace", requestlog.Active, stamp))
	for state, want := range map[string]int{"all": 4, "active": 1, "succeeded": 1, "failed": 1, "canceled": 1, "recovered": 0, "interrupted": 0} {
		rows, total := decodeObservationList(t, observationHTTP(t, h, "/v8/management/request-logs?view=routes&state="+state, ""))
		if len(rows) != want || total != want {
			t.Fatalf("state=%s rows=%+v total=%d want=%d", state, rows, total, want)
		}
	}
	if rec := observationHTTP(t, h, "/v8/management/request-logs?view=routes&state=typo", ""); rec.Code != 400 {
		t.Fatalf("invalid state status=%d", rec.Code)
	}
}

func TestObservationIntegrityFinalStateIsAuthoritativeAndTraceJoinIsUnique(t *testing.T) {
	h, store := observationHandlerFixture(t)
	stamp := time.Now()
	record := makeRecord("logical", "shared-trace", requestlog.Failed, stamp)
	record.Status, record.ErrorKind = 200, "upstream"
	record.Attempts = []requestlog.Attempt{{Index: 1, State: requestlog.Failed}, {Index: 140, State: requestlog.Failed}}
	record.AttemptsTruncated = true
	insertTestObservation(t, store, record)
	insertObservationRaw(t, store, "old-raw", "shared-trace", "old", stamp.Add(-time.Second), 200, true)
	insertObservationRaw(t, store, "new-raw", "shared-trace", "new", stamp, 200, true)
	rows, total := decodeObservationList(t, observationHTTP(t, h, "/v8/management/request-logs?view=routes", ""))
	if total != 1 || len(rows) != 1 {
		t.Fatalf("duplicate observation rows: total=%d rows=%+v", total, rows)
	}
	if rows[0].Success || !rows[0].HasError || rows[0].Status != 200 || rows[0].AttemptCount != 140 || rows[0].RawLogID != "new-raw" {
		t.Fatalf("logical result overwritten by raw transport: %+v", rows[0])
	}
	record = makeRecord("no-raw", "pending-trace", requestlog.Failed, stamp)
	record.Status = 402
	insertTestObservation(t, store, record)
	rec := observationHTTP(t, h, "/v8/management/request-logs/no-raw?view=routes", "no-raw")
	var detail requestLogRouteDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Status != 402 || detail.Success {
		t.Fatalf("status before raw indexing=%+v", detail)
	}
}

func TestObservationIntegrityMetadataAndExportsPreserveTheirSeparateContracts(t *testing.T) {
	h, store := observationHandlerFixture(t)
	stamp := time.Now()
	record := makeRecord("call", "trace", requestlog.Succeeded, stamp)
	record.Status = 200
	record.Attempts = []requestlog.Attempt{{Index: 1, AuthID: "key-ref", State: requestlog.Succeeded}}
	insertTestObservation(t, store, record)
	insertObservationRaw(t, store, "raw", "trace", "full prompt with comma, and\nnewline", stamp, 200, true)
	meta := observationHTTP(t, h, "/v8/management/request-logs/call?view=routes&metadata_only=true", "call")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"prompt", "output", "error", "system_prompt", "available_tools", "request_metadata"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("metadata contains body field %s", key)
		}
	}
	if !strings.Contains(string(fields["attempts"]), "key-ref") {
		t.Fatal("metadata missing attempts")
	}
	for _, format := range []string{"csv", "jsonl"} {
		rec := observationHTTP(t, h, "/v8/management/request-logs/export?view=routes&format="+format, "")
		if rec.Code != 200 {
			t.Fatalf("export %s=%d", format, rec.Code)
		}
		if format == "jsonl" {
			var detail requestLogRouteDetail
			if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Prompt != "full prompt with comma, and\nnewline" || detail.Output != "fixture-output" || len(detail.Attempts) != 1 {
				t.Fatalf("export lost full detail: %+v", detail)
			}
		} else {
			rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("csv rows=%d", len(rows))
			}
			values := map[string]string{}
			for i, k := range rows[0] {
				values[k] = rows[1][i]
			}
			if values["prompt"] != "full prompt with comma, and\nnewline" || values["output"] != "fixture-output" {
				t.Fatal("CSV lost full bodies")
			}
		}
	}
}

func TestObservationIntegrityWriterDoesNotRepeatedlyCountCumulativeDrops(t *testing.T) {
	store := openRouteTestStore(t)
	source := requestlog.NewStore(1)
	source.Begin(requestlog.Record{ID: "active"})
	source.Begin(requestlog.Record{ID: "overflow-1"})
	source.Begin(requestlog.Record{ID: "overflow-2"})
	writer := newObservationWriter(source, store, time.Hour, time.Now)
	for range 3 {
		writer.syncOnce()
	}
	if got := writer.Status().MissedObservations; got != 2 {
		t.Fatalf("cumulative drops repeatedly counted: %d", got)
	}
}

func TestObservationIntegrityDBFailureKeepsActiveMetadataVisible(t *testing.T) {
	h, store := observationHandlerFixture(t)
	source := requestlog.NewStore(1)
	source.Begin(requestlog.Record{ID: "still-active", Model: "fixture", Timestamp: time.Now()})
	h.SetRequestObservations(source)
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	rec := observationHTTP(t, h, "/v8/management/request-logs?view=routes&state=active", "")
	rows, total := decodeObservationList(t, rec)
	if total != 1 || len(rows) != 1 || rows[0].ID != "still-active" {
		t.Fatalf("DB failure hid active call: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "storage_error") {
		t.Fatal("DB failure warning hidden")
	}
}

func TestObservationIntegrityCursorGapSealsOnlyMissingActiveRows(t *testing.T) {
	store := openRouteTestStore(t)
	source := requestlog.NewStore(2)
	stable := source.Begin(requestlog.Record{ID: "stable", Timestamp: time.Now()})
	lost := source.Begin(requestlog.Record{ID: "lost", Timestamp: time.Now()})
	writer := newObservationWriter(source, store, time.Hour, time.Now)
	writer.syncOnce()
	lost.Finish(requestlog.Succeeded, 200, nil)
	source.Begin(requestlog.Record{ID: "new", Timestamp: time.Now()})
	writer.syncOnce()
	missing, err := store.detailRoute(context.Background(), "lost", nil)
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.detailRoute(context.Background(), "stable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if missing.State != requestlog.Interrupted || live.State != requestlog.Active {
		t.Fatalf("gap outcomes missing=%s stable=%s", missing.State, live.State)
	}
	if writer.Status().InterruptedAtRestart != 0 || !writer.Status().HistoryGap {
		t.Fatalf("gap mislabeled as restart: %+v", writer.Status())
	}
	stable.Finish(requestlog.Succeeded, 200, nil)
}

func TestObservationIntegritySearchDuringBackfillAndBeyondLastPage(t *testing.T) {
	h, store := observationHandlerFixture(t)
	ts := time.Now()
	insertObservationRaw(t, store, "indexed", "trace-1", "中文尾部", ts, 200, true)
	insertObservationRaw(t, store, "unindexed", "trace-2", "中文尾部", ts, 200, true)
	if _, err := store.db.Exec(`DELETE FROM request_log_search_entries WHERE id='unindexed'; UPDATE request_log_search_state SET ready=0`); err != nil {
		t.Fatal(err)
	}
	rows, total := decodeObservationList(t, observationHTTP(t, h, "/v8/management/request-logs?view=routes&q=中文尾部&limit=1&offset=1", ""))
	if total != 2 || len(rows) != 1 {
		t.Fatalf("backfill search total=%d rows=%+v", total, rows)
	}
	rows, total = decodeObservationList(t, observationHTTP(t, h, "/v8/management/request-logs?view=routes&q=中文尾部&limit=1&offset=99", ""))
	if total != 2 || len(rows) != 0 {
		t.Fatalf("empty-page total=%d rows=%+v", total, rows)
	}
}

func TestObservationIntegrityPendingRawHintRespectsLoggingPolicy(t *testing.T) {
	h, store := observationHandlerFixture(t)
	insertTestObservation(t, store, makeRecord("completed", "trace", requestlog.Succeeded, time.Now()))
	for _, enabled := range []bool{false, true} {
		h.cfg.RequestLog = enabled
		rec := observationHTTP(t, h, "/v8/management/request-logs/completed?view=routes&metadata_only=true", "completed")
		var meta struct {
			Pending bool `json:"raw_log_pending"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Pending != enabled {
			t.Fatalf("request-log=%v pending=%v", enabled, meta.Pending)
		}
	}
}

func TestObservationIntegrityInterruptedDetailDoesNotAwaitRawLog(t *testing.T) {
	h, store := observationHandlerFixture(t)
	h.cfg.RequestLog = true
	insertTestObservation(t, store, makeRecord("interrupted", "lost-transport", requestlog.Interrupted, time.Now()))
	for _, suffix := range []string{"", "&metadata_only=true"} {
		rec := observationHTTP(t, h, "/v8/management/request-logs/interrupted?view=routes"+suffix, "interrupted")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var detail requestLogRouteMetadata
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.State != requestlog.Interrupted || detail.RawLogPending {
			t.Fatalf("a lost transport cannot finish writing its raw log: %+v", detail)
		}
	}
}

func TestObservationIntegrityListReadsOnlyRequestedPagePreviews(t *testing.T) {
	h, store := observationHandlerFixture(t)
	stamp := time.Now().Add(-time.Minute)
	for i := 1; i <= 20; i++ {
		id, trace := fmt.Sprintf("raw-%d", i), fmt.Sprintf("trace-%d", i)
		ts := stamp.Add(time.Duration(i) * time.Second)
		insertObservationRaw(t, store, id, trace, "fixture prompt", ts, 200, true)
		if i == 19 {
			insertTestObservation(t, store, makeRecord("call-19", trace, requestlog.Succeeded, ts))
		}
	}

	// Reading an off-page preview fails deterministically, representing expensive
	// overflow pages in a wide production log row without timing assertions.
	rows, err := store.db.Query(`PRAGMA table_info(request_log_entries)`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if name == "prompt_preview" {
			columns = append(columns, `CASE WHEN id IN ('raw-20','raw-19') THEN prompt_preview ELSE json_extract('off-page preview read', '$') END AS prompt_preview`)
		} else {
			columns = append(columns, fmt.Sprintf("%q", name))
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE request_log_entries RENAME TO saved_route_entries;
CREATE VIEW request_log_entries AS SELECT ` + strings.Join(columns, ", ") + ` FROM saved_route_entries;`); err != nil {
		t.Fatal(err)
	}

	rec := observationHTTP(t, h, "/v8/management/request-logs?view=routes&limit=2", "")
	items, total := decodeObservationList(t, rec)
	if total != 20 || len(items) != 2 || items[0].ID != "raw-20" || items[1].ID != "call-19" {
		t.Fatalf("page read unrelated wide rows: total=%d items=%+v response=%s", total, items, rec.Body.String())
	}
}

func TestObservationIntegrityRouteCandidatesUseCoveringIndex(t *testing.T) {
	store := openRouteTestStore(t)
	stamp := time.Now()
	insertObservationRaw(t, store, "indexed-raw", "indexed-trace", "fixture", stamp, 200, true)
	cutoff := stamp.Add(-time.Hour).Unix()
	for _, test := range []struct{ query, state string }{{"", ""}, {"", "failed"}, {"alias", ""}, {"alias", "failed"}} {
		t.Run(test.query+"/"+test.state, func(t *testing.T) {
			query, args, err := routeRowsQuery(context.Background(), store.db, test.query, test.state, &cutoff, "", routeIdentityProjection)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			covered := false
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
				covered = covered || strings.Contains(detail, " r USING COVERING INDEX ")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !covered {
				t.Fatalf("candidate selection reads wide raw rows instead of covering metadata: %s", strings.Join(plan, "\n"))
			}
		})
	}
}
