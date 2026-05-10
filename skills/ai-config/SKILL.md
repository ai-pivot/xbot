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
| Execute command | `tui_control send_slash(command="/xxx")` | `/set-llm`, `/palette`, `/model`, `/context` |
| List subscriptions | `config subscriptions` | |
| Create new session | `CreateChat(type=agent, role=explore, instance="name")` | |

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
| `/set-llm provider=X model=Y` | Change LLM subscription | Next turn |
| `/model` | Cycle to next model | Immediate (UI) |
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
