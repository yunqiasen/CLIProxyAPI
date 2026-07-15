# Native Provider Multi-Key and Request Log Optimization Design

## Objective

Synchronize both fork repositories with their upstream `main` branches, preserve all CPA customizations, and add three related improvements:

1. Native Claude, Codex, and Gemini providers gain a provider name and multiple API keys without losing protocol-specific settings or legacy single-key compatibility.
2. Request logs use configurable retention and a non-blocking incremental SQLite index.
3. Provider usage totals load reliably and request logs display the configured provider name.

## Repository and Branch Boundaries

The backend repository is `/home/div/1_Project_dir/AI/CLIProxyAPI`. Its upstream sync line is `main`; fork development remains on `CPA-fork` and feature branches created from it.

The management UI repository is `/home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center`. Its upstream sync line is `main`; fork development remains on `CPA-UI-fork` and feature branches created from it.

For each repository:

1. Fetch `upstream` and `origin`.
2. Fast-forward local `main` to `upstream/main` without adding fork-only commits.
3. Merge updated `main` into the fork branch.
4. Resolve conflicts by retaining upstream fixes and all documented CPA behavior.
5. Create the implementation branch from the merged fork branch.

Backend conflicts must preserve structured request logs, persistent API-key usage, quota behavior, ZIP credential export, plugin deletion support, and the fork management-panel source. UI conflicts must preserve `/logs`, request-log services/styles, AnyRouter Codex connectivity compatibility, provider statistics, quota behavior, ZIP export, and plugin deletion.

## Configuration Model

### Shared key entry

Add a native key-entry type used by Claude, Codex, and Gemini:

```go
type NativeAPIKeyEntry struct {
    APIKey  string `yaml:"api-key" json:"api-key"`
    Priority int   `yaml:"priority,omitempty" json:"priority,omitempty"`
    ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}
```

Each existing native provider item gains:

```go
Name          string              `yaml:"name,omitempty" json:"name,omitempty"`
APIKeyEntries []NativeAPIKeyEntry `yaml:"api-key-entries,omitempty" json:"api-key-entries,omitempty"`
```

The existing top-level `api-key`, `priority`, and `proxy-url` fields remain supported and documented as the legacy single-key form. They are not removed or silently rewritten during config loading.

Example grouped Claude provider:

```yaml
claude-api-key:
  - name: "Claude relay A"
    base-url: "https://example.com"
    priority: 10
    api-key-entries:
      - api-key: "KEY_1"
        priority: 20
        proxy-url: "http://proxy-a"
      - api-key: "KEY_2"
        priority: 10
    models: []
    headers: {}
    prefix: ""
    disable-cooling: false
    rebuild-mid-system-message: false
    cloak: {}
    experimental-cch-signing: false
```

### Compatibility and precedence

The effective keys for one native provider item are resolved as follows:

- If `api-key-entries` contains at least one non-empty key, each valid entry produces one runtime Auth.
- Otherwise, a non-empty legacy top-level `api-key` produces one runtime Auth.
- Empty key entries are ignored.
- A key-entry `priority` or `proxy-url` overrides the provider-level value when explicitly set.
- Provider-level values remain defaults for grouped entries.
- Because integer zero is a valid explicit priority, key-entry priority uses presence-aware decoding internally, such as `*int`, while the JSON/YAML value remains a number. An omitted value inherits; an explicit `0` overrides a non-zero provider default.
- An explicitly empty key-entry proxy URL inherits the provider proxy. Clearing an inherited proxy is outside this change; users can leave the provider proxy empty and set proxies per key.
- If both legacy `api-key` and non-empty `api-key-entries` are present, grouped entries take precedence. The legacy key is retained in serialized configuration but does not create a duplicate Auth.

The provider item owns all shared settings: `name`, `base-url`, `prefix`, models, excluded models, headers, cooling behavior, and provider-specific options. Key entries own only the secret, priority override, and proxy override.

### Provider-specific fields

No existing provider behavior moves into the shared key entry:

- Claude retains `rebuild-mid-system-message`, `cloak`, and `experimental-cch-signing`.
- Codex retains `websockets`.
- Gemini retains its native model mappings, exclusions, headers, and request path.
- Existing provider-level `priority` and `proxy-url` continue to act as defaults and preserve old files.

The same generic grouping mechanism may be reused for the native Gemini Interactions list only if it already shares `GeminiKey` at merge time; its protocol identity and UI category remain unchanged.

## Runtime Auth Synthesis and Identity

The synthesizer expands each provider item into one Auth per effective key. Every generated Auth keeps the native execution provider (`claude`, `codex`, `gemini`, or the existing interactions identifier), so routing, translators, retries, cooling, and failover continue to use current code paths.

Each Auth receives:

- `Provider`: native protocol identifier.
- `Label`: configured provider name when non-empty; otherwise the existing native label.
- `Attributes["provider_name"]`: configured name when non-empty.
- `Attributes["api_key"]`: the effective key.
- `Attributes["priority"]`: effective key priority.
- `ProxyURL`: effective key proxy.
- Shared base URL, headers, model hash, prefix, exclusions, cooling metadata, and provider-specific attributes.

Auth IDs remain deterministic and unique for each effective key. The ID generator input includes the provider kind, secret, base URL, and stable key-entry position or equivalent discriminator supported by the existing generator. Reordering entries may change auth-index presentation but must not merge two distinct keys into one Auth.

The UI's secret-reveal behavior continues to use `auth-index`. Management config responses must attach an auth index to every grouped key entry, while retaining the existing top-level auth index for legacy items.

## Provider Name in Request Logs

The execution protocol and display name are separate fields:

- Protocol fields continue to identify `claude`, `codex`, and `gemini` for routing and grouping logic.
- The configured name travels through Auth label/attributes into upstream log metadata.
- Structured request-log parsing stores the configured display name in a dedicated `provider_name` column while preserving the protocol provider column.
- Existing rows without `provider_name` fall back to their stored provider protocol.
- Request-log list, detail, export, failure summaries, and provider usage responses expose the display name consistently.

Provider usage lookup remains keyed by protocol plus Auth identity, not by display name alone. This avoids collisions when two provider groups share a name and ensures renaming a group does not merge historical key counters.

## Request Log Retention

Add top-level configuration:

```yaml
# Number of days retained in the structured request-log index.
# 0 keeps request logs indefinitely.
request-log-retention-days: 7
```

Rules:

- Default is `7` when omitted.
- `0` disables age-based pruning and allows all matching raw request logs to be indexed.
- Negative values normalize to `7` during config loading.
- Changing the value is hot-reloadable with the rest of configuration.
- The management config API exposes the field and the UI settings page permits any integer greater than or equal to zero.
- API responses continue returning `retention_days`, now using the effective configured value.

This setting governs structured request-log visibility and SQLite pruning. Existing raw-log size and file-count controls remain independent; setting retention to `0` does not override raw log rotation or disk-size limits.

## Incremental Request Log Index

### Manager lifecycle

Replace request-scoped open/sync/close behavior with a long-lived request-log index manager owned by the management handler or server lifecycle. It owns one SQLite store, a bounded synchronization worker, the latest sync status, and cancellation on shutdown.

The manager API has focused operations:

- `Start` performs schema setup and queues an initial scan.
- `TriggerSync` coalesces repeated requests into one pending scan.
- `List`, `Usage`, `FailureDetails`, and `Export` read the latest committed SQLite snapshot immediately.
- `Detail` reads SQLite first and, on a miss, locates and indexes only the requested raw file before retrying.
- `Close` stops workers and closes the database.

Only one writer sync runs at a time. Readers use SQLite WAL behavior and do not hold the current global mutex while scanning files or parsing logs.

### Incremental scan

A scan:

1. Reads directory metadata for matching request-log files.
2. Compares filename, size, and modification time with compact sync-state rows.
3. Parses and upserts only new or changed files.
4. Removes indexed rows whose raw files disappeared when that deletion is observed.
5. Prunes rows older than the configured cutoff when retention is greater than zero.
6. Commits changes in bounded batches so a large first import does not monopolize the database.

The manager queues a sync after startup, after request-log endpoints observe stale status, and on a modest periodic interval. HTTP handlers never wait for a full directory scan. They may return the last completed snapshot with sync metadata such as `syncing`, `last_synced_at`, and `last_sync_error`.

If SQLite cannot be opened, existing file-based list/detail fallback remains available. A background sync error does not erase the last good index and does not turn an otherwise readable list into an HTTP timeout.

### Schema and query changes

Add migrations and indexes without deleting the existing database:

- `provider_name` for the configured display name.
- A composite index on `(auth_id, timestamp_unix DESC)` for usage aggregation.
- A composite index on `(provider, timestamp_unix DESC)` for provider filtering.
- Existing timestamp, model, status, and success indexes remain.

Retention `0` removes cutoff predicates from usage queries and skips pruning. Positive retention uses one shared cutoff helper so list indexing and usage statistics cannot disagree.

## Provider Usage Loading

Backend API-key usage reads the latest committed SQLite snapshot and never invokes a complete request-log scan inline. It merges persisted counts with in-memory recent buckets by existing Auth ID rules.

Frontend behavior:

- Entering the provider page starts a request immediately unless a short, confirmed-fresh cache exists.
- Initial cards show a loading state instead of presenting `0 / 0` as final data.
- Refresh requests are deduplicated; the interval does not overlap an active request.
- A failed refresh keeps the last successful non-empty cache.
- Empty data replaces cache only after a successful response proves there is no usage for that Auth.
- The stale interval is shortened from four minutes to a value aligned with the request-log sync interval.

Request-log UI behavior:

- Five-second refresh skips ticks while a list request is active.
- Search input is debounced.
- Timeout or sync errors preserve the current rows and show a refresh error state.
- Pagination and search continue querying SQLite rather than loading all raw files in the browser.

## Management UI Provider Editing

Claude, Codex, and Gemini cards become provider groups like OpenAI-compatible cards:

- The card identifier is `name` when supplied, with the current masked-key/index fallback for legacy items.
- The card shows the number of key entries.
- Create/edit forms provide a provider-name field and repeatable key rows containing key, priority, and proxy URL.
- Shared provider fields remain outside key rows.
- Claude, Codex, and Gemini forms retain their existing provider-specific controls.
- Existing single-key entries open as one editable key row without forcing an immediate config rewrite.
- Saving an untouched legacy entry preserves the legacy form. Adding a name or a second key saves grouped `api-key-entries`; the old top-level fields may remain for compatibility but do not execute while grouped entries exist.
- Existing API keys remain revealable through the eye button for both legacy and grouped entries.

Provider adapters search and display both provider name and all key previews. Model chips continue showing alias first and search continues matching aliases and upstream names.

## Error Handling

- Invalid negative retention is normalized to the default during config load and rejected by the management setter.
- A provider group with no effective keys produces no Auth and remains editable in the UI.
- Duplicate key values within one group are rejected by the UI and normalized defensively by the backend synthesizer so one secret does not produce duplicate Auths.
- Per-file parse failures are recorded in sync status and skipped without aborting the full batch.
- Schema migration runs transactionally where SQLite permits; startup preserves the previous database on migration failure and reports the error.
- API responses never expose API-key values, provider attributes containing secrets, or raw proxy credentials.

## Testing Strategy

### Backend

Add focused tests before implementation for:

- YAML and JSON decoding of legacy and grouped native provider configurations.
- Omitted versus explicit-zero key priority inheritance.
- Grouped entries taking precedence over a simultaneous legacy key.
- One Auth per effective key with correct native provider, label, `provider_name`, priority, proxy, models, headers, exclusions, cooling, and protocol-specific attributes.
- Stable unique Auth IDs and duplicate-key suppression.
- Management CRUD and auth-index attachment for grouped key entries.
- Retention defaults, negative normalization, positive cutoff, and `0` permanent behavior.
- Index manager reads during background sync, request coalescing, targeted detail backfill, deletion detection, and shutdown.
- SQLite migration and composite indexes.
- Provider-name parsing, storage, list/detail/export/failure responses, and protocol fallback for historical rows.
- API-key usage returning the last committed snapshot without invoking a full sync.

Run `gofmt`, focused package tests, `go test ./...`, and the required compile command. Record the known pre-merge Codex catalog priority drift only if it still exists after upstream synchronization.

### Frontend

Add tests for:

- Native provider adapters with grouped and legacy forms.
- Form conversion preserving Claude/Codex/Gemini-specific fields.
- Name and multi-key create/edit payloads.
- Key priority/proxy inheritance presentation and secret reveal.
- Provider loading state, cache preservation after failure, refresh deduplication, and successful empty response handling.
- Request-log refresh deduplication, debounce, row preservation on timeout, retention display, and provider-name rendering.

Run type-check, targeted ESLint, relevant tests, and production build.

### Integrated fork regression

After rebuilding the UI:

1. Run the UI repository's tests and build.
2. Publish/copy the generated single-file panel as `management.html` according to the existing fork release workflow.
3. Run `node test/provider_usage_match_test.mjs` in the backend repository.
4. Verify required `/logs` route and request-log tokens remain in the bundle.
5. If a local management service is already running, compare served and workspace `management.html` SHA256 values without restarting unrelated services.
6. Run live management API probes for legacy config, grouped config, retention, request-log list/detail, and usage totals.

## Documentation

Update `config.example.yaml` with grouped native-provider examples and `request-log-retention-days`. Update backend and management UI documentation describing legacy compatibility, per-key precedence, provider-specific fields, retention semantics, and request-log indexing behavior. Keep paired English/Chinese workflow documents aligned where both exist.

Documentation updates occur after tests pass and before completion is declared.

## Migration and Rollback

No automatic destructive migration rewrites user YAML. Existing single-key configurations continue to load and execute unchanged. SQLite changes are additive; existing rows receive protocol fallback until re-indexed with a configured provider name.

Rollback consists of reverting the feature commits and restoring the previous UI bundle. Legacy config remains valid. Grouped configs require flattening each `api-key-entries` row into a separate legacy provider item before running an older binary. The implementation documentation will include this deterministic flattening example.

## Acceptance Criteria

- Both repositories' `main` branches match their fetched upstream heads before fork integration.
- Both fork branches retain all listed CPA customizations after merging upstream.
- Legacy Claude, Codex, and Gemini config behaves unchanged.
- A named native provider can manage multiple keys with per-key priority and proxy while preserving all shared and protocol-specific settings.
- Each key participates independently in existing selection, retry, cooldown, and failover logic.
- Provider cards and request logs show the configured provider name.
- Provider statistics do not settle on false `0 / 0` during initial loading or failed refresh.
- Request-log endpoints return from the latest SQLite snapshot without waiting for a full scan.
- Retention defaults to seven days, accepts other non-negative values, and treats zero as permanent structured retention.
- Backend, frontend, fork regression, build, documentation, and applicable live checks pass.
