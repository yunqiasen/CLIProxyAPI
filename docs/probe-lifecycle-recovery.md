# Probe lifecycle and targeted recovery

## Approved scope

Follow the September 13 audit: invalidate old connectivity results on complete
Codex draft changes; cancel obsolete or closed-sheet probes; allow a completed
probe of unchanged saved credentials to recover only its payment-cooled model.
Do not guess aliases for unknown client model names, reset whole credential pools,
introduce response deadlines, or run paid all-key diagnostics.

## Implementation and verification

1. Reproduce stale green status and uncanceled requests using intercepted browser
   probes (no upstream traffic). Add regression tests at the probe transport seam.
2. Fingerprint the serialized save/probe draft, propagate AbortSignal, guard late
   results and cancellation across edits, new runs and unmount.
3. Test recovery at the management handler and auth manager boundaries. Recover
   only after completed output on unchanged saved settings; reject failed,
   canceled, edited-draft, disabled, newer-state and other-model/key cases.
4. Run UI tests/lint/build, Go tests/build/race, update delivery documentation and
   review the entire change against backend cea363e0 / UI 131badc1.
5. Commit and push both fork branches, deploy the built local panel, verify clean
   X-CPA-COMMIT and unchanged container identity. VPS is outside this request.

## Verification outcome

- Browser fixture: old deployed bundle fails stale-green assertion; candidate
  bundle passes edits, late completion, abort on edit/close and single-key checks.
- UI: 617 tests passed; lint/type check/production build passed.
- Backend: full Go suite and build passed; focused recovery/cancellation race
  tests passed repeatedly. Persistence retains unrelated model cooldowns.
- Review: one Standards/Spec CLI review dispatch was attempted; neither returned
  a final report within its bounded run. A connector also reported revoked auth.
  This is not independent approval. Local complete-diff review fixed SSE
  error-event-name handling and verified the negative recovery boundaries.

## TDD follow-up: empty collection equivalence

Baseline `72705851`. A public management-handler regression using two synthesized
saved credentials reproduced successful probes leaving the chosen model cooled:
configuration retained `excluded-models: []` while the UI draft sent `null`.
Strict Go structural equality mistook the equivalent wire values for an edit.

Recovery now normalizes only empty Codex exclusion lists before comparing
configuration; every other field retains strict structural equality. Real
exclusion/header changes still prevent recovery. No changes to timeouts, scheduling rules, key
selection, frontend payloads or client model aliases are included. The HTTP
regression covers nil/empty/same collections and actual additions/removals/header
edits, and checks that the second credential remains cooled.


### Follow-up review and verification

- Standards review returned three concerns: JSON-tag-dependent equivalence,
  normalization broader than the reported defect, and potential struct-copy
  hazards. Spec review returned two related equivalence risks and one comment
  clarification. These were review concerns, not independent observed failures.
- Replaced the first JSON-based implementation with explicit normalization of
  empty `ExcludedModels` only. Provider structs are copied before normalization;
  all other fields stay under strict comparison. `go vet` passed for management
  and config packages; no copylocks finding was reported.
- Added negative HTTP cases for cleared models and changed upstream mappings,
  alongside real exclusion/header edits. All eight HTTP scenarios passed,
  including ten repeated race-enabled runs with the earlier recovery tests.
- Full `go test ./...`, server build, and focused `go vet` passed. No paid upstream
  request, key-pool scan, configuration edit or VPS action was used for this fix.
- Exactly one Standards/Spec review dispatch was completed for this TDD run.
  Review findings were handled and tests rerun; no second review was requested.
