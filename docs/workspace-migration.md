# CPA Workspace Migration — 2026-11-01

## Scope

Consolidate the backend, management UI and plugin fork under
`/home/div/1_Project_dir/AI/CPA/` without changing provider settings, credentials,
plugin behavior or the deployed panel. The cleanup below also preserves all
existing historical checkouts. Plugin business changes and a new automatic delivery
service are deferred; the maintenance design is in [Plugin Fork](plugin-fork.md).
No VPS service is changed by this task.

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
retaining inodes, contents, ownership and permissions. At the initial cutover,
compatibility symlinks in the old backend checkout pointed to the new data. After
the cleanup below those links live inside the archived backend checkout; the
outer legacy paths are absent. The active data never depends on an archive link.
The new `.env` is a private copy (0600). Static management assets were preserved.

## Historical workspace cleanup

The first cutover kept the outer repositories as a temporary rollback precaution.
That was not a finished consolidation. The follow-up moved them, their complete
Git databases and all existing linked worktrees together into:

```text
/home/div/1_Project_dir/AI/CPA/_archive/20261101/
  CLIProxyAPI/                             # former main checkout and .git
  Cli-Proxy-API-Management-Center/          # former UI checkout and .git
  worktrees/
    CLIProxyAPI/                          # 33 existing linked worktrees
    Cli-Proxy-API-Management-Center/       # 10 existing linked worktrees
```

The former outer checkout paths and the two old worktree-root paths are absent;
no compatibility symlink is recreated there. The three active clones stay separate
from the archival repositories. Open the new primary checkout in the editor/app;
an existing terminal whose cwd was inside the old checkout still refers to the
archived inode until it changes directory.

A Git clone transfers committed history, not another checkout's untracked/ignored
files, working index, local-only refs, reflogs, stashes or external linked worktrees.
For that reason the historical trees were renamed intact, not re-cloned, flattened
into the active checkout, reset or deleted. `git worktree repair` rewrote both sides
of the existing links. Five UI `node_modules` symlinks were also retargeted to the
archived UI dependency directory. The old backend's config/auth/log links still
point to the current data under the active backend; do not run archival Compose
files or treat those links as independent credential backups.

Eight registrations already referenced missing `/tmp` checkouts before cleanup
(four per repository). Their Git metadata remains in the archive and is locked
against pruning; no missing working files are claimed to have been recovered.
Automatic Git maintenance and pruning are disabled in the two archive repositories.
Do not run `git clean`, `git worktree prune`, `git gc` or `git reset --hard` there.

Future isolated work belongs under `/home/div/1_Project_dir/AI/CPA/.worktrees/`,
partitioned by repository. The old global worktree directories are no longer CPA
development entry points. Project instructions and local editor trust entries use
the new paths. No global Git identity or unrelated project settings were changed.

### Cleanup verification

Private evidence: `/home/div/1_Project_dir/AI/CPA/.migration-backups/20261101-cleanup/`.
It contains the old/new path map, move journal, Git snapshots, filesystem manifests
and original symlink targets. The checks compare:

- 45 existing checkouts (two primary plus 43 linked): unchanged HEAD/branch,
  tracked/untracked status, index hashes, complete ref lists and stash refs.
- Every pre-existing filesystem entry: inode, ownership, permissions and, for
  non-directories, size and modification time; only intentional Git linkage/config
  updates and the five symlink targets are excepted. Files were renamed on the
  same filesystem without copying or following live-data links. The final inventory
  contained 132986 pre-existing entries: 132885 retained identical checked metadata;
  101 had expected Git pointer/config rewrites or one of five symlink replacements.
  Both archived Git databases passed `git fsck --connectivity-only --no-dangling`.
- All 43 linked checkouts resolve the correct archived common Git directory.
  Eight already-missing registrations remain locked.
- The CPA container keeps its ID and new bind mounts; configuration, 51 credential
  registrations, plugin enablement and the served UI are checked against baseline.
  This cleanup adds no paid model probe.

## Running service

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
for a public Git push. Original files remain available in the archived checkout.

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

Use the active new checkout and [Local Fork Delivery](local-fork-delivery.md) for
normal image/code rollback. Do not start a second instance or execute the archived
Compose files against their live-data symlinks.

If historical-directory relocation itself needs reversal, use the private
`path-map.json`, `moves-applied.json` and `symlinks-repaired.json` from the cleanup
evidence. First verify there is no new content at the old destinations and no
process writing the archives. Restore the four roots by same-filesystem rename,
run `git worktree repair` with the restored linked paths, and restore the five
original symlink targets. Recheck HEAD, refs, index, status and dependency links.
This directory reversal does not require switching the running CPA mounts back.
Keep original archive Git config in the evidence if maintenance settings need
restoring; never overwrite credentials with an old backup as part of this step.

Do not use `down -v`, clear cache volumes or discard either historical repository.
Runtime rollback and historical-directory recovery are separate operations.

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

### Cleanup follow-up review

The follow-up archive procedure passed an independent preflight and a complete
apply/verify/rollback fixture, including staged, unstaged, untracked and ignored
files, missing registrations, and an injected symlink-replacement failure. Both
Spec and Standards document reviews finished with zero remaining findings; the
initial ambiguous reference to old data links was corrected to archival links.

The follow-up changed only guidance and directory ownership, not CPA/plugin
business code or the built UI. Panel regression, shell-example syntax, document
links, config/plugin hashes and Git connectivity checks passed. A host compile
check passed in the existing container with the normal `-buildvcs=false` mode.
Local commits and exact runtime-revision checks are the final delivery steps;
this follow-up does not request another remote push or VPS deployment.
