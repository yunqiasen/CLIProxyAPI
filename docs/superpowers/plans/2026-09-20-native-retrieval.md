# Native Embeddings and Rerank Implementation Plan

> **For agentic workers:** Execute inline using the approved implementation/TDD workflow. The user approved public endpoint, shared scheduling, and probe seams in this conversation.

**Goal:** Offer embeddings and rerank as ordinary CPA APIs instead of a second management-key plugin gateway.

**Architecture:** Extend existing OpenAI-compatible model configuration with explicit `type: embeddings|rerank` and optional `upstream-path`. Keep the default chat/image behavior unchanged. Register ordinary authenticated endpoints, reuse the common execution/auth/usage pipeline, and add a small non-chat JSON executor path. Probes reuse selected-key synthesis and this same executor, including unsaved model settings. Ambiguous embedding aliases that map to different upstream models are rejected before inference.

**Tech Stack:** Go, Gin, existing auth manager/model registry, React/TypeScript management UI, Bun tests.

Baseline: `c6197a0cc80f2f899e0ea1313d23c3d5347ac0f5`.

## Approved seams and slices

- [x] Public HTTP `POST /v1/embeddings` and `/v1/rerank`: real config synthesis, scheduler, HTTP executor, and a local external-upstream fixture. Start with 404-red tests; preserve request fields and response bodies, replace only the routed model, and use upstream rather than client credentials.
- [x] Config/model metadata and failover: explicit endpoint kind, optional relative upstream path, config reload hashes; same model can use sibling keys/providers; conflicting embedding models sharing an alias fail before network access. Wrong endpoint, invalid input, canceled requests, and malformed successful responses stay errors.
- [x] `POST /v0/management/provider-connectivity-test`: saved/draft OpenAI config, a single selected key, same executor/payload policy, no key rotation, and structured result validation. Tests compare production/probe upstream bodies and verify draft isolation.
- [x] UI edit/save/probe: model kind/path fields use the shared save serializer; only embeddings/rerank probes change transport. Existing chat, Codex, image and all-keys actions retain behavior. Test serializers and provider/key-row calls, then build the single-file panel.
- [x] Full Go tests, focused race tests, Go build, UI verify, panel regression, English usage docs, one complete Standards/Spec review.
- Delivery procedure after these gates: scoped local commits, primary-branch integration, exact clean `X-CPA-COMMIT`, unchanged container ID, authenticated endpoints/panel, and delivered-binary fixtures. The executing agent records this post-commit result in `/tmp/cpa-embeddings-rerank-20260920/runtime-after.json`. No VPS action or paid-key sweep.

## Commands

```sh
go test ./internal/api -run TestRetrieval -count=1
go test ./internal/api/handlers/management -run Retrieval -count=1
go test ./sdk/cliproxy/... ./internal/config ./internal/watcher/... -count=1
go test ./... -count=1 -timeout=180s
go test -race ./internal/api ./internal/api/handlers/management ./sdk/cliproxy/auth -run Retrieval -count=3
go build -o /tmp/cpa-embeddings-rerank-20260920/server ./cmd/server
node test/provider_usage_match_test.mjs
```

The plugin source examined is `KorenKrita/cliproxy-embeddings-rerank-forward`
commit `36a44f5`. Borrow protocol/field preservation and upstream path behavior;
do not copy its private credential pool, independent cooldown state, management
API authentication, or unrestricted different-model failover.

## Checkpoint evidence (2026-09-20)

- Go full suite and focused retrieval race suite passed; server compiled.
- UI frozen install and `bun run verify`: 703 tests, 93 files, zero failures; lint, TypeScript and Vite build passed.
- Binary fixtures and browser QA passed with synthetic credentials only.
- Browser checks: selected row key, exactly one call, draft path/headers, type switching, default controls, malformed response error, save persistence and probe isolation. Final recheck also proved public alias/prefix payload-rule matching and current server option preservation while an old form stayed open.
- Evidence directory: `/tmp/cpa-embeddings-rerank-20260920/`.
- Final full-suite/build evidence: `go-full-final.log`, `go-race-final.log`, `ui-verify-final.log`.
- Final two-axis review completed through independent read-only reviewers. All five findings were reproduced and repaired before delivery; no second review invocation.

## Standards

One documented-standard finding: stale hidden provider/model fields could overwrite
newer saved values when a form was open. Fixed the existing-record merge to apply
only form-managed fields, retaining current server options and removals. New
records still retain their supplied extension metadata. Public save regression:
`OpenAI saves keep the latest hidden options instead of replaying stale form snapshots`.

## Spec

Four findings, each covered by red/green regression evidence:

1. OAuth candidates without compiled API-key capabilities could bypass mixed-type
   alias validation. The validator now checks the same advertised-route eligibility
   as the scheduler. `TestRetrievalRejectsAliasesSharedWithOAuth` verifies zero
   upstream calls instead of dispatching a vector request to Codex Responses.
2. UI probes used upstream names without public aliases/prefixes. Shared probe
   selection now uses the API-facing name. The UI request regression and both
   prefixed/unprefixed `TestRetrievalProductionAndProbePayloadParity` cases pass.
3. Newly registered retrieval models could replace chat `auto` selection.
   Automatic selection excludes these non-chat types;
   `TestRetrievalModelsDoNotReplaceChatAutoSelection` checks a real chat response.
4. Base64 vectors containing Inf/NaN passed structural validation. Float32 values
   are now checked for finiteness; `TestRetrievalEnvelopeIntegrity` covers both
   invalid values and successful base64 vectors. The binary smoke also checks it.

Summary: Standards 1 finding (stale configuration overwrite), Spec 4 findings
(worst: wrong-protocol OAuth dispatch); all repaired and regression-tested.
Review reports: `/tmp/cpa-embeddings-rerank-20260920/review/{standards,spec}.md`.
