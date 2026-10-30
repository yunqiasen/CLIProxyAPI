# Plugin Store Release Recovery

## Cause

Public GitHub REST requests share the outbound IP's hourly API budget. The
plugin store already shares API cooldowns across catalog checks and installation.
Previously the installer returned that cooldown immediately, preventing its
public release download path from running even when GitHub's website and release
assets remained available.

## Recovery behavior

- Catalog metadata checks retain the existing coalescing, cache, concurrency
  budget and GitHub `Retry-After` / `X-RateLimit-Reset` cooldown.
- An unauthenticated latest-release installation can discover the exact release
  tag through the repository's public `releases/latest` page. If this is
  unavailable, the existing registry-version fallback remains available.
- Pinned version and saved-manifest installations can use exact-tag public
  release download URLs during an API cooldown. Tags with and without a leading
  `v` are preserved; requested versions never silently become another version.
- Public metadata uses the same bounded redirects, URL/auth validation, proxy
  policy and caller cancellation as other plugin-store requests.
- Archive and `checksums.txt` downloads still undergo SHA-256 verification and
  existing ZIP/library validation before any plugin file is written.
- Authenticated metadata requests retain their failures and cooldowns; they do
  not silently switch to an anonymous transport. API requests are not repeated
  during cooldown, and public downloads do not clear that cooldown.
- Registry fetch failures, unavailable release assets, rejected downloads and
  mismatched checksums remain failures. Public download recovery is not a promise
  that GitHub or any plugin is always available.

## Regression checks

```sh
go test ./internal/pluginstore -run 'TestInstallPublicReleaseDuringGitHubAPICooldown|TestInstall.*(Recovery|Cooldown)|TestLatestWebRecovery' -count=1
go test ./internal/api/handlers/management -run 'TestPluginStoreListingCooldown|TestPluginStoreTagInstallation|TestPluginStoreInstallationPropagatesRegistry' -count=1
go test -race ./internal/pluginstore ./internal/api/handlers/management -run 'Test.*(PluginStore|PluginRelease|PublicRelease|GitHub|CanceledRequest|WebRecovery|TagScoped)' -count=1
go test ./...
go build -o /tmp/cpa-plugin-store-check ./cmd/server
```

The installer regression first exhausts a shared GitHub API limiter, then
installs and verifies a real ZIP twice without making another API request.
Coverage includes latest, exact `v` and bare tags, saved manifests, cancellation,
tag-scoped and redirected credentials, large release pages with known tags, and
a checksum mismatch with no filesystem writes.
The management regression covers a catalog-created cooldown followed by both
latest and version-selected installation and configuration persistence.

## Registry listing resilience

Registry listing retries only transport failures (no HTTP response: TLS reset,
EOF, proxy drop) up to three attempts. HTTP status errors, GitHub rate limits and
caller cancellation stop immediately and keep their typed errors, so the listing
no longer blanks out on a single dropped handshake.

## Live verification

Verify availability of the authenticated management API and management panel,
then install the originally requested plugin through
`POST /v8/management/plugins/store/<id>/install?source=official`. Confirm the
installation response, saved manifest and installed library; independently check
runtime registration using `GET /v0/management/plugins`. An HTTP installation
success does not by itself establish plugin activation or model availability.

## Review record

The pre-commit Standards and Specification reviews identified two shared issues:
authentication introduced by redirects was not retained for fallback decisions,
and large release-page bodies could discard an already known tag. Both were
reproduced with failing tests and corrected; the Standards reviewer verified both
original findings were closed. The Specification review also requested persisted
configuration assertions, now covering enabled state, exact manifest tag/version
and preservation of pre-existing plugin options.
