# Codex State and Patch Compatibility

## Compaction replay

The WebSocket normalizer preserves a complete compaction replay window when the
selected upstream supports it or registered request interceptors may handle it.
Previously the normalizer discarded opaque compaction before interceptors ran and
restored the previous transcript, undoing compaction. The proxy neither decodes
plugin blobs nor recognizes ownership by an ID prefix. Registered interceptors
receive the intact window and remain responsible for validation/decoding; their
presence is not proof that any particular blob is valid. On an upstream without native compaction support, an unhandled blob now yields
an explicit request error after both before-auth and selected-auth interception,
before executor translation can silently
discard it. Without interceptors, existing
unsupported-backend reconstruction remains unchanged.

## Route-bound reasoning

Successful Codex completions record hashes of individual opaque reasoning items
against the isolated conversation/caller scope and selected credential/model.
After replay-cache insertion, a new attempt removes only the encrypted content
and item ID of reasoning known to come from a different route. Readable summaries,
messages, tool calls/results, compaction and stored response references survive.
Empty foreign reasoning shells are removed. Same-route and unknown state remain
untouched, including after a restart. Existing signature-error recovery remains
in place for unknown incompatible state.

Ownership is bounded to 4096 items with a 30-minute lifetime, stores hashes rather
than secrets, and ignores failed/canceled completions. Concurrent completions own
individual items rather than overwriting a session-wide current route. Runtime
refresh timestamps are not route identity. Credential attributes and authentication
metadata, including endpoint/key/account changes, are part of the fingerprint.
This is targeted compatibility, not persistent history storage or route pinning.

## Apply Patch

Claude now uses its existing custom-tool bridge for `apply_patch` instead of
discarding the declaration. Gemini bridges freeform strings through a required
`input` string function argument and restores custom call events/output. Completed
arguments are decoded before emitting a freeform delta, so Unicode, escaping and
newlines remain lossless. Claude and OpenAI Chat also publish this decoded delta
before their existing input-done event. Call IDs link responses and replayed
results; mixed/parallel Gemini calls keep independent identities.

Native Codex (including Any/Agent Codex configurations) retains custom definitions,
grammar, history and output without function wrapping. The client model catalog
advertises freeform patches only when all resolved providers are verified native
Codex, Claude or Gemini executors. Unknown/mixed unsupported routes do not inherit
capability from a default model template. Existing OpenAI Chat translation remains
supported without guessing that arbitrary provider names use that executor.

Tests use translator exchanges, real executor HTTP fixtures and a real downstream
WebSocket conversation. They perform no paid credential enumeration. No plugin,
new image switch, automatic continuation or summary-generation service is added.
