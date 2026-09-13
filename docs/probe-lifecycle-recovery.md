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
