---
title: "Sandbox"
weight: 45
---

# Sandbox Guide

## Overview

xbot supports multiple sandbox modes to control the isolation level when the agent executes shell commands.

| Mode | Description | Best For |
|------|-------------|----------|
| `none` | No isolation, execute directly on the host | Personal dev machine, running inside Docker |
| `docker` | One isolated Docker container per user | Multi-user servers |

## Configuration

Configure in `~/.xbot/config.json`:

```json
{
  "sandbox": {
    "mode": "none"
  }
}
```

### SandboxConfig Reference

The sandbox config struct (`config/config.go`):

```go
type SandboxConfig struct {
    Mode       string   `json:"mode"`        // sandbox mode: "none" (default) or "remote"
    RemoteMode  string   `json:"remote_mode"`  // remote sandbox mode
    IdleTimeout Duration `json:"idle_timeout"` // idle timeout before auto-destroy
    WSPort      int      `json:"ws_port"`      // WebSocket port for remote sandbox
    AuthToken   string   `json:"auth_token"`   // Runner authentication token
    PublicURL   string   `json:"public_url"`   // Public URL for runner connection
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `mode` | `"none"` | Sandbox mode: `"none"` or `"remote"` |
| `remote_mode` | `""` | Remote sandbox mode |
| `idle_timeout` | `"30m"` | Idle timeout (`0` = never auto-destroy) |
| `ws_port` | `8080` | WebSocket port for remote sandbox connections |
| `auth_token` | `""` | Shared authentication token for runners |
| `public_url` | `""` | Public URL runners use to connect |

### none Mode (Recommended for Personal Use)

Commands execute directly on the host machine. On Windows, PowerShell is used.

```json
{
  "sandbox": {
    "mode": "none"
  }
}
```

{{< hint type=warning >}}
**No isolation means the agent can execute any command your current user has permission to run.** Ensure you trust the agent's behavior. Use sandboxing on shared or production servers.
{{< /hint >}}

### Remote sandbox (the recommended isolation)

{{< hint type=warning >}}
**The local Docker sandbox was removed entirely on 2026-09-16** (sandboxing now goes through a Runner). For isolation use the remote Runner below — commands run on **your own machine**; the server no longer manages containers. The default `mode` is `"none"` (local).
{{< /hint >}}

### Remote Sandbox

Users can connect their own remote runners to execute commands on their own machines. Configure via the CLI `/settings` panel.

**How it works**:

1. A **runner** (`xbot-runner`) connects to the xbot server over WebSocket
2. Authentication uses a shared token (`auth_token`), verified with `subtle.ConstantTimeCompare`
3. The server routes tool execution to the runner's machine
4. Supports stdio streaming output and runner-local LLM models

**Server-side configuration:**

```json
{
  "sandbox": {
    "remote_mode": "remote",
    "auth_token": "your-secure-token",
    "ws_port": 8080,
    "public_url": "ws://your-server.com:8080"
  }
}
```

**Runner-side** (on the managed machine):

```bash
xbot-runner --server ws://your-server.com:8080/ws --token your-secure-token --name my-runner
```

`--name` is how the machine is identified; the server resolves ownership through
the one-shot connect token, so the name the runner reports and the name in the
registry stay in sync.

**Routing rules** (per session — there is exactly one operator):

| Session state | Sandbox used |
|------------------------|-------------|
| Bound to runner R, R online | R's RemoteSandbox |
| Bound to runner R, R **offline** | **Hard failure** — tools refuse to run (never a silent local fallback) |
| Not bound | None (local host) |

Bind a session with the `config` tool (`runner` action, `sub=switch name=...`;
`sub=unbind` goes back to the local host) or over RPC
(`runner_session_set` / `runner_session_get`).

{{< hint type=tip >}}
**Multiple machines, one operator**: any number of runners can connect at once,
each with its own name and token. Bindings are per session, so two sessions can
work on two different machines simultaneously. Nothing is keyed by user — see
`docs/design/runner-ssh-provisioning.md`.

**Provisioning**: the built-in `xbot.ssh-runner` plugin (`plugins/xbot-ssh-runner/`)
takes an SSH command, probes the machine, installs and starts `xbot-runner`, and
registers it — no manual steps on the remote host.
{{< /hint >}}

### SandboxRouter Architecture

`SandboxRouter` (`tools/sandbox_router.go`) is the unified sandbox entry point. It routes execution requests to different backends based on per-user configuration:

- Implements both `Sandbox` and `SandboxResolver` interfaces
- Only two backends: Remote (Runner) and None (local; the default)
- Routes independently per user — different users can use different backends
- Runner selection is configurable per user via the settings panel

## Sync Configuration

When using remote runners, xbot can sync the `skills/` and `agents/` directories from the server to the runner. This ensures the runner has access to the same tools and agent definitions.

Enable sync in the runner settings via `/settings` → Runner panel.

## See also
- [Configuration](/configuration/) — sandbox config fields
- [Permission Control](/guides/permission-control/) — access control
- [Development Guide](/development/) — project structure
