# Plugin HTTP egress and single-model diagnostics

The RPC callback scope retains the HTTP client supplied for the selected auth.
This applies to execution, streaming, auth model discovery, quota, refresh and
asynchronous HTTP operations. An existing bound client wins; otherwise a plugin
management callback may supply `auth_index`. The host verifies the credential's
provider against the calling plugin and rejects disabled or unrelated auths.
Calls without a selected auth retain the existing global-proxy behavior.

FreeTier's model probe reuses `host.model.execute`, production model aliases and
the selected runtime auth ID. Its internal probe header is consumed by the plugin
and removed before contacting the upstream. Diagnostic errors preserve the raw
status/body and carry optional `request_stop: true` in the error envelope. The
RPC error implements the existing `IsRequestStop` contract: one attempt, no pool
rotation, and no credential availability penalty. Ordinary classified errors
keep their existing retry and cooldown behavior. Both plugin and host versions
from this delivery are required for the no-replay guarantee; older hosts ignore
the optional field. No ABI function or schema version changes are required.

The plugin browser cancels its own diagnostic fetch after 90 seconds and unlocks
its controls. Caller cancellation remains attached to the management callback;
no new production model or post-output timeout is introduced.

## Local configuration maintenance

The installed CPA uses a single-file bind mount for `config.yaml`. Replacing its
host inode does not replace the inode already mounted inside the container. Use
the management API for runtime changes and compare the API, container file and
host file afterward. V8 grouped YAML stores the global proxy at
`requests.proxy-url`; the legacy management API still exposes `/proxy-url`.
Preserve current operator changes before syncing files or recreating a bind.
Ordinary source hot reload replaces only the CPA process, not the container.

The local managed split proxy is `cpa-split-proxy.service` on 7891. Qoder's
explicit domain routes use direct egress; other traffic retains its configured
route through the existing 7890 forwarder. This does not change browser login
routing or the separate regmail proxy on 41001.
