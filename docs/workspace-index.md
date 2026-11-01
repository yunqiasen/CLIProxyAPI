# CPA Workspace

The active repositories and runtime data are consolidated under
`/home/div/1_Project_dir/AI/CPA/`. This parent is a navigation/data directory, not
an extra Git repository. Its `README.md` links to this tracked backend document;
the three projects retain independent history and upstream/fork branches.

## Where to work

| Checkout | Daily branch | Purpose |
| --- | --- | --- |
| `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI` | `CPA-fork` | CPA backend and local runtime |
| `/home/div/1_Project_dir/AI/CPA/Cli-Proxy-API-Management-Center` | `CPA-UI-fork` | CPA management UI |
| `/home/div/1_Project_dir/AI/CPA/freetier2api` | `freetier2api_fork` | FreeTier 2API plugin source |

In each repository, `main` is upstream-only; review updates there before merging
into the fork branch. New isolated worktrees belong under
`/home/div/1_Project_dir/AI/CPA/.worktrees/<repository>/`, not outside this parent.
Open one of the active checkouts above in the editor/app before editing.

## Directory ownership

```text
/home/div/1_Project_dir/AI/CPA/
├── README.md                         -> CLIProxyAPI/docs/workspace-index.md
├── CLIProxyAPI/                      active backend Git clone and runtime
├── Cli-Proxy-API-Management-Center/   active management UI Git clone
├── freetier2api/                     active plugin Git clone
├── docs/                            document links and private handoff notes
├── .worktrees/                      future isolated development checkouts
├── _archive/20261101/                two old repos + 43 existing linked worktrees
├── .migration-backups/              private verification and rollback evidence
└── .migration-20261101/              private earlier preparation evidence
```

The old outer repository paths have been removed, not hidden behind symlinks.
Archives preserve local branches, stashes, reflogs, uncommitted and ignored files;
they are not another development/runtime installation. Eight previously missing
`/tmp` worktree registrations remain locked in the archive as historical metadata.
Do not prune, clean or launch services from the archives.

## Runtime and ports

- Management panel: `http://100.126.43.55:8317/management.html`.
- Local API base: `http://127.0.0.1:8317/v1`; client keys unchanged.
- Operations panel: `http://127.0.0.1:8328`; separate project, not relocated.
- Existing callback ports: iFlow `11451`, Antigravity `51121`, Claude `54545`.
- Compose project: `cliproxyapi`; service/container: `cli-proxy-api`.
- Image: `local/cli-proxy-api-cpa-fork:hot-reload`.
- Config, credentials, logs/plugins and mounted static assets stay under the active
  backend checkout. Never add those private runtime files or `.env` to Git.

```sh
cd /home/div/1_Project_dir/AI/CPA/CLIProxyAPI
# Read-only service checks.
docker compose -p cliproxyapi -f docker-compose.local.yml -f docker-compose.hot-reload.yml ps
curl -fsS http://127.0.0.1:8317/
# Initial setup or runtime/image change only; not for routine edits.
docker compose -p cliproxyapi -f docker-compose.local.yml -f docker-compose.hot-reload.yml up -d --no-deps --no-build --pull never cli-proxy-api
```

## Loading changes

| Change | Current delivery behavior |
| --- | --- |
| CPA Go source | Existing supervisor recompiles and replaces the CPA process inside the same container; verify the exact committed revision |
| CPA management UI | Build the UI source and deliver the single-file `management.html` artifact |
| Plugin source or embedded plugin console | Build a compatible, uniquely versioned `.so`, then select that version through CPA; see the runbook below |
| Plugin Git commit alone | **No automatic build/deployment listener installed yet**; the proposed pipeline and acceptance gates are documented |

Plugin builds belong to the plugin project; CPA's plugin directory contains
installed artifacts, not a second source checkout. No automatic model-generation
probe is part of maintenance.

## Documentation

- [Migration, archive verification and rollback](/home/div/1_Project_dir/AI/CPA/docs/workspace-migration.md)
- [Backend local delivery](/home/div/1_Project_dir/AI/CPA/docs/local-fork-delivery.md)
- [Plugin quick guide (Chinese)](/home/div/1_Project_dir/AI/CPA/docs/plugin-fork_CN.md)
- [Plugin fork, build, load, rollback and automatic-delivery design](/home/div/1_Project_dir/AI/CPA/docs/plugin-fork.md)
- [Plugin catalog](/home/div/1_Project_dir/AI/CPA/docs/cpa_plugln.md): **historical snapshot, 2026-08-08**, not a current catalog
- [Four handoff notes](/home/div/1_Project_dir/AI/CPA/docs/handoff/): private local copies

Never publish migration evidence, credential snapshots or handoff notes. The
legacy `apply-cpa-antigravity-flash-thinking.sh` is not a supported fork updater.
