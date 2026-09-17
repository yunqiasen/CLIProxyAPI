# Agent tool terminal and readable reasoning repair

**Approval:** The user explicitly requested both repairs, shared request/probe
compatibility, realistic success replay, and repeated verification on 2026-09-17.
Existing direct-implementation and automatic local-delivery instructions apply.
Baseline: `4910834f5854a1dc0055a1044621bca931100686`.

**Goal:** Complete Agent's otherwise complete forced-function stream without
accepting a partial call, and preserve readable history when dropping unusable
long-ID encryption bindings.

**Architecture:** Keep the ID fix in the existing request sanitizer. Add a narrow
HTTP SSE adapter in executor/helps and use it from both Codex execute paths, so
management probes and public clients share behavior. Do not touch unrelated
providers, configuration, credentials, client sessions, or the management UI.

**Protocol boundary:** Only the Agent `gpt-6-astra` Responses forced-function path
is eligible. A clean EOF, matching nonempty item/call/name identities, completed
arguments matching their deltas, a completed output item, and all observed items
closed are required. Return the actual collected call in one compatibility
completion. Native outcome/output/error details stay authoritative; an already established local
correlation identity stays stable if a native terminal arrives late. Errors, incomplete/canceled
status, malformed frames, missing done events, transport failure, caller
cancellation, other item types, or compaction never become success. Keep usage
unknown when the upstream supplies none. No retry or extra model call is added.

Official schema checked on 2026-09-17:
https://developers.openai.com/api/reference/resources/responses/streaming-events
Item completion alone is not standard response completion; this is an explicit,
provider-specific compatibility policy for the verified Agent route.

## Implementation slices

- [x] Snapshot running container/source and create isolated worktree.
- [x] Run existing affected packages as a green baseline.
- [x] Add and run failing readable-history test; existing removal test is updated
      to cover opaque-only history after this approved behavior change.
- [x] Preserve readable fields with the same ID/encryption stripping used by
      signature recovery; test 64/65 boundary and immutable/idempotent input.
- [x] Add and run failing executor tests for Responses and Chat, stream and JSON.
- [x] Add the bounded SSE adapter; integrate through both HTTP read loops including
      signature-retry reader reconstruction.
- [x] Add negative and terminal-identity tests, production/probe parity, and a
      public API black-box check including a tool result follow-up.
- [x] Run full Go tests/build, race checks, panel regression and two-axis review.
- [x] Test only the first selected Agent key against the affected real route;
      preserve a failed availability result without rotating paid credentials.
- [x] Update contract/verification docs and resolve the two-axis review findings.

## Post-commit delivery gate

Commit scoped files, apply to CPA-fork, verify automatic reload with clean
X-CPA-COMMIT and unchanged container ID, and push origin/CPA-fork as explicitly
requested in this task. The post-commit outcome is retained as `runtime-after.json`
in the evidence directory below; clean revision metadata is checked after the
commit rather than asserted in this pre-commit plan.

Commands: `go test ./internal/runtime/executor/helps ./internal/runtime/executor
./internal/api/handlers/management -count=1`, then `go test ./...`, `go build -o
test-output ./cmd/server`, affected `go test -race`, and `node
test/provider_usage_match_test.mjs`. Evidence stays in the primary checkout's
`.scratch/agent-terminal-history-20260917`, outside staged source.

## Verified before delivery

Both requested behavior repairs and the shared probe path pass their permanent
regressions. The final full suite passed (4,386 top-level tests, 3,911 subtests),
race tests passed five repetitions, and all 25 black-box contracts passed.
Standards/Spec review findings were evaluated and resolved or explicitly rejected
with evidence in the local review-resolution record. The same first Agent key
returned 402 through the candidate as in the direct control: real-site terminal
success is not certified, and the user-disabled Agent Astra model remains disabled.
