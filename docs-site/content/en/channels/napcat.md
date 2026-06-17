---
title: "NapCat"
weight: 45
---

# NapCat Channel

Connect xbot via [NapCat](https://github.com/NapNeko/NapCatQQ), implementing
the OneBot 11 protocol. Compatible with any OneBot 11 implementation.

**Requires Server mode.**

## QQ channel vs. NapCat

| | QQ Channel | NapCat Channel |
|--|-----------|---------------|
| Protocol | QQ Official Bot API | OneBot 11 |
| Requires | QQ Open Platform registration | Running NapCat instance |
| Account | Bot application | Personal QQ account |
| Stability | Official support | Depends on NapCat |

## Prerequisites

1. xbot installed and running in **Server mode**
2. A running NapCat instance (via QQNT + NapCat plugin)
3. WebSocket server mode enabled in NapCat configuration

## Configuration

```json
{
  "napcat": {
    "enabled": true,
    "ws_url": "ws://127.0.0.1:3001",
    "token": "",
    "allow_from": []
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `enabled` | ✅ | `false` | Enable the NapCat channel |
| `ws_url` | ✅ | `"ws://localhost:3001"` | NapCat WebSocket URL |
| `token` | ❌ | `""` | Authentication token (if NapCat is configured with one) |
| `allow_from` | ❌ | `[]` | Whitelist of QQ numbers; empty = allow all |

## Usage

- **Private chat**: send a message to the bot's QQ number directly
- **Group chat**: @mention the bot's QQ number + your question

## Notes

- NapCat does not support message updates, so streaming rendering is not
  visible
- Supports sending images and files
- Supports automatic reconnection with exponential backoff (max 5 minutes)

## Troubleshooting

| Issue | Solution |
|-------|----------|
| WebSocket connection fails | Verify NapCat is running and the WS URL is correct |
| Messages not received | Check that the allow-list includes the sender's QQ number |
| Reconnection loops | Check network stability; NapCat has exponential backoff |
