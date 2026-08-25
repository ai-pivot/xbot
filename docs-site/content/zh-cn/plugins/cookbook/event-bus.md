---
title: "事件总线"
weight: 15
---

插件事件总线是用于**插件间通信**的进程内 pub/sub 系统（`plugin/eventbus.go`）。当插件需要彼此协调而又互不知晓身份时使用——例如 git 插件发布分支变更，CI 插件订阅它。

## API

```go
// PluginContext 经 EventBusPublisher 暴露它
type EventBusPublisher interface {
	Subscribe(topic string, handler PluginEventHandler) error
	Publish(topic string, data any) error
}
```

```go
type PluginEventHandler func(ctx context.Context, topic string, data any) error
```

示例：

```go
// 订阅者（在 Activate 中）
ctx.Subscribe("git.branch.changed", func(c context.Context, topic string, data any) error {
	branch, _ := data.(string)
	ctx.Logger().Infof("branch changed: %s", branch)
	return nil
})

// 发布者（在 Hook 处理器中）
ctx.Publish("git.branch.changed", newBranch)
```

## 权限

总线由三个权限门控（`plugin/permissions.go`）：

| 权限 | 授予 |
|---|---|
| `bus.plugin` | 使用插件间事件总线 |
| `bus.read` | 订阅 |
| `bus.write` | 发布 |

完整参与需全部声明：`"permissions": ["bus.plugin", "bus.read", "bus.write"]`。

## 语义

`PluginEventBus`（`plugin/eventbus.go:15`）：

- **读时复制**：`Publish` 在 RLock 下快照处理器列表，然后在锁外调用——处理器可在迭代期间安全订阅/退订。
- **每个处理器 panic 恢复**：panic 的处理器产生一条错误（经 `recover`），绝不崩溃发布者。`Publish` 返回所有处理器错误的切片。
- **退订**按函数指针比较（`funcEqual`，`reflect.ValueOf.Pointer`）——保留订阅时用过的同一个函数值。
- 主题是自由格式字符串；`domain.action`（`git.branch.changed`）式约定让命名空间可读。

## Tenant 作用域

`PluginManager.EventBusFor(tenantID)`（`plugin/manager.go:192`）返回**按租户隔离的总线**——租户级插件事件不会跨用户泄漏。`tenantID == 0` 返回全局总线。管理器为插件上下文接入对应总线。

## 区分：生命周期通知器

**不要**把事件总线与 `PluginEventNotifier`（`plugin/events.go`）混淆：

| | `PluginEventBus` | `PluginEventNotifier` |
|---|---|---|
| 受众 | 插件 → 插件 | 插件管理器 → 外部消费者（CLI、渠道） |
| 主题 | 任意字符串 | 无——单一流 |
| 事件 | 插件自定义数据 | `PluginEvent{Type, PluginID, ...}`（activated/deactivated/installed/reloaded/error/config_changed） |
| API | `ctx.Subscribe`/`ctx.Publish` | `PluginManager.OnPluginEvent(cb)` |

观察插件**生命周期**用通知器（如列出插件状态的 UI 面板）；插件**数据交换**用总线。

## 设计注意

- 总线事件仅内存——不持久化、不回放。发布时订阅者缺席，事件即消失。
- 从 Hook 处理器发布会同步运行在 Hook 的 goroutine 上——处理器保持轻快；重活放进插件自有的 goroutine。
- `Publish` 收集但不聚合错误；调试路径里检查返回的切片。
