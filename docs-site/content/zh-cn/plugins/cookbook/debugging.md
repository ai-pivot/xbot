---
title: "调试与日志"
weight: 19
---

调试插件的工具与技术：每插件日志文件、性能剖析器、热重载、状态检查与常见故障模式。

## 每插件日志文件

`pluginLogger`（`plugin/plog.go`）**只写每插件日志文件**——插件运行日志绝不污染主 xbot 日志：

```
~/.xbot/plugins/<id>/logs/plugin.log
```

- `rotateWriter` 按大小/时限轮转文件；`pluginLogManager` 经清理循环删除超过 `DefaultPluginLogMaxAge` 的文件。
- 只有框架级生命周期事件（discovered/activated/deactivated/failed）进入全局日志。
- 写日志失败时 `pluginLogger.emit()` 回退到全局 logrus——日志永不丢失。

读日志：

```bash
tail -f ~/.xbot/plugins/xbot.git-fancy/logs/plugin.log
```

## 从插件内打日志

```go
ctx.Logger().Info("activated", plugin.Field{Key: "widgets", Value: len(widgets)})
ctx.Logger().Warnf("trigger %q subscribe failed: %v", trigger, err)
ctx.Logger().WithField("plugin", id).Error("activation failed: ", err)
```

`Logger`（`plugin/context.go:157`）支持结构化字段、格式化变体与 `WithField`/`WithFields` 链（调用级字段覆盖预绑定字段）。

- **Script 插件**：日志写 stderr——会被捕获进 xbot 日志。stdout 仅承载协议/输出。
- **Stdio 插件**：同一规则——`print(..., file=sys.stderr)`（见 `grpc-python/main.py`）。

## 性能剖析器

`plugin/profile.go` 聚合每插件指标：工具/Hook/注入器调用次数、总耗时与最近调用时间。

```go
profiler := plugin.NewProfiler()
profiler.RecordToolCall(pluginID, duration)
profiler.RecordHookCall(pluginID, duration)
profile := profiler.GetProfile(pluginID)  // 安全副本——可自由修改
```

未剖析插件返回零值 `PluginProfile`。剖析器并发安全（`sync.Mutex`）。

## 插件状态机

`PluginState`（`plugin/plugin.go:704`）：`discovered → activating → active`，卸载时 `deactivating → inactive`，失败时 `error`。经 `PluginManager.ListPlugins()` 检查——每个 `PluginEntry` 携带 `State`、`retryCount`、`lastError`、`lastErrorAt`、`Dir`。

## 热重载

- agent 命令 `/plugin reload-all` 无需重启 server 重新激活插件。
- `WatchConfig`（`plugin/manager.go`）每 30s 轮询 `config.json` 并 diff `plugins.disabled_plugins`——但 ⚠️ **生产接线从未调用它**（grep serverapp/agent 无结果）。改 config.json **不会**触发重载；用 `/plugin reload-all` 或重启。
- **stdio 插件二进制更新后需重载**：拉起的进程二进制按激活缓存。重建 → `/plugin reload-all`。

## 自动重试

`PluginManager.SetAutoRetry(true, maxRetries)` 启动后台重试循环（`retryLoop`，5s 扫描间隔），对 error 状态插件按指数退避（1s → 30s 封顶）重新激活。⚠️ `DeactivateAll()` 会取消重试上下文——手动 `activate()` 之后必须重新启用自动重试，否则失败插件永不恢复。

## 常见故障模式

| 症状 | 可能原因 | 排查位置 |
|---|---|---|
| 插件被发现但从不激活 | `activation_events` 不匹配；禁用列表 | `~/.xbot/config.json` `plugins.disabled_plugins`；清单 `activation_events` |
| stdio 插件每次调用都超时 | 没有 flush stdout；字段名错误 | stderr 协议噪音；`plugin/protocol/protocol.go` 字段 tag |
| 渠道插件工具不可见 | config.json 缺 `channels.<name>.enabled` | `IsEnabled(nil) → false`（`serverapp/channel_plugin.go`） |
| 组件显示陈旧内容 | 脚本错误被吞；缓存未失效 | 插件日志 `runScript(...) failed` |
| Hook 从不触发 | 匹配器不匹配；会话级 vs 全局 | `OnEvent` vs `OnGlobalEvent`；匹配器模式 |
| 工具参数到达为空 | 输入 JSON 字符串未解析 | `ParseToolInputString` / `json.loads` |
| 原生插件 "not registered" | `init()` 注册未链接；ID 不匹配 | `NativeRuntime.registry`；清单 `id` vs `p.Manifest().ID` |

## 审计日志

`PluginManager.AuditLog()` 返回写入 `~/.xbot/plugins/audit.jsonl` 的 `AuditLogger`——条目记录安装/卸载/禁用/配置变更动作，含插件 ID、动作、详情与错误。适合事后排查（"谁在何时禁用了插件"）。

## 验证干净安装

```bash
# 重新审视插件目录
ls -la ~/.xbot/plugins/<id>/
cat ~/.xbot/plugins/<id>/plugin.json | python3 -m json.tool   # 清单解析检查
tail -50 ~/.xbot/plugins/<id>/logs/plugin.log                  # 最近生命周期事件
```
