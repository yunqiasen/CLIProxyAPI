# Agent forced-tool terminal compatibility

The user approved two compatibility changes on 2026-09-17: retain readable
reasoning when its encrypted item's ID exceeds the supported limit, and repair
Agent Astra's missing response terminal after a complete forced function call.
This extends the earlier terminal contract only for the specific HTTP case below.

## Complete forced-function streams

Both Codex HTTP execute paths read through `helps.NewCodexResponsesSSEReader`.
The management connectivity probe already uses these executors, so it receives
the same repair without a separate probe implementation or a new setting.

Eligibility requires the actual endpoint host `agentrouter.org`, the actual model
`gpt-6-astra`, a Responses endpoint, and a named forced function present in the
outgoing tools. Stored-response and compaction operations are excluded. Other
providers, models, automatic tool choices, native WebSocket transport, and
ordinary text-only EOF handling are unchanged.

The adapter accepts only a clean HTTP-body EOF with every observed function call
fully closed. It validates the announced item ID, call ID, function name, output
index, argument deltas, complete argument JSON, argument-done event, and completed
output-item event. Parallel calls must have unique identities and consecutive
output indexes. Their output order and actual arguments are preserved.

A qualifying stream receives exactly one `response.completed` containing its
collected function calls. A native terminal's outcome, output, usage and error
details remain authoritative; no second terminal is added. When the upstream has
not supplied lifecycle metadata before its first tool item, CPA emits an
`in_progress` creation event with one local response identity. This provisional
event is not a success claim: partial or failed streams may receive it too.
Subsequent events use increasing sequence numbers and the same client-visible
identity, including any later native terminal. A native identity supplied before
the first item is retained instead. Local IDs are correlation IDs, not upstream
stored responses; stored-response requests are excluded from this adapter.
Token usage remains null when the upstream supplies none; no token count is
invented. Raw logs retain the original upstream IDs, sequence numbers and bytes;
synthetic frames have empty `Raw`. Separate debug messages identify identity
normalization and terminal repair without mixing them into the upstream log.

Partial arguments, missing item completion, conflicting identities, additional
unfinished output, malformed frames, failure/incomplete/canceled events,
contradictory lifecycle details or SSE event names, transport read failures, and caller cancellation
remain failures. The adapter adds no request retry, model generation, timeout,
or tool execution. A function call here is ready for the client to execute; CPA
does not claim that the requested external tool has already run.

Official event definitions checked during implementation:
https://developers.openai.com/api/reference/resources/responses/streaming-events
Item-done is not standard response-done; the repair is an explicitly approved
compatibility interpretation of this provider's complete tool-only EOF.

## Readable reasoning with overlong IDs

`SanitizeCodexInputItemIDs` now removes the overlong reasoning ID and its bound
encrypted field while preserving readable summary/content and extension fields.
It shares the field-stripping helper with existing signature recovery. Empty,
opaque-only overlong reasoning still follows the prior removal behavior.
Valid-length bindings, tool call/result identifiers, compaction and reference
items retain their existing behavior. Provider-specific reasoning normalization
still runs first, so Any/Agent Astra receive readable summary with empty content.
Only the outgoing request copy changes; saved client history is untouched.

## Verification

- Executor tests cover Responses and Chat, streaming and non-streaming, with one
  selected credential and no replay of completed calls.
- Reader tests cover parallel output, lifecycle identity, native completion,
  bounded state, scope exclusions, malformed/conflicting/partial events, real
  transport errors and cancellation.
- Request tests cover both summary and content, 64/65-character boundaries,
  Unicode IDs, immutability/idempotence, Any/Agent normalization and other routes.
- Management/production parity tests exercise the same alias, high-effort payload
  rules, readable history preservation and terminal repair, and reject partial calls.
- The public binary acceptance harness reuses the function-call values from a
  captured successful Agent response. It covers valid/missing/native terminals,
  partial streams, explicit errors, broken Content-Length, both client protocols
  and response modes, a tool-result follow-up, and the management probe.
- Real Agent probing is pinned to its first configured key. Availability failures
  are recorded separately from simulated contract success, without testing a pool.

### Local acceptance artifacts

The public binary harness and captured-response replay evidence are retained in
`.scratch/agent-terminal-history-20260917/` in the primary development checkout,
not committed as portable fixtures. `public-contracts.py <server-binary>` runs 25
public-endpoint checks, including Responses/Chat in both response modes, downstream
WebSocket with HTTP upstream, a `function_call_output` follow-up and management
probe parity. It reads the selected recorded tool item from the preceding local
acceptance capture; all requests in this harness use a local fixture, not a paid
provider. Permanent request, executor, reader and probe regressions are committed
alongside the implementation.

### Verification outcome (2026-09-17)

- Final `go test -count=1 ./...`: 4,386 top-level tests and 3,911 subtests passed;
  seven pre-existing skips. Focused race tests passed five repetitions.
- Server build, management-panel regression, and all 25 public binary checks passed.
  Regression feedback also caught conflicting SSE event names; those negative cases
  now remain failures rather than receiving a compatibility completion.
- Standards and Spec reviews completed. The provisional/local identity behavior
  and upstream-only raw logging contract were clarified and locked with native
  completed/failed/incomplete terminal tests. Review dispositions remain with the
  local acceptance artifacts.
- One candidate-binary request used Agent's same first selected key. The captured
  outgoing request retained both readable history fields, removed the invalid
  binding, and kept the actual model, forced function and high reasoning effort.
  The upstream returned HTTP 402 budget-pool exhaustion, matching the earlier
  direct control. This is **not a successful real-site acceptance** of terminal
  repair. No other key was tried; saved provider/model configuration was unchanged.
