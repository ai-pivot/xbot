---
title: "事件总线"
weight: 8
---

> 本文档的英文版本包含完整的代码示例和 API 参考。请参阅 [English version](../../en/plugins/event-bus/) 获取完整内容。

本文档提供 事件总线 的中文概览。详细的 API 参考和代码示例请参阅英文版本。


The plugin event bus provides publish/subscribe communication between plugins.

## Overview

`PluginEventBus` is an in-process pub/sub system. Plugins can subscribe to topics and publish events. Each handler invocation is wrapped in panic recovery.

## API

```go
type PluginEventBus struct { ... }

func NewPluginEventBus() *PluginEventBus

func (b *PluginEventBus) Subscribe(topic string, handler PluginEventHandler) error
func (b *PluginEventBus) Publish(ctx context.Context, topic string, data any) []error
func (b *PluginEventBus) Unsubscribe(topic string, handler PluginEventHandler) error
```

Handler signature:

```go
type PluginEventHandler func(ctx context.Context, topic string, data any) error
```

## Permissions

| Action | Required Permission |
|--------|-------------------|
| Subscribe | `bus.read` |
| Publish | `bus.write` |
| Both (plugin-to-plugin) | `bus.plugin` (implies `bus.read` + `bus.write`) |

## Usage

### Subscribing

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    return ctx.Subscribe("xbot.my-plugin:events", func(ctx context.Context, topic string, data any) error {
        logger := ctx.Logger()
        logger.Info("Received event", plugin.Field{Key: "topic", Value: topic})
        return nil
    })
}
```

### Publishing

```go
func (p *MyPlugin) DoSomething(ctx plugin.PluginContext) error {
    return ctx.Publish("xbot.my-plugin:events", map[string]any{
        "action": "completed",
        "timestamp": time.Now().Unix(),
    })
}
```

### Unsubscribing

```go
// Unsubscribe uses function pointer comparison
handler := func(ctx context.Context, topic string, data any) error { return nil }
ctx.Subscribe("topic", handler)
// Later:
ctx.Unsubscribe("topic", handler)  // Must be the same function reference
```

## Topic Naming Convention

Use reverse-DNS style with plugin ID prefix:

```
xbot.<plugin-id>:<event-name>
```

Examples:
- `xbot.git-fancy:commit` — Git Fancy plugin commit event
- `xbot.my-plugin:ready` — My plugin ready event

## Implementation Details

- **Thread-safe**: Protected by `sync.RWMutex`
- **Copy-on-read**: Handlers are copied before iteration, so subscribe/unsubscribe during publish is safe
- **Panic recovery**: Each handler is wrapped in `recover()`. Panics are returned as errors, not propagated
- **Unsubscribe**: Uses function pointer comparison (`reflect.ValueOf().Pointer()`)

## See Also

- [PluginContext API](./plugin-context/) — EventBusPublisher interface
- [Permissions](./permissions/) — `bus.read`, `bus.write`, `bus.plugin`
