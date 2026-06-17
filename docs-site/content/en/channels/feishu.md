---
title: "Feishu"
weight: 25
---

# Feishu (Lark) Channel

The most popular xbot channel. Team members @mention the bot in Feishu groups
to chat with the agent — no one needs to configure their own API key.

**Requires Server mode.**

## Prerequisites

1. xbot installed and running in **Server mode**
2. Developer access on the [Feishu Open Platform](https://open.feishu.cn)
3. A Feishu app (App ID and App Secret)

## Create a Feishu app

1. Open [Feishu Open Platform](https://open.feishu.cn) → Create a custom app
   (企业自建应用)
2. Note down the **App ID** and **App Secret**
3. Under **Event Subscription**, configure:
   - **Subscription mode:** select "Use long-lived connection to receive events"
     (uses WebSocket — no public IP required)
   - **Encrypt Key / Verification Token:** only needed if you enable encryption
     in the Feishu console; leave empty otherwise
4. Add event subscriptions:
   - `im.message.receive_v1` (receive messages)
5. Publish the app

## Minimum required permissions

Under **Permissions**, grant these minimum permissions:

| Permission | Scope | Purpose |
|------------|-------|---------|
| Send and receive chat messages | `im:message` | Send and receive messages |
| Receive messages | `im:message.receive_v1` | Receive user message events |
| Send messages as bot | `im:message:send_as_bot` | Send replies |
| Get user basic info | `contact:user.base:readonly` | Identify users |

{{< hint type=note >}}
The above are the **minimum** permissions. If you want the agent to operate on
Feishu Docs, Bitable, or other resources, you'll need additional permissions
(see below).
{{< /hint >}}

### Extended permissions (optional)

| Feature | Required permission |
|---------|-------------------|
| Upload images/files | `im:resource` |
| Operate on Docs | `docx:document`, `wiki:wiki` |
| Operate on Bitable | `bitable:app` |
| Get bot info | `bot:bot:get_bot_info` |

## Configuration

Add the Feishu config to `~/.xbot/config.json`:

### Minimal config

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxxxxxxxxxxxxxxx",
    "app_secret": "your-app-secret"
  }
}
```

### Full config

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxxxxxxxxxxxxxxx",
    "app_secret": "your-app-secret",
    "encrypt_key": "your-encrypt-key",
    "verification_token": "your-verification-token",
    "domain": "open.feishu.cn",
    "allow_from": []
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `enabled` | ✅ | `false` | Enable the Feishu channel |
| `app_id` | ✅ | — | Feishu App ID |
| `app_secret` | ✅ | — | Feishu App Secret |
| `encrypt_key` | ❌ | `""` | Event encryption key (only if encryption is enabled in the Feishu console) |
| `verification_token` | ❌ | `""` | Verification token (only if encryption is enabled) |
| `domain` | ❌ | `"open.feishu.cn"` | Feishu API domain. Set to `"open.larksuite.com"` for Lark international, or your private deployment host |
| `allow_from` | ❌ | `[]` | Whitelist of user `open_id` values; empty = allow all |

## Global LLM: share an API key with your whole team

This is the most common way to use the Feishu channel: **the admin configures
the LLM once, and all Feishu users use it directly.**

### Setup

1. Configure a global LLM in `~/.xbot/config.json`:

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  },
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxxxxxxxxxxxxxxx",
    "app_secret": "xxxxxxxxxxxxxxxx"
  }
}
```

2. Start the server: `xbot-cli serve`

3. Team members @mention the bot in a Feishu group chat — no additional
   configuration needed.

### LLM resolution logic

- Users without a personal subscription → use the global `llm` config
- The admin can create per-user subscriptions (overrides the global config)
- The global LLM API key is stored encrypted in the database

## Usage

### Private chat

Send a message directly to the bot.

### Group chat

@mention the bot in a group + your question. A bare @mention with no text is
ignored.

### Interactive cards

The agent can send interactive message cards (settings panels, confirmation
dialogs, etc.). Users click buttons on the card to interact.

## Network requirements

xbot connects to Feishu servers via **WebSocket** (not HTTP callbacks), so:

- The server needs **outbound** access to the Feishu WebSocket endpoint
- **No public IP or inbound port** is needed (unlike HTTP callback mode)
- Works behind NAT/firewalls as long as outbound internet is available

## Troubleshooting

**Q: Feishu channel doesn't work?**

- Verify `feishu.enabled` is `true`
- Verify the App ID and App Secret are correct
- Verify the server is running: `xbot-cli serve`
- Check logs: `~/.xbot/logs/`

**Q: The bot doesn't receive messages?**

- Confirm `im.message.receive_v1` event is subscribed
- Confirm the app is published
- Group chats require @mentioning the bot

**Q: Users see "no LLM configured"?**

- Verify `llm.api_key` is set in `config.json`
- The global LLM config is shared by all users without personal subscriptions
