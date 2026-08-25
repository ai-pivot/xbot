---
title: "插件迁移"
weight: 19
---

xbot 为插件提供了版本化的数据迁移系统（`plugin/migration.go`）。插件注册迁移步骤，`RunMigrations` 执行所有待处理的迁移，将插件的存储数据从旧版本 schema 转换为新版本。

## 架构

- 全局注册表 `migrationRegistry`（由互斥锁保护）维护 `pluginID → []PluginMigration` 的映射。
- `RegisterMigration` 按插件累积迁移步骤；执行顺序由运行时的 semver 比较决定，与注册顺序无关。
- 已应用的迁移记录保存在插件自身存储的保留键 `_migrations` 中（`migrationRecord`，含 `applied` 迁移 ID 列表）。
- 每个迁移以事务式模式执行：**备份 → 执行 → 记录**，失败时恢复备份。

## PluginMigration

单个版本到版本的迁移步骤：

```go
type PluginMigration struct {
    FromVersion string
    ToVersion   string
    Migrate     func(ctx context.Context, storage StorageAccessor) error
}
```

- `FromVersion` 和 `ToVersion` 必须是严格的 semver 字符串（如 `"1.0.0"`）。
- `Migrate` 接收插件的 `StorageAccessor`，负责将数据从 `FromVersion` 的 schema 转换为 `ToVersion` 的 schema。
- 迁移的规范 ID 格式为 `FromVersion→ToVersion`（如 `"1.0.0→1.1.0"`）。

## 注册迁移

`RegisterMigration` 可在 `init()` 或 `Activate` 中安全调用，也支持多 goroutine 并发注册：

```go
plugin.RegisterMigration("my-plugin", plugin.PluginMigration{
    FromVersion: "1.0.0",
    ToVersion:   "1.1.0",
    Migrate: func(ctx context.Context, s plugin.StorageAccessor) error {
        old, _ := s.Get("config")
        s.Set("config", fmt.Sprintf(`{"version":"1.1.0","data":%s}`, old))
        return nil
    },
})
```

重复注册同一个迁移是无害的——已应用记录的去重保证它只会执行一次。

## 运行迁移

```go
func RunMigrations(ctx context.Context, pluginID string, currentVersion string, storage StorageAccessor) error
```

- `pluginID`：插件的唯一标识。
- `currentVersion`：迁移的目标版本（通常是清单版本）。
- `storage`：插件的存储访问器。

所有适用的迁移均成功应用后返回 `nil`；迁移失败（回滚后）或上下文被取消时返回错误。

### 执行语义

1. 校验所有已注册的迁移：`FromVersion` 与 `ToVersion` 必须能解析为 semver（否则直接报错）。
2. 按 `FromVersion`、再按 `ToVersion` 排序。
3. 从存储键 `_migrations` 加载已应用记录；「最后迁移到的版本」= 最后一个已应用迁移的 `ToVersion`。
4. 按排序后的顺序逐个评估：
   - **已应用** → 跳过。
   - **链式连续性** — 若存在最后应用版本，只有 `FromVersion` 与其相等的迁移才有资格执行，其余跳过。迁移因此构成 `1.0.0 → 1.1.0 → 1.2.0` 的链。
   - **未来迁移** — `ToVersion` 大于 `currentVersion` → 跳过。
   - `FromVersion > ToVersion` → 报错。
5. 每次迁移执行前检查上下文是否已取消。

### 备份与回滚

迁移执行前，`backupStorage` 将除保留键 `_migrations` 外的所有存储键读入内存映射。若 `Migrate` 返回错误，`restoreStorage` 删除所有非保留键并恢复备份——存储与迁移前完全一致。若迁移成功但记录保存失败，同样尝试回滚，返回的错误会同时报告两处失败。

## 编写迁移链

```go
plugin.RegisterMigration("my-plugin", /* 1.0.0 → 1.1.0 */ ...)
plugin.RegisterMigration("my-plugin", /* 1.1.0 → 1.2.0 */ ...)
plugin.RegisterMigration("my-plugin", /* 1.2.0 → 2.0.0 */ ...)

// 升级到 2.0.0 — 三个步骤按顺序全部执行。
err := plugin.RunMigrations(ctx, "my-plugin", "2.0.0", storage)

// 先升级到 1.2.0 — 只执行前两步。
// 之后以 "2.0.0" 再次调用，只执行 1.2.0 → 2.0.0。
```

插件通常在 `Activate` 实现中、读取任何数据之前调用 `RunMigrations`。

## 行为清单

以下行为均有 `plugin/migration_test.go` 测试覆盖：

| 行为 | 说明 |
|------|------|
| 顺序 | 迁移按 semver 排序执行，与注册顺序无关 |
| 幂等 | 第二次运行不会执行任何迁移——已应用的迁移绝不重跑 |
| 链式连续性 | `FromVersion` 与最后应用的 `ToVersion` 不匹配的迁移被跳过 |
| 未来版本 | `ToVersion` 高于 `currentVersion` 的迁移被跳过 |
| 失败回滚 | 原始数据保持完整；失败的迁移不会被记录 |
| 取消 | 上下文取消会在迁移之间中止执行 |
| 并发注册 | 多 goroutine 调用 `RegisterMigration` 是安全的 |
| 重复注册 | 同一迁移注册两次只执行一次 |

## 参见

- [插件存储](./storage/) — 迁移所操作的 `StorageAccessor` API
- [插件生命周期](./lifecycle/) — 在哪里调用 `RunMigrations`
- [热重载与监控](./hot-reload/) — 与迁移配合的重载行为
