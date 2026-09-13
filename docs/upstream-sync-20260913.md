# Upstream compatibility batch — 2026-09-13

## Scope

`main` follows upstream `ac02da6c` (v7.2.159). This delivery selectively integrates
four reviewed patches into CPA-fork; it does not merge all upstream changes.

| Upstream commit | Integrated behavior |
| --- | --- |
| `db143aeb` | Deterministic, collision-resistant Codex input item IDs. |
| `09471dd9` | One model's quota cooldown does not block unrelated models on the credential. |
| `48e5e9e0` | Cap auth-refresh timer sleep to recover after suspension. |
| `177c7619` | Clear Antigravity test maps in place instead of replacing sync.Map values. |

Fork-specific test adaptation also clears the remaining Home cooldown test map
in place. The Home balance test resets its SDK credit hint at setup/cleanup;
otherwise repeated runs retain the first run's hint and skip the expected KV read.
These changes are test-only and leave production credit handling unchanged.

## UI companion

The panel comes from CPA-UI-fork's selective update through upstream `f4b3043`.
Its `docs/upstream-sync-20260913.md` lists eleven integrated commits: model cache,
YAML merge/payload/default/integer fixes, log race handling/error viewer, sidebar
shortcut and drawer/segmented-control styles. Preserve the generated panel's
fork markers and all request/probe parity behavior.

## Verification

- Red/green regression checks for input IDs, model cooldown isolation and refresh
  timer behavior; full `go test ./...` and server build.
- Race tests for auth/ID helpers and ten repeated Antigravity credit/cooldown runs.
- UI: 696 tests, lint/type check/build, single-key probe lifecycle and log/UI browser checks.
- Panel: `node test/provider_usage_match_test.mjs` plus served/workspace SHA-256 equality.
- Delivery: fast-forward the primary fork checkout, let its existing watcher build,
  verify a clean exact `X-CPA-COMMIT`, API/panel availability and unchanged container ID.
  No production config/key changes, paid credential enumeration or VPS deployment.

## Review

One parallel Standards/Spec review completed. The suggested cooldown timestamp
issue is not present: availabilityBlock returns immediately when unavailable and
quotaExceeded are both false. SDK hint writes use sync.Map.Store; refresh timer
expiry always calls resetTimer again. Credential-wide quota and existing-ID
collision behavior remain covered by the existing and imported tests. No broad
selector rewrite or extra provider behavior was introduced.

## Remaining investigation

The first full test run intermittently reported empty auth/provider metadata in
`TestResponsesWebsocketKeepsPartialOutputOnBudgetFailure`, while preserving the
partial output and explicit 402 error. Twenty focused runs and thirty unmodified
baseline runs passed; full reruns passed. Root cause remains unconfirmed and this
batch does not claim to fix that intermittent log-metadata problem.

The separately audited wsrelay terminal-queue and WebSocket integration changes,
watcher revisions, 401 reset behavior and plugin protocol changes remain deferred.
