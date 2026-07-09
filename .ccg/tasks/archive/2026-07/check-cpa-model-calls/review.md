# Check WSL CPA Model Calls

## Scope

Checked the WSL-local CPA service, not the CCG external model wrapper.
No CPA code, Docker container, VPS, or config was changed.

## Runtime

- Workspace: `/home/div/1_Project_dir/AI/CLIProxyAPI`
- Branch: `CPA-fork`
- Container: `cli-proxy-api`
- Image: `local/cli-proxy-api-cpa-fork:live`
- Network mode: `host`
- Mounted paths:
  - `./config.yaml` -> `/CLIProxyAPI/config.yaml`
  - `./auths` -> `/root/.cli-proxy-api`
  - `./logs` -> `/CLIProxyAPI/logs`
  - `./static` -> `/CLIProxyAPI/static`
- Served management page: `GET http://127.0.0.1:8317/management.html` returns 200.

## Config

`config.yaml` has:

- `port: 8317`
- `logging-to-file: true`
- `request-log: true`
- `proxy-url: http://127.0.0.1:7890`

The container environment also uses `HTTP_PROXY/HTTPS_PROXY=http://127.0.0.1:7890` because it runs with host networking.

## Model-call evidence from access log

Latest successful model calls in `logs/main.log`:

```text
[2026-07-10 05:31:45] [d7b2fae9] [info ] 200 | 1m29s | 100.87.120.65 | POST "/v1/chat/completions" | model=grok4.2
[2026-07-10 05:32:02] [48c133b6] [info ] 200 | 2m6s | 100.87.120.65 | POST "/v1/chat/completions" | model=grok4.3
```

Latest failed model calls:

```text
[2026-07-09 21:45:58..21:46:13] POST "/v1/chat/completions" | model=gpt-5.4 -> 502
```

The raw request logs show the failure reason:

```text
unknown provider for model gpt-5.4
```

So `gpt-5.4` failed because the request used a model name that this CPA config cannot route to any provider. This is a route/model-alias configuration issue, not a request logging issue.

## Request-log structured list

`logs/request_logs.db` existed but was last written on 2026-07-08 before the management request-log endpoint was accessed.
Calling the management endpoint triggered the normal on-demand SQLite sync:

```text
GET /v0/management/request-logs?limit=5&offset=0 -> 200
storage=sqlite total=163 retention_days=7
```

Top rows after sync:

1. `grok4.2` -> provider `白鸽-grok`, upstream model `grok-4.20-multi-agent-xhigh`, status 200
2. `grok4.3` -> provider `Joverna-Grok`, upstream model `grok-4.3-high`, status 200
3. `gpt-5.4` -> status 502, error `unknown provider for model gpt-5.4`

Conclusion: raw request logs are being written; the structured SQLite list updates when the management request-log API is opened/refreshed.

## Code path for model display

The model name in access logs is wired correctly:

- `sdk/api/handlers/handlers.go` calls `markGinRequestModel(ctx, originalRequestedModel)` in common execution paths.
- `markGinRequestModel` calls `logging.SetGinRequestModel`.
- `internal/logging/gin_logger.go` appends `| model=<name>` to the clean access-log row.

This matches the requested behavior for `日志查看 -> 日志内容`: model API requests show the client-requested model.

## Current conclusion

- CPA model calls in WSL are working for configured aliases like `grok4.2` and `grok4.3`.
- CPA records the requested model in `logs/main.log`.
- CPA raw request logs are generated under `logs/v1-*.log`.
- The parsed request-log DB/list can show model/provider/upstream model after its endpoint syncs.
- The observed `gpt-5.4` failures are because `gpt-5.4` is currently not mapped to a provider in this local CPA config.
