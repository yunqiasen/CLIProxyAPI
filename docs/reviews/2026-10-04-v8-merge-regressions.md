# V8 merge regression attribution and verification

## Fixed baselines

- Backend fork: `1f9e319045c9df0c79f0e8c635f106df0799c85c`.
- Backend upstream: `d7914afdedca7af95ee974a42453dc49fc1388ce`.
- UI fork: `4fb2063f82593555ee83d68ff1c424d9ce4a9b4a`.
- UI upstream: `752e0ee772220ce49aae1221a3f39f23236590d7`.

Review covers behavior, not users' configuration choices. Synthetic credentials
and isolated services are used for mutations. Failed tests are not automatically
classified as distinct product defects.

## Confirmed integration regressions

1. **Native grouped provider form wiring:** identical SSR create probes show one
   credential input for Gemini/Codex/Claude on both original baselines, but zero
   on the initial integration. Fork descriptors hid the single-key input while
   upstream initialization omitted the grouped editor data. Saving/probing must
   preserve named groups, all selected keys, and per-key overrides, not just render
   an input. Actual form-to-API regression coverage is required.
2. **Provider attribution in request logs:** merged Codex HTTP/WS request logging
   lost `RequestLogProviderName(auth)` although the helper and parser survived.
   Restored the name on execute, stream, compact and duplex request records. The
   existing multi-key workflow checks the final selected credential, displayed
   provider, SQLite usage and terminal failure state together.
3. **WebSocket string input:** upstream added an array-only transcript contract.
   Preserve the fork's client text shorthand by normalizing strings at the socket
   boundary into a user input-text message; arrays and invalid non-string values
   retain upstream behavior. Existing real-socket timeout/budget/bootstrap tests
   now reach their intended executor paths instead of an early 400.
4. **Explicit first-output timeout:** upstream's silent transport-error closure
   suppressed the fork's opt-in `upstream_response_timeout`. Expose that exact
   504 code after attempts are exhausted, while preserving other upstream closure
   behavior. Both healthy-key recovery and exhaustion are exercised on sockets.
5. **Channel capacity versus quota:** upstream broadened capacity-message matching,
   causing `get_channel_failed` to become credential-quota 429. Explicit channel
   allocation codes must take precedence over generic message matching; preserve
   the attempt-local fallback marker through stream/bootstrap handling. Other
   sessions sharing the keys must remain usable. Visible output is never replayed.

## Test-contract updates (not bug fixes)

- V8 request retry configuration counts **additional rounds**. A fixture asserting
  exactly one visit per key uses zero additional rounds, not the old value two.
- Provisional `response.created` is not committed user output. Tests of errors
  *after output* now send a visible delta first; separate existing tests cover
  transparent failover during a provisional prefix.
- The compact endpoint returns a JSON compaction object, not SSE. The tool-schema
  fixture now serves the appropriate response for `/responses/compact`; its tool
  type-preservation assertions remain intact.
- Upstream can coalesce split SSE event/data lines during bootstrap. Validate the
  complete event bytes rather than arbitrary internal chunk boundaries.
- Gemini waits for trailing usage or stream termination before completing STOP.
  Tool roundtrip fixtures supply usage and use the actual namespace encoding
  (`namespace__tool`), retaining delta/done/output/call-identity checks.
- The Responses-to-Chat compatibility digest differs from upstream only because
  the fork also emits a decoded custom-input delta before its done event. The
  digest is updated alongside semantic custom-tool streaming/roundtrip tests.
- Credential cooldown failures use the fork's sanitized structured diagnostic,
  not upstream's generic `auth_unavailable` class. Provider/model/cause context
  is asserted at the handler boundary.

## Delivery gate

Full backend and UI suites, builds, affected race tests, independent review,
real-process configuration/probe/history/retrieval tests, and browser verification
must pass before primary checkout activation. Final delivery verifies a clean
`X-CPA-COMMIT`, unchanged development container ID, API availability and deployed
management HTML hash. Integration worktrees alone are not a completed delivery.

## Additional fixes from independent review and full-suite verification

- Restored media provider workbench grouping and create/update/delete/toggle
  dispatch for image, video and audio; preserving original indexes prevents a
  filtered category from modifying another provider.
- OpenAI-compatible edit initialization, save and probe now share the full model
  capability contract, including retrieval type, upstream path and unknown wire
  options. Native runtime cooling selections override stale legacy form values.
- Claude HTTP and streaming logs retain named-provider attribution, verified at
  the real request logger, not just a formatting helper.
- Channel fallback classification is preserved centrally in `resultErrorFromError`
  rather than discarding errors or special-casing only one streaming branch.
  Unknown failures retain upstream finite recovery deadlines; known transient
  transport errors remain attempt-local. Custom selectors retain sanitized quota
  diagnostics through their route-model availability check.
- The old fork's plain-text Claude policy buffering conflicted with upstream's
  live tool preview. Tool calls and real thinking now release the buffered prefix
  immediately, preserving interactive progress and no replay after delivery;
  existing plain-text pre-delivery policy handling remains intact.
- Explicit synthetic Claude cloaking retains first-user cache anchors for its
  billing fingerprint and deterministic prompt ID. Native Claude paths retain
  upstream latest-user semantics. Multi-turn byte-stability tests cover the split.
- A race in upstream's new setup-token flag readers was exposed by the fork's
  shared-credential race tests. Those three flags now use the existing metadata
  lock, matching profile writes. This is an upstream defect exposed during merge
  verification, not a user configuration problem.
- Plugin installation respects the shared GitHub rate-limit cooldown instead of
  swallowing the rate-limit error and attempting a stale registry download.
- The example panel repository again matches the fork runtime default, with a
  parsed-YAML regression. The V8 UI prerelease channel does not replace V7 stable.

## Explicit contract reconciliation

Home-owned OAuth credentials follow the new upstream ownership contract: the
local proxy reuses updated Home snapshots but does not refresh Home-owned tokens.
Local OAuth credentials retain bounded refresh. Mixing both contracts had created
contradictory tests and repeated selection loops; all Home tests now follow the
upstream ownership contract, without replacing the fork's local scheduler logic.

The panel bundle check retains its original `api-key-usage` regression token but
accepts its routed V8 equivalent, `/observability/usage/api-keys`. Other bundle
checks remain unchanged. This is an endpoint migration, not removal of usage UI.

## Verification evidence

Evidence directory: `/tmp/cpa-fix-20261004` (not committed, no real credentials).

- Full backend suite: `go test -p 4 -timeout 120s ./...` passed (`full-final4.log`).
- Affected race suites: executor, executor/helps, auth, OpenAI handlers, management,
  and cross-module integration passed (`race-final2.log`).
- UI parent rerun: 1721 tests passed, type-check/build passed; lint has zero errors
  and two warnings, including the testable initializer export's Vite HMR warning.
- Actual isolated CPA process: V0/V8 endpoints, grouped multikey migration,
  plugin loading, selected-key production probes, Agent-to-Any-to-Agent history,
  embeddings/rerank and configuration preservation passed (`runtime-final.log`).
- Browser checks: named two-key editing, existing-key reveal, actual renamed save
  preserve both credentials and the first-output policy; saved V8 JSON captured
  separately from the browser snapshot.

Independent Standards review found the example-panel source mismatch (fixed).
Independent Spec review found four entry-point defects (media, retrieval, cooling,
Claude log attribution); their repairs have dedicated production-seam regressions.
Final activation still requires a clean merge commit, unchanged container ID,
matching served UI, and authenticated API checks as specified above.

The independent final Spec follow-up found no remaining P1/P2 findings in its
reviewed scope (`spec-followup-report.md`). Parent verification additionally ran
the blocking live HTTP tool-preview matrix and complete affected race suites.
The browser selected-key probe reached the real V8 `provider-connectivity-test`
endpoint with HTTP 200 and displayed Reachable, preserving the saved two-key pool.
Final parent reruns are `full-delivery.log`, `runtime-delivery.log`,
`ui-parent-final2.log`, `race-final2.log` and `panel-final.log`.

During primary activation, the existing hot-reload container's module proxy
returned EOF for the newly introduced zeroconf dependency. The supervisor retained
the old healthy process. The two required module archives were copied from the
already-tested host Go cache into the container's existing module cache and
validated with `GOPROXY=off go mod download`. No source/config/image changes were
needed for the cache repair. This documentation commit triggers a fresh clean
build after the failed source state, using the normal HEAD watcher.
