# Web MessageStore — 单一消息状态机（方案 A）

> ⚠️ **2026-09-13 现状（先读这段再看下文）**：**渲染数据源已迁移到新状态机** ——
> `AgentPanel` → `MessageList` 读 `useAgentChatState`（`chat/reduce.ts` → `derive.ts`
> → `integrate.ts`）；本文件描述的 `MessageStore.toRows()` 现在只喂
> `chat.messages`（history 映射 / 插件上下文 / debug toolbar），**不再渲染**。
> 因此性能修复必须打在新管线的 `derive/integrate` 边界上（引用稳定性 ——
> 见 AGENTS.md「2026-09-13 回归」条目与 `chat/integrate.test.ts` /
> `components/agent/turn_perf_pipeline.test.tsx`）；打在 MessageStore 上的
> memo/引用保持修复对线上渲染**无效**（这正是 2026-09-13 长 turn 卡顿复发的根因模式）。

> 目标：从结构上消除 "turn 消失/重复" 整类 bug。当前渲染层靠启发式去重（sameTurnIdx /
> exactDup / content 匹配 / eventSeq 匹配）弥合 "两套独立数据"（committed messages +
> live progress），每个启发式都有边界情况——已产生 6+ 个补丁（iter 回退拒收、eventSeq
> 冻结、stream_delta 丢失、跨 turn 迭代号 exactDup...）。MessageStore 用数据结构保证
> 唯一性，而不是用去重逻辑保证。

## 核心思想

**每个 turn 恰好 1 个 user + 1 个 assistant 槽位**，live 是 assistant 的**未完成态**
（同对象状态迁移），渲染直接读槽位排序，**零去重**。

```
Map<turnID, TurnSlot>
  TurnSlot = {
    turnID: number
    user?: ChatMessage          // 乐观 / user_echo / DB
    assistant?: ChatMessage     // 已提交（appendAssistant / DB reload 回填）
    live?: LiveState            // streaming 进行中（assistant 未完成态）
    frozen?: boolean            // cancel 后冻结（live 保留渲染）
  }
```

## 状态迁移（写入路径）

```
turn_started(turnID)  → beginTurn(turnID)          // 创建/重置 slot；旧 turn 有 live → commit
optimistic user       → setUser(turnID, msg)        // turnID=0 待绑定（bindUser 在 turn_started 时）
user_echo             → setUser(turnID, msg)        // 回填 requestID/dbID
progress_structured   → updateLive(turnID, p)        // 更新 live.iterations/tools/phase...
stream_content        → updateLive(turnID, {content})
text(content)         → commitAssistant(turnID, content, iterations)  // live → assistant 迁移
cancel                → freeze(turnID)               // 冻结 live，保留已渲染内容
reload(rows)          → mergeHistory(rows)           // 只回填 DB 字段（dbID/persisted），不覆盖 live
session(idle)         → endTurn(turnID)              // 清理 frozen
```

**关键**：`commitAssistant` 把 `slot.live` 的内容固化到 `slot.assistant`（同一逻辑消息），
**不会产生第二行**。text 事件丢失时 live 保留（turn 不消失）；reload 回填 DB 字段但
**不清 live**（进行中状态 live 权威）。

## 读取路径

```
store.toRows(): ChatMessage[]  // 按 turnID 排序；每个 slot 输出 user + assistant
                                // assistant 有 live 时迭代/工具从 live 取（合并渲染）
store.hasLive(turnID)          // 渲染层判断 streaming
```

**无去重**：Map 天然保证每 turn 每角色一条。exactDup/sameTurnIdx/dedupLiveRows
全部删除。

## turnID=0 兼容（legacy / 乐观行）

- **乐观 user**：`setUser(0, msg)` 暂存到 pending 队列；`turn_started(turnID)` 时
  `bindUser(turnID)` 绑定最后一条未持久化 user（原 bindLastUserToTurn 逻辑移入 store）。
- **legacy 行**（DB 历史无 turnID，persisted）：存 `legacy: ChatMessage[]`，渲染在顶部。
- **AskUser resume**（同 turnID 续跑）：`beginTurn` 遇 `trigger==='resume'` 保留
  iterationHistory（不清 live），只重置流式字段。

## 迟到事件路由（跨 turn 竞态）

所有事件按 `turnID` 路由到对应 slot——旧 turn 的 text 在 turn_started(N+1) 之后到达，
只更新 slot N 的 assistant，**不污染新 turn**。这从结构上消除
"commitLiveProgressAndReset 双提交"（turn 365 出现 asst-xxx + seq-xxx 双行的 bug）。

## 迭代号语义

迭代号是 **turn 内局部序号**，只用于显示（slot 内 LiveIteration 渲染）。**不参与任何
跨行匹配/去重**。跨 turn 迭代号相同是正常的——结构上不可能再触发 exactDup 误判。

## 性能

- `toRows()` 每帧调用：维护已排序的 `turnIDs: number[]`（增量插入，turnID 单调递增），
  `toRows()` 只在 active slot 的 live 变化时重建 rows（缓存，turnID 集合变化才全排）。
- 每帧只有 live 变化：缓存 committed rows + 动态附加 live 行（当前 buildMessageRows 的
  fast path 行为，但由结构保证正确）。

## 迁移计划（✅ 已全部完成）

| Step | 内容 | 状态 |
|------|------|------|
| 1 | `MessageStore` 纯状态机类（messageStore.ts）+ 单测 | ✅ 21 tests |
| 2 | useChatMessages 内部换用 MessageStore（对外接口不变） | ✅ 55 tests |
| 3 | AgentPanel 共享 store + useProgressStream live 写入 + MessageList 读 toRows | ✅ 616 tests |
| 4 | 删除 buildMessageRows/exactDup/dedupLiveRows/dedupMessages/liveMessage prop | ✅ 574 tests |
| 5 | 全量验证 + 构建部署 + CI | ✅ |

**最终架构**：live 行由 `MessageStore.toRows()` 输出（isPartial），MessageList 直接渲染
`orderMessageRows(bindTurnIDs(messages))`，liveId 匹配 isPartial 行传 liveProgress。
渲染层零去重 —— exactDup/same-turn merge 从代码库消失。

## 风险与对策

- **乐观 user 绑定时序**：turn_started 迟到 → user 保持 turnID=0 → 渲染到 pending 区
  （底部），turn_started 到达后 bind 到槽位。与现状行为一致。
- **legacy 行排序**：persisted 无 turnID 的行渲染在顶部（现状 bindTurnIDs 行为）。
- **cancel 双提交**：commitLiveProgressAndReset 删除，text 事件是唯一提交入口；
  迟到 text 按 turnID 路由不重复。
- **性能回归**：toRows 缓存 + 增量，基准对比现有 buildMessageRows（O(N) scan + copy）。

## 渲染代价与 turn 内迭代数解耦（迭代块 containment，2026-09-13）

trace 归因（12.4s 主线程，构建 `index-B1MrMIM6.js`）显示 **App JS 不随时间增长**
（index bundle 554ms），增长的是**浏览器渲染侧**：Layout ×1.70 / Paint ×1.69 /
RasterTask ×3.26 / GPUTask ×1.82，`Layout.dirtyObjects` 每 1/10 桶 16→64（×4），
以及 7 次 `UpdateLayoutTree` 单次重算 ~4,700–4,800 个元素（≈ 整个 turn 子树）
落在 70–84ms 的 React 提交里。根因是**迭代块之间没有渲染隔离**。

修复（CSS-only，`index.css` + `TurnBody`）：

| 选择器 | 声明 | 作用 |
|---|---|---|
| `.iter-block` | `contain: layout paint; content-visibility: auto; contain-intrinsic-size: auto 320px` | 失效范围限定在块内；离屏块跳过 style/layout/paint |
| `.iter-block-live` | `content-visibility: visible` | 进行中迭代每帧被改，跳过无收益且抖 |
| `.iter-blocks` | `display: block` | **必须**：Chrome 不对 flex 子项应用离屏跳过 |
| `.iter-block + .iter-block` | `margin-top: .25rem` | 复现原 `gap-1` 间距 |
| `.virt-row` | `contain: layout` | live 行长高不触发列表整体重算 |

不变量：**追加第 N+1 个迭代块的代价 = O(1)**；参与渲染的块数由视口决定
（E2E 实测：15 迭代 → 3 块，45 迭代 → 仍 3 块）。

守护：`TurnBody.test.tsx`（类名）、`index.test.ts`（CSS 声明）、
`e2e/turn-iter-perf.spec.ts`（真实 Chromium，`checkVisibility({contentVisibilityAuto:true})` 判据）。

