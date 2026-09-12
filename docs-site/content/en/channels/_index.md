---
title: "Channels"
weight: 20
---

# Channels

xbot receives and sends messages through **channels** — pluggable adapters that
connect the same Agent engine to different platforms. Configure them once, and
your team reaches the agent through the tools they already use.

## Channel comparison

| Channel | Best for | Connection | Highlights |
|---------|----------|------------|------------|
| [CLI](/channels/cli/) | Developers, power users | Local process / WebSocket | Full TUI, streaming output, mouse, themes |
| [Feishu](/channels/feishu/) | Team collaboration | WebSocket (long-lived) | @mention in groups, interactive cards, Feishu API |
| [QQ](/channels/qq/) | QQ users | WebSocket | Official QQ Bot protocol |
| [NapCat](/channels/napcat/) | Individuals, small circles | WebSocket | OneBot 11 protocol, personal QQ account |
| [Web](/channels/web/) | Anyone with a browser | HTTP + WebSocket | Web chat, registration/login, invite-only |

## Enabling channels

All channels are enabled through `~/.xbot/config.json`. No environment
variables are required.

{{< hint type=tip >}}
**Visual configuration in the TUI:** Type `/channel` to open the channel
settings panel. You can toggle channels on/off and edit their parameters
graphically — changes are written directly to `config.json`. This is especially
useful in Remote mode, since you don't need to SSH into the server to edit
config files.

**Visual configuration in the Web UI:** Settings → Channels lists every channel —
built-ins (web/feishu/qq/napcat) and **channels registered by plugins** are
managed in one place (enable toggle + field editing, hot-restarted on save). The
Feishu card also offers a **one-click "bind Feishu agent app"** button: it returns
the official Feishu authorization link, and confirming it in Feishu grants the
current app the agent permissions, event subscriptions and card callback
(including "Create and update cards", required by the streaming progress card);
the credentials are written back to `config.json` automatically.
{{< /hint >}}

{{< hint type=important >}}
**Feishu, QQ, NapCat, and Web channels require Server mode.** Standalone mode
only supports CLI.
{{< /hint >}}

## Choosing channels

- **Quick personal trial** → CLI (Standalone mode)
- **Team uses Feishu** → Server mode + Feishu channel
- **Friends or QQ groups** → Server mode + NapCat channel
- **Public web chat** → Server mode + Web channel
- **Multi-channel simultaneously** → Server mode + enable multiple channels

## Access control

All channels support an `allow_from` whitelist:

- **Leave empty** (default): anyone can talk to the agent
- **Set a list**: only listed users can interact

Feishu uses `open_id`, QQ uses `openid`, NapCat uses QQ numbers.
