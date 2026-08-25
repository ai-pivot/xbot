---
title: "Configuration"
weight: 11
---

Give your plugin user-editable settings with declarative schema, defaults, persistence, and change notifications. The config store is `plugin/config.go`; the reference implementation is `xbot.git-fancy` (`plugins/xbot-git-fancy/plugin.json`), whose "默认 Commit 条数" and "显示 Diff 变更统计" settings appear in the web UI.

## Step 1: Declare the schema in plugin.json

```json
{
  "id": "xbot.git-fancy",
  "contributes": {
    "configuration": {
      "title": "Git Fancy",
      "properties": {
        "defaultLogLimit": {
          "type": "number",
          "label": "默认 Commit 条数",
          "description": "Git 日志面板默认加载的 commit 数量（log limit 默认值）",
          "default": 10,
          "minimum": 1,
          "maximum": 100
        },
        "showDiffStats": {
          "type": "boolean",
          "label": "显示 Diff 变更统计",
          "description": "commit 详情中显示 numstat 变更统计（++/-- 行数）",
          "default": true
        }
      }
    }
  }
}
```

`ConfigurationContribution` (`plugin/plugin.go`) supports `title`, `properties` (each a `ConfigProperty` with `type`: number/boolean/string/toggle/text, `label`, `description`, `default`, `minimum`, `maximum`), and `order`. The schema is also derivable from `web.contributes` (`configSchemaFromWebContribs`).

## Step 2: Read the merged config at runtime

`PluginContext.Config()` returns manifest defaults overlaid with user values:

```go
func (p *Plugin) Activate(ctx plugin.PluginContext) error {
	cfg, err := ctx.Config()
	if err != nil {
		return err
	}
	limit := 10
	if v, ok := cfg["defaultLogLimit"].(float64); ok {
		limit = int(v)
	}
	showStats := true
	if v, ok := cfg["showDiffStats"].(bool); ok {
		showStats = v
	}
	// ...
}
```

Script plugins get the same data without an RPC — `XBOT_PLUGIN_CONFIG` env var carries the merged config as JSON (`plugin/script_runtime.go pluginConfigJSON`).

## Step 3: React to changes

```go
ctx.OnConfigChanged(func(config map[string]any) {
	ctx.Logger().Infof("config changed: %v", config)
	// re-read values, refresh widgets, etc.
})
```

The subscription is released automatically on deactivate.

## Writing values

```go
ctx.SetConfig("defaultLogLimit", 20)  // persists + notifies subscribers
```

## How it works under the hood

- **Storage location**: `~/.xbot/plugins/<id>/config.json` (`PluginConfigStore.configPath`).
- **Merging**: `Load(pluginID)` returns user config; `GetDefaultConfig(manifest)` extracts defaults from `contributes.configuration`; the merged view is what `Config()` returns. An in-memory cache is invalidated by `Update` (`InvalidateCache`).
- **Notification**: `PluginConfigStore.Subscribe(pluginID, cb)` fires on `Update` — `notifyChange` dispatches to all subscribers.
- **Web UI + RPC**: `plugin.get_config` / `plugin.set_config` RPC methods (typed in `BackendRPC`, `web/src/plugin-api/rpc.ts`) drive the frontend settings form; `plugin.set_config` returns `{status, key}`.

## Multi-source config

The plugin system composes three layers:

| Layer | Source | Precedence |
|---|---|---|
| User config | `config.json` (written by `SetConfig` / web UI) | highest |
| Manifest defaults | `contributes.configuration.properties[].default` | middle |
| Zero values | code fallbacks | lowest |

`Config()` always returns the **merged** map — never a partial view.

## Pitfalls

- `ExportConfig`/`ImportConfig` (`plugin/export.go`) carry plugin config across machines; `ImportConfig` restores configs only for plugins that exist locally (missing ones are skipped with a warning).
- Values from JSON are `float64`/`bool`/`string` — assert types defensively (the `git-fancy` pattern above).
- Changing the schema shape of an existing key breaks users with saved configs — prefer adding new keys and migrating old ones in `Activate` (see `plugin/migration.go` `RegisterMigration` for the storage-migration machinery).
