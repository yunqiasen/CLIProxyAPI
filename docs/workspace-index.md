# CPA Workspace

> English mirror of `/home/div/1_Project_dir/AI/CPA/README.md` (Chinese is canonical).

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
├── lab/                              external-API verification scratch area (not a Git repo)
├── docs/                            document links and private handoff notes
├── .worktrees/                      future isolated development checkouts
├── _archive/20261101/                two old repos + 43 existing linked worktrees
├── .migration-backups/              private verification and rollback evidence
└── .migration-20261101/              private earlier preparation evidence
```

`lab/` holds throwaway probes and their evidence. It never becomes a product
source tree and never receives edits that belong in one of the three checkouts.

The old outer repository paths have been removed, not hidden behind symlinks.
Archives preserve local branches, stashes, reflogs, uncommitted and ignored files;
they are not another development/runtime installation. Eight previously missing
`/tmp` worktree registrations remain locked in the archive as historical metadata.
Do not prune, clean or launch services from the archives.

## Verification scratch area (`lab/`)

Third-party upstreams change model lists, WAF rules and client fingerprints on
their own schedule. Before any plugin or backend change, reproduce the symptom
here and separate "upstream behaves this way" from "our code is wrong".

```text
/home/div/1_Project_dir/AI/CPA/lab/
├── README.md      boundary, usage and pass/fail rules
├── probes/        read-only probes; no config writes, no credential edits
│   ├── lib.sh            mgmt() management calls, probe() single-credential model probe
│   ├── vendor_matrix.sh  first credential x first card model, per vendor
│   ├── card_probe.sh     every card model per vendor (highest spend, dry-run default)
│   ├── net_matrix.sh     direct vs global-proxy reachability per upstream host
│   └── parse_probe.py    probe output to TSV
└── results/       local evidence; raw dumps git-ignored, findings kept as Markdown
```

Rules that keep this area honest:

- Probes call the same production path the credential card uses
  (`POST /v0/management/auth-files/models/probe`), so a failure here is the same
  failure the operator sees.
- One credential per vendor for the sampling run. A full pool sweep is a separate,
  explicit decision.
- `HTTP 200` is not success; read the `ok` field in the JSON body.
- Findings state time, credential name and model ID. Tokens are never written down.
- A failed verification stops at verification. No code change rides along with it.

Latest findings: `/home/div/1_Project_dir/AI/CPA/lab/results/FINDINGS-2026-11-02.md`.

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
| Plugin source or embedded plugin console | With the opt-in listener enabled, a clean fork commit is tested, built, selected and verified automatically; refresh the plugin page |
| Plugin Git commit | `freetier2api_fork` only; inspect `/home/div/1_Project_dir/AI/CPA/freetier2api/scripts/dev-plugin status` for listener and exact delivered commit |

Plugin builds belong to the plugin project; CPA's plugin directory contains
installed artifacts, not a second source checkout. No automatic model-generation
probe is part of maintenance.

## Documentation

- [Migration, archive verification and rollback](/home/div/1_Project_dir/AI/CPA/docs/workspace-migration.md)
- [Backend local delivery](/home/div/1_Project_dir/AI/CPA/docs/local-fork-delivery.md)
- [Plugin quick guide (Chinese)](/home/div/1_Project_dir/AI/CPA/docs/plugin-fork_CN.md)
- [Plugin fork, build, load, rollback and automatic delivery](/home/div/1_Project_dir/AI/CPA/docs/plugin-fork.md)
- [Verification scratch area](/home/div/1_Project_dir/AI/CPA/lab/README.md)
- [Plugin catalog](/home/div/1_Project_dir/AI/CPA/docs/cpa_plugln.md): **historical snapshot, 2026-08-08**, not a current catalog
- [Four handoff notes](/home/div/1_Project_dir/AI/CPA/docs/handoff/): private local copies

Never publish migration evidence, credential snapshots or handoff notes. The
legacy `apply-cpa-antigravity-flash-thinking.sh` is not a supported fork updater.
