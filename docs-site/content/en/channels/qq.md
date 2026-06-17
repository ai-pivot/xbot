---
title: "QQ"
weight: 40
---

# QQ Channel

Connect xbot via the QQ official bot protocol. Supports private chats, group
chats, and channel messages.

**Requires Server mode.**

## Prerequisites

1. xbot installed and running in **Server mode**
2. A bot app registered on the [QQ Open Platform](https://q.qq.com)
3. App ID and Client Secret obtained
4. WebSocket connection mode enabled

## Configuration

```json
{
  "qq": {
    "enabled": true,
    "app_id": "your-app-id",
    "client_secret": "your-client-secret",
    "allow_from": []
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `enabled` | ✅ | `false` | Enable the QQ channel |
| `app_id` | ✅ | — | QQ Bot App ID |
| `client_secret` | ✅ | — | QQ Bot Client Secret |
| `allow_from` | ❌ | `[]` | Whitelist of user `openid` values; empty = allow all |

## Usage

- **Private chat**: send a message directly to the bot
- **Group chat**: @mention the bot + your question
- **QQ Channel**: @mention the bot in a channel

## Notes

- QQ channel does not support message updates (patching), so agent streaming
  rendering and progress notifications are not visible
- Markdown messages automatically degrade to plain text if sending fails
- Supports automatic reconnection with exponential backoff

## See also
- [NapCat Channel](/channels/napcat/) — OneBot 11 protocol
- [Feishu Channel](/channels/feishu/) — team collaboration
- [Configuration](/configuration/) — QQ channel settings
