---
title: "发布与分发"
weight: 21
---

如何打包、分发、版本化与迁移插件。机制：`PluginRegistry`（`plugin/registry.go`）、配置导入导出（`plugin/export.go`）、存储迁移（`plugin/migration.go`）与校验和（`plugin/manifest.go VerifyChecksum`）。

## 打包布局

```
my-plugin/
├── plugin.json       # 清单 —— 发现的入口
├── main.go           # 或 main.sh / main.py / bin/
└── web/              # 可选前端模块（index.js + chunks）
```

安装 = 把目录复制到 `~/.xbot/plugins/<id>/` 下（或经 `PluginManager.AddSearchDirs` 注册的额外搜索目录）。这就是 MVP 的全部分发方式——目前没有归档格式。

## 注册表（MVP）

`PluginRegistry`（`plugin/registry.go`）以源（`RegistrySource{Type, URL}`，`Type` 为 `github`/`url`/`local`）包装 `PluginManager`：

- **`Search(ctx, query)`** —— 仅对**本地已安装**插件按 ID/Name/Description 大小写不敏感匹配。
- **`Install(ctx, id)`** —— MVP 仅支持 **local 源**：`URL` 是插件目录路径；`InstallPlugin` 复制它。GitHub/URL 源已定义但 `InstallFromSource` 是 Phase 3。
- `RegistryEntry` 携带 ID/Name/Version/Description/Author/Source/DownloadURL/**Checksum**（插件归档的 SHA256）。

## 校验和

`VerifyChecksum(dir)`（`plugin/manifest.go`）对照目录内容校验清单中记录的 SHA256。用它检测篡改或不完整副本：

```go
if err := plugin.VerifyChecksum(pluginDir); err != nil {
	// 拒绝插件 —— 内容与声明的校验和不符
}
```

## ID 与版本规则

- **ID**：正则 `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`（`isValidPluginID`，`plugin/manifest.go`）——推荐反向 DNS（`com.example.echo-channel`、`xbot.git-fancy`）。同一正则守卫静态托管路径（`isValidPluginIDForServe`）。
- **版本**：严格 semver，由 `ParseVersion`/`parseSemver` 解析。非法版本导致清单加载失败。
- 清单 `homepage` + `author` 字段供市场展示。

## 生命周期：安装/卸载事件

`PluginManager` 经 `PluginEventNotifier`（`plugin/events.go`）发出 `PluginEventInstalled`/`PluginEventUninstalled`/`PluginEventReloaded`/`PluginEventError`。消费者用 `pm.OnPluginEvent(cb)` 订阅——市场 UI 或审计系统在此监听。所有动作落入审计日志（`~/.xbot/plugins/audit.jsonl`）。

`InstallPlugin` 在删除检查前用 `filepath.EvalSymlinks` 解析真实路径——只删除 `xbotHome` 之下的目录（符号链接穿越防护）。

## 配置导入导出

`PluginManager.ExportConfig()` 序列化所有插件的清单、状态与用户配置；`ImportConfig(data)` 为本地存在的插件恢复配置（缺失的跳过）并合并禁用集合（并集）：

```go
data, _ := pm.ExportConfig()       // ConfigExport{Version, ExportedAt, Disabled, Plugins}
err := pm.ImportConfig(data)       // 尽力恢复
```

⚠️ `ExportConfig` 获取管理器 RLock——绝不在插件的 `Activate`/`Deactivate` 内调用（它们运行在写锁下）。

## 版本化存储数据

持久化状态的插件必须跨版本迁移数据。`plugin/migration.go`：

```go
plugin.RegisterMigration("xbot.demo", plugin.PluginMigration{
	FromVersion: "1.0.0",
	ToVersion:   "1.1.0",
	Migrate: func(ctx context.Context, storage plugin.StorageAccessor) error {
		old, _ := storage.Get("count")
		storage.Set("counters.total", old)   // 重构 schema
		return nil
	},
})
```

- `RunMigrations(pluginID, storage)` 按 semver 排序步骤，每个待执行步骤只执行一次（记录在 `_migrations` 下）。
- `Migrator` 每次执行前**备份**：`~/.xbot/plugins/<id>/backups/<version>/`。回滚恢复最近备份。
- 迁移**按版本顺序串行**执行。

## 发布检查清单

1. `plugin.json` 可解析（`python3 -m json.tool`），版本号 bump 符合 semver。
2. 权限最小**且完整**（见 [权限系统](../permissions/)）。
3. Go 后端 `go build ./...` / `go test ./...`；Web 视图 `vitest`。
4. 清单权限测试（git-fancy `TestManifestPermissions` 模式）通过。
5. schema 变更注册存储迁移；手动验证备份/恢复。
6. 发布目录 `VerifyChecksum` 通过。
7. README 记录插件 ID、运行时、权限与配置 schema。
8. Channel 插件：用户还须在 config.json 加 `channels.<name>.enabled=true`——显著位置说明。
9. Web 插件在 `web/` 下随包发布构建产物；绝不把仅开发期文件放进去。
