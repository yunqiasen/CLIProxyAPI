# Codex Compaction Contract Repairs

This repair preserves existing compaction operations; it adds neither a summary
model call nor automatic continuation.

- V1 `/responses/compact` retains the entire output window in order. Invalid JSON,
  error envelopes and missing/malformed output windows are surfaced as upstream
  errors, rather than passed to the client as a successful compact operation.
- Explicit V2 `compaction_trigger` requests do not auto-inject image tools. Their
  streams require one completed compaction item with nonempty opaque content
  before successful completion. Errors, cancellations and missing completion stay
  errors; the proxy does not manufacture a compacted item or successful terminal.
- An explicit `tool_choice: none` also suppresses automatic image-tool injection.
  The proxy does not guess that arbitrary prose means a summary request.
- Public `api.openai.com/v1` Responses retains caller `context_management` and
  `max_output_tokens`, before configured payload overrides. Native Codex and
  third-party Codex endpoints keep existing field compatibility rules; support
  for public threshold compaction is not inferred from a model alias. A stripped
  native Codex output budget is not an enforced token limit.

Protocol evidence is pinned to official Codex rust-v0.144.0's V2 collector: it
requires exactly one completed compaction item and response completion; other
completed output item types do not change the compaction count. V1 differs:
its whole output window may include retained messages in addition to compaction.
The implementation does not decrypt or prune opaque state.

Regression coverage uses local HTTP upstreams and actual executor entrypoints.
Existing instruction/signature compact fixtures now include an output window,
matching the contract those tests were already intended to simulate.
