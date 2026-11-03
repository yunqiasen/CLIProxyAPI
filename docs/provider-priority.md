# Provider-first key scheduling

A provider group's `priority` selects the preferred supplier. A nested API key's
`priority` orders keys **inside that group**. A lower-priority supplier's key does
not outrank a higher-priority supplier, regardless of the key's numeric priority.

For example, Any with provider priority 10 and key priorities 9 / -50 is tried
before AgentRouter with provider priority 9 and key priority 100. After the first
Any key fails with a retryable credential error, CPA tries its remaining eligible
key before considering AgentRouter. Existing attempt budgets and request-terminal
errors still bound or stop failover; provider priority does not override them.

## Eligibility and ties

- Model support, disabled/expired state, cooldown, request credential policy and
  already-tried keys are filtered before tiers are selected. Weighted routing
  also excludes non-positive weights before tier selection.
- Select the highest remaining provider tier. Within each group in that tier,
  keep its highest remaining key tier. Equal provider tiers retain the configured
  strategy among those eligible keys; key values from different groups are not
  compared. Existing credential weights apply among the resulting candidates.
- A blank key priority keeps its existing inherited default within the group.
  Explicit zero, negative and positive priorities all remain valid; no numeric
  packing or multiplier imposes a smaller range.
- Explicit probe pins and already-established session-affinity bindings retain
  their existing precedence. Fresh affinity selections use provider-first tiers.
- For configured supplier groups, websocket preference only operates after the
  supplier/key tiers have been selected. Ungrouped credentials retain existing
  websocket behavior.

## Compatibility

The configuration format and saved values are unchanged. Native Gemini,
Interactions, Claude, Codex, xAI, Meta and media groups receive additive runtime
`provider_group` and `provider_priority` attributes. OpenAI-compatible groups use
their provider priority; their keys have no separate priority field. The existing
runtime `priority` remains the effective key priority. Vertex, OAuth/file and
other ungrouped credentials retain their legacy single-priority ordering.

Both incremental scheduler and route-aware/custom-selector paths use the same
two-stage rule. Key/group edits refresh existing scheduler shards. Explicit plugin
schedulers opting into all tiers still receive all tiers, with group attributes
available alongside the existing key priority; their custom policy is unchanged.

Verification covers production configuration synthesis, all built-in strategies,
route-aware selection, mixed protocols, 429 failover over actual HTTP Responses
execution (streaming and non-streaming), model eligibility, negative/zero/extreme
priorities, weighting, disabled keys, cooldown, affinity and hot updates. No
production credential or proxy configuration migration is required.
