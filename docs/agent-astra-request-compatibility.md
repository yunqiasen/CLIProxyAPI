# Agent Astra Request Compatibility

Baseline: `a9e3cef22605fc85f074f958a96651eb151949a9`.

## Evidence and limits

The user reported resource-mismatch, reasoning-content length, encrypted-content
verification and Chat tools/reasoning errors. Searching local request logs for the
four supplied trace IDs found copies inside the user's conversation, not the
original upstream response envelopes. These matches are not incident captures.
The current Agent Codex configuration exposes only gpt-5.6-sol; gpt-6-astra was
removed by the user. Tests use local upstream fixtures reproducing the reported
messages, not paid-key enumeration or claims of a successful live Agent replay.

`go test ./internal/runtime/executor -run '^TestAgentAstraReportedErrors$' -count=1`
reproduced eight failing HTTP/streaming cases and two already-working cases before
the repair. The minimal request consists of opaque/readable reasoning plus a
portable message; tool call/results assert preservation across retries.

## Repairs

- The gpt-6-astra history normalizer previously handled reasoning content only for
  anyrouter.top. It now also handles agentrouter.org, preserving reasoning_text
  as summary_text and emitting an empty content array. The maximum-length-zero
  error is a field-shape rejection, not proof of a large conversation. Any-only
  web_search_call conversion remains Any-only.
- Agent encrypted-content errors accept either the raw message or the existing
  OpenAI Responses bad request prefix. Named items must still match actual opaque
  reasoning in the outgoing request.
- An exact Agent Azure resource-mismatch error enters the existing one-attempt,
  same-credential portable-reasoning recovery. This is reactive: an upstream
  router can switch its internal Azure resource even when CPA keeps the same key.
  Local route ownership alone does not observe that internal change. Resource
  errors without a matching disposable reasoning candidate remain errors.
- Stored references (previous_response_id and item_reference), opaque compaction
  and absent portable history prevent this recovery. CPA does not reconstruct
  missing remote state, strip arbitrary item IDs, or return synthetic success.
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

The removed Agent model is not silently re-enabled by this code change. No config,
client transcript, credential pool, Web UI or VPS is changed. If a remaining
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

## Later complete-tool and readable-history repair

The 2026-09-17 user-approved extension is specified in
[Agent tool terminal compatibility](agent-tool-terminal-compatibility.md).
It retains readable content when an encrypted reasoning ID is overlong and adds
a strict complete-forced-function EOF adapter shared by requests and probes.
It is not a general EOF-to-success rule or an automatic continuation call.
