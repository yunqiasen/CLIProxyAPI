# CPA Workspace Migration — 2026-11-01

## Scope

Consolidate the backend, management UI and plugin fork under
`/home/div/1_Project_dir/AI/CPA/` without changing provider settings, credentials,
plugin behavior or the deployed panel. Plugin development and a new plugin
build/hot-reload workflow are deferred. No VPS service is changed by this task.

## Repository layout

| Repository | Daily branch | Initial migration revision |
| --- | --- | --- |
| `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI` | `CPA-fork` | `84c81b85` |
| `/home/div/1_Project_dir/AI/CPA/Cli-Proxy-API-Management-Center` | `CPA-UI-fork` | `c75a2ad` |
| `/home/div/1_Project_dir/AI/CPA/freetier2api` | `freetier2api_fork` | `e6f7906` |

Each directory is an independent Git clone, not a copied `.git` or a submodule.
The parent directory is not a Git repository. `origin` points to the `yunqiasen`
fork; `upstream` points to `router-for-me` for CPA/UI and `jiandanc` for the plugin.
`main` stays upstream-only. Fork changes belong on the branches listed above.

The prior CPA/UI fork commits were pushed before cloning. Existing local `main`
refs were also preserved by fast-forward in the new clones:

- Backend: `d7914afdedca7af95ee974a42453dc49fc1388ce`.
- UI: `752e0ee772220ce49aae1221a3f39f23236590d7`.

This is preservation of already-present history, not a new upstream upgrade.
The installed project skills and lock file were copied into the new CPA checkout.

## Data and service ownership

The actual `config.yaml`, `auths/` and `logs/` now reside inside the new backend
checkout. They were moved on the same filesystem with the service stopped,
retaining inodes, contents, ownership and permissions. The old paths point to
the new locations through compatibility symlinks, **not the other way around**.
The new `.env` is a private copy (0600). Static management assets were preserved.

The retained source repositories are:

- `/home/div/1_Project_dir/AI/CLIProxyAPI` (including 38 historical worktrees).
- `/home/div/1_Project_dir/AI/Cli-Proxy-API-Management-Center`.

Do not remove or relocate their `.git` directories, prune their worktrees, or
run cleanup against them. They are historical checkouts, not active development
entry points. Old maintenance commands remain a rollback concern: use the new
workspace for all routine changes. Compatibility links do not forward source edits.

The running container uses:

- Project/service/container: `cliproxyapi` / `cli-proxy-api`.
- Compose: `docker-compose.local.yml` **and** `docker-compose.hot-reload.yml`.
- Image: `local/cli-proxy-api-cpa-fork:hot-reload`.
- Network: host; restart policy: `unless-stopped`.
- New checkout mounted read-only at `/workspace`.
- New config/auth/log/static paths mounted at the same container destinations.
- Existing Go cache volumes, proxy environment and callbacks preserved.

Container recreation was necessary to switch bind mounts. Ordinary subsequent
source changes keep the existing compile-and-process-restart supervisor. This
migration adds no plugin reload mechanism.

## Maintenance entry points

- `/home/div/1_Project_dir/AI/wsl-ops-panel/config/objects/docker-cpa.yaml`:
  new `project_dir`, explicit `compose_project: cliproxyapi`, and both
  `compose_files`. Explicit files also preserve the overlay when no container
  exists. The panel registry was reloaded through its authenticated endpoint.
- `/home/div/1_Project_dir/AI/scripts/startup-notify.sh`: `CPA_DOMAIN_FILE` points
  to the migrated logs directory. No notification credentials were changed.
- `/home/div/1_Project_dir/AI/scripts/apply-cpa-antigravity-flash-thinking.sh`:
  **legacy manual entry point, not a supported fork updater**. It rebuilds from
  stock sources without the overlay; do not merely rewrite its path and run it.
- The ops panel service, CPA split proxy and unrelated regmail services remain
  at their original locations. No service unrelated to CPA was restarted.

## Documentation

The parent `README.md` navigates the three repositories, ports and common commands.
Parent `docs/local-fork-delivery.md`, `docs/plugin-fork.md` and
`docs/workspace-migration.md` link to the authoritative backend documents.

`docs/cpa_plugln.md` in the parent is the **2026-08-08 historical snapshot**, marked
outdated on migration. Four handoff notes are copied into parent `docs/handoff/`:

- `cpa-handoff-20260713-134617.md`
- `merge-handoff-20260905-201428.md`
- `provider-usage-20260715-201347.md`
- `retrieval-handoff-20260921-154438.md`

These local copies are private (files 0600, handoff directory 0700), not material
for a public Git push. Original files remain available in the old checkout.

## Verification evidence

Private baseline and result files live under
`/home/div/1_Project_dir/AI/CPA/.migration-backups/20261101-continuation/`.
Earlier preparation evidence remains in the sibling `.migration-20261101/`.
Do not publish raw inspect output, config, browser state or credential archives.

Verified during cutover:

- 76 runtime files: contents, ownership and modes equal at the move boundary;
  moved config/auth/log files retained their inodes.
- Saved CPA config SHA-256 unchanged:
  `f803c6373062fb5e84163ba525a900448465923c69b21a14fa8218ee13043c79`.
- Image ID, container environment, entrypoint, host networking and restart policy
  unchanged. Every bind source uses the new workspace.
- 51 registered credential entries; names, display names, notes, disabled states,
  types and auth indexes match the pre-migration baseline.
- `freetier2api-v0.1.23.so` remains enabled and registered. DeepSeek Vision remains
  disabled. `privacyfilter` remains configured but unregistered, as before.
- Served management HTML hash matches both the previous panel and the new static
  file. `node test/provider_usage_match_test.mjs` passes.
- One production-executor probe pinned to a single existing Raccoon credential, model
  `sn-kimi-k3`: HTTP 200, `ok: true`, text `ok`, `finish: stop`, 3191 ms.
  No credential-pool sweep or repeated model-speed test was performed.
- Browser verification passed: login, credential display-name search and the
  model dialog with four model IDs and test buttons. No additional generation
  request was sent from the browser. The private screenshot stays with local evidence.
- Ops registry reload succeeded; `/assets/cpa` serves the new workspace.
  Compose target/registry/adapter regression suite: 41 tests passed.
- `bash -n` passes for the notification script. Resolved Compose configuration
  selects the fork hot-reload image and new read-only source mount.

Recheck current health rather than treating this dated record as live status:

```sh
cd /home/div/1_Project_dir/AI/CPA/CLIProxyAPI
docker compose -p cliproxyapi -f docker-compose.local.yml -f docker-compose.hot-reload.yml ps
curl -fsS http://127.0.0.1:8317/
curl -fsS http://127.0.0.1:8328/healthz
node test/provider_usage_match_test.mjs
```

Use the existing management credential privately for `/v8/management/config`,
`/credentials` and `/plugins`. Check `X-CPA-COMMIT` against the delivery commit;
Docker's `running` state alone is insufficient. Routine commit-triggered reloads
must preserve the post-migration container ID.

## Rollback

The image is retained as
`local/cli-proxy-api-cpa-fork:migration-rollback-20261101`. Private backups include
Compose files, config/environment snapshots, auth archive and before/after runtime
manifests. The first cutover attempt failed a static-file precheck, rolled back
and restored service; after preserving the missing historical static file, the
second cutover completed and passed the checks above.

For a path-specific regression, preserve current data first. The old checkout
and old Compose files remain available; its config/auth/log compatibility links
resolve to the **current** data in the new workspace. After checking the old
Compose resolution and image against the backup, a controlled recreation from
that checkout with both Compose files can restore the old source mount. If using
that rollback, restore the ops object's previous settings and reload the registry
so management actions target the matching checkout. Keep only one instance on 8317.

Do not blindly restore the credential archive over newer credentials, use
`down -v`, remove either checkout, or clear cache volumes. Verify panel, credentials,
plugins and runtime version after any rollback. Image-only rollback details are in
[Local Fork Delivery](local-fork-delivery.md).

## Delivery review

### Standards

Independent read-only review: 0 hard violations and 0 heuristic findings. New
paths, branch roles, bilingual README summaries, runtime preservation and private
file handling were checked. Container environment values are compared as a map;
Docker may reorder the environment list without changing its values.

### Spec

Independent read-only review: 0 findings. The three clones, fork branches,
physical data move, preserved history/worktrees, documentation copies and
unchanged runtime/plugin behavior match the approved migration scope.

Additional build verification passed inside the existing development container:
`CGO_ENABLED=1 go build -mod=readonly -buildvcs=false -o /tmp/cpa-migration-build-check ./cmd/server`.
The `-buildvcs=false` flag matches the normal supervisor build, whose commit
metadata is supplied explicitly; a bare root-owned `go build` encounters Git's
ownership check on the read-only user checkout.
