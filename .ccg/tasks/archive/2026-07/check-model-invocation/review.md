# Check Model Invocation

## Scope

Checked the local CCG external model invocation chain only:

- `~/.claude/bin/codeagent-wrapper --backend claude`
- direct `claude -p`
- `~/.claude/bin/codeagent-wrapper --backend antigravity`
- direct `agy -p`

No CPA service files were modified and no Docker/VPS operation was performed.

## Evidence

### Claude wrapper

Command returned failure. Wrapper launched Claude but Claude exited with status 1.

Key output:

```text
Backend: claude
Command: claude -p --dangerously-skip-permissions ...
claude exited with status 1
```

### Direct Claude

Direct invocation also failed, so the failure is not caused by `codeagent-wrapper`.

Key output:

```text
Failed to authenticate. API Error: 403 用户已被封禁
```

### Antigravity wrapper

Wrapper reached `agy`, but Antigravity required Google OAuth login and timed out.

Key output:

```text
Authentication required. Please visit the URL to log in:
Waiting for authentication (timeout 30s)...
Error: authentication timed out.
```

### Direct agy

Direct invocation also failed due to authentication.

Key output:

```text
Error: authentication failed or timed out
```

## Result

- Claude model call: **not usable now**. Root cause shown by direct CLI: account/auth API returns `403 用户已被封禁`.
- Antigravity model call: **not usable now**. Root cause: local `agy` is not logged in / OAuth expired, and non-interactive auth timed out.
- `codeagent-wrapper` binary exists and can launch both backends, but both downstream model CLIs fail at authentication/account layer.
- This is not a CPA code/build issue.

## Next Action

- Claude: replace/repair the Claude account or auth used by the local Claude CLI, then rerun a smoke test.
- Antigravity: run interactive `agy` login/OAuth once, then rerun a smoke test.
