# interactive 子代理「身份」的两个表示（2026-11-04 P0 系列）

一次线上事故群，六个症状，同一个根因：**interactive 子代理的"身份"在整个系统里有两套表示**，
而五条执行路径各自只处理了一部分。

```
物理会话 key   web:web-4/role:inst          ← tenants.chat_id / web 前端 progressChatID
逻辑地址       agent:web:web-4/role:inst    ← SSE payload.ChatID / ctx.SessionKey
```

记忆点：**凡是跨来源比较或跨路径传递"这是哪个子代理"，先问"我现在拿的是哪一套"。**

---

## 1. restore 路径必须与 spawn 路径同构写 `cfg.CallerSessionKey`

`agent/interactive_persist.go`（`restoreOneAgent`）

`CallerSessionKey` 原来只在 `agent/interactive.go` 的 spawn 路径设置，restore 漏了 ⇒ 恢复出来的
子代理该字段为空 ⇒ `tools/send_message.go:resolveUserBoardTarget` 走 fallback
`rootChannel + ":" + rootChatID` = **用户根会话** ⇒ 卡片 `from.session` 记成御前 ⇒ 用户批复
按 origin 回流到御前。

修：restore 里补 `cfg.CallerSessionKey = mapKey`（`mapKey` 就是 interactiveKey 的产物，与 spawn 逐字一致）。

## 2. 只修 restore 没用 —— 子代理 turn 有两条不相交的执行路径

`tools/send_message.go`（`interactiveOriginSession`）

| 路径 | cfg 来源 | CallerSessionKey |
|---|---|---|
| ① `SendToInteractiveSession` | interactive 会话快照（spawn/restore） | 有 |
| ② 会话队列 / 通知注入（`drainAndProcessNotifications`）/ 用户看板回流唤醒 | `buildMainRunConfig` → `buildBaseRunConfig` | **没有**，且 `AgentDepth=0` |

② 里 ctx 只剩：`Channel="agent"`、`ChatID="web:web-4/role:inst"`（就是 interactive key）、
`SessionKey="agent:web:web-4/role:inst"`、`RootSessionKey` 同值。

所以必须从 ctx 推断：`strip "agent:"` 后含 `/` ⇒ 是 interactive key。
**CLI 会话的 chatID 是文件路径（含 `/`），无脑按 `/` 判会把 `cli:/home/...` 误认成子代理** ——
只在 `Channel=="agent"` 时启用这条兜底。

## 3. 前端 key 归一必须**循环**剥 `agent:`

`web/src/chat/normalize.ts`（`stripChannel`）、`web/src/chat/useAgentChatState.ts`（`bareChatID`）

后端 `wireSubAgentProgress` 发 `agentProgressKey := "agent:" + key`（逻辑地址），面板持物理 key。
只剥一层 ⇒ `web:web-4/...` vs `web-4/...` ⇒ 永不相等 ⇒ `normalizeEvent` 在过滤处 `return null`，
**子代理全部进度事件被静默丢弃**（SSE 明明收到）。同理 `session(idle)` 收不到 ⇒ 面板永久 busy。

修：`while (s.startsWith('agent:')) s = s.slice(6)` 再剥一层物理前缀。
两处（`normalize.ts` / `useAgentChatState.ts`）必须同一套规则。

## 4. 孙代理的 key 用 `parent_chat_id` 的**第一段**，不是全串

`storage/sqlite/interactive_agents.go`（`MarkOrphanedIdleUnloaded` 的 SQL）

```
parent_chat_id = "web-4/court.shoufu:main"        ← 孙代理的 parent 带 '/' 分隔
会话 key        = "web:web-4/court.gongbu:inst"   ← 只用第一段 origin
旧 SQL 拼出     = "web:web-4/court.shoufu:main/court.gongbu:inst"  ← 不存在
```

⇒ `NOT EXISTS` 恒真 ⇒ **每次启动把活着的孙代理标 `unloaded`** ⇒ `restoreInteractiveTree` 跳过
⇒ 重启后整棵树只剩第一层。修：SQL 里 `instr(parent_chat_id,'/')` + `substr` 取第一段。

## 5. decision 回流的 origin 是 `env.origin.session`，**不是** `from.session`

`serverapp/structured_callbacks.go`（`decisionRerouteTarget`）

`from.session` 恒为「落库的会话」= 用户会话（`buildStructuredEnvelope` 决定），**不是发起方**。
2026-11-04 调查一度误判成 `from.session`，白改一轮。

旧实现把 decision 也 `deliver → sel`，等于注入用户自己的会话 ⇒ **用户会话被自己的批复自激一轮**。
正解：只当 ① 卡片是 report/ask_user ② `env.origin.session` 非空 ③ 发起方 ≠ 当前会话 时才重定向；
`sel` 仅作审计落行。

## 6. 取消 ≠ 暂停（`waitingUser` 不复位）

`agent/agent.go`（`isWaitingUserPause`）、`agent/engine_run.go`（`runState.runCancelled`）

`s.waitingUser` 被 AskUser 置 true 后**从未复位**。用户点停止（ctx cancel）后 Run 直接返回，于是：
`progressFinalizer` 不发 `PhaseDone` + 收尾不发 `session(idle)` ⇒ 前端永久 typing。

判据换成「ctx 是否已取消」，不新增状态位：`s.runCtx.Err() != nil` ⇒ 取消优先，
必须收尾发 `PhaseDone` + `session(idle)`。真·等用户回答时 ctx 是活的。

---

## 附：两个渲染层坑（同批）

- **孤儿 reasoning**（`web/src/components/agent/TurnBody.tsx`）：turn 边界无用户消息时
  （后台任务通知自动续跑）会连续出现两个 turn，上轮末迭代纯正文、下轮首迭代纯 reasoning。
  DB 顺序是对的，但渲染上是「正文后面接思考」。语义：**思考必须在它所属输出之前** ⇒
  孤儿 reasoning 作为前缀并入下一个有 payload 的迭代；尾部孤儿保留独立块（那是进行中的思考）。
  **必须走唯一入口 `mergeIterationsForRender()`** —— 只测 `mergeOrphanReasoning` 而忘了在
  TurnBody 里接入，修复会静默失效（变异自证才发现）。
- **结构化消息正文用 Markdown**（`web/src/structured/StructuredCards.tsx` 的 `LongText`）：
  短文本直接 `MarkdownRenderer`；长文本折叠态**仍用纯文本切片**（截断的 md 会渲染出破损结构），
  只有展开态走完整 md。

## 测试锚点

| 文件 | 覆盖 |
|---|---|
| `agent/cancel_turn_end_test.go` | PhaseDone on cancel / suppress while waiting / isWaitingUserPause |
| `serverapp/structured_decision_reroute_test.go` | decision 重定向 + 御前自发的卡不重定向 |
| `tools/send_message_origin_fallback_test.go` | 队列路径 / CallerSessionKey / 主会话仍用 root / CLI 路径不误判 |
| `storage/sqlite/interactive_agents_test.go` | 新增孙代理 case（会话还在 → 绝不判死） |
| `web/src/chat/subagentProgressRouting.test.ts` | 逻辑↔物理双向匹配 + 跨会话仍丢弃（判别力） |
| `web/src/components/agent/TurnBody.orphanReasoning.test.tsx` | 6 case，打 `mergeIterationsForRender` 入口 |
| `web/src/structured/LongText.markdown.test.tsx` | 加粗/代码/列表 + GFM 表格 |
