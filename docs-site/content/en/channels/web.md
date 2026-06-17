---
title: "Web"
weight: 35
---

# Web Channel

Browser-based chat interface. Supports user registration/login, invite-only
mode, and persona isolation.

**Requires Server mode.**

## Configuration

```json
{
  "web": {
    "enable": true,
    "host": "",
    "port": 8082,
    "static_dir": "",
    "upload_dir": "",
    "persona_isolation": false,
    "invite_only": false
  },
  "admin": {
    "token": "your-secret-token"
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `enable` | ✅ | `false` | Enable the Web channel |
| `host` | ❌ | `""` | Listen address (empty = all interfaces) |
| `port` | ❌ | `0` | Listen port |
| `static_dir` | ❌ | auto-detected | Path to the frontend static files directory |
| `upload_dir` | ❌ | `""` | Custom directory for uploaded files |
| `persona_isolation` | ❌ | `false` | Isolate each web user's persona from others |
| `invite_only` | ❌ | `false` | Disable self-registration; only admin can create accounts |

{{< hint type=warning >}}
The JSON key is `enable` (not `enabled`), unlike other channels.
{{< /hint >}}

## Web UI installation

In Server mode, the installer automatically downloads the Web UI to
`~/.xbot/web/dist/`.

For manual installation, download the web release archive and extract it to
`~/.xbot/web/dist/`.

## Access

After starting the server, open `http://your-server:8082` in a browser.

## Authentication

| Method | Description |
|--------|-------------|
| Username / password | Register and login; session cookies valid for 30 days |
| CLI token | WebSocket connection using the admin token |
| Feishu login | One-click login / link via Feishu account |

## Invite-only mode

When `invite_only` is `true`:

- New users cannot self-register (receives 403)
- The admin can create accounts via Feishu admin commands or direct database
  operations
- Suitable for internal team use

## Persona isolation

When `persona_isolation` is `true`:

- Each web user's system persona is isolated from others
- User A's agent behavior settings do not affect User B
- Suitable for multi-tenant scenarios
