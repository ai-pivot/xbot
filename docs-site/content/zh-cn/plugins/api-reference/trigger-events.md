---
title: "触发事件"
weight: 8
---

插件触发机制参考：激活事件（manifest）与 widget hook 触发器。

## 激活事件

在 `plugin.json` 的 `activation_events` 中声明（`plugin/manifest.go` 验证）：

| 格式 | 说明 | 验证 |
|------|------|------|
| `"onStart"` | 启动时激活。 | 精确匹配。 |
| `"onTool:<name>"` | 首次使用工具 `<name>` 时激活。 | `onTool:` 后名称必须非空。 |
| `"onHook:<event>"` | 生命周期 hook 事件触发时激活。 | 事件必须是合法 hook 事件（见 [Hook 事件](hook-events/)）。 |
| `"onCommand:<cmd>"` | 斜杠命令运行时激活。 | `onCommand:` 后命令必须非空。 |

`activation_events` 为空或缺失时默认 `["onStart"]`。非法事件格式在 manifest 校验时失败，报错：`unknown activation event format (expected onStart, onTool:<name>, onHook:<event>, or onCommand:<cmd>)`。

## Widget 触发器

`UISlotContribution.Triggers`（仅 script 运行时）——触发 widget 即时脚本运行的 hook 匹配器列表：

```
格式："EventName:Matcher"
示例："PostToolUse:Shell*"
```

- `EventName` 是 hook 事件名（如 `PostToolUse`）。
- `Matcher` 是工具名模式（支持 `*` 通配符；`Shell*` 匹配所有 Shell 工具）。
- **同步模式**：`UISlotContribution.Sync` 为 `true` 时，触发器在 hook goroutine 中同步内联运行——工具管线阻塞直到脚本完成，引擎可立即读取输出（`toolHint` 区域插件经 `PostToolUse` hook 使用）。默认为异步（经 `triggerCh`）。

## Hook 订阅（编程式）

插件通过 `PluginContext`（`HookSubscriber`）编程式订阅：

```go
ctx.OnPreToolUse("Shell*", handler)      // matcher "" = 全部工具
ctx.OnPostToolUse("", handler)
ctx.OnEvent(HookCronFired, "", handler)  // 任意事件 + matcher
ctx.OnAllToolUse(handler)                // 同时订阅 PreToolUse 和 PostToolUse
ctx.OnError(handler)                     // PostToolUseFailure
ctx.OnGlobalEvent(event, matcher, handler) // 会话无关，绕过会话隔离
```

完整事件列表见 [Hook 事件](hook-events/)。
