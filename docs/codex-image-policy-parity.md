# Codex Image Policy and Probe Parity

## Behavior

The existing Codex provider image-generation switch is unchanged. Disabling image
generation strips the supported hosted/function image tools and associated tool
choices. Responses Lite remains a separate protocol signal: it suppresses automatic
image-tool injection and disables parallel tool calls, while retaining explicitly
supplied image tools unless the provider's existing switch removes them.

Payload preparation resolves headers in the same order as upstream transport:
client headers (or the existing Gin-context fallback), credential custom headers,
then model header overrides. Resolution uses a copy and does not overwrite caller
headers or generate transport identity values. Existing client-metadata Lite
recognition remains supported.

HTTP streaming, non-streaming, WebSocket execution and compact requests use this
policy before deciding image injection and parallel tool behavior. Compact requests
retain their existing no-auto-injection behavior; their parallel-call policy now
also honors effective credential/model Lite headers. This repair does not add a summary
engine or change compaction ownership/replay.

Management probes continue through the production executor and provider synthesis.
They follow saved settings or an unsaved draft, including both image-switch states.
A complete UI draft explicitly resets omitted settings with null; a partial API
draft still inherits fields it omits. The selected credential stays pinned. Probes
retain representative image tools and let production rules transform them, rather
than using an independent text-only shortcut. A successful probe is not a claim
that every optional image tool has been executed successfully.

## Verification

- Executor transport tests capture actual HTTP/WebSocket upstream requests across
  five execution modes and client, credential, model, metadata and context sources.
- Model-over-credential and credential-over-client precedence, explicit image tools,
  both provider-switch states and caller-header immutability are covered.
- Management tests compare production, saved-setting probes and unsaved-setting
  probes for Any and Agent using local proxy fixtures. They check upstream model,
  selected key, image tools, tool choice and parallel-call behavior, including
  drafts that contradict saved configuration without mutating it.
- Existing provider draft serialization and request tests remain applicable; no UI
  contract or new switch is introduced by this repair.

Run the new end-to-end regressions with:

```sh
go test ./internal/runtime/executor ./internal/api/handlers/management \
  -run 'TestCodexExecutorLitePolicySources|TestProviderConnectivityImagePolicyMatchesProduction' -count=1
```

The full suite, a server build and race-enabled runs of these tests are required
before local delivery. Other planned repairs (route state, patch bridges, compaction
replay and terminal handling) are separate work and are not implied by this change.
