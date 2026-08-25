---
title: "配置系统"
weight: 11
---

为插件提供用户可编辑的设置：声明式 schema、默认值、持久化与变更通知。配置存储实现为 `plugin/config.go`；参考实现是 `xbot.git-fancy`（`plugins/xbot-git-fancy/plugin.json`），其"默认 Commit 条数"与"显示 Diff 变更统计"设置在 Web UI 中可见。

## 第一步：在 plugin.json 中声明 schema

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

`ConfigurationContribution`（`plugin/plugin.go`）支持 `title`、`properties`（每个是 `ConfigProperty`，含 `type`：number/boolean/string/toggle/text、`label`、`description`、`default`、`minimum`、`maximum`）与 `order`。schema 也可从 `web.contributes` 推导（`configSchemaFromWebContribs`）。

## 第二步：运行时读取合并配置

`PluginContext.Config()` 返回清单默认值叠加用户值的合并结果：

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

Script 插件无需 RPC 即可拿到同样数据——`XBOT_PLUGIN_CONFIG` 环境变量携带合并配置的 JSON（`plugin/script_runtime.go pluginConfigJSON`）。

## 第三步：响应变更

```go
ctx.OnConfigChanged(func(config map[string]any) {
	ctx.Logger().Infof("config changed: %v", config)
	// 重新读取值、刷新组件等
})
```

订阅在插件 deactivate 时自动释放。

## 写入值

```go
ctx.SetConfig("defaultLogLimit", 20)  // 持久化 + 通知订阅者
```

## 底层机制

- **存储位置**：`~/.xbot/plugins/<id>/config.json`（`PluginConfigStore.configPath`）。
- **合并**：`Load(pluginID)` 返回用户配置；`GetDefaultConfig(manifest)` 从 `contributes.configuration` 提取默认值；`Config()` 返回合并视图。内存缓存在 `Update` 时失效（`InvalidateCache`）。
- **通知**：`PluginConfigStore.Subscribe(pluginID, cb)` 在 `Update` 时触发——`notifyChange` 分发到所有订阅者。
- **Web UI + RPC**：`plugin.get_config` / `plugin.set_config` RPC 方法（在 `BackendRPC` 中定型，`web/src/plugin-api/rpc.ts`）驱动前端设置表单；`plugin.set_config` 返回 `{status, key}`。

## 三层配置来源

| 层 | 来源 | 优先级 |
|---|---|---|
| 用户配置 | `config.json`（`SetConfig` / Web UI 写入） | 最高 |
| 清单默认值 | `contributes.configuration.properties[].default` | 中 |
| 零值 | 代码兜底 | 最低 |

`Config()` 始终返回**合并**后的 map——绝不返回部分视图。

## 避坑

- `ExportConfig`/`ImportConfig`（`plugin/export.go`）跨机器迁移插件配置；`ImportConfig` 只为本地存在的插件恢复配置（缺失的跳过并告警）。
- JSON 值类型是 `float64`/`bool`/`string`——防御性断言（上面的 git-fancy 模式）。
- 改变已有 key 的 schema 形状会破坏已保存配置的用户——优先新增 key，并在 `Activate` 里迁移旧 key（存储数据迁移机制见 `plugin/migration.go` `RegisterMigration`）。
