# Local Fork Delivery

Code changes in this workspace are delivered through tests, documentation, review,
a local commit, a rebuilt local CPA fork image, and live verification. Local
commit and local container update are automatic parts of code delivery unless
the current task explicitly pauses them. Remote publishing is a separate action.

## Identify the existing service

The verified local checkout uses:

- Branch: `CPA-fork`.
- Docker endpoint: `unix:///var/run/docker.sock`.
- Compose project/service: `cliproxyapi` / `cli-proxy-api`.
- Compose file: `docker-compose.local.yml`.
- Image: `local/cli-proxy-api-cpa-fork:live`.
- Host networking and bind mounts for `config.yaml`, `auths/`, `logs/`, and `static/`.

Recheck the Docker context and container labels before each update. Preserve its
mounts, plugin files, network, proxy environment, and restart policy. The local
Compose override is machine-specific; retain it rather than replacing it with
the stock `docker-compose.yml`, whose image/pull policy is different.

## Verify and commit

1. Inspect the full task diff, including new files. Preserve unrelated work.
2. Run focused tests, the full suite, relevant race checks, and the server build.
   Use an isolated source worktree for full tests when the runtime checkout has
   root-owned log files; record that distinction without changing runtime data.
3. Update corresponding docs. Complete reviews required by the active workflow,
   including both the configured frontend model and Claude when specified.
4. Stage only the intended source, tests, and docs, and create the local commit.
   New docs covered by `docs/*` need explicit staging. Leave credentials,
   runtime configuration, logs, binaries, and unrelated investigation files out.

A local commit is not a request to push, publish a release/image, or deploy a VPS.

## Build the exact committed fork

Keep the running image available for rollback, and build from a Git archive so
untracked credentials and runtime files stay outside the Docker build context:

```sh
cd /home/div/1_Project_dir/AI/CLIProxyAPI
COMMIT=$(git rev-parse HEAD)
SHORT_COMMIT=$(git rev-parse --short=12 HEAD)
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE=local/cli-proxy-api-cpa-fork:live
ROLLBACK_IMAGE=local/cli-proxy-api-cpa-fork:rollback-$(date -u +%Y%m%dT%H%M%SZ)
OLD_IMAGE=$(docker inspect -f '{{.Image}}' cli-proxy-api)
docker image tag "$OLD_IMAGE" "$ROLLBACK_IMAGE"

git archive --format=tar "$COMMIT" | docker build \
  --build-arg VERSION="CPA-fork-$SHORT_COMMIT" \
  --build-arg COMMIT="$COMMIT" \
  --build-arg BUILD_DATE="$BUILD_DATE" \
  --tag "$IMAGE" -
```

Use the repository Dockerfile and its CGO-enabled build. Verify the installed
plugins' ABI and runtime-library requirements before changing the toolchain or
base image. Build failure leaves the running container unchanged. Confirm the
new image exists before recreating it.

## Update and verify

Use the verified local Compose project and override. `--no-build` uses the image
built from the exact commit above; `--pull never` preserves that local image:

```sh
docker compose -p cliproxyapi -f docker-compose.local.yml \
  up -d --no-deps --no-build --pull never cli-proxy-api
```

Recreation briefly interrupts active requests. Keep all data mounts and the prior
image; stop only the CPA service during its replacement.

Verify all of the following before reporting delivery:

- The container is running without a restart loop and uses the newly built image.
- The authenticated local management response includes `X-CPA-COMMIT` equal to
  the committed revision and the expected fork version/build date.
- The local API answers and an authenticated `/v1/models` request succeeds.
- `/management.html` is reachable and matches the intended fork UI bundle.
- A small request through the changed protocol completes correctly; inspect the
  final SSE event as well as HTTP status, and check the indexed log outcome.
- Existing mounts, network, environment, credential files, and plugin loading
  remain consistent. Normal runtime logging/credential refresh may continue.

Read locally stored credentials only for these checks; keep them out of command
output, shell history, Git, and reports. Avoid printing full configuration or raw
request-log contents when a status, hash, count, or revision header is enough.

## Roll back a failed runtime update

Retag the recorded old image as the same local `live` image, recreate only the
same service with the same Compose command, and repeat availability checks:

```sh
docker image tag "$ROLLBACK_IMAGE" "$IMAGE"
docker compose -p cliproxyapi -f docker-compose.local.yml \
  up -d --no-deps --no-build --pull never cli-proxy-api
```

Keep the delivery commit and evidence for investigation. Database volumes, raw
logs, configuration, and credentials stay in place during either direction.
