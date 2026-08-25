---
title: "热重载与监控"
weight: 18
---

`PluginManager` 支持运行时重载、配置驱动的启用/禁用、失败插件的自动恢复、健康检查以及聚合指标。

## 重载单个插件

`Reload(ctx, pluginID)` 在不重启 xbot 的情况下从磁盘重新加载单个插件：

1. 若插件处于激活状态则先停用（`StateDeactivating` → `Deactivate` → `StateInactive`）。
2. 释放绑定在旧插件上下文上的 `OnConfigChanged` 订阅。
3. 移除旧条目并注销其全部 widget（`widgetRegistry.UnregisterAll`）。
4. 重新扫描插件目录（`findPluginDir`，覆盖 `DefaultPluginDirs` + 附加目录）并重新加载清单（`LoadManifest`）。
5. 重建存储（`NewFileStorage`；失败时回退到 `noopStorage`）并使插件配置缓存失效。
6. 构建全新的 `PluginEntry`（新的 `PluginContext`、日志器、widget 注册表），并通过 `RuntimeFactory.Create` 重建运行时。
7. 若清单声明了 `onStart` 激活事件，则自动重新激活。
8. 发出 `PluginEventReloaded` 事件并写入 `AuditReload` 审计条目。

```go
if err := pm.Reload(ctx, "xbot.genui"); err != nil {
    // 清单 / 运行时 / 激活错误
}
```

## 全量重载

`ReloadAll(ctx)` 停用所有插件、清空条目映射、从磁盘重新发现并重新激活：

1. 期间抑制 widget 更新（`widgetRegistry.SuppressUpdates`），避免淹没 WebSocket 推送缓冲区。
2. `DeactivateAll(ctx)` —— 注意这也会停止自动重试 goroutine。
3. 注销所有 widget，然后用全新映射替换条目映射。
4. `Discover(ctx)` + `ActivateAll(ctx)`。
5. 异步（在 goroutine 中）调用已注册的 `OnReload` 回调，使慢监听器（如 WebSocket widget 推送）无法阻塞 RPC 处理器。

```go
pm.OnReload(func() { /* ReloadAll 完成后执行 */ })
if err := pm.ReloadAll(ctx); err != nil { /* 发现/激活错误 */ }
```

## 配置监控

`WatchConfig(configPath, interval)` 轮询 `config.json` 并对 `plugins.disabled_plugins` 的变化做出响应：

```go
stop := pm.WatchConfig("/home/user/.xbot/config.json", 30*time.Second)
// ...
close(stop)
```

- 轮询间隔最小为 5 秒。
- 每个 tick 比较配置文件的修改时间；发生变化时重新读取文件，并将 `plugins.disabled_plugins` 列表与上一份快照做 diff。
- **新禁用**的插件会被停用（`StateDeactivating` → `Deactivate` → `StateInactive`）并加入 `disabled` 集合。
- **新启用**的插件会从禁用集合移除，然后就地重新激活（条目存在、状态为 `StateInactive`、声明了 `onStart`）或从磁盘发现并激活。

## 自动重试

`SetAutoRetry(enabled, maxRetries)` 为处于错误状态的插件启动后台重试循环：

```go
pm.SetAutoRetry(true, 5) // 每个插件最多重试 5 次；0 = 无限次
```

- goroutine（`retryLoop`）以 `retryInterval` 为周期运行（默认 5 秒；`SetRetryInterval` 仅供测试使用，不用于生产）。
- 每个 tick，`retryErrorPlugins` 扫描所有条目；对错误状态且 `retryCount` 低于 `maxRetries` 的插件按指数退避重试：`1s * 2^(attempt-1)`，上限 30 秒（`retryInitialDelay` / `retryMaxDelay`）。
- 重试将条目置为 `StateDiscovered` 并调用 `activate`。成功时重置重试计数与 `lastError`，并发出 `PluginEventActivated`（携带 `{"recovered": true, "attempt": n}`）；失败时记录 `lastError`/`lastErrorAt`，并通过 `notifyPluginError` 调用插件的错误回调。

> **重要**：`DeactivateAll`（以及 `ReloadAll`）会停止自动重试 goroutine 并将 `autoRetry` 置为 `false`。如果在 `DeactivateAll` 之后手动激活插件，需要再次调用 `SetAutoRetry` 以恢复自动恢复能力。

## 健康检查

插件可以实现可选的 `HealthChecker` 接口：

```go
type HealthChecker interface {
    HealthCheck(ctx context.Context) error
}
```

```go
results := pm.HealthCheck(ctx) // map[pluginID]error — nil 表示健康
```

只有 ACTIVE 状态的插件会被检查；未实现 `HealthChecker` 的插件被视为健康（`nil` 错误）。

## 指标

`Metrics()` 返回插件系统的聚合计数：

```go
type PluginMetrics struct {
    TotalPlugins   int   `json:"total_plugins"`
    ActivePlugins  int   `json:"active_plugins"`
    TotalTools     int   `json:"total_tools"`
    TotalHooks     int   `json:"total_hooks"`
    TotalEnrichers int   `json:"total_enrichers"`
    ToolCallCount  int64 `json:"tool_call_count"` // 运行期累计工具执行次数
    HookCallCount  int64 `json:"hook_call_count"` // 运行期累计 hook 分发次数
}
```

工具/hook 数量与调用计数只汇总 ACTIVE 插件的 `PluginContext`。`String()` 打印简洁的状态摘要：`PluginManager{total=5, active=3, error=1, disabled=1}`。

## 参见

- [插件生命周期](./lifecycle/) — 激活状态与事件
- [日志与审计](./logging/) — 重载操作会被审计
- [插件配置](./configuration/) — 插件配置的热重载
