# Any and AgentRouter Responses Compatibility

## Scope and status

This repair targets the configured Any `gpt-6-astra` (`cpa-6a`) and
AgentRouter `gpt-5.6-sol` (`cpa-5.6s`) routes. Other providers sharing an alias
are not evidence of AgentRouter compatibility. The signature/framing and
first-output repairs use protocol signals. The September 9 channel-allocation exception below is deliberately scoped to the
verified AnyRouter endpoint; it is not a general credential-cooldown change.

The earlier signature/framing repair used baseline
`6a10ebbed5cf4b6b849f888800d4c7bdeaa25aaf`. The September 8 stall-recovery diff
uses fixed baseline `cbf101e4840ec562c1fec6048bcbe9cc9e940ffc`.
Tests run in an isolated worktree; route-pinned diagnostics use a separate
loopback service. Local delivery applies the reviewed changes to `CPA-fork`,
commits them locally, and verifies automatic code hot reload with the exact
`X-CPA-COMMIT` and unchanged container ID. Normal source edits do not recreate
the container. Additional provider/model probes and remote publication are
outside this task.

## September 9: Any channel allocation must not cool shared credentials

A direct differential probe on `anyrouter.top` held the API key, model and input
constant. An existing `prompt_cache_key` completed while a different session key
failed. Subsequent Mac captures explicitly report `get_channel_failed` and model
capacity exhaustion. CPA treated these HTTP 500 responses as credential failures,
cooled every attempted key, and then returned `auth_unavailable` even to another
session that the same upstream credentials could still serve.

The HTTP Responses executor now marks a valid JSON `error.code=get_channel_failed`
response for `gpt-6-astra` with status 500, 502, or 503 from exactly `anyrouter.top` as
credential-fallback eligible, before output. This includes the SSE
`response.failed` form: the upstream event is normalized to the same original error
body and its default 502 status is classified by the same narrow exception. The
HTTP non-streaming, HTTP streaming, and Codex WebSocket terminal paths all use this
classification. It preserves the original error, allows bounded credential rotation,
and does not change shared credential availability. Failed attempts still count as
failures. Credential ordering, configured retry limits and post-output replay rules
are unchanged. No session IDs are shared or rewritten for recovery.
Other hosts, authentication/payment/quota failures, empty or malformed errors and
ordinary 5xx responses keep their existing handling. This exception applies to
normal streaming/non-streaming HTTP Responses requests, not image or compact APIs.

This repairs CPA's amplification of the upstream failure; it does not create Any
capacity or guarantee that a new session will succeed. When all attempted channels
return this error, the caller receives the original channel error rather than a
new false credential-unavailable state. Existing unrelated cooldowns retain their
normal recovery deadlines. A terminal SSE capacity event after no downstream output
is therefore retried across the bounded credential set; a capacity event after real
output remains a terminal failure and is never replayed.

Reproduction: `TestAnyRouterChannelCapacityDoesNotBlockOtherSessions` uses real
config synthesis, manager selection, the Codex executor and an HTTP proxy fixture.
Before the fix both streaming and non-streaming cases fail with `auth_unavailable`
on the healthy second session. After the fix both sessions remain independent;
all-failed requests attempt each credential once, and a stream that has already
emitted text is not replayed. `TestResponsesChannelCapacityErrorScope` verifies
provider/status boundaries, malformed input and preservation of the underlying
status/retry hint.

```sh
go test ./test ./internal/runtime/executor/helps \
  -run 'TestAnyRouterChannelCapacity|TestResponsesChannelCapacityErrorScope' -count=1
go test -race ./test ./internal/runtime/executor/helps \
  -run 'TestAnyRouterChannelCapacity|TestResponsesChannelCapacityErrorScope' -count=1
```

## Findings

1. The pinned baseline rejected actual AgentRouter-generated encrypted reasoning
   when continued through Any. In this sample, Any also rejected its own earlier
   reasoning. Removing only rejected reasoning changed the result to a completed
   response. Model switching alone is therefore not a sufficient retry trigger.
2. The Codex executor parsed SSE line by line and missed valid multiline terminal
   events. A trailing `event:` field must also apply to its current SSE frame.
   These protocol issues are demonstrated by deterministic fixtures, not claimed
   as framing behavior captured from either live provider.
3. Structured `no_capacity` / `too_many_requests` errors were classified as 502
   instead of 429.
4. Any's configured upstream WebSocket upgrade returned HTTP 404 in the captured
   test. Its HTTP Responses route worked. The client WebSocket endpoint worked
   when only Any's upstream `websockets` option was disabled. Keep AgentRouter's
   existing HTTP setting; do not disable the client WebSocket endpoint.
5. Live AgentRouter samples can emit a `keepalive` event and can fail with an
   explicit overloaded-server error after lifecycle events. One single HTTP
   upstream attempt also emitted more than one `response.created`. Neither is
   evidence of an extra CPA request; correlate with numbered upstream attempts.

## Repair contract

### Narrow signature recovery

- Successful ordinary requests retain their reasoning history.
- Recover only structured `invalid_encrypted_content` or
  `thinking_signature_invalid` rejection, not matching words in free text.
- HTTP 400/422 rejection or an initial SSE/WebSocket signature error permits
  one repaired request using the same selected model, endpoint, and credential.
- Use `error.param` to target an input index when available. Otherwise remove
  only encrypted reasoning items. Preserve visible messages, function/custom
  tool call IDs and results, image parts, and other request parameters.
- Do not mutate the caller's request. Keep both attempts in raw logs.
- Opaque compaction, a nonempty remote `previous_response_id` in the recovery
  payload, and history with no remaining portable items are not automatically
  rewritten. Existing HTTP full-history reconstruction remains responsible for
  supported incremental client requests.
- Explicit external `ExecutionLifecycle` management retains its original
  attempt/lease semantics: no hidden executor signature retry or delayed first
  lifecycle event.

### Streaming boundaries

- Read bounded complete SSE frames, including multiline `data:`, CRLF,
  byte-fragmented transport, trailing event fields, final frames at EOF, and
  legacy independent JSON data lines without separators.
- Lifecycle events, `keepalive`, and empty `reasoning` item announcements remain
  provisional until output commits. Opaque `encrypted_content` on an otherwise
  empty announcement is retained but does not count as actual reasoning output.
  Bootstrap buffering is bounded by 16 frames or 64 KiB; crossing the bound
  commits the stream and disables recovery. This memory bound must not disable
  an opted-in first-output watch when only lifecycle/heartbeat frames arrived.
- Reset provisional translation state on recovery, so rejected response IDs and
  one-time Claude token estimates do not leak into the successful attempt.
- Any real output event, including tool arguments, commits the attempt. Subsequent
  failure stays failure; do not restart generation or splice another response.
- EOF/malformed termination before HTTP streaming output commits is a retryable
  upstream 502, not a request-scoped 408. The existing credential policy may try
  a healthy key. Output already committed and externally managed attempts keep
  their existing ownership/replay boundary. Exhaustion remains failure.
- Genuine EOF, malformed/truncated streams, repeated signature rejection, and
  upstream overload never become a fabricated `response.completed`. Existing
  tool-only completion handling remains intact.
- Capture response headers before starting the stream goroutine; the retry may
  replace its local upstream response without racing the initial return.

## September 8: stalled AgentRouter requests

### Evidence and limits

- In the fixed 10:51:49–11:32:45 sample, `cpa-5.6s` used AgentRouter
  `gpt-5.6-sol`: 24 requests, 5 completions, 18 explicit overloaded/server errors,
  and 1 stream-read error. These are sample counts, not provider-wide rates.
- Both CPA's upstream-side raw logs and downstream packet captures contain
  `response.failed`. Many failures followed real reasoning/native search work;
  there was no successful completion for CPA to restore. Visible history and
  tool relationships were preserved, not transformed into an empty request.
- A separate last request (`400d4439`, 11:46:59) connected but never received
  response headers. The preceding client tool had already exited successfully.
  CPA's stream heartbeat starts too late to bound this wait. Caller cancellation
  did propagate; the missing control was the upstream first-output wait.
- Later direct comparison probes returned exhausted-budget 402 or inactive-group
  403. These are distinct from the earlier overloaded errors and prevented a
  successful search-on/off comparison. Do not remove native search or ordinary
  history without evidence, or claim that local recovery fixes provider capacity.
- AgentRouter and the separate local Agent2API provider are different routes.
  This repair does not change Agent2API or add other providers as backup aliases.

### Opt-in first-output watch

Set `responses-first-output-timeout-seconds: 120` **inside the existing Any and
AgentRouter `codex-api-key` entries only**. Preserve their keys, model mappings,
headers, proxies and other options. Omission/0 disables the watch; no provider
name or hostname is hard-coded. Configuration load, management GET/PUT/PATCH,
credential synthesis, and reload diagnostics retain the per-group setting.

- Starts on one selected credential's HTTP Responses attempt, including its
  existing optional one-shot signature repair. Covers waiting for response
  headers as well as a prefix of lifecycle/empty reasoning events.
- Uses a child cancellation context, not a deadline on the entire client request.
  The original request remains live so normal credential recovery can proceed.
- On the first real reasoning, text, completed output item, or function/native
  tool event, stop the timer. A long generation or later pause is not bounded by
  this setting. Empty scaffolding/heartbeats do not restart the timer.
- Frame acceptance and expiry are synchronized: an expired attempt does not emit
  late output, and accepted real output is not subsequently timed out/replayed.
- A silent attempt returns HTTP 504 with code `upstream_response_timeout`; the
  existing credential/round/bootstrap retry limits remain in charge. This is a
  per-attempt bound, not a total request-duration guarantee: a larger credential
  pool and additional configured retry rounds can increase total waiting time.
- After available retries finish, HTTP and client WebSocket callers receive the
  explicit error, not fake completion or an unexplained WebSocket 1006 close.
  Other transient WebSocket failures keep the existing reconnect policy.
- Caller cancellation takes precedence and stops the selected upstream promptly;
  it is not reclassified as a timeout or used to start another credential.
- Non-streaming clients are handled incrementally on the upstream SSE side, so
  the watch stops when real work starts rather than timing the whole response.
- The setting applies to the Codex **HTTP upstream** Responses path (both HTTP
  and WebSocket clients). Native upstream WebSocket liveness and
  `/responses/compact` retain their existing behavior. Both local Any and
  AgentRouter routes were verified to use HTTP upstream transport.

### Regression coverage

Public HTTP/WebSocket fixtures use real request handlers, executors, credential
selection, configuration synthesis, and the indexed request-log API. Coverage
includes missing headers, lifecycle/opaque reasoning then silence, successful
second-key recovery, exhausted keys, heartbeat-buffer overflow, caller cancel,
long reasoning/text/tool/native-search output, no replay after output, and final
credential/error attribution. Test waits use 1 second; the intended local setting
is 120 seconds.
Private packet captures, credentials and source history remain outside Git.

## Earlier signature/framing live verification and limits

The private diagnostic configuration pins one credential per provider. The source
models actually generated the opaque reasoning used in switch tests. The client
submitted that reasoning unchanged; CPA performed any recovery.

- Four HTTP cases (AgentRouter -> Any, Any -> AgentRouter, and both same-provider
  continuations) completed with the synthetic history marker preserved.
- Raw Any captures show 400 -> 200 using identical endpoint and headers, with
  only encrypted reasoning removed from the retry body.
- Fresh client WebSocket requests completed in both directions when both upstream
  routes used HTTP. Any's native upstream WebSocket remains unverified after the
  observed 404; do not advertise native WebSocket support for this route.
- The final same-client-WebSocket sequence completed five turns: Any request,
  Any incremental continuation, switch to AgentRouter with full history,
  AgentRouter incremental continuation, and switch back to Any with full history.
  Each completion retained the marker. An earlier run hit AgentRouter overload;
  that failure remains recorded.
- An additional sequence intended to test cross-model `previous_response_id`
  reuse stopped at AgentRouter with HTTP 402: `Budget pool quota has been
  exhausted`. That last cross-model-incremental step is **not verified**. Further
  live probes were stopped; no credential rotation or quota changes were made.
- After the approved TDD extension, an exhausted HTTP 402 billing budget is
  exposed as one explicit client-WebSocket error after available credential
  retries finish. Successful failover remains successful; partial output is
  preserved without replay. Other transient credential/rate-limit/transport
  errors retain the prior reconnect policy.
- The public request-log API now consumes the last turn in the client WebSocket
  timeline when legacy HTTP request/response sections are absent. It retains the
  client model, actual terminal status, partial output, error reason, and final
  provider/credential. Parser revision 4 refreshes retained indexed logs without
  modifying their original bytes.
- Exact replay of AgentRouter's logged headers/body completed directly. An earlier
  generic Python probe returned 401, and a custom-User-Agent comparison returned
  503. Those non-equivalent/transient samples do not establish a bad credential
  or a definitive header requirement.
- Tool/image/compaction preservation and malformed framing are deterministic
  local fixture coverage, not claims that all live provider features were tested.

Private captures and diagnostics are kept outside the repository; do not commit
credentials, source ciphertext, or full raw request logs.

## Regression commands

Run from the repository root in an isolated worktree:

```sh
go test ./... -count=1 -timeout=180s
go test -race ./test -run 'TestResponses' -count=3 -timeout=180s
go test -race ./internal/runtime/executor \
  -run 'TestCodex|TestHomeCodex' -count=3 -timeout=120s
go test -race ./internal/runtime/executor/helps \
  ./sdk/api/handlers/openai ./sdk/cliproxy/auth -count=1 -timeout=120s
go build -buildvcs=false -o /tmp/cpa-compat-check ./cmd/server
node test/provider_usage_match_test.mjs
git diff --check
```

The build flag above is for diagnostic worktree builds. Delivery must instead
embed the exact local commit per the local-fork delivery procedure.

The approved TDD tests use actual HTTP/WebSocket endpoints, real executors and
credential selection, a local external-upstream fixture, and the public indexed
request-log endpoint. They do not mock CPA internals or query SQLite directly.
The initial tests reproduced an unexplained WebSocket 1006 close for HTTP 402,
and a request-log entry with empty model and status 0. Both are now green.

Public contract tests (also run with `-race -count=3`):

- `TestResponsesFirstOutputTimeout*` (headers, scaffolding, active work, HTTP/WS,
  bounded exhaustion, cancellation, opt-in scope and heartbeat bounds)
- `TestResponsesFailoverAfterEmptyReasoningScaffold`
- `TestResponsesRecoversPrematureEOFBeforeOutput`
- `TestResponsesDoesNotReplayAfterRealOutput`
- `TestResponsesWebsocketRecoversBeforeOutput`
- `TestResponsesPreservesReasoningScaffoldOnSuccess`
- `TestResponsesExhaustedEOFRemainsFailure`

- `TestResponsesWebsocketReportsExhaustedBudget` (one/all credentials exhausted)
- `TestResponsesHTTPKeepsBudgetFailover`
- `TestResponsesWebsocketKeepsBudgetFailover`
- `TestResponsesWebsocketKeepsPartialOutputOnBudgetFailure`

Primary executor regression tests:

- `TestCodexResponsesSSEFrameCompatibility`
- `TestResponsesSSEReaderTrailingEventField`
- `TestResponsesSSEReaderByteFragments`
- `TestCodexCapacityEventMapsTo429`
- `TestCodexCapacityStreamCommitBoundary`
- `TestCodexSignatureRecoveryPreservesVisibleHistory`
- `TestCodexSignatureSSERecoveryCommitBoundary`
- `TestCodexNonstreamSignatureSSERecovery`
- `TestCodexSignatureRecoveryRetriesOnlyOnce`
- `TestCodexSignatureRecoveryPreservesManagedAttemptBoundary`
- `TestCodexWebsocketSignatureRecovery`
- `TestCodexWebsocketSignatureRecoveryBoundaries`
- `TestPortableResponsesSignatureRetryPreservesHistory`
- `TestResponsesSignatureRecoveryCancellation`
- Existing Home terminal-stream fresh-dispatch regressions

The broader executor `-race` run exposed an Antigravity test cleanup race in
`TestAntigravityExecute_NoCreditsWithoutConductorFlag`. The same test reproduced
with `-race -count=10` on the unchanged baseline. Report this independently;
do not describe the broad race run as passing or delete the old test. The Codex
retry header race discovered in this change was fixed and specifically re-tested.

## Local delivery

1. Check the complete task diff against this contract and resolve existing review
   findings. Preserve actual external review outcomes; failed calls are not approval.
   Follow the user's latest instruction to proceed with local delivery rather than
   extending this Any/Agent task with additional review-provider probes.
2. Apply reviewed code/tests/docs to primary `CPA-fork`, preserving unrelated work,
   and create the local delivery commit.
3. Back up the local config. Change only Any's `websockets: true` to `false` for
   this verified route; preserve its credentials, model, alias, and proxy settings.
   This is a local capability setting, not a global default or a committed secret.
4. Wait for the existing development supervisor to compile the exact local commit
   and replace the CPA process. Ordinary source delivery keeps the container ID,
   mounts, environment, network and restart policy unchanged.
5. Verify `X-CPA-COMMIT`, API and management panel availability, then exercise
   switched-history HTTP and client WebSocket continuations with route pinning.
   If verification regresses, restore the previous image and config together.
6. No push, published release/image, or remote deployment is included.
