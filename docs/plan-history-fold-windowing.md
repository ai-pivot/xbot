# 技术方案：历史消息「折叠视图」下发 + 按需 Hydrate + 分页强化

> 生成时间：2026-09-30
> 状态：设计定稿，待用户确认后实施
> 分支：`feat/history-fold-windowing`（基于 origin/master `6321c3e3`）
> 探索方式：4 路并发 explore（前端折叠渲染 / 历史加载链路 / 后端接口与存储 / 前端状态机）+ 关键源码人工复核

---

## 0. TL;DR

历史消息接口（`/api/history`）目前把每个 turn 的**全部迭代**（含工具详情）全量下发；实测单 turn 最多 1,661 个迭代 ≈ 3.6MB，切会话时 `/api/history` 曾达 12.9MB。而渲染层实际会把「连续纯工具迭代」折叠成一个块（`mergeToolRuns`）——**大部分下发的工具详情在折叠态根本不被逐个展示**。

本方案：后端默认下发**折叠视图**——每个 fold run 只完整下发头部迭代，被吸收的纯工具迭代降级为**骨架占位**（保序、带 `folded_tool_count` 计数、不含工具详情载荷）；前端在折叠块上渲染「+N 个工具」徽标，用户展开时经新端点 `POST /api/iterations` **段式按需拉取**（展开区域内自动滚动续拉，复用 loadMore 的 arm/disarm 哨兵模式）；消息级反向分页（`before_id` 游标 + turn 原子边界）**已存在**，只需参数微调。

**与 2026-09-21「禁止迭代截断」铁律的关系（必须先讲清）**：那次定稿禁的是「尾部截断且无取回通路 ⇒ 迭代 1..59 永久不可见」。本方案**不删任何迭代**——每个迭代号仍完整出现在响应中（真实或骨架），序列 1..N 连续无 gap，且每个骨架都有确定的取回通路。禁令要演进为：

> **迭代「存在性」必须完整：每个迭代号必须出现在响应中（真实或骨架），禁止尾部截断；迭代「工具详情载荷」允许骨架省略，但必须同时满足：① 骨架带计数；② 提供按 `(turn_id, iteration 区间)` 的完整取回端点；③ 骨架永不覆盖已加载的真实数据。**

---

## 1. 背景与目标

### 1.1 现状问题

- **体积**：`ConvertMessagesToHistoryWithIterations`（`channel/subscription.go:257`）把窗口内每个 turn 的全部 `iteration_history` 行（含 `tools` JSON：summary/args/detail——工具详情是主要载荷）装配进响应。生产取证：单 turn 1,661 迭代 ≈ 3.6MB；`/api/history` 峰值 12.9MB ⇒ 切会话 `switchMs 7175ms`、50,633 DOM 节点。
- **渲染真相**：`TurnBody.mergeToolRuns`（`web/src/components/agent/TurnBody.tsx:941`）把「头部迭代（有 tools，**可带 content/reasoning**）+ 连续纯工具迭代」折叠为**一个渲染块**，成员迭代不产生独立块、其工具只以 pill 形态合并进头部块的工具行。被吸收成员的 `summary/detail/args` 只有在用户点开单个 pill 浮层时才被读取。
- 曾经的「三处截断」（每 turn 尾部 60 迭代）因**缺 iter 即 bug、无取回通路**被 `99ff8460` 全部删除并钉死守护——任何新方案必须先回答「如何不重蹈覆辙」。

### 1.2 需求（用户原意，逐条）

1. 接口默认只返回「当前展示的内容」：被折叠的纯工具迭代，默认不返回（其工具详情）。
2. **坑**（用户点名）：被折叠成同一块的第一个工具，可能来自有 content 或 reasoning 的迭代——**头部迭代必须完整返回**；之后所有被折叠成员必定是纯工具迭代。
3. 在展开的窗口里加入**自动滚动加载**逻辑。
4. 为纯工具迭代加一个 **index**（寻址用）。
5. 简化视图需返回**被折叠工具数量**，或返回能让前端计算出该数字的信息。
6. 进一步：整个历史**分页加载**，从最后向之前分页；**被折叠工具的迭代必须作为整体处理**（分页边界不得劈开）。

### 1.3 目标

- G1：长会话首屏/翻页的 history payload 从 MB 级降到 ~100KB 级（收益以实测为准，方案内附估算口径）。
- G2：默认渲染与现状**像素级等价**（折叠块的工具行少掉的成员 pills 用「+N 工具 · M 迭代」徽标占位——这是唯一的可见差异，且是本特性 UI 本身）。
- G3：展开后数据完整、无 gap、可幂等重放；加载失败可降级（复用 `replay_gap → reload` 通路）。
- G4：不破坏任何既有不变量：连续前缀渲染、`unreachableGapSig` gap 检测、`history_replaced` 幂等短路、live/SSE 路径、压缩点定位、虚拟列表行高。

---

## 2. 现状分析（探索结论）

### 2.1 渲染折叠规则（`mergeToolRuns`，`TurnBody.tsx:941-956`）

```ts
hasTools(it)  = it.tools.length > 0
absorbs(it)   = hasTools(it) && !it.content && !it.reasoning   // 纯工具迭代
for i in iters:
    head = iters[i]
    if !hasTools(head): 输出 head（独立块，绝不合并）      // 纯文本/纯思考迭代
    else:
        j = i; tools = [...head.tools]
        while j+1 < len && absorbs(iters[j+1]): j++; tools.push(...iters[j].tools)
        输出 (j==i) ? head : { ...head, tools }            // 保留**头部**迭代号
```

要点：
- **头部允许带 content/reasoning**（`hasTools` 只要求 tools>0）——这正是用户点名的坑：折叠块的首工具可能属于有文本的迭代。历史上曾把「头部无文本」加进条件，导致"失败 chip + 失败 pill 单独成行"事故（源码注释 `:934-940`）。
- 被吸收成员的 content/reasoning 按定义必空 ⇒ 丢弃不丢信息；`{...head, tools}` 保留头部的一切（iteration 号、content、reasoning、subAgents、指标）。
- 合并块引用不稳定（每次 `{...head, tools}` 新建对象）——但 iteration 号不变 ⇒ 高度缓存 key（`turnID:iteration`）稳定，几何不抖。
- **GenUI 工具**（`uiMode` 非空）由 `FoldedToolGroup` 分流渲染为顶层卡片（元数据驱动，永不折叠成 pill）；`mergeToolRuns` 层面 GenUI 不阻碍吸收（成员的 GenUI 卡片照常随块渲染）。

### 2.2 历史加载链路

```
POST /api/history {channel, chat_id, limit=30(默认)/100(前端), before_id=0}
  → channel/web/web_api.go:90 handleHistory
  → serverapp/callbacks.go:292 HistorySnapshot
      ├─ sess.GetHistoryBeforeForDisplay(beforeID, limit)      // 消息行窗口
      │    └─ storage/sqlite/history.go:1247 replayForDisplayWindow
      │         └─ ★ turn 边界对齐：窗口下界回退到该 turn 第一条 user 行
      │           （cursor 必在 turn 边界 —— 消息分页天然 turn 原子，测试守护）
      ├─ GetIterationHistoryByTurns(tenantID, turnIDs)          // 一次 IN 批量、全量
      └─ channel.ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)
  ← {messages, processing, active_progress, last_seq, has_more, oldest_id}
```

- **前端**：`useChatMessages.reload()`（初始 `limit:100`）→ `parseHistoryMessages` → `store.mergeHistory(replace:true)` → `useAgentChatState` effect → `historyToReplaced`（`chat/integrate.ts:38`）→ `dispatch('history_replaced')`（`chat/reduce.ts:1144`）→ `deriveRows` → `rowsToChatMessages` → `MessageList`（虚拟列表）→ `TurnBody`。
- **loadMore 向上翻页**（`useChatMessages.ts:558`）：`before_id = oldest_id` 游标、`limit:100`、IO 哨兵 + `loadMoreArmedRef` 触发权状态机（一次手势=一次请求，实测风暴根治）、停机判据 `noExactDups.length===0`、`ΔscrollTop==ΔtotalSize` 视口锚定。
- **`history_replaced` 的幂等**（`reduce.ts:1425-1437`）：历史 effect 每帧跑，逐项引用相等 ⇒ 返回原 state（零渲染）。**任何新增字段都必须参与幂等/引用稳定性**，否则长 turn 卡顿回归（历史教训）。
- **`active_progress`**：仅 `beforeID==0`（首屏）附带，busy 会话的 live 快照（含 `iteration_history` 全量）。

### 2.3 存储与协议

- `iteration_history`（每会话独立 DB，v71）：
  - **turn 内序号 = `iteration` 列**（1 起单调连续；续跑经 `resolveResumeTurnID + IterationStart` 续接不重置）——**这就是天然 index**。
  - `id` 列是全表自增主键，但**现有所有查询都不 SELECT 它**；`message_id` 恒为 0。
  - 现有索引只有 `(tenant_id, turn_id)` 和 `(message_id)`；**没有** `(tenant_id, turn_id, iteration)` 复合索引，也**没有任何** iteration 范围查询。
- `protocol.HistoryIteration`（`events.go:211`）：`Iteration/Content/Reasoning/Tools/Tokens/TTFTMs/TokensPerSec/TotalMs/ElapsedWall`。
- `HistoryMessage.IterationsTruncated`（`events.go:252`）：死字段（恒 0，截断已删）；前端 `AssistantMessage.tsx:97-103` 有配套的「更早的 N 个迭代未加载」死钩子。**语义是"早于窗口、已丢弃"，与"被折叠"不同义，不复用**（复用会让 `history_iterations_complete_test.go:36` 的 `==0` 断言语义混乱）。

### 2.4 关键约束（不可违反）

| # | 约束 | 出处 | 对方案的影响 |
|---|---|---|---|
| C1 | **迭代号连续前缀渲染**：`continuousIterations`/`extendContiguous` 遇断号即截断其后全部 | `progressStore.ts:131`、`TurnBody.tsx:965` | 折叠视图**绝不能让响应里出现迭代号空洞** ⇒ 骨架占位 |
| C2 | **`unreachableGapSig`**：合并后仍有洞且洞在权威窗口外 ⇒ 整会话强制 reload | `reduce.ts:133/1177`、`p0-unreachable-gap.test.ts` | 同上；骨架保序即无洞 |
| C3 | **`mergeIterations` 同号权威覆盖**（union，dedup by iteration 号） | `reduce.ts:97` | 骨架/真实同号合并方向必须显式定义 |
| C4 | **`history_replaced` 幂等短路**：字段引用不稳定 ⇒ 每帧重建 Turn ⇒ 卡顿回归 | `reduce.ts:1425` | 新字段走同一套 `reuseIfSame`/引用透传纪律 |
| C5 | **envelope seq（per-route）与 ProgressEvent.Seq（per-Run）不可混用** | `web-linearizability.md:150` | 新端点走 REST，不进 SSE envelope |
| C6 | **消息分页 turn 边界对齐不可破坏** | `history.go:1283-1304` + `history_display_test.go` | 「折叠段整体性」由消息分页的 turn 原子天然保证 |
| C7 | **每会话一 DB**：新查询必须经 `TenantSession.SessionService()` | `session-db.md` | 新 SQL 的落点 |
| C8 | **`CommittedPayload` 非空约束**（`NonEmpty<WebIteration>`） | `chat/types.ts:131` | 骨架也是 WebIteration，turn 非空性不变 |
| C9 | **live/SSE 路径迭代完整**（流式事件、`text_final`/`phase_done` 的 progressHistory） | `reduce.ts:824-1000` | 骨架化只作用于 DB 历史投影；live 照旧 ⇒ 两视图在 `mergeIterations` 处交汇，靠 C3 规则收口 |

---

## 3. 核心设计

### 3.1 D1：骨架占位迭代（skeleton）——「省略载荷」而非「删除迭代」

被折叠吸收的纯工具迭代**不从 `iterations` 数组删除**，而是降级为骨架：

```jsonc
// 真实迭代（head / 带文本 / GenUI / 首尾关键迭代）
{ "iteration": 3, "content": "...", "reasoning": "...",
  "tools": [ { "name": "Shell", "label": "...", "status": "done", "summary": "...", "args": "...", ... } ],
  "tokens": 812, "ttft_ms": 240, ... }

// 骨架迭代（被折叠吸收的纯工具成员）—— 同号保序，载荷省略
{ "iteration": 4, "folded": true, "folded_tool_count": 2,
  "content": "", "reasoning": "",           // 按定义必空
  "tools": null,                            // ★ 省略的就是这坨（含 summary/args/detail）
  "tokens": 63, "ttft_ms": 120, ... }       // 轻量指标保留（插件 IterationSlot 指标不受影响）
```

- 协议变更：`protocol.HistoryIteration` 增加
  ```go
  // Folded = 该迭代的工具详情按「折叠视图」省略（该迭代是某 fold run 的被吸收成员）。
  // 前端不得把它当成"无工具迭代"；它携带 FoldedToolCount 计数，且可经
  // POST /api/iterations 按 (turn_id, iteration 区间) 完整取回。
  Folded bool `json:"folded,omitempty"`
  // FoldedToolCount = 该迭代真实拥有的工具数量（= DB tools JSON 数组长度）。
  // 折叠块徽标的「+N 个工具」= Σ成员 FoldedToolCount —— 计数由前端计算，后端只给原料。
  FoldedToolCount int `json:"folded_tool_count,omitempty"`
  ```
- **体积**：骨架每条 ~100B vs 全量几 KB（tools 详情占绝对大头）。1,661 迭代 turn：≈3.6MB → ~150-200KB（骨架化 + 保留指标数字；最终以 fixture 实测为准，P0 验证步骤里明确要求量测）。
- 为什么不是「删除 + fold_runs 侧表」：数组稀疏 = C1 断号截断 + C2 gap 强制 reload，且要同步改 `extendContiguous`/`continuousIterations`/`unreachableGapSig`/`foldToolsIntoIterations`/高度 key 一整圈——改动面大、全是回归雷区。骨架保序让这组不变量**零改动**。

### 3.2 D2：fold run 判定——单一规范、后端同构实现

**后端必须跑完整的 run 边界判定**（不能只做 per-iteration 判定），因为需求 2 要求**头部迭代完整返回**——head 是谁取决于吸收链：

```go
// channel/fold_view.go（新文件）——与前端 mergeToolRuns 同构的 Go 版判定
// 规范（唯一来源，两侧对齐）：
//   head      = 首个 tools>0 的迭代（可带 content/reasoning）
//   absorbed  = head 之后连续的「纯工具迭代」：
//               content=="" && reasoning=="" && tools>0 && 不含 GenUI 工具(UIMode!="")
//   成员 → 骨架（tools=nil, folded=true, folded_tool_count=len(tools)）
//   head、带文本迭代、无工具迭代、含 GenUI 工具的迭代 → 完整返回
func FoldIterationView(recs []sqlite.IterationRecord) []HistoryIteration
```

- 判定基准对齐现状代码语义：`Content == "" && Reasoning == ""`（DB 空串判定，与前端 `!it.content && !it.reasoning` 同构；不做 TrimSpace——真实数据里 content 非空即有内容）。
- **GenUI 豁免**：含 `UIMode != ""` 工具的迭代不骨架化（它们渲染为顶层卡片，属于「当前展示的内容」）。
- **首迭代不骨架**：一个 run 的 head 即使是纯工具迭代也完整返回（它贡献折叠块的首批 pills——需求 2 的镜像规则）。
- **前端单点推导 run 边界**：`mergeToolRuns` 是 fold run 边界的唯一前端推导点（live 与历史走同一函数），后端不下发边界侧表。两侧判定的一致性由**同构 fixture 契约测试**钉死（见 §6-T1）。

**前端 `mergeToolRuns` 扩展**（`TurnBody.tsx`，约 +6 行）：

```ts
const absorbs = (it) => (it.tools.length > 0 || it.folded) && !it.content && !it.reasoning
// 合并时：
//   tools 只 concat 真实成员的 tools（骨架无 tools 数组，不 concat）
//   块级折叠计数 foldedToolCount = Σ 被吸收成员的 foldedToolCount
//   块级折叠成员 foldedIters = 被吸收成员的 iteration 号列表（骨架+真实混合，
//   live 真实成员照旧 concat tools 且不计数）
out.push(j === i ? head : { ...head, tools, foldedToolCount, foldedIters })
```

混合语义说明：同一 run 内「历史骨架成员」与「live 已提交的真实纯工具成员」可以共存（turn 提交后、历史 reload 前）——真实成员贡献 pills，骨架成员贡献计数，两者都满足 absorbs，规则自洽。

### 3.3 D3：计数——「能让前端计算」的形态

- **块级**「+N 个工具」= Σ 成员 `folded_tool_count`（`mergeToolRuns` 合并时累加）。
- **块级**「M 个迭代」= 成员数（前端自有）。
- 不新增 `HistoryMessage` 级汇总字段、不发明 `fold_runs` 侧表——**每个骨架自带计数**就是用户要的「让前端可计算」的形态，且 per-iteration 计数让「部分展开/部分加载」的中间态也能正确显示剩余计数。

### 3.4 D4：按需加载——`POST /api/iterations` + 段式拉取 + 新事件

**端点**（走 REST `authenticatedPOST`，与 `/api/history` 同风格；不走 SSE envelope——C5）：

```jsonc
// 请求
{ "channel": "web", "chat_id": "xxx", "turn_id": 9,
  "from_iteration": 4,            // 闭区间起点
  "limit": 100 }                  // 段大小，服务端上限 500
// 响应
{ "ok": true,
  "iterations": [ /* 完整 HistoryIteration（真实，folded=false） */ ],
  "next_from_iteration": 104,     // 下一段起点；has_more=false 时为 0
  "has_more": false }             // 已到该 turn 最后一个迭代
```

- 服务端：新 SQL `GetIterationHistoryRange(tenantID, turnID, fromIter, limit)`
  （`storage/sqlite/session.go`，紧邻 `GetIterationHistoryByTurn:517`）：
  ```sql
  SELECT ... FROM iteration_history
  WHERE tenant_id = ? AND turn_id = ? AND iteration >= ?
  ORDER BY iteration ASC LIMIT ?
  ```
  经 `TenantSession.SessionService()`（C7）；**同时新增复合索引 `(tenant_id, turn_id, iteration)`**（迁移：`idx_iter_history_turn (tenant_id, turn_id)` 升级为三列；老库 `CREATE INDEX IF NOT EXISTS` 幂等追加）。
- 转换复用 `channel/fold_view.go` 的映射（同一段代码服务 `/api/history` 装配与新端点，防两处映射漂移；注意现有 `MaterializeIteration` 丢弃 `tpot_ms/input_tokens/...` 的既有行为不扩权）。
- Handler：`channel/web/web_api.go` + 路由 `channel/web/web.go`（`POST /api/iterations`）→ `callbacks.IterationsRange(...)`（`serverapp/callbacks.go`）。鉴权与 `/api/history` 完全一致（同会话属主校验：非属主会话 404，不泄露会话存在性）。

**前端编排**：

- `web/src/components/agent/api.ts`：`fetchIterations(params): Promise<IterationsResponse>`。
- 新 hook `useFoldedIterations`（或并入 `useChatMessages`，倾向独立 hook + `ChatStore.dispatch` 单通道）：管理 per-run 加载状态（`Map<turnID, {loading, nextFrom, hasMore}>`）。
- **新 DomainEvent**：`chat/types.ts`
  ```ts
  { type: 'iterations_loaded', turnID: number,
    iterations: WebIteration[], nextFrom: number | null, hasMore: boolean, seq?: null }
  ```
- **`reduce.ts` 新 case**（模板 = `text_final` 的 committed 增量分支 `:940-968` + `phase_done` 的 `mergeIterations`）：
  - 目标 turn `committed`/`frozen`/`live`（三态都 union——live 分支同构）→ `mergeIterations(既有, incoming)`；
  - **幂等短路**：union 结果逐项引用相等 ⇒ 返回原 state（C4）；
  - 不触碰 `activeTurn`/`lastSeq`/`gapReloadToken`。
- **段式自动滚动加载**（用户需求 3 的落点）：
  1. 用户点击折叠徽标 → dispatch 第一段请求（`from_iteration = run 首个成员迭代号`）；
  2. 响应 union 后，若 `has_more && nextFrom <= run 尾成员迭代号` → 徽标变为「加载中」态并在**展开的 pill 行区域尾部渲染哨兵**（IO，rootMargin 适度提前量）；
  3. 哨兵可见 → 拉下一段；**触发权复用 `loadMoreArmedRef` 模式**（IO 驱动 arm/disarm，observer deps 只含哨兵存在性，loading/回调走 ref 现读——防「一次手势 11 次请求」风暴复辟）；
  4. 段全部到位（run 内所有成员非 folded）→ 徽标消失，pills 全量渲染（>8 工具时沿用既有 `7 + +N` 溢出）。
- 失败降级：请求失败 → 徽标转「重试」态；连续失败 → 复用 `replay_gap{force_reload}` → `reload()` 既有通路整会话对账（骨架化响应本身完整，reload 安全）。

### 3.5 D5：`mergeIterations` 骨架感知（同号合并方向）

`reduce.ts:97` 的同号覆盖规则显式化为四象限：

| incoming \ prev | prev 骨架 | prev 真实 |
|---|---|---|
| **incoming 真实** | incoming 胜（hydrate 替换骨架） | incoming 胜（现状：权威覆盖） |
| **incoming 骨架** | prev 胜（引用稳定，幂等零渲染） | **prev 胜**（★骨架永不覆盖真实——历史 reload/`active_progress` 不得抹掉已展开数据） |

判定函数 `isFoldedIteration(it)`（`tools.length===0 && it.folded`——tools 空且未标 folded 的空迭代是另一种合法形态，不得混淆）。这是本方案最关键的一致性规则：**「折叠」是数据缺失标记，不是新数据**。

### 3.6 D6：`active_progress`（busy 恢复视图）—— 阶段 P1

busy 会话切回/熄屏恢复时 `active_progress.iteration_history` 仍全量（FetchAll），会绕过 P0 收益（且历史上 11.2MB 事故正来自它）。P1 把 `FoldIterationView` 同构接入 `GetActiveProgress`（`agent/agent_backend_methods.go`）的快照与增量（`fromIter` delta）两条路径：

- 引擎内存快照 `iterationHistories` 里存的是 `agent` 侧结构，骨架化在**投影层**做（不碰引擎内部，`lastProgressSnapshot`/增量协议不变）；
- 增量段返回骨架同样合法（C3 四象限兜底）；
- `maxIncrementalIterations=30` 增量限流的 `resync_required` 语义不变。
- 风险注记：CLI（TUI）也消费 `get_active_progress`/`get_history` RPC——见 §7-R5 的兼容决策。

### 3.7 D7：历史整体分页——已就绪，微调收尾

- **方向**：现状 `before_id` 游标已是从最新往最旧 ✓（需求 6 前半自动满足）。
- **整体性**：`replayForDisplayWindow` 的 turn 边界对齐（C6）保证**分页永不在 turn 中间劈开** ⇒ 迭代（连同 fold run）天然整体 ✓（需求 6 后半自动满足——这也是用户设计「折叠段整体处理」的既有结构保证，方案确认而非新造）。
- 微调（P2）：
  - 初始页 `limit:100 → 50`（`useChatMessages.ts:497`）：骨架化后单页体积约降一个数量级，更小首页 + 更稳的翻页节奏；**最终值以实测定**（P0 验证步骤含 payload 量测）。
  - （可选）loadMore 哨兵 `rootMargin` 提前预取，翻页无感。
  - RPC `get_history`（CLI 链路，`req_types.go:327` 无分页参数）**不动**——CLI 是 TUI 全量渲染场景，收益低、改动面大；标注为「明确不做的范围」。

### 3.8 D8：「为纯工具迭代加 index」——由现有 `iteration` 列 + 新复合索引满足

- **逻辑 index**：turn 内 `iteration` 列（1 起单调连续、续跑续接、全链路 dedup/高度 key/合并键都在用它）——按需拉取的寻址键就是 `(turn_id, iteration)`，**无需发明新序号**。
- **物理 index**：新增 `(tenant_id, turn_id, iteration)` 复合索引，让 `iteration >= ? ORDER BY iteration LIMIT ?` 范围查询免全 turn 扫描（现状最大 turn 1,661 行，扫也快，但索引成本极低且防未来量级）。
- `iteration_history.id`（全表自增主键）**不引入**：跨 turn 不连续、与前端任何 key 无关联，引入只添混乱。
- 骨架迭代的 `iteration` 号即「纯工具迭代在折叠段中的 index」——展开加载用它定位，徽标计数用它圈定 run 区间。

### 3.9 协议变更汇总

| 层 | 变更 |
|---|---|
| `protocol/events.go` | `HistoryIteration` + `Folded` + `FoldedToolCount`（omitempty，旧客户端忽略 ⇒ **向后兼容**） |
| `protocol/events.go` | `HistoryMessage.IterationsTruncated` 保持恒 0 不动（语义不同，不复用） |
| 新 REST | `POST /api/iterations`（请求/响应见 §3.4） |
| 前端 `types/shared.ts` | `WebIteration` + `folded?: boolean` + `foldedToolCount?: number` |
| 前端 `chat/types.ts` | `DomainEvent` + `iterations_loaded` |
| 鉴权 | 与 `/api/history` 同（cookie `authenticatedPOST` + 会话属主校验） |

---

## 4. 关键边界与不变量（实施时的检查表）

1. **head 永不骨架**——run 的头部迭代（纯工具形态时也是）完整下发；其 pills 是折叠块的默认可见内容（需求 2）。
2. **GenUI 迭代永不骨架**（顶层卡片属于「当前展示内容」）。
3. **turn 的最终回复迭代永不骨架**——最后迭代含 content（非纯工具）自然豁免；被取消的 turn 末迭代可能是纯工具，按规则处理（可能骨架），展开可取回，无信息丢失。
4. **序列 1..N 连续无洞**（C1/C2）：骨架保序；`iterations_loaded` union 同号替换不产生新洞。
5. **骨架永不覆盖真实**（D5 四象限）——覆盖 `history_replaced` 重放、`active_progress` 水合、`text_final` 并路等所有 union 入口。
6. **幂等与引用稳定**（C4）：`iterations_loaded` 重放返回原 state；`normalizeWebIteration` 产出的骨架对象在未变化时引用稳定（`parseHistoryMessages` 每次构造新数组——同现状，幂等短路依赖 reduce 层 `reuseIfSame`，不新增要求）。
7. **压缩点定位不变**：`Compactions.AfterIteration` 依赖迭代号——骨架保留迭代号 ⇒ 定位语义不变（`compactionByIter` 免检）。
8. **被吸收成员的 `subAgents` 现状即不渲染**（`{...head}` 只保留 head 的）——本方案不改变该语义（与现状一致），如需修复另立 issue。
9. **i18n**：徽标文案三语（zh/en/ja）新 key；**命名避开禁词** `collapseAll*`/`collapseLevel*`/`mergeTools*`/`processed`/`collapseProcess`（`noLegacyFoldFormat.test.tsx` 的 `FORBIDDEN_CODE`/`DEAD_KEYS` 会红）——建议 `agent.fold.toolsBadge` / `agent.fold.expand` / `agent.fold.loadingMore`。
10. **虚拟列表/窗口化**：折叠块高度变化（徽标出现/消失、展开 pills）由既有 `record 高度变化自动 unsettle + 重新结算` 机制兜住，无需新机制；但 E2E 必须覆盖「展开前后滚动总高一致性」（曾有 content-visibility 鬼打墙教训：占位高度机制不得引入滚动总高漂移——本特性不新增占位机制，展开是真实内容挂载，风险低，仍需守护）。

---

## 5. 实施阶段

> 每阶段独立可交付、CI 全绿后再进下一阶段；阶段内步骤按依赖排序。

### P0：折叠视图 + 按需加载（核心闭环）

| # | 文件 / 函数 | 改动 |
|---|---|---|
| 0.1 | `protocol/events.go` | `HistoryIteration` + `Folded` + `FoldedToolCount` |
| 0.2 | `channel/fold_view.go`（新） | `FoldIterationView(recs)`：同构 run 判定 + head/GenUI/文本豁免 + 骨架映射；`IsPureToolIteration(rec)` 导出供测试 |
| 0.3 | `channel/subscription.go:257` `ConvertMessagesToHistoryWithIterations` | 结构化装配处（`:345-378`）改调 `FoldIterationView`（骨架化只发生在该路径；legacy `Detail` 回落路径不动——它只在无结构化数据时触发，老数据无骨架） |
| 0.4 | `storage/sqlite/session.go` | `GetIterationHistoryRange(tenantID, turnID, fromIter, limit)` + `scanIterationRecords` 复用 |
| 0.5 | `storage/sqlite/sessiondb.go` + schema 迁移 | `CREATE INDEX IF NOT EXISTS idx_iter_history_turn_iter ON iteration_history(tenant_id, turn_id, iteration)` |
| 0.6 | `serverapp/callbacks.go` | `IterationsRange(senderID, sel, turnID, fromIter, limit)`（属主校验 + 会话库收口） |
| 0.7 | `channel/web/web_api.go` + `web.go` | `handleIterations` + 路由 `POST /api/iterations` |
| 0.8 | `web/src/types/shared.ts` + `components/agent/normalize.ts` | `WebIteration.folded/foldedToolCount` + `normalizeWebIteration` 收口解析 |
| 0.9 | `web/src/chat/types.ts` + `chat/reduce.ts` | `iterations_loaded` 事件 + case（union + 幂等）+ `mergeIterations` 骨架感知四象限 |
| 0.10 | `web/src/components/agent/TurnBody.tsx` | `mergeToolRuns` 吸收扩展 + 块级 `foldedToolCount/foldedIters` 累加 |
| 0.11 | `web/src/components/agent/FoldedToolGroup.tsx`（或新组件 `FoldedToolsBadge.tsx`） | 徽标（+N 工具 · M 迭代 / 加载中 / 重试）+ 展开点击 + 段加载哨兵（arm/disarm） |
| 0.12 | `web/src/components/agent/api.ts` + 新 `useFoldedIterations` | `fetchIterations` + per-run 加载编排 + dispatch |
| 0.13 | `web/src/chat/integrate.ts` + `derive.ts` + `AssistantMessage.tsx` | 字段透传（`historyToReplaced`→`commitViaFold`→Row→`TurnBody` props）；死钩子 `iterationsTruncated` 提示保持不动（P2 再决定移除） |
| 0.14 | i18n `zh-CN/en/ja` | 徽标/加载/重试文案 |
| 0.15 | 守护测试 | 见 §6 |

### P1：`active_progress` 折叠视图（busy 恢复）

| # | 文件 | 改动 |
|---|---|---|
| 1.1 | `agent/agent_backend_methods.go` | `GetActiveProgress` 的 FetchAll/增量投影调 `FoldIterationView`（投影层，不碰引擎内存结构） |
| 1.2 | 契约测试 | `active_progress_snapshot_complete_test.go` 语义演进（同 §6-T2） |
| 1.3 | CLI/TUI 消费面评估 | 见 §7-R5 决策 |

### P2：分页微调与收尾

| # | 文件 | 改动 |
|---|---|---|
| 2.1 | `hooks/useChatMessages.ts:497` | 初始页 `limit` 按实测调优（100 → 50 候选） |
| 2.2 | `MessageList.tsx` loadMore 哨兵 | （可选）`rootMargin` 预取提前量 |
| 2.3 | `AssistantMessage.tsx:97` | 移除/改造 `iterationsTruncated` 死钩子提示 |
| 2.4 | docs | gotchas 条目修订 + 新条目（见 §6-D） |

---

## 6. 守护测试（判别力优先：还原旧实现必红）

### 演进既有（语义升级，不是删除防线）

| 测试 | 现断言 | 演进为 |
|---|---|---|
| T1 `channel/history_iterations_complete_test.go` | 120 迭代全量下发、`IterationsTruncated==0`、1..120 连续 | 120 迭代**号全部下发**（真实+骨架混合、1..120 连续无洞）+ 纯工具迭代 `Folded==true` 且 `FoldedToolCount==len(tools)` + head/文本/GenUI 迭代完整 + **`/api/iterations` 可完整取回 1..120**（含 tools 全文） |
| T2 `web/src/chat/iterationBound.test.ts` | 客户端不得截断 | 保留「迭代号序列不截断」+ 新增「骨架不覆盖真实」「骨架保序不造洞」 |
| T3 `agent/active_progress_snapshot_complete_test.go`（P1） | FetchAll 500 迭代完整 | 同 T1 模式（快照视图骨架化 + 取回端点兜底） |

### 新增

| # | 测试 | 断言要点 |
|---|---|---|
| T4 同构契约（Go `channel/fold_view_test.go` + TS `turnBody` 单测） | **同一组 fixture**（含：head 带文本/head 纯工具/混合 run/GenUI 豁免/单迭代 run/全纯工具 turn/取消 turn 尾迭代纯工具）在两侧跑出**相同 run 边界与相同骨架集合**——判定漂移即红 |
| T5 SQL `storage/sqlite` | `GetIterationHistoryRange`：区间闭开边界、limit 截断、`next_from` 计算、跨段拼接连续、tenant/turn 隔离 |
| T6 `mergeToolRuns`（TS） | 骨架成员被吸收且计数累加正确；head 工具照常 concat；混合 run（真实+骨架成员）正确 |
| T7 `mergeIterations` 四象限（TS） | 真实胜骨架 / 骨架不覆盖真实 / 骨架vs骨架引用稳定 |
| T8 `reduce iterations_loaded`（TS） | union + 幂等重放返回原 state + committed/frozen/live 三态路径 + 不触碰 lastSeq/gapReloadToken |
| T9 `unreachableGapSig` | 骨架序列 + 段加载中途 ⇒ **不**触发 gap reload |
| T10 REST `channel/web` | `/api/iterations`：属主校验（非属主 404）、turn 不存在、越界 from、limit 上限 500 |
| T11 E2E `web/e2e/folded-iterations.spec.ts` | 真实浏览器：徽标计数正确（N=ΣtoolCount）→ 展开 → 段式加载（mock 分段响应，断言请求数=段数）→ pills 全渲染无 gap → 再次展开幂等（零额外请求）；加载失败 → 重试 → 成功；展开前后滚动总高稳定（无鬼打墙） |
| T12 性能守护（Go） | fixture 1,661 迭代 turn：折叠视图序列化体积 < 全量视图的 10%（数字阈值以本次实测校准一次后钉死，防体积回潮） |

### 文档同步（落地 PR 内完成）

- `docs/agent/gotchas-web-frontend.md`：§「迭代历史禁止任何有界化/截断」条目 → 修订为 §0 的新表述（含本条目的链接与事故背景，**保留历史教训原文**，追加演进说明）。
- `channel/subscription.go:1377` 的「禁止再引入任何有界窗口/尾部截断」注释同步修订（明确「骨架≠截断」的三个充要条件）。
- `docs/agent/web-consistency-design.md`：Snapshot（`get_history`）语义补充折叠视图；新增 `/api/iterations` 到 Snapshot 拉取清单。
- AGENTS.md 的 warnings 索引里「三处压体积各截一刀」条目追加后续演进指针。
- docs-site：不涉及公共 API/配置/工具面变化，无需更新（本特性是内部历史投影优化）。

---

## 7. 风险与缓解

| # | 风险 | 缓解 |
|---|---|---|
| R1 | **两端判定漂移**（Go `FoldIterationView` vs TS `mergeToolRuns`）⇒ 后端骨架化了前端不会吸收的迭代 ⇒ 渲染出「空块」 | T4 同构 fixture 契约测试钉死；规范唯一来源 = §3.2 的伪代码（两处实现都引用它）；变更任何一侧必须跑双测 |
| R2 | **骨架覆盖真实**（reload/`active_progress` 抹掉已展开数据） | D5 四象限 + T7；这是唯一敢动 `mergeIterations` 语义的点，保持其他象限行为不变 |
| R3 | **展开大 run 一次性拉爆**（一个 run 数百迭代） | 段式拉取（`limit` + `next_from`）+ 哨兵续拉；服务端硬上限 500/段 |
| R4 | **`history_replaced` 幂等被新字段击穿** ⇒ 长 turn 卡顿回归 | 新字段进 `reuseIfSame` 比较；T8 幂等断言；`turn_perf_pipeline.test.tsx` 照跑 |
| R5 | **CLI/TUI 与 RPC 消费方兼容**：`get_history` RPC（CLI）走同一 `ConvertMessagesToHistoryWithIterations` ⇒ CLI 也会拿到骨架 | `Folded/FoldedToolCount` 是 omitempty 新字段——**CLI 现状把迭代全量渲染进 TUI 滚动区**，骨架会让 CLI 的纯工具迭代显示为空。**决策点**：a) CLI 跟进（TUI 有自己的折叠渲染，需评估）；b) P0 先让 `ConvertMessagesToHistoryWithIterations` 接受 `foldView bool` 参数——REST 路径 true、RPC 路径 false（CLI 零影响），P1/后续再评估 CLI。**推荐 b**（改动一行调用点，风险隔离）。⚠️ 前提核对：CLI 的迭代渲染消费 `HistoryIteration.Tools`——方案探索未覆盖 CLI 渲染细节，实施 0.3 时先跑一次 CLI 手测回归 |
| R6 | **E2E mock 漂移**（历史教训：mock 字段与真机不符 ⇒ 假绿） | T11 用真实 handler 构造（go test 起服务 或 真实链路 spec）；字段断言以真机载荷探针为准 |
| R7 | **展开态不持久**：切会话再切回（replace reload）后折叠态重置（数据不丢，交互态重置） | 接受为设计语义（与「用户没主动展开」一致）；如未来要持久化，走 per-session UI 偏好存储另立方案 |
| R8 | **`parseHistoryMessages` 的 `assertIterationContinuity`**（`useChatMessages.ts:172`）若已存在连续性断言，骨架响应可能触发其告警路径 | 实施时核对该函数：它应天然通过（骨架保序）；若它有「tools 必须非空」类断言则按新语义调整——属于 §6-T2 客户端语义演进的一部分 |

---

## 8. 验证方案

1. **单元/契约**：§6 全清单先红后绿（T4-T12 每条都有「还原即红」判别力自证）。
2. **体积量测**：fixture 生成 1,661 迭代 turn（含真实比例的纯工具迭代），对比 `/api/history` 响应字节数（折叠 vs 全量视图），记录进 PR 描述与 T12 阈值校准。
3. **端到端手测脚本**：长会话（生产取样 `chat_07B68B101679` 类）切会话 switchMs 对比；展开一个 500+ 工具 run 的滚动体验与请求瀑布（DevTools 确认段数与每段体积）。
4. **回归面**：`go test ./...` + `golangci-lint` + 前端 vitest 全量 + 既有 E2E（`turn-iter-perf` / `loadmore-pagination` / `msg-actions` 等）不红。
5. **CI**：pre-commit hook 全绿（gofmt/lint/build/test + 前端 eslint/vitest/build）。

## 9. 回滚策略

- 协议字段 omitempty + 前端 `folded` 可选 ⇒ **feature flag 成本为零**：`ConvertMessagesToHistoryWithIterations` 的 `foldView` 开关（R5-b 已引入）置 false 即回滚全量视图；`/api/iterations` 端点保留无害。
- 骨架化出问题时的影响面 = 历史展示（渲染层），**不触碰 LLM 上下文**（LLM replay 走 `session_messages` + `getHistoryFromWith`，与展示投影 `replayForDisplayRecords` 是两条路——已核实 `history_command_row_test.go`/`history_iterations_complete_test.go` 的双路结构）。
- DB 零迁移风险：只加索引（`IF NOT EXISTS`），不改表结构、不改写路径。

## 10. 待用户确认的开放问题

1. **CLI/RPC 是否跟进折叠视图**（R5）：推荐先隔离（RPC 路径 foldView=false），后续单独评估 CLI。
2. **`active_progress` 骨架化的阶段归属**：本方案排 P1（切 busy 大会话场景收益明显）；若你希望首版就闭环，可把 1.1-1.3 提进 P0（增量约 1-2 天工作量）。
3. **初始页 limit 调优值**（P2-2.1）：建议以 T12 量测数据定夺（候选 30/50/100）。
4. **徽标交互形态**：本方案默认「点击徽标 = 展开+加载」；若你想要「自动展开」（run 进入视口即预取），改 IO 触发条件即可（机制已备）——涉及交互偏好，先按点击展开实施。
5. **被吸收成员的 `subAgents` 现状不渲染**（§4-8）：保持现状，如要修另立 issue。

---

## 自审记录

- ✅ 目标一致性：五点需求（默认折叠视图 / head 完整 / 展开自动滚动加载 / index / 计数可计算）+ 分页整体性（§3.7 确认由既有 turn 原子边界保证）逐条有落点；G1-G4 可验证。
- ✅ 与 2026-09-21 铁律的冲突已显式解决（§0 + §6-D 文档演进），不是绕过。
- ✅ 既有不变量全数盘点（C1-C9），骨架设计对它们零侵入；唯一动语义处（mergeIterations）以四象限显式化并配 T7。
- ✅ 步骤具体到文件:函数；依赖排序（协议→后端→前端解析→状态机→渲染→交互→测试）。
- ✅ 风险清单含真实链路陷阱（R5 CLI 兼容、R6 mock 漂移、R8 既有断言核对），无「防御性编程」式兜底——骨架语义从数据模型根上定义（folded 标记 + 取回通路），不叠加猜测性防护。
- ✅ 守护测试有判别力自证要求（先红后绿 / 还原即红），符合项目「Always Reproduce Before Fixing」原则。
