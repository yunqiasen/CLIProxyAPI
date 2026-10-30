# Stream Terminal and Observation Parity (2026-10-30)

Three CPA defects produced client-visible failures or misleading audit rows.

## 1. Reasoning-only terminal

`internal/translator/openai/openai/responses/openai_openai-responses_response.go`
only emitted a terminal event when a message or tool item existed. A valid
reasoning-only Chat stream that ended with `finish_reason=stop` and `[DONE]`
therefore reached the client as `response.failed` with
`upstream stream closed before a terminal event`.

The terminal gate now also accepts reasoning output when the upstream declared
`stop` or an incomplete finish reason (`length`/`max_tokens`, `content_filter`).
Reasoning plus `[DONE]` without any `finish_reason` remains a failure: that is a
genuine truncation, not a completed turn.

## 2. Observation outcome parity

`sseJSONValidationState` tracks the Responses terminal event of every forwarded
frame. When the upstream channel closes, the stream lifecycle reports the same
outcome the downstream framer produces:

- `response.completed` / `response.incomplete` / `response.done` → succeeded;
- `response.failed` / `response.error` / `error` → failed with the upstream payload;
- data without any terminal event → failed with
  `upstream stream closed before a terminal event`.

This closes the case where `request_log_entries.error` recorded `response.failed`
while `request_log_observations` still reported `succeeded`. The frame bytes are
never rewritten by this tracking.

## 3. Whole-request attempt budget

Retry layers previously had independent allowances: credential failover, outer
retry rounds and stream bootstrap restarts. One dead AgentRouter path produced
24 upstream attempts and about 254 seconds in a single request.

`requestAttemptBudget` is attached once per downstream request and consumed
before every upstream stream attempt. The limit derives from the existing
configuration:

```
transportLimit = max(request-retry + 2, 3)
attemptLimit   = max((request-retry + 1) * transportLimit, 6)
```

With the default `request-retry: 2` that is 12 attempts instead of unbounded
layer multiplication. Home dispatch keeps its own contract. Pre-output retries
still apply; nothing is replayed after output commits.

## 4. Codex-aware idle heartbeat

Codex wraps `stream.next()` in an event-level idle timeout. The SSE parser drops
comment-only lines, so `: keep-alive` never reset that timer. For Codex clients
CPA now emits `response.in_progress`, which the client explicitly ignores, and
keeps the comment heartbeat for every other client. A heartbeat is skipped while
a partial frame is buffered so it cannot split an event.

## Verification

- `go test ./sdk/... ./internal/...` (except the pre-existing `internal/util`
  repository-walk tests blocked by the root-owned `auths/freetier2api` directory).
- Delivered-binary loopback replay: reasoning-only stop → `response.completed`;
  reasoning-only length → `response.incomplete`; no `finish_reason` → still
  `response.failed`; captured k3 histories (243/77 items) → `response.completed`
  with zero empty non-tool messages.
- Observation regressions cover missing terminal, `response.failed` and normal
  completion.
