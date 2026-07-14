# Native Provider Multi-Key and Request Log Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Synchronize both CPA forks with upstream, add named multi-key native providers, make request-log retention configurable, and remove synchronous log indexing from management requests.

**Architecture:** Backend native provider items remain the unit for shared protocol settings and expand into one Auth per effective key. A long-lived SQLite index manager performs coalesced background scans while HTTP handlers read the last committed snapshot. The management UI adopts grouped native key editing, explicit loading states, and non-overlapping log/stat refreshes.

**Tech Stack:** Go 1.26+, Gin, modernc SQLite, React, TypeScript, Vite, SCSS, Node test runner, Git worktrees.

---

## File Map

### Backend repository: `/home/div/1_Project_dir/AI/CLIProxyAPI`

- `internal/config/config.go` — new retention field, native key-entry type, provider names, grouped key normalization.
- `internal/config/clone.go` — deep-copy grouped key entries.
- `internal/config/parse.go` — load-time defaults and normalization.
- `internal/config/native_provider_keys_test.go` — legacy/grouped decoding and precedence tests.
- `internal/watcher/synthesizer/config.go` — expand native provider groups into Auth records.
- `internal/watcher/synthesizer/config_test.go` — Auth identity, inheritance, provider-name, and protocol-field tests.
- `internal/watcher/diff/config_diff.go` and tests — hot-reload detects name/key-entry/retention changes.
- `internal/api/handlers/management/config_auth_index.go` — attach auth indexes to grouped entries.
- `internal/api/handlers/management/config_lists.go` and tests — CRUD, normalization, deletion, and grouped payload support.
- `internal/api/handlers/management/config_basic.go` and tests — retention management endpoint.
- `internal/api/server.go` — route retention endpoint and initialize/close the index manager at the existing handler lifecycle boundary.
- `internal/api/handlers/management/request_logs_store.go` — additive schema migration, indexes, cutoff-aware queries.
- `internal/api/handlers/management/request_logs_manager.go` — long-lived store and coalesced background synchronization.
- `internal/api/handlers/management/request_logs.go` — snapshot-based list/detail/export/failure handlers.
- `internal/api/handlers/management/api_key_usage.go` — snapshot-only usage reads and configured retention.
- `internal/api/handlers/management/request_logs_test.go`, `request_logs_manager_test.go`, `api_key_usage_test.go` — retention, async index, provider-name, and latency behavior.
- Runtime executor/log metadata files found after upstream merge — propagate Auth display name as `provider_name` without replacing protocol `provider`.
- `config.example.yaml`, `README.md`, `README_CN.md` — configuration, migration, and rollback documentation.
- `test/provider_usage_match_test.mjs` — bundle-level regression tokens.
- `static/management.html` — generated UI artifact only.

### UI repository: `/home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center`

- `src/types/provider.ts` — grouped native key types.
- `src/types/config.ts` — retention setting if represented in the global config model.
- `src/services/api/transformers.ts` — normalize legacy and grouped native payloads.
- `src/services/api/providers.ts` — serialize grouped native providers and preserve protocol fields.
- `src/features/providers/adapters.ts` — provider name, key count, key previews, and selectors.
- `src/features/providers/types.ts` — grouped resource selector/raw types.
- `src/features/providers/sheets/forms/ApiKeyEntriesEditor.tsx` — optional per-key priority alongside proxy and reveal controls.
- `src/features/providers/sheets/forms/BaseProviderForm.tsx` — native group name and key rows while retaining Claude/Codex/Gemini controls.
- `src/features/providers/sheets/ProviderSheet.tsx` and `useProviderWorkbench.ts` — grouped create/edit/delete flow.
- `src/components/providers/hooks/useProviderRecentRequests.ts` — short cache, initial loading, failed-refresh preservation.
- `src/features/requestLogs/RequestLogsPanel.tsx` — non-overlapping refresh, debounced search, stale-row preservation.
- `src/services/api/requestLogs.ts` — sync metadata and configured retention response types.
- Settings page/API service files located after upstream merge — edit `request-log-retention-days`.
- `src/i18n/locales/*.json` — labels and retention descriptions in all existing locale files.
- `test/native_provider_multikey_test.mjs`, `test/provider_recent_requests_test.mjs`, `test/request_logs_refresh_test.mjs` — pure transformer/state regressions.

## Task 1: Synchronize Backend Upstream into the Fork

**Files:** Git history only; resolve conflicts in files reported by Git.

- [ ] **Step 1: Refresh remotes and verify clean tracked state**

```bash
cd /home/div/1_Project_dir/AI/CLIProxyAPI
git status --short
git fetch --prune upstream
git fetch --prune origin
```

Expected: tracked files are clean; existing unrelated untracked files remain untouched.

- [ ] **Step 2: Fast-forward backend `main`**

```bash
git switch main
git merge --ff-only upstream/main
git rev-parse main upstream/main
```

Expected: both hashes are identical; no merge commit exists on `main`.

- [ ] **Step 3: Merge updated `main` into `CPA-fork`**

```bash
git switch CPA-fork
git merge --no-ff main
```

Resolve reported conflicts by retaining upstream changes plus CPA request logs, API-key usage, fork panel source, quota refresh, ZIP export, plugin deletion, and AnyRouter support. Never restore a deleted upstream block blindly; adapt CPA code to the new API shape.

- [ ] **Step 4: Verify preserved backend fork markers**

```bash
rg -n 'request-logs|aXRequestLogs|provider_usage_match' internal test static/management.html
rg -n 'yunqiasen/Cli-Proxy-API-Management-Center' internal config.example.yaml
node test/provider_usage_match_test.mjs
```

Expected: request-log route/test markers and fork UI repository remain; Node regression passes.

- [ ] **Step 5: Run post-merge backend baseline**

```bash
go test ./...
go build -o /tmp/cli-proxy-api-merge-check ./cmd/server
rm /tmp/cli-proxy-api-merge-check
```

Expected: all tests and compile pass. If a failure appears, isolate whether it is an upstream/fork merge regression before feature changes.

- [ ] **Step 6: Commit and push the backend merge checkpoint**

```bash
git status --short
git push origin main
git push origin CPA-fork
```

Expected: `origin/main` and `origin/CPA-fork` contain the reviewed sync checkpoint.

## Task 2: Synchronize UI Upstream into the Fork

**Files:** Git history only; resolve conflicts in files reported by Git.

- [ ] **Step 1: Refresh remotes and verify clean tracked state**

```bash
cd /home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center
git status --short
git fetch --prune upstream
git fetch --prune origin
```

Expected: tracked files are clean.

- [ ] **Step 2: Fast-forward UI `main`**

```bash
git switch main
git merge --ff-only upstream/main
git rev-parse main upstream/main
```

Expected: both hashes are identical.

- [ ] **Step 3: Merge updated `main` into `CPA-UI-fork`**

```bash
git switch CPA-UI-fork
git merge --no-ff main
```

Resolve conflicts while preserving `/logs`, `RequestLogsPanel`, request-log API/styles, provider usage totals/details, AnyRouter request builder, quota refresh-all, ZIP export, and plugin deletion.

- [ ] **Step 4: Verify preserved UI markers**

```bash
rg -n "path:.*\/logs|RequestLogsPanel|request-logs" src
rg -n 'createCodexConnectivityRequest|Session_id|prompt_cache_key' src test
node --experimental-strip-types --test test/codex_connectivity_request_test.mjs
```

Expected: markers exist and connectivity test passes.

- [ ] **Step 5: Run post-merge UI baseline**

```bash
npm run type-check
npm run lint
npm test -- --run
npm run build
```

Expected: type-check, lint, tests, and build pass using the scripts present after merge. If `npm test -- --run` is not a declared script, run the repository's declared test command from `package.json` with equivalent one-shot behavior.

- [ ] **Step 6: Commit and push the UI merge checkpoint**

```bash
git push origin main
git push origin CPA-UI-fork
```

Expected: both UI sync branches are published.

## Task 3: Create Isolated Feature Worktrees

**Files:** Worktree metadata only.

- [ ] **Step 1: Create backend implementation worktree from merged fork**

```bash
cd /home/div/1_Project_dir/AI/CLIProxyAPI
git worktree add /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation -b codex/native-provider-log-implementation CPA-fork
```

Expected: new worktree is on `codex/native-provider-log-implementation`.

- [ ] **Step 2: Create UI implementation worktree from merged fork**

```bash
cd /home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center
git worktree add /home/div/.config/superpowers/worktrees/Cli-Proxy-API-Management-Center/native-provider-log-ui -b codex/native-provider-log-ui CPA-UI-fork
```

Expected: new worktree is on `codex/native-provider-log-ui`.

- [ ] **Step 3: Copy approved design and plan into the backend feature branch**

```bash
cd /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation
git cherry-pick 04c95e3a
git show codex/provider-log-multikey-design:docs/superpowers/plans/2026-07-14-native-provider-multikey-request-log.md > /tmp/native-provider-plan.md
mkdir -p docs/superpowers/plans
cp /tmp/native-provider-plan.md docs/superpowers/plans/2026-07-14-native-provider-multikey-request-log.md
git add -f docs/superpowers/plans/2026-07-14-native-provider-multikey-request-log.md
git commit -m "docs: plan native provider and log optimization"
```

Expected: implementation branch contains the approved spec and this plan.

## Task 4: Add Backward-Compatible Native Provider Configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/clone.go`
- Modify: `internal/config/parse.go`
- Create: `internal/config/native_provider_keys_test.go`

- [ ] **Step 1: Write failing config tests**

Create table-driven tests covering this YAML:

```yaml
request-log-retention-days: 0
claude-api-key:
  - name: relay-a
    api-key: legacy-key
    priority: 8
    proxy-url: http://provider-proxy
    api-key-entries:
      - api-key: key-a
        priority: 0
      - api-key: key-b
        priority: 20
        proxy-url: http://key-proxy
    rebuild-mid-system-message: true
    experimental-cch-signing: true
codex-api-key:
  - api-key: legacy-codex
    websockets: true
gemini-api-key:
  - name: gemini-relay
    api-key-entries:
      - api-key: gemini-a
```

Assert retention `0` survives, names decode, explicit priority zero is distinguishable from omission, grouped keys deep-clone, and legacy fields remain available.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/config -run 'TestNativeProvider|TestRequestLogRetention' -count=1
```

Expected: compile or assertion failure because new fields/types are absent.

- [ ] **Step 3: Implement config types and effective-key helper**

Add `RequestLogRetentionDays int` to `Config`, default it to `7`, normalize negatives to `7`, and add:

```go
type NativeAPIKeyEntry struct {
    APIKey   string `yaml:"api-key" json:"api-key"`
    Priority *int   `yaml:"priority,omitempty" json:"priority,omitempty"`
    ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

type EffectiveNativeAPIKey struct {
    APIKey   string
    Priority int
    ProxyURL string
    Index    int
}
```

Add `Name` and `APIKeyEntries` to Claude, Codex, and Gemini structs. Implement one helper that trims keys, suppresses duplicates, prefers non-empty grouped entries over the legacy key, inherits provider priority/proxy, and honors explicit priority zero.

- [ ] **Step 4: Update cloning and sanitizers**

Deep-copy each `APIKeyEntries` slice. Sanitizers must keep a provider item when it has either a valid grouped key or a valid legacy key, trim names/URLs, and remove duplicate grouped secrets without rewriting legacy-only items.

- [ ] **Step 5: Run config tests**

```bash
gofmt -w internal/config/config.go internal/config/clone.go internal/config/parse.go internal/config/native_provider_keys_test.go
go test ./internal/config -run 'TestNativeProvider|TestRequestLogRetention|TestClone' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit config model**

```bash
git add internal/config
git commit -m "feat(config): add grouped native provider keys"
```

## Task 5: Expand Native Provider Groups into Runtime Auths

**Files:**
- Modify: `internal/watcher/synthesizer/config.go`
- Modify: `internal/watcher/synthesizer/config_test.go`
- Modify: `internal/watcher/diff/config_diff.go`
- Modify: `internal/watcher/diff/config_diff_test.go`

- [ ] **Step 1: Write failing synthesizer tests**

Construct Claude, Codex, and Gemini groups with two keys. Assert two Auths per group, native `Provider`, configured `Label`, `Attributes["provider_name"]`, inherited and overridden priorities/proxies, shared headers/model hash/prefix, Claude cloak/CCH/rebuild fields, Codex websockets, and duplicate suppression.

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/watcher/synthesizer -run 'TestConfigSynthesizer.*Native|TestConfigSynthesizer.*Grouped' -count=1
```

Expected: FAIL because grouped entries are not synthesized.

- [ ] **Step 3: Implement shared native Auth expansion**

Refactor each native loop to call the config effective-key helper. Preserve protocol-specific attribute assembly, then apply each key's secret, priority, proxy, deterministic ID, display label, and provider name. Keep `Provider` unchanged.

- [ ] **Step 4: Add hot-reload diff coverage**

Add tests proving changes to provider `name`, grouped key order/content, key priority/proxy, and retention are detected. Extend diff output without printing secrets.

- [ ] **Step 5: Run watcher tests**

```bash
gofmt -w internal/watcher/synthesizer/config.go internal/watcher/synthesizer/config_test.go internal/watcher/diff/config_diff.go internal/watcher/diff/config_diff_test.go
go test ./internal/watcher/synthesizer ./internal/watcher/diff -count=1
```

Expected: PASS and no key values appear in diff messages.

- [ ] **Step 6: Commit runtime expansion**

```bash
git add internal/watcher
git commit -m "feat(auth): synthesize native provider key groups"
```

## Task 6: Extend Management Config CRUD and Retention API

**Files:**
- Modify: `internal/api/handlers/management/config_auth_index.go`
- Modify: `internal/api/handlers/management/config_lists.go`
- Modify: `internal/api/handlers/management/config_basic.go`
- Modify: `internal/api/server.go`
- Create: `internal/api/handlers/management/config_native_multikey_test.go`
- Create or modify: `internal/api/handlers/management/config_basic_test.go`

- [ ] **Step 1: Write failing management tests**

Test GET/PUT/PATCH for grouped Claude/Codex/Gemini payloads, grouped key auth-index attachment, deletion by provider index/name rather than a nested secret, untouched legacy payload preservation, duplicate rejection/normalization, retention GET/PUT accepting `0` and rejecting negatives.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/api/handlers/management -run 'Test.*Native.*MultiKey|Test.*RequestLogRetention' -count=1
```

Expected: FAIL with missing grouped CRUD/index behavior.

- [ ] **Step 3: Implement grouped CRUD**

Normalize nested entries through the config helper. Prefer stable provider index for patch/delete. Keep legacy key/base-URL matching for old clients. Attach `auth-index` to each returned nested key using the synthesized Auth lookup while preserving top-level legacy auth index.

- [ ] **Step 4: Implement retention endpoint**

Add GET/PUT management handlers for `request-log-retention-days`, validate integers `>= 0`, persist through the existing config save path, and register routes beside other basic config endpoints.

- [ ] **Step 5: Run management tests**

```bash
gofmt -w internal/api/handlers/management/config_auth_index.go internal/api/handlers/management/config_lists.go internal/api/handlers/management/config_basic.go internal/api/server.go internal/api/handlers/management/config_native_multikey_test.go internal/api/handlers/management/config_basic_test.go
go test ./internal/api/handlers/management -run 'Test.*Native.*MultiKey|Test.*RequestLogRetention|TestDelete.*Key' -count=1
```

Expected: PASS, including legacy deletion regressions.

- [ ] **Step 6: Commit management config support**

```bash
git add internal/api
git commit -m "feat(management): manage grouped native provider keys"
```

## Task 7: Add Provider Display Name to Structured Logs

**Files:**
- Modify: runtime log metadata files identified with `rg -n 'AuthLabel|UpstreamMetadata|Provider:' internal/runtime internal/logging`
- Modify: `internal/api/handlers/management/request_logs.go`
- Modify: `internal/api/handlers/management/request_logs_store.go`
- Modify: `internal/api/handlers/management/request_logs_test.go`

- [ ] **Step 1: Write failing provider-name tests**

Add parser/store tests for metadata containing protocol `provider=claude` and display `provider_name=relay-a`. Assert list/detail/export/failure responses show `relay-a`, historical rows lacking the field show `claude`, and usage grouping still uses Auth ID/protocol identity.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/api/handlers/management -run 'TestRequestLog.*ProviderName' -count=1
```

Expected: FAIL because schema/parser lacks the display field.

- [ ] **Step 3: Propagate and store display name**

Extend runtime upstream metadata with a display-name field sourced from `Attributes["provider_name"]`, falling back to Auth label only when it is not the generic native label. Parse it into `requestLogListItem`, store it in additive `provider_name`, and expose fallback with `COALESCE(NULLIF(TRIM(provider_name), ''), provider)`.

- [ ] **Step 4: Add schema migration and indexes**

Create `provider_name` via the existing column migration and add:

```sql
CREATE INDEX IF NOT EXISTS idx_request_log_entries_auth_time
ON request_log_entries(auth_id, timestamp_unix DESC);
CREATE INDEX IF NOT EXISTS idx_request_log_entries_provider_time
ON request_log_entries(provider, timestamp_unix DESC);
```

- [ ] **Step 5: Run tests**

```bash
gofmt -w internal/runtime internal/api/handlers/management/request_logs.go internal/api/handlers/management/request_logs_store.go internal/api/handlers/management/request_logs_test.go
go test ./internal/api/handlers/management ./internal/runtime/executor/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit provider-name logging**

```bash
git add internal/runtime internal/api/handlers/management
git commit -m "feat(logs): record native provider display names"
```

## Task 8: Introduce the Background Request Log Index Manager

**Files:**
- Create: `internal/api/handlers/management/request_logs_manager.go`
- Create: `internal/api/handlers/management/request_logs_manager_test.go`
- Modify: `internal/api/handlers/management/request_logs_store.go`
- Modify: management handler construction/lifecycle files found after merge.

- [ ] **Step 1: Write failing manager tests**

Use a temporary log directory and injectable scan hook. Test initial scan, `TriggerSync` coalescing, reads while a scan is blocked, bounded batch upserts, deletion detection, retention `0`, positive pruning, last-error preservation, and clean `Close`.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/api/handlers/management -run 'TestRequestLogIndexManager' -count=1
```

Expected: compile failure because manager is absent.

- [ ] **Step 3: Implement manager lifecycle**

Implement a manager with one long-lived store, context cancellation, one buffered trigger channel, atomic/mutex-protected status, one writer goroutine, and read methods. `TriggerSync` must use a non-blocking send so concurrent callers coalesce.

- [ ] **Step 4: Implement incremental batches and deletion detection**

Compare compact DB sync states with directory metadata, parse changed files only, upsert in bounded transactions, remove DB IDs absent from a completed directory snapshot, and prune only when retention is greater than zero.

- [ ] **Step 5: Run race-aware manager tests**

```bash
gofmt -w internal/api/handlers/management/request_logs_manager.go internal/api/handlers/management/request_logs_manager_test.go internal/api/handlers/management/request_logs_store.go
go test -race ./internal/api/handlers/management -run 'TestRequestLogIndexManager' -count=1
```

Expected: PASS without races or goroutine leaks.

- [ ] **Step 6: Commit manager**

```bash
git add internal/api/handlers/management
git commit -m "refactor(logs): add background request log index"
```

## Task 9: Make Request Log and Usage Handlers Snapshot-Based

**Files:**
- Modify: `internal/api/handlers/management/request_logs.go`
- Modify: `internal/api/handlers/management/api_key_usage.go`
- Modify: `internal/api/handlers/management/request_logs_test.go`
- Modify: `internal/api/handlers/management/api_key_usage_test.go`

- [ ] **Step 1: Write failing non-blocking handler tests**

Block the manager scan hook and issue list, failure-details, export, and usage requests. Assert they complete from the previous snapshot, return sync metadata, and do not call full sync. Test detail miss performs only one-file backfill.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/api/handlers/management -run 'Test(RequestLogs|GetAPIKeyUsage).*Snapshot|TestRequestLogDetail.*Backfill' -count=1
```

Expected: FAIL because handlers still call `syncRequestLogStore`.

- [ ] **Step 3: Replace inline synchronization**

Remove request-path calls to full sync and the global scan mutex. Read through manager methods, trigger background sync without waiting, return `syncing`, `last_synced_at`, and `last_sync_error`, and preserve file fallback when SQLite startup failed.

- [ ] **Step 4: Centralize retention cutoff**

Use one helper that returns no SQL cutoff when configured retention is `0`; positive values produce a Unix cutoff. Apply it to usage/detail queries and response `retention_days`.

- [ ] **Step 5: Run handler tests and benchmark timing assertion**

```bash
gofmt -w internal/api/handlers/management/request_logs.go internal/api/handlers/management/api_key_usage.go internal/api/handlers/management/request_logs_test.go internal/api/handlers/management/api_key_usage_test.go
go test -race ./internal/api/handlers/management -count=1
```

Expected: PASS; blocked scan tests prove handlers do not wait.

- [ ] **Step 6: Commit snapshot handlers**

```bash
git add internal/api/handlers/management
git commit -m "perf(logs): serve management reads from indexed snapshots"
```

## Task 10: Add UI Grouped Native Provider Data Contracts

**Files:**
- Modify: `src/types/provider.ts`
- Modify: `src/services/api/transformers.ts`
- Modify: `src/services/api/providers.ts`
- Modify: `src/features/providers/types.ts`
- Modify: `src/features/providers/adapters.ts`
- Create: `test/native_provider_multikey_test.mjs`

- [ ] **Step 1: Write failing transformer/adapter tests**

Cover legacy single key and grouped payloads. Assert name, nested entries, optional priority zero, proxy, protocol-specific fields, key count, identifiers, search terms, and serialized kebab-case payload.

- [ ] **Step 2: Run test and confirm failure**

```bash
node --experimental-strip-types --test test/native_provider_multikey_test.mjs
```

Expected: FAIL because native grouped fields are dropped.

- [ ] **Step 3: Extend TypeScript contracts**

Add `NativeApiKeyEntry` with `apiKey`, optional `priority`, `proxyUrl`, and `authIndex`. Add optional `name` and `apiKeyEntries` to native configs while keeping legacy `apiKey`, `priority`, and `proxyUrl`.

- [ ] **Step 4: Normalize and serialize grouped native providers**

Transform both forms, preserve explicit zero, retain Claude/Codex/Gemini fields, serialize nested entries, and avoid writing empty grouped arrays over legacy data.

- [ ] **Step 5: Update adapters**

Use provider name as identifier, count grouped keys or one legacy key, include all masked key previews/search tokens, and keep selector identity based on provider index/name rather than a nested secret.

- [ ] **Step 6: Run tests and type-check**

```bash
node --experimental-strip-types --test test/native_provider_multikey_test.mjs
npm run type-check
```

Expected: PASS.

- [ ] **Step 7: Commit UI data contracts**

```bash
git add src/types src/services/api src/features/providers test/native_provider_multikey_test.mjs
git commit -m "feat(provider): support grouped native provider payloads"
```

## Task 11: Build Native Multi-Key Provider Forms

**Files:**
- Modify: `src/features/providers/sheets/forms/ApiKeyEntriesEditor.tsx`
- Modify: `src/features/providers/sheets/forms/BaseProviderForm.tsx`
- Modify: `src/features/providers/sheets/ProviderSheet.tsx`
- Modify: `src/features/providers/useProviderWorkbench.ts`
- Modify: `src/features/providers/components/ProviderResourceTable.tsx`
- Modify: locale JSON files under `src/i18n/locales/`
- Extend: `test/native_provider_multikey_test.mjs`

- [ ] **Step 1: Add failing pure form conversion tests**

Extract/export pure conversion helpers if current form logic is inline. Test untouched legacy save remains legacy; adding a name or second key emits grouped entries; duplicate secrets fail validation; Claude cloak/CCH/rebuild, Codex websocket, and Gemini fields survive round-trip.

- [ ] **Step 2: Run test and confirm failure**

```bash
node --experimental-strip-types --test test/native_provider_multikey_test.mjs
```

Expected: FAIL on missing conversion behavior.

- [ ] **Step 3: Extend key editor and native forms**

Add provider name and repeatable key rows with secret reveal, priority, and proxy. Keep shared base URL/models/headers outside rows. Render protocol-specific controls exactly where they currently exist.

- [ ] **Step 4: Update save/delete and table behavior**

Save grouped payloads by provider index, retain legacy save when untouched, display name and key count, and delete the provider group rather than one nested key unless the user edits and removes that row.

- [ ] **Step 5: Add locale strings**

Add concise translations for provider name, key priority, inherited priority/proxy hints, duplicate key validation, and multi-key count to every locale file using the existing English fallback style where a maintained translation is unavailable.

- [ ] **Step 6: Run focused validation**

```bash
node --experimental-strip-types --test test/native_provider_multikey_test.mjs
npm run type-check
npx eslint src/types/provider.ts src/services/api/transformers.ts src/services/api/providers.ts src/features/providers
```

Expected: PASS.

- [ ] **Step 7: Commit forms**

```bash
git add src/features/providers src/i18n/locales test/native_provider_multikey_test.mjs
git commit -m "feat(provider): edit named native multi-key groups"
```

## Task 12: Fix Provider Usage Loading and Request Log Refresh

**Files:**
- Modify: `src/components/providers/hooks/useProviderRecentRequests.ts`
- Modify: `src/features/requestLogs/RequestLogsPanel.tsx`
- Modify: `src/services/api/requestLogs.ts`
- Create: `src/components/providers/hooks/providerRecentRequestsCache.ts`
- Create: `src/features/requestLogs/requestLogRefreshState.ts`
- Create: `test/provider_recent_requests_test.mjs`
- Create: `test/request_logs_refresh_test.mjs`

- [ ] **Step 1: Write failing cache-state tests**

Test initial loading is distinguishable from loaded zero, in-flight requests deduplicate, failure preserves prior non-empty data, successful empty response clears data, and stale time is shorter than the previous 240 seconds.

- [ ] **Step 2: Write failing request-log refresh tests**

Test interval ticks do not overlap, search debounce emits one query, timeout preserves rows, and sync metadata/error updates independently from row data.

- [ ] **Step 3: Run tests and confirm failure**

```bash
node --experimental-strip-types --test test/provider_recent_requests_test.mjs test/request_logs_refresh_test.mjs
```

Expected: FAIL because state helpers do not exist.

- [ ] **Step 4: Implement provider usage state**

Move cache transitions into a pure helper. Use an explicit `hasLoaded` flag, a short stale window aligned with backend sync, one in-flight promise, and generation/request IDs so a failed old request cannot overwrite a newer success.

- [ ] **Step 5: Implement request-log refresh state**

Use an in-flight guard or abort controller, debounce search, preserve rows on errors, and display backend `syncing`, `last_synced_at`, `last_sync_error`, and `retention_days` without blocking list rendering.

- [ ] **Step 6: Run tests and validation**

```bash
node --experimental-strip-types --test test/provider_recent_requests_test.mjs test/request_logs_refresh_test.mjs
npm run type-check
npx eslint src/components/providers/hooks src/features/requestLogs src/services/api/requestLogs.ts
```

Expected: PASS.

- [ ] **Step 7: Commit refresh fixes**

```bash
git add src/components/providers/hooks src/features/requestLogs src/services/api/requestLogs.ts test
git commit -m "fix(ui): stabilize provider usage and log refresh"
```

## Task 13: Add Retention Setting to the Management UI

**Files:** settings page, config types, and API service files found with `rg -n 'logs-max-total-size|request-log' src` after upstream merge; locale files.

- [ ] **Step 1: Add a failing serialization test**

Extend an existing settings API test or create `test/request_log_retention_setting_test.mjs`. Assert `0`, `7`, and a custom positive value serialize unchanged while negative and non-integer values are rejected.

- [ ] **Step 2: Run test and confirm failure**

```bash
node --experimental-strip-types --test test/request_log_retention_setting_test.mjs
```

Expected: FAIL because the setting is absent.

- [ ] **Step 3: Implement API and settings control**

Add GET/PUT calls for `/request-log-retention-days`, render a numeric input with minimum zero beside request-log settings, and describe `0` as permanent structured retention while raw rotation remains independent.

- [ ] **Step 4: Run validation**

```bash
node --experimental-strip-types --test test/request_log_retention_setting_test.mjs
npm run type-check
npx eslint src
```

Expected: PASS.

- [ ] **Step 5: Commit retention UI**

```bash
git add src test/request_log_retention_setting_test.mjs
git commit -m "feat(settings): configure request log retention"
```

## Task 14: Complete Backend Regression and Documentation

**Files:**
- Modify: `config.example.yaml`
- Modify: `README.md`
- Modify: `README_CN.md`
- Modify: focused docs discovered by `find docs -maxdepth 2 -type f` after merge.

- [ ] **Step 1: Run full backend formatting and tests**

```bash
cd /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation
gofmt -w internal cmd sdk test
go test ./...
go build -o /tmp/cli-proxy-api-feature-check ./cmd/server
rm /tmp/cli-proxy-api-feature-check
node test/provider_usage_match_test.mjs
git diff --check
```

Expected: all commands pass.

- [ ] **Step 2: Update configuration examples**

Document legacy single-key and named grouped examples for Claude, Codex, and Gemini; key-level inheritance; protocol-specific fields; retention default `7`; retention `0`; and raw-log rotation independence.

- [ ] **Step 3: Add migration and rollback example**

Show deterministic flattening of one grouped provider into separate legacy provider items for rollback to an older binary. Keep `README.md` and `README_CN.md` structurally aligned.

- [ ] **Step 4: Verify documentation consistency**

```bash
rg -n 'request-log-retention-days|api-key-entries|Claude|Codex|Gemini' config.example.yaml README.md README_CN.md docs
git diff --check
```

Expected: configuration semantics agree across files.

- [ ] **Step 5: Commit backend docs**

```bash
git add config.example.yaml README.md README_CN.md docs
git commit -m "docs: document native key groups and log retention"
```

## Task 15: Build UI Artifact and Run Cross-Repository Regression

**Files:**
- Generated: UI `dist/index.html`
- Modify: backend `static/management.html`

- [ ] **Step 1: Run complete UI verification**

```bash
cd /home/div/.config/superpowers/worktrees/Cli-Proxy-API-Management-Center/native-provider-log-ui
node --experimental-strip-types --test test/*.mjs
npm run type-check
npm run lint
npm run build
git diff --check
```

Expected: all tests, type-check, lint, and build pass.

- [ ] **Step 2: Copy the generated single-file panel**

```bash
cp dist/index.html /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation/static/management.html
```

Expected: backend artifact changes and UI source remains the source of truth.

- [ ] **Step 3: Run bundle-level backend regression**

```bash
cd /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation
node test/provider_usage_match_test.mjs
rg -n 'request-logs|/logs' static/management.html
go test ./...
go build -o /tmp/cli-proxy-api-final-check ./cmd/server
rm /tmp/cli-proxy-api-final-check
git diff --check
```

Expected: request-log markers remain and backend verification passes.

- [ ] **Step 4: Commit UI source and backend artifact**

```bash
cd /home/div/.config/superpowers/worktrees/Cli-Proxy-API-Management-Center/native-provider-log-ui
git add src test
git commit -m "feat(ui): deliver native provider and log management"

cd /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation
git add static/management.html test/provider_usage_match_test.mjs
git commit -m "chore(ui): embed provider log management panel"
```

Expected: source and generated artifact are separate commits in their owning repositories.

## Task 16: Live Verification, Review, and Fork Integration

**Files:** no source changes unless verification exposes a regression.

- [ ] **Step 1: Verify served panel only if local service is already running**

```bash
cd /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation
if curl -fsS --max-time 3 http://100.126.43.55:8317/management.html -o /tmp/live-management.html; then
  sha256sum /tmp/live-management.html static/management.html
fi
```

Expected: if served from this build, hashes match; otherwise record that deployment was not changed during development.

- [ ] **Step 2: Run final status and history audit**

```bash
git status --short
git log --oneline CPA-fork..HEAD

git -C /home/div/.config/superpowers/worktrees/Cli-Proxy-API-Management-Center/native-provider-log-ui status --short
git -C /home/div/.config/superpowers/worktrees/Cli-Proxy-API-Management-Center/native-provider-log-ui log --oneline CPA-UI-fork..HEAD
```

Expected: no unintended files and commits are scoped.

- [ ] **Step 3: Request code review**

Use `superpowers:requesting-code-review` against both feature branches. Resolve findings with focused tests and rerun affected suites.

- [ ] **Step 4: Re-run completion gate after review fixes**

```bash
cd /home/div/.config/superpowers/worktrees/CLIProxyAPI/native-provider-log-implementation
go test ./...
go build -o /tmp/cli-proxy-api-reviewed ./cmd/server && rm /tmp/cli-proxy-api-reviewed
node test/provider_usage_match_test.mjs
git diff --check

cd /home/div/.config/superpowers/worktrees/Cli-Proxy-API-Management-Center/native-provider-log-ui
node --experimental-strip-types --test test/*.mjs
npm run type-check
npm run lint
npm run build
git diff --check
```

Expected: all checks pass after review.

- [ ] **Step 5: Integrate into fork branches**

Use `superpowers:finishing-a-development-branch`. Merge backend feature branch into `CPA-fork` and UI feature branch into `CPA-UI-fork`, then push only to `origin`:

```bash
git push origin CPA-fork
git -C /home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center push origin CPA-UI-fork
```

Expected: fork branches contain reviewed feature commits; no fork-only commit is pushed to upstream.
