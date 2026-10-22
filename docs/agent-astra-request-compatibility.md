# Agent Astra Request Compatibility

Baseline: `a9e3cef22605fc85f074f958a96651eb151949a9`.

## Evidence and limits

The user reported resource-mismatch, reasoning-content length, encrypted-content
verification and Chat tools/reasoning errors. Searching local request logs for the
four supplied trace IDs found copies inside the user's conversation, not the
original upstream response envelopes. These matches are not incident captures.
During the September 15 investigation, Agent exposed only gpt-5.6-sol because
gpt-6-astra had been removed temporarily. The model was configured again before
the September 20 capture described below.

`go test ./internal/runtime/executor -run '^TestAgentAstraReportedErrors$' -count=1`
reproduced eight failing HTTP/streaming cases and two already-working cases before
the repair. The minimal request consists of opaque/readable reasoning plus a
portable message; tool call/results assert preservation across retries.

## Repairs

- The gpt-6-astra history normalizer previously handled reasoning content only for
  anyrouter.top. It now also handles agentrouter.org, preserving reasoning_text
  as summary_text and emitting an empty content array. The maximum-length-zero
  error is a field-shape rejection, not proof of a large conversation. Proactive
  web_search_call conversion remains Any-only. Agent converts completed search
  history only after an exact rejected-state envelope, as described below.
- Agent encrypted-content errors accept either the raw message or the existing
  OpenAI Responses bad request prefix. Named items must still match actual opaque
  reasoning in the outgoing request.
- An exact Agent Azure resource-mismatch error enters the existing one-attempt,
  same-credential portable-history recovery. This is reactive: an upstream router
  can switch its internal Azure resource even when CPA keeps the same key. Local
  route ownership alone does not observe that internal change. Resource errors
  without matching disposable route-bound state remain errors.
- Stored references (previous_response_id and item_reference), opaque compaction
  and absent portable history prevent this recovery. CPA does not reconstruct
  missing remote state, strip client-owned user message IDs, or return synthetic
  success.
  Failure after committed text/reasoning/tool output does not trigger replay.
- Production and management probes share these helpers. The existing synthesis/
  alias/payload test now covers Agent content normalization and resource-error
  retry with exactly the selected key; no independent probe rules were added.

## Protocol choice

Configure Agent gpt-6-astra under the existing Codex/Responses provider, not an
OpenAI-compatible Chat upstream entry. A Chat client can still call CPA: the Codex
executor translates messages, tools and reasoning to upstream /responses. The
regression exercises both streaming and non-streaming Chat clients and confirms
function tools and high reasoning survive. Removing tools or reasoning_effort to
make /chat/completions return 200 is not an equivalent repair.

The September 15 code change did not silently re-enable the removed Agent model.
No config, client transcript, credential pool, Web UI or VPS was changed. If a remaining
resource error refers to stored/compacted state rather than disposable reasoning,
use the original upstream resource or replay a genuinely complete readable history;
otherwise preserve the error instead of silently discarding context.

## Verification and review

The user approved the final review gate. Two independent review axes completed:
Standards reported one non-blocking duplicated test runner; Spec reported no
findings. The duplicate execution/drain logic was extracted into a test helper.
No runtime behavior changes were requested by review.

The complete Go suite, build, affected race tests, HTTP/WebSocket recovery,
post-output no-replay and production/probe parity are checked before delivery.
Delivery follows local-fork-delivery.md: scoped commit, automatic hot reload,
exact clean revision header, unchanged container ID, API/panel availability and
local upstream fixture verification. This does not certify Agent's live Azure
resource routing; the user-disabled model remains disabled.

## September 20: portable continuation across Agent resource switches

A captured long Codex request showed the remaining failure after the earlier
reasoning repair. The local routing strategy was `fill-first`, not session
affinity. After a selected Agent credential received a real Astra rate limit,
CPA could select another credential. The request still carried Azure-resource
bindings on historical messages and function items, so Agent rejected the old
conversation even after encrypted reasoning was removed.

A same-key live differential replay used the repaired captured body.
Keeping historical item IDs returned the resource-mismatch 400; removing only
the assistant message/function/custom-tool item IDs returned HTTP 200 with
`response.completed`. Readable text, reasoning summaries, function `call_id`,
function outputs and search history remained present. This proves the remaining
400 was route-bound item identity, not conversation length or account balance.

For the exact Agent resource-mismatch envelope, the one pre-output retry now:

- removes rejected encrypted reasoning while retaining readable summaries;
- removes top-level IDs from reasoning, assistant messages and function/custom-tool
  items while preserving client-owned user message IDs, `call_id` and results;
- originally retained native search item IDs; the September 21 repair below
  instead retains their complete records as readable history after rejection;
- remains disabled for `previous_response_id`, `item_reference`, opaque
  compaction, missing portable history, repeated rejection and committed output.

A manager-level regression covers the observed chain: first credential emits an
Astra 429, bounded failover selects a second credential, the second resource
rejects old bindings once, and the same credential completes after the portable
retry. The existing conversation continues without requiring a new client task.
The upstream 429 remains a real failure signal; CPA does not turn it into success.

## Later complete-tool and readable-history repair

The 2026-09-17 user-approved extension is specified in
[Agent tool terminal compatibility](agent-tool-terminal-compatibility.md).
It retains readable content when an encrypted reasoning ID is overlong and adds
a strict complete-forced-function EOF adapter shared by requests and probes.
It is not a general EOF-to-success rule or an automatic continuation call.

## September 20 follow-up: masked resource name

Baseline: `d69b8cc111670c5e3c2b30d170e03e263ade34b1`.

The retained request-log index for `9d04ee2f` (10:04:27, local time) contains
`different *** OpenAI resource`, not `different Azure OpenAI resource`.
The executor's upstream-error log for `7aff0ffe` (10:55:51) independently records
the same masked wording before the error reaches the client. The morning's
12 indexed Agent resource failures contain eight masked messages and four Azure
messages; these are incident sample counts, not a provider-wide failure rate.
The old recognizer accepted only Azure, so the masked envelope skipped recovery
even after the earlier portable-item-ID repair.

The completed request immediately before `9d04ee2f` used the same CPA credential
and client turn. The failure was the next Responses request in that task, not a
retroactively failed completed response. Stable CPA key selection alone does not
make every historical item valid at Agent's backing resource. No global routing
strategy or session files are changed by this repair.

The matcher now accepts exactly `Azure` or the observed literal `***`. It retains
all existing gates: Agent Responses endpoint, invalid-request error type, empty
code/parameter, route-bound input state and portable history. It does not match
arbitrary resource names, quoted errors, rate-limit envelopes or other providers.
The existing one-attempt recovery, ID cleanup and history-preservation rules are
reused, including their stored-reference, compaction and post-output guards.

### Verification

- The original masked envelope fails against the baseline through the public
  recovery helper, HTTP executor, SSE executor, native WebSocket executor,
  management probe and credential-failover manager. Restoring the one-line matcher
  change makes these same regressions pass.
- A copied baseline runtime binary returns the exact masked HTTP 400 after one
  attempt against a local upstream fixture. The repaired binary passes six
  runtime cases: HTTP-400 and SSE-failure recovery, each through streaming,
  non-streaming and the management probe. Each performs two attempts on the same
  key/session and retains readable summaries, text, tool call IDs/results,
  search history and the client-owned message ID.
- The manager fixture covers Astra rate limit -> another credential -> masked
  resource rejection -> same-credential repair -> completed response.
- Public HTTP regressions separately verify that rate limits after text,
  reasoning, function or native-search output remain failures, preserve the
  original error details, and do not replay the already-started work.
- `go test ./... -count=1 -timeout=180s`, the affected four-package race suite
  (`-count=3`), server build and management-bundle regression all passed.

The 12:20 single-key direct check used only Agent's first configured credential
and a two-message request. It returned upstream HTTP 402 with the budget-pool
message before the resource comparison could complete. No other key was tried;
this does not establish the user's account balance or live recovery success.
Local protocol replay is verified separately from upstream availability. The
saved provider settings and its existing disable switch remain unchanged.

Standards and Spec were manually reviewed against this baseline: no remaining
findings. The change is confined to the shared recognizer, its regression tests
and this document; this is not an independent dual-model review.


## September 21: search history and encryption-first recovery

Review baseline: `3a4a4b50e66fda3f1fe2ca6743f0a2c8f7e203a1`.

The reported trace `288391cb0f3c9ed6b46b903b685afb80` is an actual AgentRouter
`gpt-6-astra` failure, not a copy in a user prompt. The request-log index records
request `e4af285e` at 10:15:29. Main logs show portable-history recovery at
10:15:44 and another resource rejection at 10:15:49. The previous repair was
already running. The raw request log rotated away, so the exact first upstream
error and full outgoing body are not established by these artifacts.

The corresponding client transcript contains two completed native search records:
`search` at 10:15:10 and `open_page` at 10:15:26, followed by local function calls
and results. Recovery previously retained their server-owned IDs. The copied
running baseline binary reproduces a rejection after its one retry with this
history shape. An independent chained-error case reproduces encrypted-state
rejection followed by resource rejection: the first repair only removed reasoning
state and left other disposable route bindings active.

The shared one-shot recovery now converts completed native search records into
the same data-only assistant history representation used on Any, preserving the
entire original JSON record, including queries, URLs and extension fields. Exact
Agent encrypted-state and resource-state errors repair supported disposable
bindings together rather than spending the retry on only one binding type.
Successful Agent calls and their native search output are unchanged. No extra
retry, model substitution, key sweep, or replay after committed output is added.

Stored-response references, item references, opaque compaction, unfinished search
records, missing search actions and absent portable conversation content retain
the existing conservative boundaries. User-owned message IDs, readable reasoning,
function/custom-tool call IDs, arguments and results remain intact. Client input
bytes and saved provider configuration are not changed.

Verification includes a generated-history sequence, not only a prebuilt request:
Agent first returns native search/open-page records and a function call; the client
appends the tool result, continues on Agent, and then sends the same stored history
to Any. Both stream and nonstream variants fail on the baseline and pass with the
repair. HTTP/SSE rejection, native WebSocket and management-probe regressions use
the same shared recovery. The built-binary fixture passes 12 resource/encryption,
HTTP/SSE and stream/nonstream/probe combinations on one selected key/session.

```sh
go test ./test -run TestAgentToAnyConversationRetainsGeneratedSearchHistory -count=1
go test ./internal/runtime/executor ./internal/runtime/executor/helps \
  ./internal/api/handlers/management ./test \
  -run '(Agent|Any.*History|Signature|ProviderConnectivityAndProductionShareAnyAgentPipeline)' -count=1
```

At 18:10 on September 21, a separate CPA running the candidate binary replayed the
captured search/reasoning/message/tool correlation IDs through production routing,
with tool text redacted and exactly the first configured key per site. Agent
returned HTTP 402 for its upstream budget pool; Any returned HTTP 500
`get_channel_failed` (request `202609211810156688625482WOK491S`). Neither reached
`response.completed` or the state-repair boundary. This is **not completed
real-site acceptance**, nor evidence that all keys lack balance. The saved Agent
exclusion and all production configuration remained unchanged. Full Go tests,
focused race tests, build and panel regression passed independently of these
external availability failures.


Review: the complete diff and new tests received manual Standards and Spec passes
against the fixed baseline, AGENTS.md, CONTEXT.md and the compatibility ADR; no
remaining actionable code findings were identified. Independent review subprocess
attempts ended without verdicts because of external service/authentication errors
and were not counted as approvals. This is not an independent dual-model signoff.


## Conversation compatibility acceptance

The user subsequently scoped completion to Codex conversation/channel compatibility,
excluding upstream capacity and budget availability. The earlier 18:10 live errors
remain historical observations, not blockers for this task. The public-handler matrix
in `test/codex_conversation_switch_contract_test.go` extends verification to both
channel directions, returning to a channel, independent concurrent sessions, and
copying retained history into a fresh session over HTTP/SSE/WebSocket. Native search,
readable reasoning and function/custom-tool results survive all 84 client turns.
The pre-repair helpers fail this matrix; `ae3933b5` passes without additional production
changes. See [the acceptance contract](any-agent-responses-compatibility.md#acceptance-conversation-and-channel-compatibility)
for the exact scenarios and limits.

## October 21: request-ID metadata and Sol conversation switching

Review baseline: `53d8336261cbb436f69e42addb7f80a5a44f91ff`.

The original upstream response is retained in local request `a26b6308`, not only
quoted in a user prompt. At 15:32:01 +08:00, `cpa-6s` resolved to AgentRouter's
`gpt-6.1-sol` and returned HTTP 400 with request ID
`20261021073158346501676MSvAiaYZ` and trace
`968823f431986387c18884d75f28b92f`. Its message has the new suffix:

```text
The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. (request id: ...) [trace_id=...]
```

Only one upstream attempt was recorded. The existing anchored recognizer allowed
an optional trace ID but not the preceding request ID, so the one-shot recovery
was skipped. A differential fixture changes only that suffix: with the request ID
both Sol and Astra skip recovery on the baseline; without it both enter recovery.
The minimal reproduction needs only an assistant message with a historical item
ID. The full captured request had 155 items: 72 messages, 25 reasoning items,
29 function calls and 29 matching outputs, with one remaining opaque reasoning
item. It contained no stored parent reference, item reference or compaction.

The shared recognizer now accepts an optional nonempty alphanumeric/underscore/
hyphen request ID before the optional trace ID, for the exact Agent resource and
named-encryption errors. Anchored message matching, endpoint, error type, empty
code/parameter and referenced-item validation remain intact. Additional prose,
malformed metadata, unknown errors and unrelated endpoints do not enable repair.
This is a model-independent error-envelope repair, not a new model-name rule.

The existing shared recovery still makes at most one same-key retry before output,
retains readable summaries, messages, tool call IDs/arguments/results and search
records, and preserves the remote-reference and compaction boundaries. It changes
only the outbound copy; it does not reset the client's conversation, alter provider
configuration/priority or add a second scheduler. Repeated rejection and errors
after real text, reasoning or tool output remain failures rather than replaying
already-visible work.

### Regression scope

The new regressions first fail against the baseline and then pass with the shared
recognizer change. They cover HTTP 400, SSE failed terminals, native WebSocket,
production/selected-key management-probe parity, and rate limit followed by
another credential's resource rejection. Existing Astra coverage remains active.
The public HTTP/SSE/downstream-WebSocket matrix adds another 84 client turns using
Agent `cpa-6s`/Sol and Any `cpa-6a`/Astra: continuing, switching both directions,
returning to a channel, concurrent independent sessions, and retained history
copied into a fresh client session. Terminal, identity and history assertions are
unchanged. These are local protocol tests, not a claim about live upstream capacity.

```sh
go test ./internal/runtime/executor/helps ./internal/runtime/executor \
  ./internal/api/handlers/management ./test \
  -run 'AgentRequestID|AgentSol|ConversationSwitch.*Contract|CodexAgentMessageOnlySSERecoveryBoundaries|CodexWebsocketAnyAgentHistoryCompatibility|ProviderConnectivityAndProductionShareAnyAgentPipeline' \
  -count=1
```

### Plugin disposition for this incident

- `codex-switch-guard`: native per-item ownership and precise rejected-state
  recovery already own this responsibility. A second ownership store, route lock
  or blanket conflict response is not part of this repair.
- `codex-app-multisession-compat`: retain its orphan-output boundary cases as a
  reference. All 29 captured function outputs have preceding matching calls, so
  this incident does not establish a missing delegation-normalization feature.
- `codexreflow`: existing framing and terminal validation remain in use. Extra
  inference rounds and replay caches are unrelated to this pre-output HTTP 400.
- `cpa-scheduled-tests`: excluded as requested. No scheduled inference is added.

A separate-process replay uses the sanitized 155-item captured shape and a
loopback upstream only. The copied baseline deployment returns the same 400 after
one attempt in all 12 HTTP/SSE/resource/encryption and client/probe combinations.
The candidate passes 21 checks: 12 successful one-shot recoveries plus repeated
failure and post-output no-replay checks. The full Go suite, affected race suite
(`-count=3`), server build and management-bundle regression also pass. No live
upstream generation probe, provider edit or plugin installation is part of this
verification.

Independent Standards and Spec reviews completed against the fixed baseline with
zero findings on either axis. The complete diff was also inspected before local
delivery; no provider settings or unrelated plugin-host changes are included.

## October 22: numeric errors wrapped by the Agent relay

Review baseline: `e080f6ee22d6a267ebe96162fb2ce5b0025afa99`.

The screenshot matches the persisted error of local request
`v1-responses-2026-10-22T100734-f6d01045`, started at 10:06:07 +08:00.
The observation record identifies AgentRouter, client alias `cpa-6s`, upstream
`gpt-6.1-sol`, HTTP 400 and exactly one attempt. Main logs confirm the same
upstream error at 10:07:34. This is not text quoted inside a user prompt.

The preceding 10:04:20 request had already recovered once and completed on the
same credential. The exact client turn then recorded one opaque reasoning item,
one tool call and its matching result before this failed continuation. There was
no compaction in that turn fragment. Thus the screenshot also exercises continued
tool work after a successful switch, not just the first request after switching.

The outer `invalid_request_error` has a null code and a message beginning with
`failed to decode error response`. Its `raw:` payload is another JSON error:

```json
{"error":{"code":0,"message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. trace_id: 3a1ac94610531a8eb4c492e638839a77","type":"invalid_request_error"}}
```

The relay appends `[trace_id=f87913149f33c5d348f6aa95e2ee36b5]` outside that
embedded JSON. The preceding repair covered direct errors with bracketed trace
IDs and request IDs, but not this decoder envelope. Independently, numeric code
`0` and the colon-style trace suffix also prevented recognition. Each condition
fails a separate minimal regression on the baseline.

The shared Agent recognizer now accepts the observed numeric zero and colon trace
form. It decodes exactly one embedded JSON value only for the known decoder
prefix, validates both error envelopes and the outer diagnostic suffix, and then
uses the existing anchored resource/named-encryption matching. Other numeric
codes, string codes, unrelated parameters/types, arbitrary quoted errors,
recursive wrappers, extra JSON and malformed trailers do not trigger repair.
The upstream error bytes are preserved; this parser is not a general error rewrite.

Repair still modifies only the outgoing copy, using the existing one-shot,
same-credential recovery. It preserves readable reasoning, messages, search
records, tool arguments and matching results. It neither locks the conversation
to Agent nor resets client history. Stored response references, item references
and opaque compaction keep their existing boundaries. Output already delivered
to the client is not replayed, and repeated rejection remains a failure.

### Verification and evidence boundary

- The recorded decoder message fails through the public HTTP, SSE and downstream
  WebSocket handlers on the baseline, then passes after the change.
- Numeric/direct and wrapped errors add 168 client turns using retained output:
  Agent/Sol and Any/Astra in both directions, returning to a route, independent
  concurrent sessions, and copied history. Existing 168-turn coverage remains.
- Native upstream WebSocket, selected-key management-probe parity, rate-limit
  failover to another credential, repeated failures, and failures after text,
  reasoning or tool output all pass with the new envelopes.
- A separate-process comparison uses the copied running baseline binary and the
  candidate binary. The baseline reproduces the error after one attempt in all
  12 numeric/wrapped, HTTP/SSE, client/probe combinations. The candidate passes
  21 recovery and no-replay checks. Every upstream in this comparison is loopback.
- The current raw request file has rotated away. The binary comparison therefore
  uses the previously sanitized 155-item history as a representative long
  conversation, not as the captured October 22 request body. The retained error,
  route and attempt count are from the current incident.
- Full `go test ./... -count=1 -timeout=180s`, the affected four-package race suite
  (`-count=3`), server build and management-bundle regression pass. These suites
  use local fixtures, with no paid-key sweep, provider edit or plugin installation.

```sh
go test ./internal/runtime/executor/helps ./internal/runtime/executor \
  ./internal/api/handlers/management ./test \
  -run 'AgentNumericCodeAndDecodeEnvelopeRecovery|AgentDecodeEnvelopeRecoveryGuards|ConversationSwitch.*Contract|AgentSol|CodexAgentMessageOnlySSERecoveryBoundaries|CodexWebsocketAnyAgentHistoryCompatibility|ProviderConnectivityAndProductionShareAnyAgentPipeline' \
  -count=1
```

### Bounded live check and review

A separate candidate process made one logical request using only Agent's first
configured key and the production Responses executor. It carried an opaque
reasoning item already returned by `cpa-6a`, with readable test messages replacing
private text. No new Any request was sent. Agent returned HTTP 404, "The API
deployment for this resource does not exist", request
`20261022080430357626519aPyLYTXW`, trace
`713cc48976df60ffc6809c6cc44ae1f1`. There was no `response.completed`; no other
key was attempted. This is a different upstream outcome, not real-site acceptance
of the recorded 400 or the user's original window. The production configuration,
credential pool and runtime were untouched by this isolated check.

Independent read-only reviews completed on both axes: Standards found no rule
violations and two non-blocking naming/test-representation suggestions; Spec found
no actionable findings. The suggestions were left unchanged under the repository's
small-change/KISS rule. The final evidence additions were manually checked.
