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
    "invite_only": true
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
| `invite_only` | ❌ | **`true`** | Disable self-registration. **Defaults to true (secure default)** — the first account can still register (first-user bootstrap); after that only an admin can create accounts. Set explicitly to `false` to re-open public registration. |

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

## Message Composer

Rich-text (WYSIWYG) editor with Markdown shortcuts (`**bold**`, `-` lists, etc.) and links:

| Feature | Description |
|---------|-------------|
| Links | Typing `https://…` followed by a space auto-links; pasted links and Markdown link syntax (`[text](URL)`) render live; links are highlighted in the theme accent color |
| Link editing | Select text to reveal a floating toolbar (bold/italic/strikethrough/inline code/link); `Ctrl/Cmd+K` adds or edits a link, one-click unlink |
| File upload | **Any file type is accepted** (no type whitelist). Click 📎, **paste** (screenshots upload automatically), or **drag-and-drop** files onto the composer to attach them |
| Upload limit | 10MB per file (size only — no type restrictions) |

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

### First-user bootstrap

A brand-new deployment has no accounts yet, so the first registration is
allowed — that account becomes the operator. The registration page says so
explicitly (*"First-time setup · one-time only"*) and states that registration
closes automatically afterwards, so a visitor never has to guess whether the
endpoint is open to everyone. Once the account exists, `/register` shows an
invite-only notice and the login page hides the register entry entirely.

## UI mode (Auto / Desktop / Mobile)

The web UI ships two shells — desktop and mobile — and picks one automatically
from the viewport width (≤767px → mobile shell). Narrow tablets, landscape
phones and small desktop windows sit right on that boundary, so you can pin the
shell manually under **Settings → Appearance → UI mode**:

| Mode | Behavior |
|------|----------|
| Auto (default) | Follows the viewport breakpoint (≤767px = mobile shell) |
| Desktop | Always use the desktop shell (multi-panel / Dockview layout) |
| Mobile | Always use the mobile shell (drawer sessions + bottom nav) |

The preference is stored locally (`localStorage`) and takes effect immediately;
it also syncs to the server (`web:ui:ui-mode`) so other browsers/devices keep
the same choice. It only switches the **shell** — CSS breakpoints are
unchanged, so forcing the desktop shell on a narrow screen yields a compressed
desktop layout.

## Persona isolation

When `persona_isolation` is `true`:

- Each web user's system persona is isolated from others
- User A's agent behavior settings do not affect User B
- Suitable for multi-tenant scenarios

## See also
- [Feishu Channel](/channels/feishu/) — team collaboration
- [CLI Channel](/channels/cli/) — terminal TUI
- [Configuration](/configuration/) — web channel settings
