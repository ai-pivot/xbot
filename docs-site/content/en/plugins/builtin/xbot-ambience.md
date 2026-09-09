---
title: "xbot.ambience — Web Ambience Layer"
weight: 3
---

# xbot.ambience

A **script-runtime** plugin (UI-only) that adds an ambience layer to the web UI:
wallpapers, glass (frosted) surfaces, an animated desk-pet widget and particle
overlays.

| | |
|--|--|
| **Plugin ID** | `xbot.ambience` |
| **Runtime** | `script` (no binary — manifest + web assets only) |
| **Permissions** | `ui` |
| **Contributes** | `ambience` (wallpapers, overlays), widgets, user settings |

## What it provides

- **Wallpapers** — CSS-gradient presets plus user uploads stored locally in
  IndexedDB (`xbot-ambience`). Images are downscaled to ≤1600 px on upload.
- **Glass surfaces** — the plugin overrides the app's `--bg-*` CSS variables
  with `color-mix(...)` values; opacity and blur are user-adjustable (blur
  defaults to 0 to avoid per-frame GPU re-compositing).
- **Desk pet** — a small widget whose mood follows the agent lifecycle:
  `turn.started → thinking`, `progress.iteration → working`,
  `turn.ended → done | sad`, idle 10 min → `sleeping`.
- **Particles** — optional star-dust overlay (disabled by default).

## Configuration

**Settings → Appearance → Ambience** — pick a wallpaper, adjust glass
opacity / blur, and optionally enable a per-session profile (a different
wallpaper per chat).

## Notes

- Asset uploads are **local to the browser** (IndexedDB) — the profile itself
  syncs through `user_settings`, so another device falls back to the plugin
  preset until you re-upload there.
- Pure UI contribution: registers no tools and needs no channel activation.

## See also

- [Plugin system overview](/plugins/)
- [Web plugin system](/plugins/web/)
