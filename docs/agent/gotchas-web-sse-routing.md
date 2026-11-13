# Web SSE 路由：channel-qualified，绝不许猜

2026-11-13 用户报「非 web channel 的会话在 Web 上看收不到 SSE，busy 也不对」，
并给出正确直觉：「只要打开对应会话就会订阅这个 SSE，无论它是哪个 channel 都应该没区别」。

结论：**直觉是对的，实现不是。** 当前 `channel` 是路由键的一部分，而订阅端有三处
「猜 channel」，猜错即静默失联。

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
