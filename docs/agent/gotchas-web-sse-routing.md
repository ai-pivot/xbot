# Web SSE 路由：channel-qualified，绝不许猜

2026-11-13 用户报「非 web channel 的会话在 Web 上看收不到 SSE，busy 也不对」，
并给出正确直觉：「只要打开对应会话就会订阅这个 SSE，无论它是哪个 channel 都应该没区别」。

结论：**直觉是对的，实现不是。** 当前 `channel` 是路由键的一部分，而订阅端有三处
「猜 channel」，猜错即静默失联。

## 已连接但只有心跳：恢复路径漏绑进度（2026-11-13）

用户实测子代理 busy、迭代持续更新，但订阅 `channel=agent` 只有心跳与
`ask_user_resolved`。不能据此断言 Run 没运行；session-tree 与 session-state
可能使用不同状态源。生产日志确认目标所属主会话曾恢复其子代理。

`restoreOneAgent` 重建 `RunConfig` 后漏调用 `a.wireSubAgentProgress`，而
`SendToInteractiveSession` 浅拷贝恢复出的配置。于是 Run 和迭代持久化照常，
但 `ProgressEventHandler` 与流式回调均为空，既无 SSE 业务推送，也无
`lastProgressSnapshot`（实时快照返回 null）。修复是恢复时与 spawn 同样接线。

守护：`agent/interactive_restore_progress_test.go` 真实走 restore → send → Run，
覆盖前台/后台、连续两个 turn 的 structured/stream/done 与 turn 身份；撤掉恢复接线
时四个 send 均无推送、测试必红。`channel/web/subagent_progress_sse_test.go`
通过真实 HTTP 订阅生产形态的 agent key，验证进度/流式载荷到达 agent 路由、
不会误投 web 路由。带 Iteration 的流式帧保持 `progress_structured` envelope，
由前端 normalizer 提取 stream 载荷，不应仅凭事件名判定“无流式”。

验收边界：上述为本地修复/测试；未部署或重启生产后端前，已有恢复实例仍持有旧回调，
不能宣称生产问题已消失。

## 子代理 BackgroundTask 归属与路由收敛（2026-11-13 P0）

此次回归来自把顶层 AgentID 从 `/taizi` 改成单段 `taizi`，而工具上下文根据
是否含 `/` 判子代理，误把它的后台任务归属设成根会话。仅补一个路径前缀不够：
同 role 的多实例、不同根会话拥有相同 AgentID 路径，仍会串消息。

现在唯一地址实现是 `internal/sessionaddr`：`Key/Parse` 序列化，`Child/SplitChild`
构造/反解 interactive 地址，`Scope` 为后台任务与 TODO 读写提供相同 owner。
生产 spawn、restore、one-shot 已有的 `CallerSessionKey` 是精确物理 map key，
任务地址统一为 `agent:<CallerSessionKey>`；嵌套 key 自带的 agent 层必须保留。
角色路径仅用于显示/调用链，不许当投递地址。旧任务只有 AgentID 时，只有**唯一**
匹配才可恢复，歧义/缺失保持原 key pending，绝不按角色猜父级或改投主会话。

`routeBackgroundTaskToOwner` 专管任务归属，`sendToExactInteractiveSession`
是自动通知的唯一 interactive 投递原语：通过内部 context 传精确地址，禁止
human-facing 模糊 role/instance 查找与跨树 fallback；失败同时检查 error 和
OutboundMsg.Error，认领单条事件、失败原位补回，不吞同 key 的兄弟通知。
初始化/恢复完成后 `retryOwnerNotifications` 只重投该 owner 的 pending。
子代理向父级汇报仍使用显式 ParentKey，不能与任务 owner 语义互换。
自动后台任务 turn 不再反向通知上级；pending 自动事件也不冒充用户输入，
用户/父级真实派单仍保留 completion 回执。

守护：真实 BackgroundTaskManager→bgNotifyLoop→interactive pending 管线覆盖
顶层/嵌套、同角色多实例、不同根会话、缺失/初始化/恢复、歧义 legacy key，
并验证主会话无副本。`internal/sessionaddr/guard_test.go` 扫描 AST，禁止重新
引入按 slash 判断 AgentID 或手拼 channel:chatID。地址有 round-trip/fuzz 测试。
CLI/Web 旧镜像只在兼容边界归一化，已存在子会话/真实根会话优先，不靠路径猜。

## 恢复出来的子代理：BgSessionKey 曾取到「无冒号的调用链路径」（2026-11-13）

生产核对（6973 个任务、9237 条完成通知配对）确认 **57 例真实错投**：创建方全是
`agent/ba/…taizi_dpsk-perf-1`，完成通知却落在 `web/fc/chat_9C218CD94A75`
（其父/主会话）；时间 07:31–12:21，全部早于 `12dbd0c4` 生效（13:05 部署）。

根因不是 fg/bg 模式（两者实跑都正确），而是 **restore 路径**：恢复时
`buildSubAgentRunConfig` 把 `cfg.SessionKey` 设成调用链身份 `main/<role>`（**没有冒号**，
不是会话地址），而旧 `BgSessionKey` 逻辑在恢复配置上就取了它 ⇒ 通知 key 非法 ⇒
旧兜底按「顶层子代理」把它注进主会话。新建（spawn）的会话 `SessionKey` 带冒号，
所以只有恢复出来的实例中招 —— 与现场「同角色新实例正常、老的 taizi 一直漏」一致。

现在 `BgSessionKey` 唯一来源是 `sessionaddr.Scope`：优先 `CallerSessionKey`
（spawn/restore 都写的精确 map key）→ `SessionKey` → 主会话才用 `RootSessionKey`。
恢复后工具上下文与新建同构，`agent:<key>` 精确投递。

守护：`agent/restored_owner_scope_test.go` 走真实 `restoreOneAgent`，断言
`cfg.AgentID` 仍判为子代理、`CallerSessionKey == key`、`BgSessionKey == agent:<key>`，
fg/bg 两个模式各一遍。部署后实测：13:05 之后 23 个新任务、0 错投。

排查口径（可复用）：会话库里 `Background task started [task_id: X]` 所在库 = 创建者，
`[System Notification] Background task X completed` 所在库 = 接收者；两者不同即错投。

## 路由键

`channel/web/web_eventstream.go`：

```go
func sessionRouteKey(channel, chatID string) string { return channel + "\x00" + chatID }
```

⇒ `web\x00foo` 与 `cli\x00foo` 是**两条互不相交**的路由（同一个 chatID 在两个 channel 下是不同会话）。

## 三处必须同源

| 环节 | 位置 | 用什么 channel |
|---|---|---|
| 发事件 | `web.go` `SendSessionState` / `SendQueueState` | `ev.Channel`（真实） |
| 订阅 | `web_sse.go` `resolveSSESession` | **必须解析出真实 channel** |
| 前端 | `AgentPanel.tsx` `messageChannel` / `progressChannel` | `params.channel ?? activeSession.channel ?? 'web'` |

任何一处用了"猜"的 channel，链路就断。

## 历史上两次「猜」的 bug

**① 后端硬编码 web（2026-11-13 修）**
`resolveSSESession` 旧实现：

```go
if sel.ChatID != chatID {
    sel = SessionSelector{Channel: "web", ChatID: chatID}   // ❌
    if webChatIDLooksLikeSubAgent(chatID) { sel.Channel = "agent" }   // 只救 agent
}
```

后果链：
1. cli/feishu 会话订阅挂到 `web\x00chatID`，事件在 `cli\x00chatID` ⇒ 一条都收不到；
2. `isUnknownSession(senderID, sel.Channel, sel.ChatID)` 用这个猜出来的 channel 探测，
   CLI 会话 chatID 是**文件路径**（不含冒号），`SessionExists("web", "/path")` 恒 false
   ⇒ **SSE 直接 404**，EventSource 根本连不上（不是"收到少"，是"没连上"）。

`inferAPISessionChannel`（REST 读路径）是同一缺陷的副本，只猜 agent 然后 `return "web"`。

**修法**：`resolveChannelForChatID(senderID, chatID)` —— 不猜，用 `SessionExists` /
`LocalSessionExists` 逐个候选（当前会话 channel → web → cli → feishu → agent）探测，
唯一命中即用；多命中优先当前会话并 warn；零命中返回 `""` 由调用方兜底。
测试 `channel/web/web_channel_resolve_test.go`。

**② 前端只剥一层前缀（2026-11-04 修，`cbd74927`）**
子代理进度事件的 `payload.ChatID` 是**逻辑地址** `agent:web:web-4/role:inst`
（`agent/interactive.go` 的 `agentProgressKey := "agent:" + key`），而面板持**物理 key**
`web:web-4/role:inst`。旧 `stripChannel` 只剥一层 ⇒ `web:web-4/...` vs `web-4/...` ⇒ 永不相等
⇒ `normalizeEvent` 在 chat 过滤处 `return null`，**子代理全部进度事件被静默丢弃**；
同一份逻辑在 `useAgentChatState.bareChatID` 里管 `session(idle)` ⇒ **面板永久 busy**。

修法：先循环剥 `agent:` 再剥一层物理前缀，两处（`normalize.ts` / `useAgentChatState.ts`）
必须同一套规则。

## 前端不许兜底成 web

`AgentPanel.tsx` 的 `params.channel ?? activeSession?.channel ?? 'web'` 三级兜底，
最后一级是错值的来源；`DockviewContainer.tsx` 的 `activateSession(params.sessionId, params.channel ?? 'web')`
还会把错值**写回** `activeSession`，之后其他面板读到的都是 `web`（错误自我放大）。

## 验收判据

1. 打开一个 cli 会话 → 浏览器 Network 里 `/api/sse?chat_id=...&channel=cli`（不是 `channel=web`）；
2. 该会话发一轮 → SSE 有 `progress` / `stream` 帧；
3. 该会话 turn 结束 → `session(idle)` 到达，面板退出 busy；
4. 打开子代理会话 → 同样三条成立（`channel=agent` + 物理 key）。
