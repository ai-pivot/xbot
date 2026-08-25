---
title: "xbot-git-fancy — Fancy Git Panel"
weight: 3
---

The Git Fancy plugin is a fancy Git panel for the web UI: branches, working-tree changes with ±line stats, paginated commit history, commit details, and a full-width Monaco diff tab. It follows the VSCode "editor view" semantics — diffs open in the main editor area as dynamic tabs.

The backend (`plugins/xbot-git-fancy/`) is a pure stdio IPC Go plugin: xbot spawns the binary and drives it via JSON-over-stdio (`protocol.Run`). All git commands are **read-only** and execute in the session's working directory (injected by the server as `params.cwd`); the plugin itself is stateless.

## Features

- **Status panel** (right sidebar) — current branch, clean/dirty state, ahead/behind, working-tree changes (path, status, ±lines)
- **Commit history** — paginated (`skip`/`limit` + total count, "load more" supported)
- **Commit details** — author, email, ISO date, message, touched files with numstat ±line counts
- **Diff tab** — full-width Monaco `DiffEditor` for any changed file (working tree vs HEAD, or commit-scoped via `git show`); original/modified file content served for the native diff view
- **Branch list** — current + local branches
- **Safe by design** — `GIT_OPTIONAL_LOCKS=0` on every command; no writes, no mutations

## Installation

```bash
cd plugins/xbot-git-fancy
make build          # → bin/git-fancy-plugin
make install        # → ~/.xbot/plugins/xbot.git-fancy/
```

Or from the repository root: `make plugins-install`.
Dev mode without install: `make plugins-build` + `XBOT_PLUGIN_DIRS="$(pwd)/plugins" xbot`.

The frontend views (`web/src/plugins/git-fancy/`: `index.tsx`, `commit.tsx`,
`shared.tsx`) are bundled into the web build — the `plugin.json` web
declaration (`entry`, view contributions) is what registers them with the
frontend plugin runtime. The backend binary serves the panel's data via RPC.

## Configuration

The plugin declares a configuration schema in `plugin.json` (editable from
the plugin manager panel):

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `defaultLogLimit` | number | 10 | Default number of commits loaded by the log panel (1–100) |
| `showDiffStats` | boolean | true | Show numstat ±line statistics in commit details |

## Architecture

```text
web Git panel (right sidebar)
  → ctx.rpc.call('xbot.git-fancy.status', { cwd })
  → xbot web_plugin_rpc handler → stdio RPC to the git-fancy process
  → git command executed in session CWD (read-only)
  → structured JSON result → panel render

commit click → openViewTab('xbot.git-fancy.commit', { hash })
  → commit view (main editor area, dynamic tab)
  → ctx.ui.openFileTab(path) / openViewTab(diff) → Monaco DiffEditor
```

### Manifest

```json
{
  "id": "xbot.git-fancy",
  "runtime": "stdio",
  "entry": "./bin/git-fancy-plugin",
  "activationEvents": ["onStart"],
  "permissions": ["rpc", "ui"],
  "web": {
    "entry": "index.js",
    "contributes": [
      { "kind": "view", "id": "xbot.git-fancy.panel", "container": "right_sidebar", "title": "Git" },
      { "kind": "view", "id": "xbot.git-fancy.commit", "container": "main", "title": "Commit", "dynamic": true }
    ]
  }
}
```

Note the `permissions` array: the frontend plugin context is built from it —
the `ui` permission is what makes `ctx.ui.openViewTab` available. A manifest
that declares `container:"main"` views but omits `"ui"` will fail silently
(the open-diff-tab click does nothing). This is guarded by
`plugins/xbot-git-fancy/main_test.go` (`TestManifestPermissions`).

### RPC Methods (all read-only)

| Method | Params | Returns |
|--------|--------|---------|
| `status` | `cwd` | `repo`, `branch`, `clean`, `changes[]` (path/status/±lines), `ahead`, `behind` |
| `log` | `cwd`, `limit` (≤100), `skip` | `commits[]` (hash/author/when/subject), `total` |
| `commit` | `cwd`, `hash` | full hash, author, email, ISO date, message, `files[]` (path/status/±lines) |
| `diff` | `cwd`, `path`, `commit?` | unified diff, line-level parse (add/del/ctx/hunk with line numbers), `original`/`modified` content, add/del counts |
| `branches` | `cwd` | current branch + sorted local branch list |

`cwd` is injected by the server's `web_plugin_rpc` handler from the session's
current directory — the plugin never guesses the user's workspace.

### Diff Parsing

`parseUnifiedDiff` converts `git diff` output into line-level entries
(`hunk`/`add`/`del`/`ctx`/`meta`) with old/new line numbers, so the frontend
can render VSCode-style ± coloring. Untracked files are rendered as
all-additions (original = empty). Commit-scoped diffs use `git show
<commit>:<path>` for both sides; a missing side (added/deleted file, root
commit) yields an empty string instead of an error.

## Testing

```bash
cd plugins/xbot-git-fancy
go test ./...
```

## Source Files

| File | Role |
|------|------|
| `main.go` | Stdio `protocol.Run` loop; git command wrappers; diff parser |
| `plugin.json` | Manifest: id `xbot.git-fancy`, config schema, web view contributions |
| `main_test.go` | Unit tests (manifest permissions, RPC handlers) |
| `Makefile` | build / test / install / clean targets |
