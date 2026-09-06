# Responses Local Compatibility

This CPA fork change repairs local Responses stream handling and request-log
classification. Provider configuration, credential selection, stable AuthIDs,
provider display names, raw log storage, and the management UI stay unchanged.

## Streaming contract

| Condition | Downstream behavior |
| --- | --- |
| Executor fails before the first complete data frame | Preserve a non-2xx JSON error; plugin direct responses retain their status, headers, and body. |
| Failure or early EOF after streaming starts | Preserve earlier output and emit one terminal failure. |
| Official Codex client | Use `event: response.failed` with nested `response.error`. |
| Other Responses clients | Use `event: error` with the existing Responses error shape. |
| A terminal response already arrived | Keep that terminal result; retain subsequent transport errors in diagnostics rather than emitting a second terminal event. |
| Completed response omits its output items | Keep the fork's reconstruction from previously received output-item events. |

The shared validator reassembles fragmented JSON, multiline `data:` fields, and
split CRLF line endings before forwarding them. Tests exercise every two-chunk
split and byte-by-byte delivery for event-first, data-only, data-before-event,
and CRLF/multiline streams, including UTF-8 and literal SSE-looking text inside
JSON strings. Complete data JSON does not release a partial trailing `event:`
field. A final valid field without a newline is delivered at EOF.

Legacy line-sized executor chunks remain supported. Bare JSON frames used by
WebSocket executors remain supported by the validator. Complete SSE frames with
blank-line delimiters are preferred for executor and plugin integrations.

HTTP error normalization keeps useful type/code/message/param fields, redacts
recognized credential values, and bounds diagnostic text. It also applies to
errors detected while closing the stream. Raw upstream request/response logging
remains separate from these downstream diagnostics.

## Multi-key behavior

The existing AuthManager and executor retry policies remain in control. The
regression fixture uses a real Codex executor, a local HTTP upstream, two AuthIDs,
the HTTP handler, request-log middleware, and SQLite indexing to verify:

1. An HTTP 429 on the first key can succeed through the second key.
2. An SSE quota error before output can succeed through the second key.
3. A quota error after output preserves that output, ends with a failure, and
   does not replay the request through another key.

After available credential retries, HTTP 402 billing failures are sent as one
explicit WebSocket error before closing; they are not converted to a bare
transport disconnect. Other transient-error reconnect rules remain unchanged.

When WebSocket data and error channels close together, forwarding checks for the
pending upstream error before synthesizing an EOF error. This preserves the
existing 401/429 pinned-credential replay decision and transport-close handling.

## Request logs and statistics

- Parse complete SSE frames, multiline data, and legacy consecutive JSON lines.
  An `event:` field may follow `data:`; the last event field within a delimited
  frame is used when the JSON payload omits its type.
- Recognize top-level errors, nested `response.error`, and failure events with
  no message. Preserve partial assistant text independently from error details.
- Prefer a successful downstream completion over errors from earlier attempts.
  Recover an upstream-only error from the explicitly numbered final attempt;
  use unnumbered sections only for an unambiguous single-attempt log.
- Keep request totals attributed to the final provider/AuthID. Earlier failed
  attempts remain available in the original raw log.
- When a WebSocket log has no legacy HTTP request/response sections, index its
  last client turn from `WEBSOCKET TIMELINE`, including inherited client model,
  final operation status, partial text, and the final credential's outcome.
- Preserve spaces and newlines while joining text deltas. Named/custom tools
  and built-in tool-only turns are classified as tool calls, not empty replies;
  tool arguments and internal tool text are excluded from assistant text.
- Parser revision advances to **4**, including client WebSocket timeline parsing. The next request-log sync re-parses
  retained raw files with the new rules and refreshes their indexed statistics.
  Original raw log bytes are preserved.

## Verification

Run from the repository root, preferably in an isolated worktree so runtime
files under `logs/` do not affect source-tree tests:

```sh
GOMAXPROCS=2 go test -p 2 -count=1 ./...
GOMAXPROCS=2 go test -race -p 2 -count=1 \
  ./sdk/api/handlers/... ./internal/api/handlers/management \
  ./internal/util ./sdk/cliproxy/auth ./internal/client/codex/live
GOMAXPROCS=2 go build -p 2 -o test-output ./cmd/server && rm test-output
node test/provider_usage_match_test.mjs
git diff --check
```

Primary regressions:

- `TestResponsesCompatibilityArbitraryChunkBoundaries`
- `TestResponsesCompatibilityTrailingEventField`
- `TestPluginResponsesStreamCompatibility`
- `TestForwardResponsesWebsocketPrefersPendingErrorOverClosedData`
- `TestRequestLogResponsesOutcomes`
- `TestRequestLogResponsesMultiKeyWorkflow`
- `TestRequestLogResponsesRefreshesRevisionTwo`

The in-process WebRTC audio/data bridge test uses loopback peers to avoid host
VPN/container ICE selection. Production media code and test assertions for
audio, bidirectional data, capacity release, and logging remain intact.

## Scope

Upstream quota and genuine generation failures remain upstream outcomes.
Conversation history and tool-call/result relationships are preserved. The
[Any and AgentRouter compatibility repair](any-agent-responses-compatibility.md)
adds bounded, explicit-signature recovery and executor-level SSE framing support;
its verification and local-delivery gates are documented separately.

Local verification results and review status are recorded in the
[implementation plan](superpowers/plans/2026-09-05-responses-local-compatibility.md).
