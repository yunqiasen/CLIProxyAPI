# Responses Local Compatibility Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans task-by-task. Follow the current delivery scope below; preserve the earlier no-commit phase as historical verification evidence.

**Goal:** Make CPA preserve useful responses, expose genuine stream failures, and classify request logs correctly across supported upstream formats.

**Architecture:** Selectively port the HTTP Responses SSE repair from upstream `522b4de5`, preserving fork routing, provider identity, plugins, output-item reconstruction and WebSocket semantics. Extend the management log parser with protocol-aware error/tool extraction and complete SSE-frame parsing. Refresh cached log summaries through a parser-revision bump rather than rewriting raw logs.

**Tech Stack:** Go 1.26, Gin, existing AuthManager/executor interfaces, SSE, SQLite, Go tests.

---

## Current delivery scope (2026-09-06)

The user explicitly requested a local commit and an update of the local CPA fork
container after the initial verification phase. That instruction supersedes the
initial no-commit/no-restart restriction for local delivery. Complete the frontend
model plus Claude review, commit only the task files, rebuild from that commit,
update the existing local service, and verify its running revision. Git push,
releases, remote image publication, and remote/VPS deployment remain excluded.
The completion rule is now recorded in `AGENTS.md` and both README files.

## Initial-phase constraints and evidence

- Baseline: CPA-fork `5a108ca2`; comparison main `5208aec7`.
- Worktree: `/home/div/.config/superpowers/worktrees/CLIProxyAPI/responses-local-compat`.
- No push, release, deployment, service restart, credential/configuration changes or blanket history removal.
- Existing diagnostic report: `/tmp/cpa-empty-text-audit.c2shPP/diagnosis.md`.
- Existing baseline tests pass for all handler packages, management, executor/helpers and auth.
- Historical reasoning rejections remain a separate evidence-driven compatibility task; preserve valid messages and tool-call/result relationships.

## Task 1: Pin the real HTTP and log failure seams

**Files:**
- Create `sdk/api/handlers/openai/openai_responses_compatibility_test.go`.
- Create `internal/api/handlers/management/request_logs_responses_test.go`.

- [x] Add a scripted executor registered with a real AuthManager and call the real `/v1/responses` route. Use these wire payloads:

```json
{"type":"response.created","response":{"id":"resp_fixture"}}
{"type":"response.output_text.delta","delta":"partial"}
{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}}
{"type":"response.completed","response":{"status":"completed","output":[]}}
```

Assert that post-start quota/transport failures retain a terminal error, pre-payload errors retain a non-2xx status, multiline/split data remain valid, terminal errors occur once, and partial output does not trigger another executor call.

- [x] Add full log-parser tests for nested failure, partial output followed by failure, multiline data, final-attempt upstream errors missing downstream, earlier failed attempts followed by success, and nameless built-in tool calls.
- [x] Add a SQLite test starting with parser revision 2 and a cached successful row for a failed Responses log. A subsequent sync must reparse it, preserve the raw file and change provider failure totals.
- [x] Run red tests:

```sh
GOMAXPROCS=2 go test -p 2 -count=1 -run 'TestResponsesCompatibility|TestRequestLogResponses' ./sdk/api/handlers/openai ./internal/api/handlers/management
```

Expected: assertion failures on the diagnosed behaviors, not compilation errors.

## Task 2: Port only the HTTP SSE compatibility change

**Files:**
- `sdk/api/handlers/handlers_stream.go`
- `sdk/api/handlers/stream_forwarder.go`
- `sdk/api/handlers/openai/openai_responses_handlers.go`
- Existing matching stream/forwarder tests

- [x] Apply the `522b4de5` patch only for stream validation/forwarding and their tests. Review conflict hunks against CPA-fork, retain `markGinRequestModel`, fork model preparation and completion-output reconstruction, and exclude unrelated image/executor/orphan-delegation changes.

```sh
git show 522b4de5 -- sdk/api/handlers/handlers_stream.go sdk/api/handlers/stream_forwarder.go | git apply
```

- [x] Merge the Responses handler's framer and streaming functions from the same commit, keeping fork-only code outside those functions. The resulting path must buffer incomplete frames, sanitize error details, propagate pending errors when channels close, avoid duplicate terminal events, and detect clean EOF without a terminal response.
- [x] Update older framing-only tests to complete their streams; replace HTTP-only suppression expectations with explicit errors. Keep the separate WebSocket suppression tests unchanged.
- [x] Run the handler tests and the upstream-compatible stream regression tests.

## Task 3: Repair log outcomes, SSE extraction and tool-only summaries

**Files:**
- `internal/api/handlers/management/request_logs.go`
- Create `internal/api/handlers/management/request_logs_responses.go`
- `internal/api/handlers/management/request_logs_store.go`
- `internal/api/handlers/management/request_logs_responses_test.go`

- [x] Parse complete SSE data frames, including multiline data and legacy one-JSON-event-per-line logs; reuse the iterator for response text, errors and called tools.
- [x] Read top-level `error` and Responses `response.error`; a `response.failed` event is a failure even if its message is missing. Preserve partial answer text separately from error text.
- [x] Prefer the final downstream outcome over prior attempt errors. Recover an upstream-only error only from a clearly matched final attempt and only when the downstream has no successful completion. Keep final-provider/AuthID selection atomic.
- [x] Recognize nameless `tool_search_call`, web/file search and other built-in call types by explicit event type; use the type as a display label without reading tool arguments as assistant text. Preserve custom/named tool behavior.
- [x] Set `requestLogParserRevision` to 3 and verify revision-2 rows refresh without raw-log mutation.
- [x] Run management tests, including successful multi-key retry/provider attribution and store refresh.

## Task 4: Verify and deliver local changes

- [x] Run the original diagnostic overlay against the repaired worktree, using redacted local-log replay separately from repository tests.
- [x] Run focused packages with the race detector and then all packages:

```sh
GOMAXPROCS=2 go test -race -p 2 ./sdk/api/handlers/... ./internal/api/handlers/management
GOMAXPROCS=2 go test -p 2 ./...
GOMAXPROCS=2 go build -p 2 -o /tmp/cpa-responses-compat-server ./cmd/server
git diff --check
```

- [x] Check that no provider/key schema, AuthID, registry, config, raw log, UI bundle or running service changed.
- [x] If the main CPA-fork workspace is still at the verified baseline with no tracked edits, transfer only the tested patch and newly added source/tests/plan into it. Re-run focused tests and diff checks there.
- [x] Record reproduced failures, red/green verification, fixed-point SHA and remaining upstream/history limitations. Hold at the diagnosis review gate before any commit; leave push/release/deployment untouched.

## Initial-phase verification record (2026-09-06)

Fixed point: `5a108ca2cf2afb87c94b07710589c1296302f114`.
At this historical checkpoint, implementation was local and uncommitted.
The subsequent local commit/container delivery is covered by the current scope
and the delivery checks below.

### Additional reproduced boundaries

- Arbitrary chunk splitting originally produced 35 failures around partial SSE
  field names and SSE-looking text inside JSON strings. The shared line-break
  helper and validator buffering repaired those boundaries.
- Data-before-event splitting then reproduced 46 failures: complete data JSON
  released a partial trailing event field. The validator now waits for the
  trailing field's newline or EOF; all split positions and byte-wise delivery pass.
- Full request-log parsing reproduced three field-order failures: a trailing
  failure event was lost, a successful reply retained an earlier quota error,
  and a later event field was ignored. Delimited frames now retain their trailing
  event while legacy unseparated event/data logs keep their previous behavior.
- The real multi-key workflow on the unchanged fixed point reproduced a missing
  terminal failure after partial output. HTTP and pre-output SSE 429 retries
  already passed there; the repaired workflow passes all three scenarios.
- The existing WebRTC bridge test failed in two of three unchanged-baseline runs.
  A test-only loopback-peer setup passed 20 consecutive runs and final full/race
  suites; production media behavior and timeouts were untouched.

### Final implementation checks

| Check | Result | Local evidence |
| --- | --- | --- |
| Full Go suite, fresh run | 90 tested packages pass; 7773 tests/subtests pass; 7 test skips; 32 packages without tests | `/tmp/cpa-responses-compat-final-tests.jsonl` |
| Race suite | All 8 selected packages pass | `/tmp/cpa-responses-compat-final-race.log` |
| Server build | Pass; temporary output removed | `go build -p 2 -o test-output ./cmd/server` |
| Management UI static regression | Pass; UI bundle unchanged | `node test/provider_usage_match_test.mjs` |
| Formatting and diff checks | All 21 changed Go files formatted; diff check passes | `gofmt -l` and `git diff --check` |
| Original diagnostic replay | Pass; retained rate-limit log replay passes | `/tmp/cpa-responses-compat-recorded-replay.log` |
| Data-before-event HTTP red/green | 46 reproduced split failures, then pass | `/tmp/cpa-data-before-event-red.log`, `/tmp/cpa-data-before-event-green.log` |
| Data-before-event log red/green | 3 reproduced outcome failures, then pass | `/tmp/cpa-log-data-before-event-red.log`, `/tmp/cpa-log-data-before-event-green.log` |
| Multi-key fixed-point reproduction | Post-output failure missing on baseline | `/tmp/cpa-multikey-baseline-red.log` |

The original logs for `invalid_responses_request`, `array_above_max_length`, and
`invalid_encrypted_content` have rotated. Their live-log replay cases explicitly
skip; the rate-limit sample is the only retained original-log replay in this run.

### Final self-review

- **Standards:** reviewed the full unstaged diff and every new Go file against
  `AGENTS.md`. No outstanding documented-standard issue identified. No new
  runtime timeouts, dependencies, configuration keys, or translator-only changes.
- **Spec:** checked stream failure delivery, no post-output replay, final-attempt
  provider/AuthID attribution, raw-log preservation, parser refresh, tool-only
  summaries, plugin behavior, WebSocket error handling, and output-item repair.
  The field-order findings above were reproduced and fixed before the final suite.
- This is a local self-review, not an independent reviewer approval. Older `.ccg`
  approval artifacts are outside this change and are not review evidence here.
- The implementation, tests, and compatibility documentation have been transferred
  to the primary workspace and rechecked. This was the initial no-commit gate;
  the current delivery scope above now includes local commit and container update.
  Push, release, and remote deployment remain excluded.

### Initial patch-transfer record

- Applied the verified patch to `/home/div/1_Project_dir/AI/CLIProxyAPI` only after
  confirming the same fixed-point HEAD and an empty tracked/index diff.
- All 25 delivered files match the isolated worktree byte for byte: 21 Go files,
  both existing SDK advanced guides, this plan, and the new compatibility guide.
- Main-workspace focused tests passed in all 8 selected packages, followed by
  server build, management UI static regression, and `git diff --check`.
- The first primary-workspace run hit only two unchanged source-tree traversal
  tests: `TestNoInPlaceSJSONWrites` and `TestInPlaceByteWritesAreReviewed`, while
  walking the existing root-owned `logs/request-log-parts-api-request-2265857001`
  directory. Its permissions and contents were left untouched. Both tests passed
  in the isolated full/race suites; only the primary-workspace repeat used
  `-skip '^(TestNoInPlaceSJSONWrites|TestInPlaceByteWritesAreReviewed)$'`.
- Initial/repeated primary-workspace test evidence:
  `/tmp/cpa-responses-compat-main-focused-tests.log` and
  `/tmp/cpa-responses-compat-main-verified-tests.log`.
- Complete local patch: `/tmp/cpa-responses-local-compat-20260906.patch`.
  File/hash manifest: `/tmp/cpa-responses-local-compat-20260906-manifest.json`.
  The two new Markdown files match the existing `docs/*` ignore rule, so they are
  explicitly included in the patch/manifest and require explicit staging later.
- At the initial patch-transfer checkpoint, HEAD and index were unchanged.
  Existing untracked investigation files, configuration, credentials, runtime
  logs, running services, and the separate UI repository were preserved.
  The feature worktree is retained as the isolated test workspace.


## Local commit and container delivery checks (2026-09-06)

The delivery includes 29 task files: the verified 21 Go files, compatibility and
SDK documentation, this plan, and the AGENTS/README/local-delivery workflow docs.
The primary workspace and isolated worktree match byte for byte. Only this file
list is staged; existing runtime configuration and investigation files stay out
of the commit and Docker build context.

### Repeated final checks

- Fresh full suite: 90 tested packages pass; 7773 tests/subtests pass; 7 test
  skips; 32 packages without tests. Evidence:
  `/tmp/cpa-local-delivery-20260906/final-full-tests.jsonl`.
- Fresh race run: all eight selected packages pass. Evidence:
  `/tmp/cpa-local-delivery-20260906/final-race.log`.
- Server build, management UI regression, formatting, and diff checks pass.
  The temporary build output was removed; the deployed UI source is unchanged.

### Review outcomes

- The configured frontend model (Antigravity) was invoked three times. Each
  returned `authentication timed out`; none produced a review verdict.
- Claude was invoked through the configured wrapper and directly with its
  existing user settings. The wrapper exited with status 1; the direct call
  returned HTTP 403 for the configured upstream account. No Claude approval
  was produced, and no authentication configuration was changed.
- Supplemental Gemini review used the existing local CPA alias `gem3.5f`.
  The initial full-diff response ended with `finish_reason: length` and is not
  an approval. A focused review of every changed production Go file plus the
  compatibility contract completed with `finish_reason: stop` and **APPROVE**
  for both specification and standards, with no actionable findings.
- Self-review covered the entire 29-file task diff, including all tests and
  delivery documentation. Gemini approval and local tests do not constitute
  dual-model approval; the missing Claude verdict is recorded explicitly.
- Review artifacts are retained under `/tmp/cpa-local-delivery-20260906/`,
  including `gemini-cpa-supplemental.review`, `claude-direct.review`, and the
  Antigravity progress/exit records. Older `.ccg` approvals are not used.

### Runtime handoff

The local Docker endpoint, Compose project/service, image name, bind mounts,
environment, network, and restart policy were checked against the running
container and `docker-compose.local.yml`. The pre-update snapshot is
`/tmp/cpa-local-delivery-20260906/runtime-predeploy.json`; it records hashes and
counts, not credentials. Delivery builds from the exact local commit using
[Local Fork Delivery](../../local-fork-delivery.md), retains the previous image,
recreates only `cli-proxy-api`, and verifies its commit header, API, streamed
response, indexed log outcome, management UI, and plugin loading. Deployment
results are retained alongside the snapshot; Git push and remote delivery stay
outside this task.
