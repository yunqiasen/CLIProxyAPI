# Media Provider Foundation and Image Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a dedicated `media-providers` backend and Image Provider management entry while reusing CPA's scheduler, retry, cooldown, request logging, and OpenAI image endpoints.

**Architecture:** Media providers synthesize one runtime auth per API-key entry and bind a provider-scoped media executor. Models with `generate` or `edit` capability are registered as OpenAI image models; custom operations use `/v1/media/:kind/:operation` and resolve the operation after auth selection. Existing `openai-compatibility` remains untouched.

**Tech Stack:** Go 1.26, Gin, YAML v3, CPA auth manager/model registry, React 19, TypeScript 6, Zustand, Bun/Vite.

---

### Task 1: Configuration contract and sanitization

**Files:**
- Create: `internal/config/media_provider.go`
- Create: `internal/config/media_provider_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/parse.go`
- Modify: `sdk/config/config.go`
- Modify: `config.example.yaml`

- [ ] Write tests that parse `media-providers` with provider/key priority, image model capabilities, no-model operation, async settings, and reject/drop malformed values.
- [ ] Run `go test ./internal/config -run MediaProvider -count=1`; expect RED because media types do not exist.
- [ ] Add `MediaProvider`, `MediaAPIKeyEntry`, `MediaModel`, `MediaOperation`, and `MediaAsyncOperation`; implement `SanitizeMediaProviders` with trimmed fields, normalized methods/modes, duplicate-operation removal, and supported enum checks.
- [ ] Add `Config.MediaProviders`, call sanitization from both config load paths, export SDK aliases, and document a complete image example.
- [ ] Run the focused tests; expect PASS.

### Task 2: Runtime auth synthesis and config diff

**Files:**
- Create: `internal/watcher/diff/media_provider.go`
- Create: `internal/watcher/diff/media_provider_test.go`
- Modify: `internal/watcher/diff/config_diff.go`
- Modify: `internal/watcher/synthesizer/config.go`
- Modify: `internal/watcher/synthesizer/config_test.go`
- Modify: `internal/watcher/clients.go`
- Modify: `internal/util/provider.go`

- [ ] Write failing tests proving two media keys create two stable auths, per-key priority overrides provider priority, disabled providers synthesize nothing, proxy/header metadata survives, and config changes are reported.
- [ ] Run `go test ./internal/watcher/... -run MediaProvider -count=1`; expect RED.
- [ ] Add deterministic `MediaProviderKey(kind,name)`, synthesize `media_kind`, `media_provider_name`, `base_url`, `api_key`, `provider_key`, priority, headers, proxy, and cooling metadata.
- [ ] Add media diff output and include media keys in client counts.
- [ ] Run focused watcher tests; expect PASS.

### Task 3: Management CRUD and auth-index projection

**Files:**
- Create: `internal/api/handlers/management/config_media_providers.go`
- Create: `internal/api/handlers/management/config_media_providers_test.go`
- Modify: `internal/api/server.go`

- [ ] Write handler tests for GET auth-index values, PUT normalization, PATCH preservation, DELETE by index, invalid kind/mode HTTP 400, and persistence excluding auth-index.
- [ ] Run `go test ./internal/api/handlers/management -run MediaProvider -count=1`; expect RED.
- [ ] Implement GET/PUT/PATCH/DELETE `/v0/management/media-providers`, using stable synthesizer IDs and `persistLocked`.
- [ ] Register protected management routes.
- [ ] Run focused management tests; expect PASS.

### Task 4: Media executor and standard Image API routing

**Files:**
- Create: `internal/runtime/executor/media_executor.go`
- Create: `internal/runtime/executor/media_executor_test.go`
- Modify: `sdk/cliproxy/service.go`
- Modify: `sdk/cliproxy/auth/conductor.go`

- [ ] Write failing fixture tests for JSON generation, multipart edit, provider headers, key auth, proxy metadata, non-2xx retry errors, and alias-to-upstream model rewriting.
- [ ] Run `go test ./internal/runtime/executor ./sdk/cliproxy/... -run Media -count=1`; expect RED.
- [ ] Implement `MediaExecutor` with OpenAI image defaults, configured operation overrides, JSON/multipart/binary body preservation, API request/response logging, and status errors.
- [ ] Detect media auths in executor registration, model registration, and API-key alias compilation; register image-capable models as `registry.OpenAIImageModelType`.
- [ ] Run focused tests; expect PASS.

### Task 5: Generic operations and async polling

**Files:**
- Create: `internal/api/media_operations.go`
- Create: `internal/api/media_operations_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/runtime/executor/media_executor.go`
- Modify: `internal/runtime/executor/media_executor_test.go`

- [ ] Write failing tests for `/v1/media/image/:operation`, image convenience aliases, model `required|optional|none`, JSON URL/base64 extraction, binary passthrough, provider failover, and submit/poll completion/failure.
- [ ] Run `go test ./internal/api ./internal/runtime/executor -run Media -count=1`; expect RED.
- [ ] Implement generic and convenience handlers that filter candidates by kind/operation/model and call the auth manager.
- [ ] Implement response normalization and context-bound async polling with the same credential and no overall post-connect timeout.
- [ ] Run focused tests; expect PASS.

### Task 6: Management UI data/API integration

**Files:**
- Modify: `src/types/provider.ts`
- Modify: `src/types/config.ts`
- Modify: `src/services/api/transformers.ts`
- Modify: `src/services/api/providers.ts`
- Modify: `src/stores/useConfigStore.ts`
- Modify: `src/features/providers/types.ts`
- Modify: `src/features/providers/descriptors.ts`
- Modify: `src/features/providers/adapters.ts`
- Modify: `src/features/providers/useProviderWorkbench.ts`
- Test: `test/media_provider_contract_test.mjs`

- [ ] Add a Node contract test that expects media normalization, exact kebab-case serialization, kind-filtered resources, and CRUD calls to `/media-providers`; run it and observe RED.
- [ ] Add media config types, transformer, serializer preserving unknown fields, API methods, store section, image provider brand/resource adapter, and workbench mutations.
- [ ] Run the Node contract test and `bun run type-check`; expect PASS.

### Task 7: Image Provider form and connectivity test

**Files:**
- Create: `src/features/providers/sheets/forms/MediaOperationsEditor.tsx`
- Modify: `src/features/providers/sheets/forms/BaseProviderForm.tsx`
- Modify: `src/features/providers/sheets/forms/ModelEntriesEditor.tsx`
- Modify: `src/features/providers/sheets/forms/useConnectivityTest.ts`
- Modify: `src/features/providers/sheets/ProviderSheet.tsx`
- Modify: `src/features/providers/brandLogos.ts`
- Modify: `src/i18n/locales/en.json`
- Modify: `src/i18n/locales/zh-CN.json`
- Modify: `src/i18n/locales/zh-TW.json`
- Modify: `src/i18n/locales/ru.json`

- [ ] Extend the contract test to require Image Providers category, model capability controls, operation fields, per-key priority, and image generation connectivity payload; verify RED.
- [ ] Reuse the base provider form layout, add image capability and operation editors, allow model-less operations, and test each key via `/v0/management/api-call` against the configured image operation.
- [ ] Add all locale keys and a theme-safe image icon.
- [ ] Run Node/Bun tests, type-check, and ESLint; expect PASS.

### Task 8: Build, static panel, docs, and end-to-end verification

**Files:**
- Modify: `static/management.html`
- Modify: `README.md`
- Modify: `README_CN.md`
- Modify: `docs/superpowers/specs/2026-07-26-media-provider-foundation-image-design.md`

- [ ] Run `bun run test && bun run lint && bun run build` in the UI worktree.
- [ ] Copy `dist/index.html` to backend `static/management.html` and run `node test/provider_usage_match_test.mjs`.
- [ ] Run `gofmt -w` on changed Go files, `go test ./...`, and `go build -o test-output ./cmd/server && rm test-output`.
- [ ] Start an isolated local CPA instance with an img-lite fixture, verify `GET /v1/models`, `POST /v1/images/generations`, one custom no-model operation, key failover, request-log provider name, and config hot reload.
- [ ] Update English/Chinese docs with routes, configuration, migration behavior, and verified commands; run `git diff --check` in both repos.
