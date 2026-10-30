# Active Codex Tool Discovery

Any/Agent history compatibility converts completed client `tool_search_call` /
`tool_search_output` pairs into portable messages, avoiding resource-owned item
and reasoning dependencies. This must not turn a freshly discovered tool into
prose only.

The shared helper also retains the callable function/namespace definitions for
verified pairs **after the latest user message** in the outgoing `tools` array.
Deferred functions discovered in this active turn become callable immediately.
This applies to normal request normalization, a signature retry, and an observed
credential switch. Production requests and management probes use the same path.

Boundaries:

- Older user turns remain inert history; a new turn can discover tools again.
- Current explicit tool definitions win; discovery does not overwrite their
  schemas, namespace descriptions, sibling functions, or tool-choice policy.
- For repeated discoveries, the latest discovered schema wins unless the request
  explicitly defines that function.
- Incomplete, duplicate, reversed, unmatched, server-executed, or cross-user-turn
  pairs do not activate tools. Hosted MCP metadata is not registered as functions.
- Compaction, remote item references, unknown routes and caller-owned input remain
  under the existing preservation guards. No catalog is stored across requests.

Regression entry points:

- `TestResponsesActiveToolSearch*` (helper invariants and recovery)
- `TestRouteStateSwitchRetainsActiveDiscoveredTools` (credential switch)
- `TestProviderActiveToolSearchProductionProbeParity` (HTTP, stream and probe)

Protocol reference: OpenAI's tool-search guide, client-executed search example,
where the returned `tool_search_output.tools` loads callable tools, rather than
just displaying their descriptions.
