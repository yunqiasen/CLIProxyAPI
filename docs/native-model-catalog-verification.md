# Native model catalog verification (2026-10-07)

Scope: native discovery-only model display management across the CPA backend and
management UI. Review bases: backend `a197a582`, UI `bc01a3b`.

## Checks

- Full Go tests, focused race tests (`ModelCatalog|Catalog` in config,
  modelcatalog, API handlers and server packages), and a server build pass.
- UI `bun run verify`: 1805 tests pass, TypeScript/build pass, lint has no errors
  and two pre-existing warnings.
- The built single-file panel retains the existing fork regression tokens and
  adds catalog endpoint/control checks. The old bundle fails the new checks;
  the rebuilt bundle passes.
- Isolated compiled-server acceptance covers two client keys, five catalog
  formats, all-hidden arrays, private inventory, side-effect-free preview and
  direct Responses/embeddings calls before and after hiding. Responses complete;
  embeddings retain their values. No production visibility rules are edited.
- Browser acceptance covers exact and wildcard hide/restore, cancel, bulk hide,
  ordered pins, sorting, format switching, identifier search, keyboard controls,
  Chinese/English, desktop and 390px light/dark layouts.
- Additional browser fault-injection checks use a local fixture proxy: pending
  saved policies retain a draft preview, Refresh confirms activation without a
  second write, and a successful save recovers a failed inventory read. Opening
  an already-persisted but inactive policy exposes the same pending/Refresh state.
- Home tests exercise actual existing catalog builders, client-key forwarding
  instead of the management bearer, source errors and unchanged management
  restrictions. Plugin tests combine lifecycle/body/header handling with native
  curation and trusted inventory capture.

## Spec axis (frontend model)

Actual completed reviews used the configured Gemini frontend family through an
existing provider (`gemini-3.7-flash`). The Antigravity CLI login failed; that call
was not treated as approval. The final Gemini review reports no actionable
findings after examining both repositories and the approved specification.

## Standards axis (Claude)

Actual completed Claude reviews used the existing configured Claude provider;
responses identify `claude-opus-5`. Failed or cancelled attempts on other existing
review channels were not treated as approval; credentials were never rotated
within a provider.

Findings and dispositions:

1. **Fixed:** the inventory capture writer embedded an unset response-writer
   interface. An optional method could panic. A regression reproduced the panic;
   the complete writer now explicitly handles all methods without touching the
   real connection. Capture lifecycle notification is released on completion.
2. **Fixed:** trailing preview JSON used a manufactured EOF sentinel. Actual
   decode errors are preserved and multiple JSON values receive a descriptive
   internal error. Malformed requests still return 400 without changing policy.
3. **Improved:** delayed activation and inventory-error recovery now have usable
   previews and an explicit Refresh path, including the initial-open case.
4. **Rejected with source/test evidence:** Grok inventory allegedly needed a
   user-agent selector. It directly calls the existing Grok builder; the selector
   is not on that path. Public, inventory and preview tests verify Grok metadata.
   Claude acknowledged the supplied unchanged-source context.
5. **Not applicable:** the 300ms production async backoff is not a Go test TTL
   sleep. Tests await operations without elapsed-time or timer-boundary assertions.
   Claude acknowledged this distinction.
6. **Intentional:** pristine Save avoids duplicate writes; Refresh checks pending
   activation. New draft edits clear old saved status rather than falsely marking
   an unsaved policy applied. Claude acknowledged this behavior.
7. **Rejected with executable evidence:** Claude's final claim that reading a nil
   Go map panics is incorrect; map lookup returns the zero value. The existing
   pointer guard handles it. `TestModelCatalogInventoryWithNilPluginMap` explicitly
   sets the map to nil and verifies HTTP 200 with `model_sort_enabled:false`.
8. **Out of scope / unchanged:** Home's existing Grok builder does not invent
   reasoning levels absent from Home data. Its context length is already copied;
   this feature preserves the original builder and metadata contract.

Both review axes completed; no valid unresolved findings remain. Raw model
responses and test/browser evidence are retained in the local feature ticket's
verification directory, not in release artifacts. Routine delivery uses scoped
commits and automatic local process replacement, with container identity,
configuration hash, clean commit marker and served-panel hash checked separately.
