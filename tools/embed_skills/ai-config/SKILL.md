---
name: ai-config
description: "Guide for AI to configure xbot TUI, themes, subscriptions, and settings. Activate when the user asks to customize the TUI appearance, create themes, manage LLM subscriptions, or make bulk configuration changes."
---

# AI Config Guide

## Tool Summary

| Task | Tool | Example |
|------|------|---------|
| List all settings | `config list` | Shows keys, values, descriptions, permissions |
| Read a setting | `config get(key)` | `config get("theme")` |
| Change a setting | `config set(key, value)` | `config set("max_iterations", "50")` |
| Switch session | `tui_control switch_session(chat_id)` | |
| Switch theme | `tui_control set_theme(theme_name)` | `tui_control set_theme("ocean")` |
| Adjust layout | `tui_control set_layout(key, value)` | `tui_control set_layout("sidebar_width", "30")` |
| Execute command | `tui_control send_slash(command="/xxx")` | `/set-llm`, `/palette`, `/set-model`, `/context` |
| List subscriptions | `config action=subscriptions` or `config action=subscription sub=list` | |
| Create new session | `CreateChat(type=agent, role=explore, instance="name")` | |

## Model Management

| Task | Tool call | Notes |
|------|-----------|-------|
| List all models | `config action=model sub=list` | Shows all models across subscriptions with status (normal/offline/disabled) |
| Show current session model | `config action=model sub=active` | Returns sub_id + model of current session |
| Switch session model | `config action=model sub=switch sub_id=xxx model=yyy` | Per-session, doesn't affect other sessions |
| Set max context | `config action=model sub=set_context sub_id=xxx model=yyy value=131072` | Per-model max context tokens |
| Set max output | `config action=model sub=set_output sub_id=xxx model=yyy value=8192` | Per-model max output tokens |
| Enable model | `config action=model sub=enable sub_id=xxx model=yyy` | Makes model selectable |
| Disable model | `config action=model sub=disable sub_id=xxx model=yyy` | Greys out model |
| Add/register model | `config action=model sub=add sub_id=xxx model=yyy max_context=131072` | Register a model not in provider's list |
| Remove model | `config action=model sub=remove sub_id=xxx model=yyy` | Permanently delete model config |
| Refresh model list | `config action=model sub=refresh` | Live-fetch /models from all providers |

Use `config action=model sub=list` first to get `sub_id` and available `model` names.

**Note**: `set_context` and `set_output` only update the specified field — they do NOT
reset other per-model config (max_output, thinking_mode, api_type). Passing `max_output=0`
to `update` or `add` means "don't set" (0 is treated as "not provided"), not "clear to 0".

## Subscription Management

| Task | Tool call | Notes |
|------|-----------|-------|
| List subscriptions | `config action=subscription sub=list` | Same as `config action=subscriptions` |
| Add subscription | `config action=subscription sub=add name=xxx provider=openai api_key=sk-xxx model=gpt-4o` | Creates new LLM subscription |
| Remove subscription | `config action=subscription sub=remove sub_id=xxx` | Deletes subscription + its models |
| Update subscription | `config action=subscription sub=update sub_id=xxx api_key=newkey` | Only specified fields are changed |
| Set default | `config action=subscription sub=set_default sub_id=xxx` | User-level default for new sessions |
| Enable/disable | `config action=subscription sub=set_enabled sub_id=xxx value=false` | Disabled subs keep credentials |
| Rename | `config action=subscription sub=rename sub_id=xxx name=newname` | Rename subscription |

### Typical workflow: add a new LLM provider
```
config action=subscription sub=add name="deepseek" provider=openai base_url="https://api.deepseek.com" api_key="sk-xxx" model="deepseek-chat"
→ Returns subscription ID
config action=model sub=set_context sub_id=<id> model="deepseek-chat" value=131072
```

### Typical workflow: switch current session's model
```
config action=model sub=list
→ Find desired model + sub_id
config action=model sub=switch sub_id=<id> model=<model>
```

## Theme Creation

External themes are JSON files in `~/.xbot/themes/<name>.json`. The system loads them automatically when `setTheme` is called.

**Correct workflow:**
1. `FileCreate` the theme JSON to `~/.xbot/themes/<name>.json`
2. `tui_control set_theme("<name>")` to switch to it
3. Check `ThemeNames()` includes it (via `Shell: grep -r name ~/.xbot/themes/`)

**Minimal theme JSON** (only override colors you want to change):
```json
{
  "accent": "#ff6b6b",
  "surface": "#1a1a2e",
  "text_primary": "#e0e0e0"
}
```

All fields are optional; defaults fill the rest. Full field list: `text_primary`, `text_secondary`, `text_muted`, `fg_most_subtle`, `fg_guide`, `success`, `warning`, `error`, `info`, `accent`, `accent_alt`, `bar_filled`, `bar_empty`, `border`, `title_text`, `surface`, `bg_panel`, `gradient`, `error_bg`, `success_bg`, `warning_bg`, `info_bg`, `gdocument_text`, `gheading_text`, `gcode_block`, `gcode_text`, `glink_text`, `gblock_quote`, `glist_item`, `ghorizontal_rule`, `fg_bright`, `bg_hover`, `bg_inset`, `bg_overlay`, `success_muted`, `warning_muted`, `error_muted`, `info_muted`, `accent_start`, `accent_end`.

## Slash Commands via send_slash

`send_slash` injects the command into the input box as if the user typed it. The result arrives in the **next turn** — you can't see the output within the same turn.

| Command | Effect | Result timing |
|---------|--------|--------------|
| `/set-llm <sub-name> provider=X model=Y api_key=K` | Create/update named personal LLM subscription | Next turn |
| `/set-model <model>` | Switch model across subscriptions | Next turn |
| `/palette` | Open command palette for user | Immediate (UI) |
| `/context` | Show context usage bar | Immediate (UI) |
| `/new` | Start new chat session | Next turn |

For commands that open UI panels (`/palette`, `/context`), tell the user what will appear — you won't see the panel content.

## Bulk Configuration

To apply multiple settings at once:
1. `config list` to see all options
2. `config set(key, value)` for each change
3. Layout changes apply instantly; config changes persist on restart

Example "fancy" setup:
```
tui_control set_theme("ocean")
tui_control set_layout("sidebar_width", "25")
tui_control set_layout("chat_center", "true")
tui_control set_layout("layout_mode", "compact")
config set("language", "zh")
```
