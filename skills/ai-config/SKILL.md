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

Themes are JSON files in `~/.xbot/themes/<name>.json`. To create a custom theme:

1. **Check existing themes**: `Shell: ls ~/.xbot/themes/`
2. **Read a template**: `Shell: cat ~/.xbot/themes/default.json` (if exists)
3. **Create new theme**: `FileCreate` to `~/.xbot/themes/<name>.json` with the theme JSON
4. **Switch to new theme**: `tui_control set_theme("<name>")`

Theme JSON format:
```json
{
  "name": "my-theme",
  "colors": {
    "background": "#1a1b26",
    "foreground": "#a9b1d6",
    "cursor": "#c0caf5",
    "selection": "#33467c"
  }
}
```

If no existing theme files exist, themes are built-in. Use `tui_control set_theme("<name>")` to cycle through built-in themes. Use `config get("theme")` to check current.

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
