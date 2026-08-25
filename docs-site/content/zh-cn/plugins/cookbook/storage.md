---
title: "存储系统"
weight: 16
---

每个插件都有隔离的、持久化的键值存储。实现：`plugin/storage.go`（文件后端）+ `PluginContext` 上的类型化访问器。hello-world 示例就是靠它在重启间保留 `tool_call_count` 计数器的。

## API

```go
// StorageProvider（plugin/context.go:54）
type StorageProvider interface {
	Storage() StorageAccessor
	StorageInt(key string) (int64, bool)
	StorageBool(key string) (bool, bool)
	StorageJSON(key string, value any) error
	StorageGetJSON(key string, target any) error
}

type StorageAccessor interface {
	Get(key string) (string, bool)
	Set(key, value string) error
	Delete(key string) error
	Keys() []string
	Clear() error
}
```

## 用法

```go
func (p *Plugin) Activate(ctx plugin.PluginContext) error {
	// 原始字符串
	storage := ctx.Storage()
	storage.Set("last_run", time.Now().Format(time.RFC3339))
	if v, ok := storage.Get("tool_call_count"); ok {
		// ...
	}

	// 类型化助手（解析失败返回零值 + false）
	if n, ok := ctx.StorageInt("counter"); ok {
		_ = n
	}
	ctx.StorageBool("feature_enabled")       // (bool, bool)
	ctx.StorageJSON("session", someStruct)   // 序列化 + Set
	ctx.StorageGetJSON("session", &target)   // Get + 反序列化（返回 error）
	return nil
}
```

## 持久化细节

`NewFileStorage(pluginDir)`（`plugin/storage.go:25`）：

- **位置**：`<plugin-dir>/data/storage.json`（对 `~/.xbot/plugins/<id>/` 下的插件即 `~/.xbot/plugins/<id>/data/storage.json`）。
- **原子写入**：`json.MarshalIndent` → 写 `storage.json.tmp` → `os.Rename`。绝无撕裂文件。
- **权限**：`0600`——绝不为插件存储用 `0644`。
- **损坏文件恢复**：无法解析的 storage.json 记录警告并重新开始。
- **并发**：所有操作由 `sync.RWMutex` 保护。

## 权限

`storage.private` 授予插件自有存储。`storage.shared` 声明共享存储（预留给未来的跨插件存储）。

## 数据迁移

存储数据是你自己的版本管理责任。`plugin/migration.go` 提供机制：

```go
plugin.RegisterMigration("xbot.demo", plugin.PluginMigration{
	FromVersion: "1.0.0",
	ToVersion:   "1.1.0",
	Migrate: func(ctx context.Context, storage plugin.StorageAccessor) error {
		// 把数据从 1.0.0 schema 迁移到 1.1.0
		return nil
	},
})
```

`RunMigrations(pluginID, storage)` 按 semver 顺序应用待执行的步骤；已执行步骤记录在 `_migrations` 键下，绝不重跑。每次执行前先**备份**（`Migrator`，`~/.xbot/plugins/<id>/backups/<version>/`）——回滚恢复最近的备份。

## Script 插件

脚本没有存储 API——把状态写入 `XBOT_WORK_DIR` 下的文件，或（更好）搭配一个小的 stdio 伴生进程。尽可能让脚本插件保持无状态：它们的输出缓存已经按 workDir 隔离。

## 避坑

- 存储是每插件**不共享**的——两个插件永远看不到彼此的键（那是 [事件总线](../event-bus/) 的职责）。
- `StorageInt`/`StorageBool` 在缺失**或**值不可解析时都返回 `(零值, false)`——把 `false` 理解为"不可用"，不是"错误"。
- `Delete` 与 `Clear` 立即持久化（每次调用重写文件）。大量变更合并成少量 `Set`。
