# Native credential import

The existing `/v8/management/credentials` and legacy
`/v0/management/auth-files` upload handlers accept
native Claude Code `.credentials.json` (`claudeAiOauth`) and Codex `auth.json`
(`tokens`) in the same multipart/raw-body and batch import flows as CPA files.
Only credential fields are extracted. CLI settings, MCP settings and unrelated
wrapper data are omitted. Import does not test credentials, issue model calls,
reset quota or change transport defaults.

## Validation

- Claude needs a refresh token; Codex needs access and refresh tokens.
- Native wrappers must be JSON objects. Ambiguous or null wrappers are rejected.
- Optional expiration and last-refresh timestamps must have valid types and
  RFC 3339 values; Claude's `expiresAt` also accepts integer epoch milliseconds.
- Native input is limited to 1 MiB. Existing normalized CPA files, identified by
  their explicit `type` field, retain their existing validation and size behavior.
- Validation errors return HTTP 400 without creating a credential.

## Destination and preservation

Native imports use `native-<provider>-<24-hex-chars>.json`. The suffix is the first
12 bytes of SHA-256 over `provider:identity`. Codex uses `account_id`, falling back
to its refresh token; Claude uses its refresh token. When no stable account ID is
available, a rotated refresh token produces a different name.

The handler validates and stages a complete normalized file, then publishes it
with an atomic no-replace link. Staging files are not JSON credential files and
are removed afterward. **Any existing destination returns HTTP 409**, including
an exact repeat or the same account with newer tokens. Existing bytes, disabled
state, routing priority, exclusions, transport and proxy policy stay intact.
Normal CPA-format uploads keep their original explicit replacement behavior.

## Response contract

Successful single-file uploads add the actual destination name to the existing
`{"status":"ok"}` response. Batch uploads retain their existing destination
`files` list and per-file `failed` entries; partial success returns HTTP 207.
