---
title: "Built-in Plugins"
weight: 1
geekdocCollapseSection: true
---

xbot ships two built-in plugins in the repository under `plugins/`:

| Plugin ID | Directory | Type | Purpose |
|-----------|-----------|------|---------|
| `xbot.genui` | `plugins/xbot-genui/` | Go stdio **channel plugin** | The `display_html` tool — LLM-generated interactive UI (charts, 3D, animations) rendered as a streaming preview in the web chat |
| `xbot.git-fancy` | `plugins/xbot-git-fancy/` | Go stdio plugin | Fancy Git panel — branches, working-tree changes, paginated commit history, commit details, and a full-width Monaco diff tab |

Both are zero- or minimal-dependency Go binaries driven over JSON-on-stdio, so they are easy to build, audit, and replace.

## Installation

Two supported ways to use them:

**1. Install into the user plugin directory (production-style)**

```bash
# From the repository root: builds and installs BOTH plugins
make plugins-install
# → ~/.xbot/plugins/xbot.genui/ and ~/.xbot/plugins/xbot.git-fancy/
```

Reload to activate (or restart xbot):

```
tui_control(action=reload_plugins)
```

**2. Run directly from the repository checkout (development)**

```bash
make plugins-build
XBOT_PLUGIN_DIRS="$(pwd)/plugins" xbot
```

The `XBOT_PLUGIN_DIRS` environment variable is a path-separated list of
extra directories scanned during plugin discovery. Alternatively, list the
repository path permanently in `config.json`:

```json
{
  "plugins": {
    "enabled": true,
    "dirs": ["/path/to/xbot/plugins"]
  }
}
```

User-installed copies always take precedence: discovery dedups by plugin ID
and scans `~/.xbot/plugins/` first, so an installed copy shadows the
repository version.

## Discovery Mechanism

1. At startup, the agent creates the `PluginManager` (when `plugins.enabled`
   is true) and calls `Discover()` (see `agent/agent.go`).
2. `Discover()` scans the directories returned by
   `plugin.DefaultPluginDirs(xbotHome)` — `~/.xbot/plugins/`,
   `~/.xbot/plugins/builtin/`, plus any `XBOT_PLUGIN_DIRS` entries and the
   `plugins.dirs` entries from `config.json`.
3. Every subdirectory containing a valid `plugin.json` becomes a candidate
   plugin; duplicates (same manifest `id`) are skipped with a warning.
4. `ActivateAll()` then activates every discovered plugin whose
   `activationEvents` includes `onStart`.

## Channel Activation (GenUI)

The GenUI plugin additionally declares a **channel provider** (`genui`).
Channel plugin instances are only created when the channel is enabled in
`config.json`:

```json
{
  "channels": {
    "genui": { "enabled": "true" }
  }
}
```

Installing the plugin alone is NOT enough — without the `channels.genui`
entry, `IsEnabled` returns false and the `display_html` tool stays invisible
to the LLM.

## Plugin Pages

- [xbot-genui](./xbot-genui/) — GenUI (display_html): interactive UI generation
- [xbot-git-fancy](./xbot-git-fancy/) — Fancy Git panel
