# Native Compatibility Verification — 2026-09-15

Approved scope: tickets 02–05 following image/probe policy delivery `6b79c33d`.
No summary engine, automatic continuation, new image switch, plugin installation,
credential enumeration, UI redesign or VPS deployment is included.

## Regression evidence

- A WebSocket compacted window reaches both before-auth and selected-auth decoders
  before translation; stale history is not restored. Unknown opaque state on an
  unsupported route returns an explicit error instead of disappearing.
- Same-route reasoning survives; known foreign key/model/endpoint reasoning loses
  only route-bound fields. Failed/canceled attempts, tenant isolation and parallel
  completions have regression coverage. Compaction and stored references remain.
- Claude/Gemini/OpenAI Chat patch exchanges retain definitions, input, IDs, results,
  namespaces and decoded delta/done consistency. Native Codex/Any/Agent fixtures
  preserve freeform definitions, grammar, replay and responses unchanged.
- V1 malformed envelopes and non-object output entries fail without pruning valid
  windows. V2 missing/duplicate/empty-state compaction fails across HTTP and WS,
  streaming and non-streaming; upstream errors retain details. Other completed
  output types may coexist with the single required compaction item, matching the
  official Codex rust-v0.144.0 collector.
- Full relay queues retain terminal events. Cancellation and request-ID reuse do
  not close another request. Concurrent auth-bearing WebSocket log records survive.

## Checks and review

Run `go test ./...`, `go build -o test-output ./cmd/server`, affected race tests,
`git diff --check`, and `node test/provider_usage_match_test.mjs`. Local fixture
process checks exercise `/v1/responses`, `/v1/responses/compact`, valid/invalid
compaction terminals and native patch round trips without paid upstream requests.

The final two-axis review reported three Standards findings and two Spec findings.
The duplicated early-decoder-check finding, namespace matcher finding and malformed
V1 entry finding were reproduced and corrected. The suggestion to reject additional
non-compaction V2 items was checked against official source and rejected; the
collector checks the compaction count, not the total output-item count. A four-
transport positive regression preserves that distinction. Failed/stalled review
invocations are not recorded as approvals.

Delivery follows `local-fork-delivery.md`: scoped commit, clean running revision
header, unchanged container identity, live API/panel checks and fork push. Review
transcripts and local test logs are stored outside Git; config and credentials
remain untouched.
