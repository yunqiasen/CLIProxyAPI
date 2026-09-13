# Codex Connectivity Test Results

The management panel tests a selected Codex credential through
`POST /v0/management/provider-connectivity-test`. The backend uses the production
streaming executor and returns a JSON envelope with `status_code`, `header`, and
`body`. Its `body` can contain the complete SSE transcript rather than one JSON
response object. The outer HTTP 200 alone is not proof of a successful generation.

## Panel verdict

`simulateCodexProvider` in the management UI source owns the per-key verdict:

- Retain support for existing JSON response bodies. Explicit JSON errors or a
  non-completed response status remain failures, including with HTTP 200.
  Standalone JSON event envelopes use the same completion/error checks as SSE.
- Accept an SSE result only after a `response.completed` event with an object
  response. The status must be `completed`; an error must be absent.
- Lifecycle events, partial text and `[DONE]` alone do not establish completion.
- An explicit `error`, `response.failed`, `response.incomplete`, or
  `response.cancelled` event wins over a completed event. Malformed event data also
  remains a failure.
- Accept normal SSE framing: named events, multiline data, comments, CRLF,
  an initial BOM, empty keepalives and a complete last frame without a delimiter.
- Show the upstream error message or incomplete reason. If neither exists, use
  the existing localized request-failure message instead of dumping the transcript.
- Keep each selected key's state and grouped success/failure counts independent.

## September 13: production request parity

The probe and normal `/v1/responses` traffic share `NewCodexAutoExecutor`,
Responses source translation, native provider credential synthesis, configured
alias/prefix/reasoning-suffix resolution, model capability metadata, and payload
rules (including source/alias/header gates). HTTP and supported WebSocket execution
therefore receive the same compatibility repairs automatically.

The management input accepts `codex_config`, the serialized native provider draft.
It overlays a cloned saved provider; explicit legacy per-field overrides still win.
A draft never supplies credentials: only the selected `auth_index`/`api_key` does.
The editor reuses its save builder and managed-field list, including explicit
clears and opaque provider/model options. It strips the key pool from the draft.
The backend synthesizes exactly one temporary auth without registering it in the
live manager. Header-only probes use the same synthesis. A completed probe of an
unchanged saved credential may now recover that model's payment cooldown as
described below.

Normal provider-row probes use the first key only; key-row probes use that key.
The edit sheet's explicit **all keys** button remains available and bounded to four
concurrent requests. Group-level testing uses one key per provider, not every key.
A failed selected key never causes a probe of a sibling key. Live investigation
must pin one key per affected site rather than scanning paid pools.

The Codex browser request has no independent 30-second whole-response deadline.
The executor's provider-configured first-output watch still applies (zero disables
it), and real output stops that watch. Other provider probe timing is unchanged.
The edit sheet fingerprints the complete serialized Codex draft, not a second
list of selected fields. Editing it clears old results and cancels in-flight
Codex probes. Closing the sheet or replacing a test cancels the network request;
late results cannot restore stale success or loading state. Provider-list refresh
and unmount also cancel outstanding probes and stop queued bulk tests.

A completed test can recover a 402/403 model cooldown only for its selected saved
credential and unchanged production settings. Draft-only, failed, partial and
canceled tests never reset production state. The manager compares the pre-probe
snapshot atomically; newer failures, configuration edits and manual disabling
win over old probe success. Other keys/models and usage counters stay unchanged.
This is operator-triggered, single-key recovery, not automatic paid pool scanning
or a shorter default cooldown. Unknown client model names remain errors; consult
`/v1/models` rather than guessing aliases.

A green probe confirms that one request completed for the selected provider/key
and draft, not that arbitrary old conversation history or every upstream request
will succeed. Regressions separately exercise old history, signature recovery,
output commit boundaries and the same production/management upstream fixture.

## Regression and local delivery

In `Cli-Proxy-API-Management-Center`, run:

```sh
bun install --frozen-lockfile
bun test tests/codexProviderProbe.test.ts tests/codexProviderProbeResponses.test.ts tests/codexProviderParity.test.ts
bun run test
bun run lint
bun run type-check
bun run build
```

The response regression passes realistic management envelopes through the actual
normalizer and `simulateCodexProvider`, rather than stubbing an already successful
UI result. Both successful and incomplete/failed transcripts are covered.

For an unpublished local panel build, set
`remote-management.disable-auto-update-panel: true` in the local configuration.
The background GitHub updater runs at each CPA process start and can otherwise
replace the local repair with the last published panel. This existing switch only
controls panel downloads; it does not disable Go, config, or auth hot reload.
Keep production/release update policy unchanged and keep credentials out of Git.

Copy the generated `dist/index.html` to this repository's `static/management.html`
and run `node test/provider_usage_match_test.mjs`. Verify the built page's selected
key test, not just the backend's HTTP status. Follow the existing local delivery
procedure: scoped commits, the mounted panel update, exact runtime commit metadata,
matching served-file hash, and an unchanged container ID. No container recreation
is required for this panel repair. Remote publication and VPS updates follow
an explicit deployment request independently of the local hot-reload checks.

Read `X-CPA-COMMIT` from an authenticated management response, such as
`GET /v0/management/config`. Public model endpoints do not expose this header.

## Review boundaries (September 13)

- Image-source exclusion is covered for both WebSocket executor modes as well as
  HTTP. A regression failed on the missing WebSocket guard before the repair.
- Saved header-only credentials and runtime custom headers survive when no draft
  overrides them. Explicit draft/header clears and selected-key priority remain.
- Codex completion checks stay strict. The legacy xAI raw transport explicitly
  retains its previous statusless-response handling and existing timeout.
- The UI chooses the public model name/prefix, like an API client; only the backend
  resolves that route to the upstream model and attaches model capabilities.
- Required endpoint arguments prevent future signature-recovery callers from
  silently omitting the host-scoped Agent error classification.

Live acceptance also caught a last-hop Responses sanitizer dropping the structured
cooldown fields. The actual HTTP handler now preserves only the concrete CPA
cooldown diagnostic; arbitrary upstream JSON remains sanitized. Streaming and
non-streaming `/v1/responses` route tests cover model, cause and Retry-After.

The live browser check additionally caught delimiter-free executor chunks being
concatenated by the management collector (`data: {...}data: {...}`). Probes now
use the same Responses stream framer as the public HTTP handler. A two-event
upstream fixture asserts valid, separated SSE frames, not just a completion
substring; the UI still rejects malformed or incomplete streams.


## Lifecycle regression

The browser fixture `tests/browser/codexProbeLifecycle.cjs` in the UI repository
reads the local panel and intercepts every connectivity request; it never saves
provider edits or sends an upstream paid probe. Run with `PLAYWRIGHT_MODULE`
pointing to an installed Playwright package. Optional `PROBE_PANEL_HTML` selects a
candidate bundle before deployment. It checks draft edits, late results, request
cancellation, restarting a test and single-key selection.
