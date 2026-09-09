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
  response. If a status is present, it must be `completed`; an error must be absent.
- Lifecycle events, partial text and `[DONE]` alone do not establish completion.
- An explicit `error`, `response.failed`, `response.incomplete`, or
  `response.cancelled` event wins over a completed event. Malformed event data also
  remains a failure.
- Accept normal SSE framing: named events, multiline data, comments, CRLF,
  an initial BOM, empty keepalives and a complete last frame without a delimiter.
- Show the upstream error message or incomplete reason. If neither exists, use
  the existing localized request-failure message instead of dumping the transcript.
- Keep each selected key's state and grouped success/failure counts independent.

This interpretation is scoped to Codex connectivity tests. It does not alter
provider configuration, selected credentials, model aliases, request headers,
executor retry/timeout behavior, production Responses forwarding, or raw logs.
A green management probe does not establish that all production streams are stable.

## Regression and local delivery

In `Cli-Proxy-API-Management-Center`, run:

```sh
bun install --frozen-lockfile
bun test tests/codexProviderProbe.test.ts tests/codexProviderProbeResponses.test.ts
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
or remote publication is required for this panel repair.

Read `X-CPA-COMMIT` from an authenticated management response, such as
`GET /v0/management/config`. Public model endpoints do not expose this header.
