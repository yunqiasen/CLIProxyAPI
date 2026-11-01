# FreeTier 2API Fork Maintenance

Updated: **2026-11-01**. The commit-triggered publisher is implemented in the
plugin repository. Service activation is opt-in; inspect `scripts/dev-plugin
status` rather than inferring delivery from source files or HTTP 200.

## Ownership and daily operation

| Component | Checkout | Daily branch |
| --- | --- | --- |
| Host | `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI` | `CPA-fork` |
| Plugin | `/home/div/1_Project_dir/AI/CPA/freetier2api` | `freetier2api_fork` |
| Management UI | `/home/div/1_Project_dir/AI/CPA/Cli-Proxy-API-Management-Center` | `CPA-UI-fork` |

Keep `main` upstream-only. Review upstream changes, merge into the fork, test,
review and commit. The publisher neither commits nor pushes. Pause delivery while
performing upstream/state/ABI maintenance. Only clean `freetier2api_fork` commits
are eligible; build immutable Git archives, never the changing working tree.

The canonical installation, commands and failure procedures live with the code:
[Local delivery manual](/home/div/1_Project_dir/AI/CPA/freetier2api/docs/local-delivery.md).
The plugin `origin` is `yunqiasen/CLIProxyAPI-freetier2api-plugin`, and `upstream` is
`jiandanc/CLIProxyAPI-freetier2api-plugin` on GitHub.

```bash
cd /home/div/1_Project_dir/AI/CPA/freetier2api
./scripts/dev-plugin status
./scripts/dev-plugin pause
./scripts/dev-plugin resume
./scripts/dev-plugin publish
./scripts/dev-plugin publish --retry
./scripts/dev-plugin rollback
```

## Build and selection contract

- Native C ABI (`cliproxy_plugin_init` / dlopen), not Go `plugin.Open`.
- Use a digest-pinned Debian bookworm linux/amd64 builder with CGO/gcc/readelf.
  Builds run without network, credentials or a Docker socket mount; test a fresh
  scratch copy, then compile the unmodified read-only commit snapshot.
- The verified local image ID is
  `sha256:0ba64f001aac341381b87e899320067012f5912a669f836773ee4042cbc545b1`
  (Go 1.26.4). Other machines must prepare their own compatible pinned image.
- `main.pluginVersion` is injected at build time. Ordinary versions follow
  `BASE-fork.COMMIT12-b.GENERATION`; filename is `freetier2api-vVERSION.so`.
- Pin the current loaded version before installing a candidate. Publish using a
  same-filesystem staging copy and non-overwriting atomic link. Never overwrite a
  loaded file or reuse a previously unloaded native-library path/inode.
- Installed directory:
  `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI/logs/plugins/linux/amd64`.
  Its `.local-delivery.lock` serializes every cooperating publisher, even callers
  with different state directories. Backups and journals stay outside discovery
  under `/home/div/1_Project_dir/AI/CPA/freetier2api/.local/delivery`.

| Operation | Interface |
| --- | --- |
| Select version (JSON string body, not an object) | `PUT /v8/management/config/plugins/configs/freetier2api/store/version` |
| Inspect registration and discovery | `GET /v8/management/plugins` |
| Read embedded console | `GET /v0/resource/plugins/freetier2api/console` |
| Read credential metadata | `GET /v8/management/credentials` |
| Read host model registry, no generation | `GET /v1/models` |

The additive `active_path` field is sourced from the host runtime registration,
not file discovery; an older host without it fails publisher preflight. Native
files may remain mapped even when registration failed, so mappings alone are not
registration evidence.

The `store/` segment is required. Configuration saves are asynchronous. Acceptance
requires matching selector, registered metadata, enabled state, process identity,
actual registered `active_path`, mapped inode, artifact checksum and embedded-console checksum. Configuration,
credential labels/notes/enabled state and API model inventory must match the
pre-switch baseline. An intentional model inventory/schema change needs a separate
compatibility/acceptance decision, not silently weakening that check.

Do not use `/v0/management/plugins/freetier2api/models` for automatic acceptance:
it may start upstream discovery. The publisher performs no sign-in, quota refresh
or model generation. Management credentials are fetched privately from the local
Docker container and sent only to loopback; no credential command-line arguments,
logs, manifests or tracked files.

## Rollback and retention

A failed build leaves the runtime alone. A failed selection uses the durable
journal and verifies restoration. The failed commit is latched until explicit
retry or a newer eligible commit. External version/config changes during an
unfinished switch cause an inspection-required recovery state rather than
clobbering an operator's work. Process identity changes during recovery also stop
automatic mutation. Normal host restarts after a completed publication can be
observed without republishing the same commit.

**Old-path rollback is not valid for a retired Go library.** Isolated native tests
reproduced callback ownership failure on A→B→A; A→B→A′ with a fresh inode passed.
The publisher copies old bytes to `OLD_METADATA_VERSION-rollback.GENERATION`.
Registered metadata stays at the original version; filename/selector generation
is verified separately. If candidate selection was attempted, restore through a fresh instance even when
the host retained the original: legacy reconfigure may not restart quiesced tasks.
Keep the original instance only for failures before candidate selection.

Go native libraries retain mapped runtimes after logical shutdown. The publisher
counts mapped device/inode pairs (including deleted files), defaults to an eight
image ceiling per CPA process, and reserves two slots before publication (candidate
plus rollback). At the threshold it pauses; it does not restart the host or claim
unlimited reloads. Deleting artifacts does not reclaim mappings. Arrange explicit
host maintenance and verify process replacement before proceeding; do not simply
kill the supervised child or raise the limit.

Quiesce/reconfigure background-restart regressions and native callback/stream
fixtures are covered. The migration-era v0.1.23 binary was additionally tested in
an isolated process and accepted quiesce; no unsupported-legacy bootstrap restart
is required for that artifact. These fixtures are not a promise of uninterrupted
streams for every upstream implementation.

The console is embedded from
`/home/div/1_Project_dir/AI/CPA/freetier2api/internal/admin/console.html`; changing it
uses this plugin pipeline, not the separate management-panel build. Source,
installed artifacts and runtime state remain separate on purpose.
