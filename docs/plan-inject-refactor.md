# 计划：Inject 路径重构 — 消除 silent drop + 收敛 chatID 格式约定

> 生成时间：2026-05-19 19:10
> 状态：待确认

## 背景与目标

bg task completion inject 路径刚修了一个 chatID 前缀 bug。根因是 chatID 格式约定分散在 17+ 个 drop 点中，全部静默丢弃。本次重构目标：

1. **所有 silent drop 点加日志** — 任何消息丢弃至少有 debug 日志可追溯
2. **收敛 chatID 格式拼接** — 用 helper 函数替代散落各处的 `channelName+":"+chatID`
3. **统一 inject 入口** — cron/event/peer 都走 `injectBgUserMessage`，不再内联拼装
4. **event 消息增加 TUI 通知** — 当前 event inject 不走 injectCLIUserMessage，TUI 看不到

**不做的事**（风险/收益比不合理）：
- ❌ Hub subscriber key 统一为 `"channel:chatID"` — 涉及 20+ 文件、3 层协议（WS/RPC/event），改动面过大
- ❌ 消除 ChannelCliChannel/RemoteCLIChannel/CLIChannel 三元分裂 — 它们服务于不同运行模式（local/remote/in-process），架构上有存在的理由
- ❌ 合并 injectCLIUserMessage 和 injectInbound — 它们服务于不同目的（UI 显示 vs Agent 处理），走不同路径（channel dispatcher vs bus）

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `agent/agent.go` | inject* 函数定义、channelFinder 调用 | 修改 |
| `agent/agent_process.go` | drainSessionBgNotifications | 修改 |
| `channel/cli_update_handlers.go` | handleInjectedUserMsg session filter | 修改 |
| `channel/cli.go` | CLIChannel.InjectUserMessage asyncCh drop | 修改 |
| `channel/web_remote_cli.go` | RemoteCLIChannel inject/progress drops | 修改 |
| `channel/web_hub.go` | Hub sendToClient drop points | 修改 |
| `channel/channel_cli.go` | ChannelCliChannel eventCh drop | 修改 |
| `agent/client.go` | dispatchWSMessage unknown type | 修改 |

### Silent Drop 点清单（17 处，按优先级）

| # | 文件:行 | 条件 | 风险 | 日志 | 建议 |
|---|--------|------|------|------|------|
| 1 | agent.go:1438 | channelFinder nil | 低(正常) | 无 | Debug |
| 2 | agent.go:1441 | channelFinder 找不到 | ⚠️ 中 | 无 | Warn |
| 3 | agent.go:1445 | 无 UserMessageInjector | 低 | 无 | Debug |
| 4 | cli.go:731 | asyncCh 满 | ⚠️ 中 | 无 | Warn |
| 5 | web_remote_cli.go:107 | client 离线 | 低 | Debug ✅ | 已有 |
| 6 | web_hub.go:103 | conn 已断开 | 极低 | 无 | Debug |
| 7 | web_hub.go:109 | sendCh 满 | ⚠️ 中 | 无 | Warn |
| 8 | web_hub.go:150 | broadcast sendCh 满 | 低 | 无 | Debug |
| 9 | web_hub.go:70 | flush sendCh 满 | ⚠️ 中 | 无 | Warn |
| 10 | channel_cli.go:74 | eventCh 满 | ⚠️ 中 | 无 | Warn |
| 11 | cli_update_handlers.go:881 | suLoading | 低(正常) | 无 | Debug |
| 12 | cli_update_handlers.go:886 | session filter | 低(正常) | 无 | Debug |
| 13 | client.go:91 | unknown WS msg type | ⚠️ 中 | 无 | Warn |
| 14 | agent.go:2610 | bus.Inbound 满 | ⚠️ 中 | 无 | Warn |
| 15 | agent.go:2631 | event bus.Inbound 满 | ⚠️ 中 | 无 | Warn |
| 16 | agent.go:1561 | chat queue 满 | 低 | Warn ✅ | 已有 |
| 17 | agent.go:1917 | bus.Outbound 满 | 低 | Warn ✅ | 已有 |

## 详细计划

### 阶段一：收敛 chatID 格式约定（低风险）

- [ ] 1.1：在 `agent/agent.go` 顶部添加 helper 函数 `qualifyChatID(channel, chatID string) string` — 返回 `channel+":"+chatID`
- [ ] 1.2：在 `channel/web_remote_cli.go` 顶部添加 helper `stripChannelPrefix(chatID string) (channel, plainID string)` — 用于 Hub lookup
- [ ] 1.3：替换 `injectCLIUserMessage` 中的内联拼接为 `qualifyChatID` 调用
- [ ] 1.4：替换 `RemoteCLIChannel.InjectUserMessage` 中的内联 strip 为 `stripChannelPrefix` 调用

### 阶段二：Silent drop 加日志（低风险）

- [ ] 2.1：`agent/agent.go` injectCLIUserMessage — 3 个 drop 点（nil finder / not found / no injector）加日志
- [ ] 2.2：`agent/agent.go` injectInbound — bus.Inbound 满 / agent context done 加日志
- [ ] 2.3：`agent/agent.go` injectEventMessage — 同上
- [ ] 2.4：`channel/cli.go` CLIChannel.InjectUserMessage — asyncCh 满 Warn
- [ ] 2.5：`channel/channel_cli.go` ChannelCliChannel.sendMsgBestEffort — eventCh 满 Warn
- [ ] 2.6：`channel/web_hub.go` sendToClient — conn nil / sendCh 满 加日志
- [ ] 2.7：`channel/web_hub.go` broadcastToAll / subscribe flush — sendCh 满 加日志
- [ ] 2.8：`channel/cli_update_handlers.go` handleInjectedUserMsg — session filter / suLoading guard 加 Debug
- [ ] 2.9：`agent/client.go` dispatchWSMessage — 未知消息类型加 Warn

### 阶段三：统一 inject 入口（中风险）

- [ ] 3.1：将 cron `SetInjectFunc` 闭包改为调用 `injectBgUserMessage`（当前是内联拼装 injectCLIUserMessage + injectInbound）
- [ ] 3.2：`injectBgUserMessage` 增加 `extraContent string` 参数，供 cron 传 "⏰ [定时任务]" 前缀
- [ ] 3.3：`injectPeerMessage` 已经调用 `injectBgUserMessage`，无需改

### 阶段四：Event 消息增加 TUI 通知（低风险）

- [ ] 4.1：`injectEventMessage` 改为先调 `injectCLIUserMessage` 再调 `injectInbound`（当前只有 injectInbound，TUI 看不到 event 消息）

## 验证方案

- `go build ./...` 编译通过
- `go test ./...` 全部通过
- `golangci-lint run ./...` 无新 warning
- 手动验证：启动 TUI → 创建 bg task → 确认 completion notification 正常显示

## 回滚策略

所有改动在本 worktree 的 dev-v2 分支上，不合并到 master。出问题直接删除分支。

## 注意事项

- Hub key 格式保持纯 chatID 不变（`/home/user/tmp`），仅 InjectUserMessage 传参用 `channel:chatID` 格式
- 不修改 Hub subscribe/unsubscribe 的 key 格式
- 不修改 WS 协议层（MsgTypeSubscribe 的 chat_id 字段）
- 日志级别原则：正常过滤行为用 Debug，消息丢失风险用 Warn
