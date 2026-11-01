# FreeTier 2API Fork Workspace

Status: workspace prepared on 2026-11-01. Plugin behavior changes and a new plugin
hot-reload workflow are deferred to a separate task.

## Repositories and branches

- Checkout: `/home/div/1_Project_dir/AI/CPA/freetier2api`.
- Fork (`origin`): `https://github.com/yunqiasen/CLIProxyAPI-freetier2api-plugin.git`.
- Upstream: `https://github.com/jiandanc/CLIProxyAPI-freetier2api-plugin.git`.
- `main` contains upstream history only.
- `freetier2api_fork` is the daily development branch and tracks
  `origin/freetier2api_fork`.
- Initial fork revision: `e6f7906`. No plugin business code was changed during migration.

## Future upstream synchronization

Use a clean checkout. Keep uncommitted work separate before switching branches:

```sh
cd /home/div/1_Project_dir/AI/CPA/freetier2api
git fetch upstream
git switch main
git merge --ff-only upstream/main
git push origin main
git switch freetier2api_fork
git merge main
# Review compatibility and test before committing resolutions or pushing the fork.
```

If fast-forward fails, inspect the divergent history instead of resetting or
force-pushing. Keep upstream changes out of the development branch until they
have been reviewed. New feature branches start from `freetier2api_fork`.

## Integration boundary

The running CPA still uses the existing `freetier2api-v0.1.23.so` under
`/home/div/1_Project_dir/AI/CPA/CLIProxyAPI/logs/plugins/linux/amd64/`.
A source checkout is not an installed plugin. This migration neither replaces
that binary nor changes the plugin configuration.

Before future plugin delivery, inspect this version's build scripts, the running
host toolchain and plugin ABI, configuration migration needs and replacement
mechanism. Decide and test the build/load/rollback procedure separately. The
existing CPA Go-source supervisor does not establish plugin hot-reload support;
no plugin reload latency or in-process replacement guarantee is made here.
