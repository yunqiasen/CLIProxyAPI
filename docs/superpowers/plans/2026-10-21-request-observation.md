# Request Observation Implementation Plan

> For agentic workers: execute the approved slices with TDD and one parent-owned final review. Implementation approval is the user's "好，上手吧"; do not reopen routine gates.

**Goal:** Extend Request logs with active calls, ordered route attempts and state/error filters, plus minimal native credential import compatibility.
**Architecture:** Passive context-bound observation, coalesced indexing into the existing log SQLite database, existing list/detail UI with opt-in route view. Preserve raw logs and all execution behavior.
**Tech Stack:** Go 1.26, Gin, SQLite; React 19, TypeScript, Bun 1.3.14.

- [x] Observer slice: `internal/requestlog/{types,store,context}.go`, public-seam red/green tests for transitions, same-key attempts, capacity, copies and concurrency. `go test -race ./internal/requestlog`.
- [x] Runtime slice: observation middleware, execution lifecycle context propagation, logging helper request/response hooks and full trace header for raw logs. Isolated handler/executor tests cover same-key compatibility retry, cancellation, streaming and WS turns with unchanged upstream call counts.
- [x] Index slice: management observation projection, same-database metadata table, async coalescing, routes list/detail/filter/export correlation and failure visibility. Test restart, retention, body search and V0/V8 compatibility with temporary databases.
- [x] UI slice: state filters/current credential/route details in the existing panel; stable polling and selected-view exports. Pure/static-render tests plus isolated browser route verification. All added strings in four locales.
- [x] Import slice: strict native CLI format normalization/validation through existing upload handlers, unchanged normalized-file semantics and validated batch errors. Focused upload regressions.
- [x] Run full `go test ./...`, focused race checks, server build and `bun run verify`; build single-file panel and run `node test/provider_usage_match_test.mjs` against it.
- [x] Review both fixed baselines (backend `1805e99db9467cd5de760ca5c8e91f638c0d5ba5`, UI `6085e5fbf3656ca8ebd017109e5180a4768cc5e8`) on independent Standards and Spec axes and fix findings.
## Local delivery procedure

Create scoped local commits and fast-forward them into the primary workspaces, publish the panel locally, then verify automatic clean-commit hot reload, authenticated management queries, matching panel hashes and unchanged container identity. Runtime evidence is saved outside Git so recording it does not trigger a second process replacement. No push or VPS deployment is included in this approval.
