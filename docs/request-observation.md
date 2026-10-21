# Request log routing observation

Approved by the user on 2026-10-21 ("好，上手吧") after agreeing to extend the existing Log viewer -> Request logs rather than add a dashboard.

## Scope

- One logical model call appears once, from active state through success, failure or cancellation. A WebSocket connection can contain multiple calls; an idle connection is not an active model call.
- Show current route/credential reference, an ordered attempt history including executor-internal same-key recovery, original log detail and coarse error categories. Recovered failures are successful logical requests, not final failures.
- Add state filtering and retain stable history/search/detail behavior. Use small metadata and reuse the current SQLite log database and complete-body search; retain raw prompt/response/tool logs under existing controls.
- Recognize and validate native Claude/Codex credential exports in the existing credential import path. Preserve normalized CPA uploads and current quota/credential behavior.
- Exclude new token statistics, timing metrics, latency charts, trends, Pacer, scheduling changes, extra model calls, and new plugin pages.

## Architecture and contracts

`internal/requestlog` owns bounded, concurrent request records and context-bound handles. Middleware supplies transport metadata. The existing execution lifecycle owns each logical call; logging-helper seams record physical attempts without changing request payloads, retries or streaming. Each call gets an execution ID distinct from the inbound trace ID. Nested internal executions share the outer call.

Management's existing log-index lifecycle asynchronously coalesces observation snapshots into a metadata table in `request_logs.db`. The hot request path performs no disk/network work. Index failures/observation loss are reported to the page, and remain separate from inference outcomes. Stored active rows become interrupted after restart, not successful or indefinitely active; details stop waiting for a raw log from the lost transport. Existing retention applies to the new metadata table.

The existing list/detail endpoints accept `view=routes`. This view combines observations with legacy raw-log rows, correlating only by the full server-generated `X-CPA-Log-Trace-ID` recorded in the cloned log headers. It preserves legacy endpoint behavior without the opt-in query. List state filters are `active`, `succeeded`, `failed`, `canceled`, `interrupted`, `recovered` (or absent for all). Detail adds `state`, `phase`, `recovered`, `error_kind`, `attempts`, `attempts_truncated`, `trace_id`, and `raw_log_id`. An observation's ID stays stable when its raw file is indexed. Multiple calls on one WebSocket transport retain independent observation IDs and share a clearly identified raw transport log.

The UI polls bounded metadata pages and reconciles rows by ID; it pauses background refresh while reading history/details, searching, scrolling or hidden. No required external assets are added to the single-file panel.

## Acceptance

Offline tests exercise active-to-terminal identity, retry recovery, same-key replay, independent WebSocket turns, nested execution, stream failures and cancellation; concurrent updates, capacity limits, redaction, database failure/restart/retention, full-text/CJK search and legacy V0/V8 contracts; stale UI requests/connection changes; native import validation, batch behavior and collision preservation. Run full Go and UI checks, independent Standards/Spec reviews, scoped local commits, then verify clean-commit automatic hot reload and unchanged container identity. Use isolated upstream fixtures, never paid credential sweeps or quota redemption.

## Implementation: Persistence/Management Slice

### Schema additions (request_logs.db)

- `request_log_entries.trace_id` — nullable TEXT, indexed (`idx_request_log_entries_trace`). Populated by the raw log parser from the `X-CPA-Log-Trace-ID` header in the HEADERS section. Old rows without trace_id remain queryable.
- `request_log_observations` table — metadata-only observation records. No prompt/output bodies (those stay in `request_log_entries`). Columns: id, trace_id, timestamp, timestamp_unix, url, method, model, provider, auth_id, upstream_model, ip, session_id, parent_session_id, state, phase, error_kind, recovered, attempt_count, attempts_truncated, attempts_json, version, created_at, updated_at. Indexed on trace_id, state, timestamp_unix.

### Coalescing writer

- `observationWriter` in `request_log_observations.go` runs a background goroutine owned by `requestLogIndexManager`. Started in `Start()`, closed in `Close()`.
- Polls `ObservationSource.Changes(after uint64)` every sync interval. Upserts records into `request_log_observations`.
- Keeps last successful cursor on write failure; retries on next cycle. Reports missed (evicted) observations via `observationWriterStatus.warning()`.
- On restart (new process), `markActiveObservationsInterrupted` converts persisted active rows to `interrupted`.
- `TriggerSync` on the index manager also schedules an asynchronous observation write; list/detail reads never flush SQLite inline.

### Routes view (view=routes)

- List: `GET /request-logs?view=routes[&state=active|succeeded|failed|canceled|interrupted|recovered]`
- Detail: `GET /request-logs/:id?view=routes`
- Export: `GET /request-logs/export?view=routes[&state=...]`
- List returns existing shape + state, phase, error_kind, recovered, trace_id, raw_log_id, attempt_count, attempts_truncated. Attempts excluded from list.
- Detail adds attempts array and retains full prompt/output/tools/error metadata.
- UNION query: observations LEFT JOIN raw entries by exact trace_id + raw-only legacy rows. No duplicates.
- Uses existing compact/shared-body indexed search for body matching. No full-body rescan.
- `observation_warning` string in list response when capture/write gaps exist.
- Raw detail absent before final index returns observation with empty content, not 404.
- `metadata_only=true` on a route detail returns metadata and attempts without loading bodies. `raw_log_pending` distinguishes delayed full-content indexing from logging being disabled.
- Legacy endpoints (no view=routes) preserve existing contract.

### Handler integration

- `Handler.SetRequestObservations(ObservationSource)` — parent calls before `SetLogDirectory`/`StartRequestLogIndex`.
- Store passed to index manager through `requestLogIndexManagerOptions.ObservationStore`.

## Verification notes (2026-10-21)

- Full Go tests, focused observer/executor/management race checks, server compilation, and the panel bundle regression pass. The isolated compiler uses `-buildvcs=false` because an unrelated empty `/tmp/.git` interferes with Go's worktree VCS discovery; normal delivery retains the supervisor's commit stamping.
- UI `bun run verify`: 1,906 passing tests, no lint errors (two pre-existing warnings), single-file build.
- Isolated production server and local upstream: 25 API checks for active/final identity, same-key compatibility recovery, credential failover, cancellation, full detail/search/export, V0/V8 compatibility and native credential preservation. Four abrupt-process-restart checks cover interrupted rows and ended raw-log waiting.
- Real Chromium: 18 checks covering the existing logs route, compact status controls, slow single-flight metadata polling, hidden-page pause, one-time body loading, authenticated raw download, search stability, close/rerender races, recovery attempts and a 390 px viewport. No uncaught browser errors.
- All fixture calls used local artificial credentials. No live provider probes, quota operations, production configuration edits, push or VPS deployment are included.

## Final independent review

### Standards

The independent Standards reviewer reported no documented-rule violations and two non-blocking judgement calls: a forwarding-only `detailRouteFallback` method and duplicate native-import control flow. The forwarding method was removed and both formats now share validation/serialization. Focused route tests and native-import race tests pass after those behavior-preserving changes.

### Spec

The independent Spec reviewer reported zero missing, incorrect, or unrequested requirements in the reviewed backend/UI diff. Both reviewer processes completed successfully. The parent checked and resolved the two Standards findings; no second delegated review or inference-routing change was introduced.

Remaining actionable findings: Standards 0; Spec 0. Local commits and clean-commit runtime verification follow the delivery procedure in the implementation plan; no remote delivery is implied.
