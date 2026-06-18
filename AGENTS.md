# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Branch Workflow
- This repository is maintained as a fork of `router-for-me/CLIProxyAPI`.
- Primary local workspace: `/home/div/1_Project_dir/AI/CLIProxyAPI`.
- The old `/home/div/1_Project_dir/AI/CLIProxyAPI-CPA-fork` symlink was removed; do not recreate or use it.
- Fork remote: `origin` -> `https://github.com/yunqiasen/CLIProxyAPI.git`.
- Upstream remote: `upstream` -> `https://github.com/router-for-me/CLIProxyAPI.git`.
- `main` is the upstream sync line.
- `CPA-fork` is the team mainline branch for daily development.
- Land upstream updates in `main` first, review them, then merge the needed changes into `CPA-fork`.
- Create feature branches from `CPA-fork` unless the work is specifically for upstream sync.
- Push daily fork work to `origin/CPA-fork`; do not push fork-only work to upstream.
- Keep `README.md` and `README_CN.md` aligned when editing workflow docs.

## CPA Fork Customizations
- Management UI source is maintained separately at `/home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center` on branch `CPA-fork`.
- CPA fork deployments must load the management panel from `https://github.com/yunqiasen/Cli-Proxy-API-Management-Center`, not the upstream UI repository. Keep `internal/config.DefaultPanelGitHubRepository`, `internal/managementasset.defaultManagementReleaseURL`, and `config.example.yaml` aligned with that fork.
- Other servers should be able to deploy this CPA fork normally with Docker and automatically receive the forked UI through the management panel release download. Do not require manual copying of `static/management.html` for normal server installs.
- Forked management UI releases must upload the built single-file panel as an asset named exactly `management.html`; labels are not enough because the backend updater downloads by asset name.
- `static/management.html` is the deployed single-file management UI bundle. It is generated/minified; prefer changing the web UI source, rebuilding, then copying the built HTML into this file.
- Keep the custom request logs page at `/logs`. The built bundle must include `function aXRequestLogs(){`, route `{path:\`/logs\`,element:(0,R.jsx)(aXRequestLogs,{})}`, and calls to `/v0/management/request-logs`.
- Keep the logs page "日志内容" format aligned with upstream's clean row layout, and show the client-requested model for model API requests. The backend access log emits this as `model=<name>` via `logging.SetGinRequestModel` in the common execution path; the UI parser renders it as a model badge.
- Keep request-log data collection separate from the raw text logs. Raw logs must not be replaced by parsed/exported request-log data.
- The AI providers page uses the newer upstream provider layout, but must retain fork behavior: provider success/failure totals, hover details grouped by model/status/error/count, provider-specific matching, and no duplicate native tooltip.
- AI provider model chips must show the model alias first because that is the API-facing model name. Fall back to the raw model name only when alias is empty. Search/filter must still match both alias and raw model name.
- Editing an AI provider must allow revealing existing API keys with the eye button; do not reintroduce the upstream bug where the key stays hidden.
- Plugin management must keep upstream plugin deletion UI: delete button, confirmation dialog, and restart-required notice after deletion.
- Quota management must keep the fork behavior: current-page refresh is separate from true background refresh-all with bounded concurrency.
- Auth files credential download must keep ZIP export for selected credentials instead of triggering one browser download per credential.

## Management UI Verification
- After changing `static/management.html`, run `node test/provider_usage_match_test.mjs`.
- If the management service is running locally, verify the served page matches the workspace file with `curl -sS --max-time 8 http://100.126.43.55:8317/management.html -o /tmp/live-management.html && sha256sum /tmp/live-management.html static/management.html`.
- For frontend-only management UI changes, preserve the regression tokens in `test/provider_usage_match_test.mjs` instead of removing the test to make a build pass.

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
