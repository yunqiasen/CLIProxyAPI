# Local Fork Delivery

Every completed code-change task includes tests, relevant documentation, required
review, a scoped **local Git commit**, and verification of the running CPA. Execute
these steps automatically, without another user reminder. Do not auto-push.

## Local code hot reload

The local development service uses:

- Branch/workspace: `CPA-fork`, `/home/div/1_Project_dir/AI/CLIProxyAPI`.
- Docker endpoint: verify the current context; normally `unix:///var/run/docker.sock`.
- Compose project/service/container: `cliproxyapi` / `cli-proxy-api`.
- Base configuration: machine-local `docker-compose.local.yml`.
- Development overlay: tracked `docker-compose.hot-reload.yml`.
- Development image: `local/cli-proxy-api-cpa-fork:hot-reload`.
- Read-only source mount: primary checkout at `/workspace`.
- Existing config/auth/log/static mounts, proxy environment, host network and
  restart policy are inherited unchanged. Two named volumes retain Go caches.

The development image contains the Go toolchain and a small Python supervisor.
No extra model/provider calls or third-party watcher are needed. The supervisor
fingerprints Go files and embedded JSON/text/template assets under `cmd`,
`internal`, and `sdk`, plus `go.mod`, `go.sum`, the build script and Git HEAD.
It deliberately does not scan runtime logs, credentials, `.git` objects or the
management bundle. Config/auth updates retain CPA's existing hot loader; updating
mounted `static/management.html` only needs a browser refresh.

After a stable source change, compilation runs while the previous CPA process
continues serving. A successful build is published atomically, then the supervisor
signals the old process and starts the new one. Compilation failure leaves the
old process and binary intact. A save during compilation triggers a fresh build
before process replacement. The container ID stays unchanged.

This is **automatic compile-and-process-restart**, not in-process code patching.
The process switch can briefly interrupt active streams. Initial migration to this
runtime requires a one-time container recreation. Toolchain, supervisor, image,
OS dependency, Compose or plugin ABI changes also require a controlled rebuild;
normal Go source edits do not. Keep plugin builds compatible with the toolchain.

## One-time setup or runtime-image update

Inspect the current container first and privately save its image, Compose labels,
mounts and environment. Preserve the machine-local base Compose file; never replace
it with the stock upstream file. Save the prior image under a rollback tag.

Build from a Git archive so runtime credentials and untracked files stay outside
the build context. The existing host proxy is inherited only as build arguments:

```sh
COMMIT=$(git rev-parse HEAD)
OLD_IMAGE=$(docker inspect -f '{{.Image}}' cli-proxy-api)
docker image tag "$OLD_IMAGE" "local/cli-proxy-api-cpa-fork:rollback-$(date -u +%Y%m%dT%H%M%SZ)"
git archive "$COMMIT" | docker build --network=host \
  --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY \
  -f Dockerfile.hot-reload -t local/cli-proxy-api-cpa-fork:hot-reload -
docker compose -p cliproxyapi -f docker-compose.local.yml \
  -f docker-compose.hot-reload.yml up -d --no-deps --no-build --pull never cli-proxy-api
```

The first source compilation may take longer while the caches are cold. Wait for
`[hot-reload] process replaced` and successful API checks, not just Docker's
`running` state. The first image setup should be verified on a separate local
container before replacing the existing service.

The supervisor exits on an unexpected CPA process exit so the inherited container
restart policy can act. A runtime startup regression still needs investigation and
rollback; compilation success alone does not certify delivery.

## Every code-change task

1. Inspect the task diff and preserve unrelated work. Run focused tests, the full
   suite and appropriate race checks/build verification. Use an isolated worktree
   for full tests when runtime log permissions interfere; do not change log data.
2. Update corresponding docs and finish the active workflow's required review.
3. Apply the verified files to primary `CPA-fork`. Stage only this task's changes
   (use explicit staging for ignored Markdown), and **commit automatically**.
   The watcher does not run `git add`, `git commit` or `git push`.
4. Wait for automatic recompilation. Git HEAD itself is watched: a commit updates
   build metadata even if the source already hot-reloaded before that commit.
5. Verify `X-CPA-COMMIT` equals the exact local commit, the authenticated API and
   management endpoint respond, the served panel matches the mounted bundle, and
   the changed path works. Confirm the container ID has not changed during normal
   source reload. Retain useful logs outside Git without exposing credentials.

Source-dirty builds carry `<commit>-dirty` metadata. They are development feedback,
not completed delivery. A failed build is logged once per source state; fix/save
source or create the intended commit to trigger another attempt. Roll back code
with a deliberate local revert when appropriate, not by discarding unrelated work.

Test the watcher/build public process contract without Docker:

```sh
python3 -m unittest discover -s test -p local_hot_reload_test.py -v
```

This compiles a real small Go HTTP service, changes its source, observes the new
response and child PID, verifies a broken build keeps the old process available,
then commits and checks clean revision metadata without restarting the supervisor.

## Runtime rollback

For a previous development runtime image, retag the saved image as
`local/cli-proxy-api-cpa-fork:hot-reload` and recreate the same service with both
Compose files. For rollback from initial hot-reload setup to the earlier immutable
runtime, restore the saved image tag expected by the unchanged base Compose file
and recreate using **only** `docker-compose.local.yml`.

Preserve config, credentials, raw logs, static assets and caches. Do not use
`down -v`, clear volumes or replace unrelated services. Recheck API, panel and
version after rollback.

## Production releases

The original `Dockerfile` is unchanged: production images contain the compiled
server, not the development watcher/toolchain. Build immutable images from the
exact release commit with `VERSION`, `COMMIT`, and `BUILD_DATE` build arguments;
updates then require service recreation. Local hot reload does not authorize
Git push, GitHub releases, remote image publication or VPS deployment.
