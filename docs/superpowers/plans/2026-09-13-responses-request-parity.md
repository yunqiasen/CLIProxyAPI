# Responses compatibility and probe parity

Baseline: backend `91ac83dad38f35adbfe55a9ef86a77dafa2babca`; UI `403cc9d86fc0cc40784a648bcd961d869f4deb27`.

The user approved the previously verified history and cooldown repairs and requested
that management probes automatically share production request behavior. Public seams
are Codex Execute/ExecuteStream, the management provider-connectivity HTTP endpoint,
and auth-manager selection after MarkResult. Fixtures cover failures; live checks
pin one existing key per provider and never enumerate the whole key pool.

## Delivery slices

1. Add failing executor tests for Any gpt-6-astra completed search history and nonempty
   reasoning content. Preserve all portable information on a request copy. A helper in
   executor/helps performs the known-host/model transformation; HTTP and WebSocket use
   it at the last request preparation boundary. Other hosts/models remain unchanged.
2. Reproduce AgentRouter's null-code invalid_request_error with the exact captured
   decrypt-failure envelope. Recognize only that envelope on the exact Agent host,
   with an actual referenced opaque reasoning item. Reuse the existing one-shot,
   pre-output signature recovery; ordinary free-text errors do not trigger recovery.
3. Preserve upstream payment/account/authentication causes when all matching keys
   are cooling down. Both ordinary and fast/mixed schedulers use one error builder.
   Keep current cooldown durations and targeted reset behavior; never reset on probe.
4. Management Codex probes synthesize a single credential through the same native
   config synthesizer, retain saved/draft provider settings and aliases, and execute
   the same Responses source protocol as production. The UI sends its serialized
   provider draft, defaults to one key, and rejects non-completed response bodies.
   Explicit all-key actions remain explicit. Parity regressions exercise production
   and management requests against one local upstream fixture.
5. Run focused/race/full Go tests, UI tests/lint/types/build, update compatibility
   docs and AGENTS parity rules, review the complete two-repository diff, commit,
   and deliver the built panel plus code to primary workspaces. Verify clean-commit
   hot reload, unchanged container ID, panel hashes and pinned low-cost live probes.
6. Push fork branches only after checks. Inspect VPS AGENTS and runtime; back up
   deployment state before a scoped fork update. Do not change unrelated services,
   user model mappings, keys, cooldown states, or the removed Agent cpa-6a mapping.

## Regression boundaries

- Original bodies, ordinary messages, images, call IDs/results, search details,
  and readable reasoning survive; transformations are idempotent.
- No replay after text/reasoning/tool output, repeated failures stay failures,
  external execution lifecycle ownership and cancellation remain intact.
- Cooldown errors distinguish payment pools, account quota and authentication;
  one healthy key still serves and sibling models/providers remain independent.
- Saved and unsaved probes inherit per-provider first-output watch, headers,
  endpoint, key identity, source translation, model metadata and payload config.
- One selected test invokes one credential only; an error never tests another key.

## Final review record

One parallel Standards/Spec review covered both complete diffs at the fixed
baselines above. Reviewers ran read-only and did not probe paid keys.

### Standards

Six findings: panel delivery pending; public-name selection versus upstream route
resolution; optional recovery endpoint; four scheduler error-builder call sites;
repeated UI count expression; shared header-only config matching.

- Panel delivery is a required delivery step after the UI commit/build.
- Made the recovery endpoint mandatory and updated callers/tests.
- Kept public-name selection in the API client; it is the inverse operation of
  backend upstream resolution, not a second router. Alias/prefix parity tests cover it.
- Kept one shared diagnostic classifier/formatter at the existing selection seams.
  Moving the scheduler boundaries or trivial presentation counts adds no behavior.
- Header-only matching is pinned to config index/base URL/API-key kind; normal
  synthesis creates no auth for an empty key pool. Probe tests cover header-only
  routing without adding auth to the live registry.

### Spec

Four findings: missing WebSocket image exclusion; xAI completion-parser spillover;
saved runtime header-only credentials; speculative mixed suffix/alias mismatch.

- Reproduced and repaired the first three with red/green regression tests.
- Added mixed-selection reasoning-suffix coverage. Canonical model keys already
  remove suffixes; manager preselection handles per-auth route rewriting. No
  diagnostic behavior change was needed for that conjecture.

No second full review was started. Final tests/build and live delivery evidence
are recorded separately from these review outcomes.
