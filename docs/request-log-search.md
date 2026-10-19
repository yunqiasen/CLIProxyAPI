# Request log search

## Why the request list stalled

The original SQLite predicate applied `lower(...) LIKE '%query%'` to 23 fields,
including complete prompts, outputs, errors and system prompts. List pagination
ran that search once to count and again to select the page. In the investigated
14,777-row snapshot, repeated system prompts accounted for hundreds of megabytes.
Queries for a tool name or an absent value could take several seconds even with
no background indexer writing to the database.

Debouncing alone does not solve this server-side work. Truncating prompts,
searching previews only or switching to token-only full-text search would change
the existing substring, short-query and CJK search contract.

## Compact, shared-content index

`request_logs_search.go` owns the additive search schema and its lifecycle:

- `request_log_search_entries` keeps short searchable fields and sort keys apart
  from the wide detail rows.
- `request_log_search_texts` stores full search bodies once per SHA-256 digest of
  the original bytes. SQLite performs `lower()` during insertion to retain its
  exact case and invalid-UTF-8 behavior.
- `request_log_search_refs` maps shared bodies to request identifiers.
- `request_log_search_state` records whether every structured row is indexed.

The list searches compact fields and distinct bodies, counts matching identifiers
once, and loads details only for the requested page. CSV/JSONL exports use the same
matching predicate. If short fields match all entries, a zero LIMIT skips body
search altogether. A plain AND guard is insufficient: SQLite may evaluate the
body lookup first; an error-producing body view locks this optimization down in
a regression test.

Full detail rows, raw logs, previews, provider/model labels, retention settings and
API response shapes remain unchanged. Search still accepts the existing SQLite
LIKE wildcards `%` and `_`; this is not a new query syntax or a permission filter.

## Migration, concurrency and cancellation

- Background synchronization backfills existing structured records in bounded
  batches. It does not depend on raw files still existing.
- Until the index is complete, the original full-table search remains active, so
  partial migration does not hide history. A restart resumes completed batches.
- The first backfill finishes within one synchronization pass. New raw files
  arriving during that pass are indexed on the following pass; this briefly
  delays their visibility, not request execution or existing-history access.
  The measured 14,777-row migration took about eight seconds. Cancellation is
  checked between bounded batches, and queued synchronization is retained.
- Normal upserts update structured rows, compact fields and body references in one
  transaction. Failure rolls the whole batch back. Database triggers invalidate
  rows written by an older binary and remove references for deleted entries.
- After retention pruning and synchronization, unreferenced search bodies are
  removed. Raw-file rotation alone does not delete retained structured history.
- Writers reserve the SQLite writer lock at transaction start. List and export
  reads explicitly use read-only/deferred transactions: readiness, matching,
  counts and returned rows observe one snapshot without blocking the writer.
- A canceled list request ends immediately. It must not fall through into a raw
  file scan; file-mode filtering and page parsing also check caller cancellation.

This adds no configuration, paid probes, provider scheduling changes or model
requests. Existing databases remain readable by the previous binary because the
primary schema is unchanged. Rollback retains the additive tables; an older
binary uses its original search and its writes invalidate the new index for a
later upgrade. Do not remove log files or rebuild the database to apply this fix.

## Verification

```sh
GOMAXPROCS=2 go test -p 2 ./internal/api/handlers/management \
  -run 'TestRequestLog|TestParseRequestLog|TestSyncRequestLog' -count=1
GOMAXPROCS=2 go test -p 2 -race ./internal/api/handlers/management \
  -run 'TestRequestLog|TestParseRequestLog|TestSyncRequestLog' -count=1
go test ./...
go build -o test-output ./cmd/server && rm test-output
node test/provider_usage_match_test.mjs
```

New tests compare the old and indexed list/export results for every searchable
field, case variants, CJK text, wildcards, nulls, invalid UTF-8 and long body tails,
including page offsets beyond the last result. Other regressions cover shared
body storage, interrupted migration, missing raw files, legacy SQL updates,
retention and deletion, transactional rollback, cancellation, concurrent versions
and reading while a writer transaction is open.

The optional benchmark copies a **consistent, standalone SQLite backup**, never
the actively written database or its WAL files. It reads the backup but writes
only its private temporary copy and the requested report. The original predicate
and the new index run against the same rows with the production Go SQLite driver.

```sh
CPA_REQUEST_LOG_SNAPSHOT=/private/request-log-snapshot.db \
CPA_REQUEST_LOG_BENCHMARK_REPORT=/private/request-log-benchmark.json \
GOMAXPROCS=2 go test -p 2 ./internal/api/handlers/management \
  -run '^TestRequestLogSearchSnapshotTiming$' -count=1 -v
```

The management UI's `docs/request-log-smoothness.md` documents the independent
browser layout and interaction checks. Run those against both Vite and the built
single-file panel before delivery. Preserve the generated panel's backend
compatibility tokens rather than weakening the bundle regression test.

## Measured private snapshot (2026-10-19)

The production Go store was exercised on a temporary copy of the same 14,777
records, with page size 30 and no model calls. These are isolated query timings,
not a claim about a deployed server or a universal latency bound. Full rows and
totals matched in every old/new comparison. The table records the post-review
rerun; earlier measurements were 7.00 s and 8.04 s for the old tool-name and
absent-query scans, respectively, illustrating host-load variation.

| Search | Original query | Indexed query |
| --- | ---: | ---: |
| No query | 6.8 ms | 3.9 ms |
| Tool name: `exec_command` | 10253.7 ms | 269.3 ms |
| Absent query | 16100.6 ms | 153.9 ms |
| CJK substring: `工具` | 1700.9 ms | 166.3 ms |
| Universal wildcard: `%` | 19.8 ms | 54.1 ms |
| Single letter: `a` | 25.9 ms | 60.8 ms |

Backfill took 8.14 s. It produced 5,797 distinct
search bodies (62.37 MiB). Original detail records were retained.

Universal matches add identifier materialization/counting overhead compared with
the old short-circuit predicate (about 54-61 ms here); their body scan is skipped.
The target improvement is removal of repeated full-body scans taking seconds,
without narrowing the searchable content or changing matching semantics.

Validation used Go 1.26.0, the pinned Bun 1.3.14, and Playwright 1.53.0.
The Bun 1.3.14 build produced the identical browser-verified panel.

Validation completed before the review gate: all Go packages, focused request-log
race tests, server compilation, the generated management-panel regression check,
1,845 UI tests plus lint/build, six viewport cases against Vite and the built panel,
eight Vite interaction cases and seven built-panel interaction cases. The extra
Vite-only case verifies connection changes. The two existing UI lint warnings are
outside this repair; no new warning remains in the changed TypeScript modules.

## Fixed points

- Backend: `efba8cd31f9f56d38e826ef3c7a16608e25ff107`.
- Management UI: `f38492a357b5c675e09f94721121b29070dc3790`.
- This diagnosis is verified in isolated worktrees. Review approval precedes the
  scoped commits and delivery to the primary local hot-reload workspace.

## Review resolution

The approved repair was reviewed independently for documented standards and the
accepted behavior. The review found maintainability improvements rather than a
reproduced functional regression. Shared-body query arguments are now bound
explicitly after the short-field arguments, and both paths retain the same
`lower(CASE ...)` form for result labels. Search readiness/page errors identify
the failing read stage while preserving their underlying errors. A regression
first failed for all three unlabeled read stages and then passed with wrapping.

The original full-row predicate and the independent test field list deliberately
remain separate compatibility references. Sharing them with the new index could
make a dropped field disappear from both the implementation and its oracle. The
four body values form an unordered searchable set; their insertion order is not
part of the query contract, so no extra domain wrapper was introduced. The
one-time backfill visibility trade-off is documented above rather than extending
this repair into a background scheduler redesign.

After the review refinements, the full Go suite, focused race suite, server build,
management-panel regression and the private snapshot comparison passed again.
The final panel was rebuilt with UI commit
`6085e5fbf3656ca8ebd017109e5180a4768cc5e8` as its version, then passed all six
viewport and seven built-panel interaction checks before backend delivery.
