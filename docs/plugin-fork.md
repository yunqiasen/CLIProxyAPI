# FreeTier 2API Fork Maintenance

## Status and ownership

Source inspection date: **2026-11-01**. This runbook organizes existing build and
manual loading interfaces. **Commit-triggered automatic plugin delivery is
proposed, not implemented or enabled.** This documentation task does not build
or replace the installed plugin, request a plugin-version switch, or change
plugin settings and credentials.

| Component | Primary checkout | Development branch |
| --- | --- | --- |
| CPA host | `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI` | `CPA-fork` |
| FreeTier plugin | `/home/div/1_Project_dir/AI/CPA/freetier2api` | `freetier2api_fork` |
| CPA management panel | `/home/div/1_Project_dir/AI/CPA/Cli-Proxy-API-Management-Center` | `CPA-UI-fork` |

The plugin's `origin` is
`https://github.com/yunqiasen/CLIProxyAPI-freetier2api-plugin.git`; `upstream` is
`https://github.com/jiandanc/CLIProxyAPI-freetier2api-plugin.git`. Its `main` is the
upstream synchronization line. `freetier2api_fork` tracks
`origin/freetier2api_fork`; feature branches start there. The initial fork source
revision is `e6f7906400689559fc8dbef18d7dbd15b62ca2a6`.

The migration retained this installed file:

```text
/home/div/1_Project_dir/AI/CPA/CLIProxyAPI/logs/plugins/linux/amd64/freetier2api-v0.1.23.so
SHA-256: c33b033f867f54907b516a0232c5df2682c49944f5466df5a22992f4c4a96c29
```

These are snapshot facts, not proof that this file was built from the fork
revision above. Recheck the file, container mounts and registered metadata before
maintenance. A checked-out repository or a discovered filename is not a delivered
plugin.

## Existing capabilities and boundaries

- `scripts/build.sh` runs `test ./... -count=1 -timeout 300s`, then builds a native
  library with `-buildmode=c-shared`, producing `dist/freetier2api.so` on Linux.
- `scripts/godocker.sh` sets `CGO_ENABLED=1` and runs the Debian-based
  `golang:1.24` image with a C compiler. `go.mod` declares Go 1.22. The helper fixes
  the image tag, not its digest; a reproducible publisher still needs an exact
  image digest. Avoid Alpine/musl artifacts for the current glibc host. Match the
  host OS/architecture; setting `GOARCH` alone does not install a cross C compiler.
- CPA loads the native C ABI with `dlopen` and `cliproxy_plugin_init`, **not Go's
  `plugin.Open`**. Both current SDK copies declare native ABI 1 and RPC schema 6.
  Check ABI layout, callbacks, schema and glibc compatibility after upstream merges;
  matching Go compiler versions alone is not an ABI compatibility check.
- `main.pluginVersion` accepts `-ldflags '-X main.pluginVersion=VERSION'`.
  Without injection, `internal/app/app.go` supplies `0.1.23`. Renaming a library
  does not change the metadata it reports.
- `internal/admin/console.html` is embedded by `internal/admin/console.go`.
  Editing this plugin console requires a new `.so`. CPA's separate management
  panel and its generated `static/management.html` are different artifacts.
- `Host.ApplyConfig` supports replacing a loaded plugin when the selected file
  path changes. The ordinary path does not restart the CPA process/container.
  Rewriting bytes at the same path is not the replacement mechanism.
- The CPA Go-source supervisor watches the host checkout, not the sibling plugin
  repository. Its existing host hot reload is not a plugin commit watcher.

### Filename and selection rules

`internal/pluginhost/platform.go` accepts versions matching
`^[0-9][0-9A-Za-z.+-]*$`. For example, the following fork version is accepted:

```text
metadata.version / configured version: 0.1.23-fork.e6f790640068
filename: freetier2api-v0.1.23-fork.e6f790640068.so
```

The filename parser splits at the **last** `-v`; avoid adding another `-v` inside
a suffix. Dotted numeric versions compare numerically; versions with suffixes
fall back to lexical comparison, not full SemVer ordering. Select the exact
version explicitly rather than depending on hash/timestamp sorting.

Discovery scans regular library files in `<plugins-dir>/<GOOS>/<GOARCH>` and
`<plugins-dir>`. Symlinks are skipped. Use immutable, unique versioned filenames;
keep staging files without a library extension and backups outside both scanned
directories. Initial host cleanup can delete unselected versions, so a second
`.so` in the scan directory is not a durable rollback backup.

### Actual management interfaces

| Operation | Existing interface |
| --- | --- |
| Read or change the selected version | `GET`, `PUT` or `PATCH /v8/management/config/plugins/configs/freetier2api/store/version` |
| Inspect discovery and registration | `GET /v8/management/plugins` |
| Fetch embedded plugin console | `GET /v0/resource/plugins/freetier2api/console` |
| Read plugin console model view (may start upstream discovery) | `GET /v0/management/plugins/freetier2api/models` |
| Read CPA's API-facing model inventory | `GET /v1/models` with an existing CPA API key |

**The `store/` segment matters.** The host reads `store.version`, falling back to
`store.release-tag`. Writing
`/v8/management/config/plugins/configs/freetier2api/version` only preserves an
opaque plugin field; it does not select the loaded version. `PUT`/`PATCH` of the
correct leaf takes a JSON string, such as `"0.1.23-fork.e6f790640068"`, not
`{"value":"..."}`. V8 config saves enqueue an asynchronous reload: HTTP 200 means
the setting was saved, not that registration succeeded.

The plugin's own management/resource routes retain their V0 paths; the V8 host
configuration API does not automatically rename plugin-owned routes. The plugin
console model endpoint calls `EnsureModels` when caches are empty and can start
upstream discovery and cache writes. Keep it out of automatic acceptance; use
the host's registry-backed `/v1/models` for the inventory check.

## Upstream synchronization

Run in a clean plugin checkout. Preserve unfinished work separately; do not stash,
reset or overwrite it as part of an automated sync.

```bash
set -euo pipefail
cd /home/div/1_Project_dir/AI/CPA/freetier2api
test -z "$(git status --porcelain)"
git fetch upstream
git switch main
git merge --ff-only upstream/main
# Review the incoming source, ABI, configuration/state migrations and changelog.
git switch freetier2api_fork
git merge --no-commit --no-ff main
# Resolve conflicts, inspect the complete diff and run tests before committing.
```

If fast-forward fails, inspect divergence rather than resetting or force-pushing.
If review rejects a merge that is in progress, use `git merge --abort`. Complete
the reviewed merge on `freetier2api_fork`; do not publish an uncommitted tree.
Updating `main` must never trigger plugin publication. Pushes are separate,
explicitly requested actions: `git push origin main` for the sync line and
`git push origin freetier2api_fork` for daily fork work.

## Manual build from a fixed commit

The commands below are a maintenance procedure, **not commands run by this
migration**. Start with a reviewed, committed plugin change. Use a clean commit
snapshot so concurrent edits never leak into the artifact:

```bash
set -euo pipefail
cd /home/div/1_Project_dir/AI/CPA/freetier2api
test "$(git branch --show-current)" = freetier2api_fork
test -z "$(git status --porcelain)"
COMMIT=$(git rev-parse HEAD)
SNAPSHOT=$(mktemp -d /tmp/freetier2api-build.XXXXXX)
git archive "$COMMIT" | tar -x -C "$SNAPSHOT"
cd "$SNAPSHOT"
BASE_VERSION=$(sed -n 's/^const DefaultVersion = "\(.*\)"/\1/p' internal/app/app.go)
test -n "$BASE_VERSION"
VERSION="${BASE_VERSION}-fork.${COMMIT:0:12}"
mkdir -p dist
./scripts/godocker.sh test ./... -count=1 -timeout 300s
./scripts/godocker.sh build -buildmode=c-shared \
  -ldflags="-s -w -X main.pluginVersion=${VERSION}" \
  -o "dist/freetier2api-v${VERSION}.so" .
ARTIFACT="$SNAPSHOT/dist/freetier2api-v${VERSION}.so"
sha256sum "$ARTIFACT"
file "$ARTIFACT"
readelf -h "$ARTIFACT"
nm -D "$ARTIFACT" | grep ' cliproxy_plugin_init$'
```

These use existing script interfaces; `scripts/build.sh` is also the existing
one-command test/build entry point, but it does not inject a unique fork version.
Keep the full commit, chosen version, artifact checksum, build image ID/digest,
OS/architecture and test output with the artifact outside the scan directories.
Export-symbol and ELF checks are preliminary checks, not runtime acceptance.
Before publication, verify that the branch still points to `COMMIT`; do not
publish an obsolete build over a newer delivery.

## Manual load, verification and rollback

Use one publisher, including manual maintenance. Check the Docker context,
container Compose labels and actual mount sources first:

```bash
docker context show
docker inspect cli-proxy-api --format '{{.Id}} {{.Config.Image}} {{json .Config.Labels}}'
docker inspect cli-proxy-api --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{println}}{{end}}'
```

For the migrated local layout, host `logs` is mounted at `/CLIProxyAPI/logs` and
the plugin directory below is on that mount. Do not use a stale pre-migration
path or blindly assume a remote Docker daemon shares the local filesystem.
Acquire the shared publisher lock before changing even the old version pin:

```bash
CPA_ROOT=/home/div/1_Project_dir/AI/CPA/CLIProxyAPI
PLUGIN_DIR="$CPA_ROOT/logs/plugins/linux/amd64"
exec 9>"$CPA_ROOT/logs/freetier2api-publish.lock"
flock -n 9
```

1. Read registered metadata, the current version selector and model inventory.
   Record the old filename/hash/version and whether the selector existed. Retain
   the complete old library in a protected backup directory outside discovery.
   Preserve plugin options, credentials and state; review state-format changes
   separately. Confirm global and per-plugin enablement are already intended.
2. If selection was unpinned, pin the current known-good version and verify it
   before placing the new library. Otherwise, a later config reload could choose
   the new file before the planned switch.
3. Stage the new immutable file on the same filesystem, then rename it into the
   scan directory. Hold a publisher lock throughout placement, selection,
   verification and any rollback. For the fixed-commit build above:

   ```bash
   FINAL="$PLUGIN_DIR/freetier2api-v${VERSION}.so"
   STAGING="$PLUGIN_DIR/.freetier2api-${VERSION}.$$.tmp"
   test ! -e "$FINAL"
   test ! -e "$STAGING"
   install -m 0644 "$ARTIFACT" "$STAGING"
   cmp "$ARTIFACT" "$STAGING"
   mv -T "$STAGING" "$FINAL"
   ```

   Cooperating publishers must use this same lock. Do not overwrite an existing
   version, switch symlinks, or copy over a currently loaded `.so`.
4. Select the version through the existing API. Set `CPA_URL` and
   `MANAGEMENT_KEY` through the existing local credential mechanism; keep shell
   tracing off and omit credentials from logs/manifests:

   ```bash
   set +x
   : "${CPA_URL:?Set the verified CPA origin}"
   : "${MANAGEMENT_KEY:?Set the existing management credential}"
   SELECTOR="$CPA_URL/v8/management/config/plugins/configs/freetier2api/store/version"
   curl -fsS --connect-timeout 3 --max-time 8 -X PUT "$SELECTOR" \
     -H "Authorization: Bearer $MANAGEMENT_KEY" \
     -H 'Content-Type: application/json' \
     --data-binary "\"${VERSION}\""
   ```
5. Poll registration with a bounded **maintenance wait**, separate from model
   request timeouts. This example permits 60 observations; a miss requires
   investigation/rollback, not another automatic model call:

   ```bash
   accepted=false
   for attempt in $(seq 1 60); do
     if curl -fsS --connect-timeout 3 --max-time 8 "$CPA_URL/v8/management/plugins" \
       -H "Authorization: Bearer $MANAGEMENT_KEY" |
       jq -e --arg version "$VERSION" \
         '.plugins[] | select(.id == "freetier2api") |
          .registered == true and .effective_enabled == true and
          .metadata.version == $version' >/dev/null; then
       accepted=true
       break
     fi
     sleep 1
   done
   test "$accepted" = true
   curl -fsS --connect-timeout 3 --max-time 8 "$SELECTOR" -H "Authorization: Bearer $MANAGEMENT_KEY"
   curl -fsS --connect-timeout 3 --max-time 8 "$CPA_URL/v0/resource/plugins/freetier2api/console" \
     -o "$SNAPSHOT/served-console.html"
   : "${CPA_API_KEY:?Set one existing CPA API key}"
   curl -fsS --connect-timeout 3 --max-time 8 "$CPA_URL/v1/models" \
     -H "Authorization: Bearer $CPA_API_KEY" \
     -o "$SNAPSHOT/registered-models.json"
   ```

   Inspect the served console's expected change and compare model registration
   with the baseline; an empty inventory may be correct for the existing
   account/configuration. Fetching HTML with curl does not execute console JS.
   Check the plugin's lifecycle/error logs and confirm the CPA PID/container ID
   did not change for this plugin-only replacement. `path` in `/plugins` comes
   from discovery and may describe a failed candidate while registered metadata
   still describes the old plugin. Require the **selected version, registered
   metadata version, `registered`, `effective_enabled`, console and models** to
   agree; no single field is sufficient.
6. On failure, restore the old immutable file from the external backup if it was
   cleaned up, then `PUT` the old exact version to the same `SELECTOR`. Repeat the
   registration/UI/model checks and verify background-worker behavior. Keep the
   known-good version explicitly pinned while a failed candidate remains in the
   scan directory. Only restore an originally absent selector after removing
   that candidate from discovery and checking the resulting selection.

The example stops on a failed check; it does not install an automatic rollback
trap. If the shell exits, reacquire the same lock before manual rollback. A future
publisher must retain the lock across failure handling and route every failure
to explicit rollback. After a verified outcome, release it with `flock -u 9` and
`exec 9>&-`. Do not infer successful rollback solely from HTTP 200; a timed-out
save can still complete asynchronously and also needs status inspection.

The host inventory read and fetching console HTML are not generation probes.
Acceptance must not automatically run tasks, check-ins, refresh-all, account
rotation or paid inference. Preserve existing scheduled-task settings, and use
fixtures for lifecycle/load testing. An actual model call requires a separate
explicit action; it is not part of this maintenance runbook.

## Lifecycle gaps to test before automation

- **Background workers after failed replacement:** the host quiesces the old
  plugin before loading its replacement. This plugin's `plugin.quiesce` calls
  `StopBackgroundWork`. Host `rollbackReplacement` calls `callRegister`, which
  chooses `plugin.reconfigure` for an already registered instance. The plugin's
  reconfigure handler does not call `StartBackgroundWork`. This is a
  **source-indicated rollback risk, not a reproduced live failure**. Existing
  host rollback mocks and the plugin's ordinary reconfigure test do not exercise
  this combined lifecycle. Add a fixture test covering register → quiesce →
  failed replacement → rollback → background work before claiming recovery.
- **Retired instances:** successful replacements append old instances to the
  host's retired list rather than promptly unloading them. Repeated delivery
  needs a bounded-loop fixture test measuring memory, goroutines, callbacks,
  streams and shutdown. File cleanup is not evidence of module reclamation.
- **State and in-flight work:** verify backward-compatible persistent state,
  active-stream behavior and repeated switches in a controlled fixture. The
  presence of the replacement path does not guarantee interruption-free work.

The normal plugin delivery path requires no CPA restart. If lifecycle/ABI recovery
needs process replacement, handle that as an explicit recovery operation with
its impact and retained rollback artifact, not a hidden publisher side effect.

## Proposed commit-triggered delivery — not implemented

The intended workflow is:

```text
clean reviewed freetier2api_fork commit
  -> immutable commit snapshot
  -> pinned-image tests and version-injected c-shared build
  -> single-publisher lock + discard superseded builds
  -> preserve known-good artifact and selector
  -> atomic immutable-file placement
  -> select exact store.version through V8 API
  -> poll registration + metadata + console + model inventory
  -> record success, or restore old version and verify rollback
```

Requirements for a future implementation:

- Watch only the approved fork branch; skip `main`, dirty trees and uncommitted
  changes. Coalesce queued commits and recheck the newest eligible commit before
  publication. Never stage, commit or push source on behalf of the watcher.
- Keep generated snapshots, artifacts, backups, publication state and logs in
  `/home/div/1_Project_dir/AI/CPA/freetier2api/.local/` in the future implementation;
  add an explicit ignore rule before use. This directory is outside CPA discovery,
  and the watcher must not track its own output. No such workspace was installed
  by this documentation task.
- Pin the builder image digest and platform; record source SHA, version, image,
  checksum, tests and acceptance results. Only one publisher may switch versions.
- Keep build failures and superseded builds out of discovery. Use neither a
  symlink switch nor same-path library overwrite; retain backups externally.
- Save only the intended version selector, preserving all unrelated settings and
  credentials. Reconcile asynchronous reloads rather than treating the save
  response as deployment success.
- Implement and test rollback, repeated replacement, cancellation and worker
  restart before enabling unattended publication. Do not promise a reload SLA or
  zero interruption before measurement.
- No automatic paid-model calls, added background business actions, routine CPA
  restart, remote deployment or release publication.

No watcher, service unit, commit hook, publisher CLI or automatic rollback system
was added by documenting this workflow.

## Source map

Paths below are relative to the indicated repository; function names identify
interfaces without relying on changing line numbers.

| Repository | Evidence |
| --- | --- |
| Plugin | `scripts/build.sh`, `scripts/godocker.sh`, `go.mod`: current build/test/toolchain behavior |
| Plugin | `main.go`, `internal/app/app.go`: injected/default metadata version |
| Plugin | `cgo.go`, `cpasdk/pluginabi/types.go`: native entry point and ABI/schema |
| Plugin | `internal/admin/console.go`, `internal/admin/console.html`, `internal/admin/admin.go`: embedded console and routes |
| Plugin | `internal/admin/models.go`, `internal/admin/catalog.go`: console model reads can initiate upstream discovery |
| Plugin | `internal/app/main.go`, `internal/app/host.go`, `tests/integration/plugin_register_test.go`: register/reconfigure/quiesce and background lifecycle |
| Host | `internal/pluginhost/loader_unix.go`, `sdk/pluginabi/types.go`: native loading and ABI validation |
| Host | `internal/pluginhost/platform.go`: `pluginFileFromPath`, `selectPluginFilesWithCandidates`, `comparePluginVersions`, `cleanupUnselectedPluginFiles` |
| Host | `internal/pluginhost/config.go`: `pluginConfigDesiredVersion` and normalization |
| Host | `internal/pluginhost/host.go`: `ApplyConfig`, `rollbackReplacement`, `callRegister`, `retireLoadedPluginLocked` |
| Host | `internal/api/server_management_v8.go`, `internal/api/handlers/management/config_v8.go`, `handler.go`: V8 leaf writes and asynchronous reload |
| Host | `internal/api/handlers/management/plugins.go`, `plugin_store.go`: list fields and selected store version |
| Host | `internal/pluginhost/management.go`: plugin-owned management/resource prefixes |
