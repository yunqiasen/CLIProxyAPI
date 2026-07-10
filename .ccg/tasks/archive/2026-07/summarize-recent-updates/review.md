# Summarize Recent Updates

## Scope

Read-only summary of current `CPA-fork` recent commits and upstream merge contents.

## Current branch state

- Branch: `CPA-fork`
- Remote: `origin/CPA-fork`
- Local is ahead because of CCG archive commits.
- Existing unrelated local changes were not modified:
  - `static/management.html`
  - `config.yaml-替换VPS配置说明`

## Summary

Recent meaningful update is the upstream sync from `main` into `CPA-fork` at merge commit `34048b4f`, bringing upstream to `v7.2.57` / `15f30371`.

Main contents:

- Model registry updates: GPT-5.6 Sol/Terra/Luna, GPT-5.5 revisions, Codex modality restriction to text/image.
- Middleware/logging: Codex response websocket logging support.
- Translator/runtime: cache-control support, OpenAI max_tokens to Gemini maxOutputTokens mapping, interactions protocol translators, XAI reasoning replay support.
- Auth/config/plugin: Claude model ID prefix handling, plugin auth disabled status, plugin store auth/artifact handling, management plugin API changes.
- Fork preservation: kept CPA request model logging via `logging.SetGinRequestModel`, fork management UI repository defaults, and request logger behavior.

Verification from previous sync task:

- `go test ./internal/pluginstore ./sdk/api/handlers -count=1`
- `go test ./internal/api -run 'TestRequestLoggerHotReloadsAfterCommercialModeDisabled|TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory' -count=1`
- `go build -buildvcs=false -o /tmp/cli-proxy-api-test ./cmd/server`

## Notes

Two later commits only archive local CCG check tasks:

- `f3b03cdb chore: archive ccg task check-model-invocation`
- `1bdb9f40 chore: archive ccg task check-cpa-model-calls`
