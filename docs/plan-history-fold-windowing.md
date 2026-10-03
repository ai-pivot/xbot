# 技术方案：历史消息「展示区域」分页加载 + 工具组详情按需下发

> 生成时间：2026-09-30（v2，按用户纠正重写）
> 状态：设计定稿，待用户确认后实施
> 分支：`feat/history-fold-windowing`（基于 origin/master `6321c3e3`）
> v1→v2 修订原因（用户 2026-09-30 指出三点）：
> ① **折叠对象是工具组（tool group），不是迭代**——v1 误建为「迭代骨架 + 新增展开徽标交互」，导致默认视图 pills 缺失、新增交互，违反「用户无感」要求；
> ② **分页计量 = 展示区域**（折叠的 tool 组算一个区域），初始加载最后 turn 的最后 100 个区域，动态往上加载——v1 沿用消息行计量，巨型 turn 单行仍带整包迭代；
> ③ **任何情况下历史必须完全连续且线性一致；用户前端体验与现状基本无感，交互完全没区别**。

---

## 0. TL;DR

现状：`/api/history` 把每个 turn 的**全部迭代**（含工具详情 `summary/args/detail`——payload 绝对大头）全量下发；实测单 turn 1,661 迭代 ≈ 3.6MB，切会话峰值 12.9MB（switchMs 7175ms）。而渲染层实际形态是：`mergeToolRuns` 把连续纯工具迭代**折叠成工具组**（一个 pill 行），工具详情只有点开浮层才被读取。

方案（两个正交优化，均不改变用户可见形态）：

1. **载荷瘦身（折叠对象 = 工具组）**：默认下发**全部迭代**（保序、连续），但窗口内所有工具只带 **pill 渲染所需的轻字段**（name/label/status/elapsedMs/exitCode/callID/uiMode），**省略详情大字段**（summary/args/detail/toolHints），迭代级打 `tools_folded` 标记。⇒ 默认视图与现状**像素级一致**（pills 全渲染、+N 徽标照旧），详情在**浮层打开时**经新端点按需拉取（mergeIterations 同号覆盖，迭代号不变）。
2. **展示区域分页**：两级分页——外层消息行 `before_id` 游标照旧（turn 原子边界已保证「区域整体性」）；内层**每个 turn 首次只下发其最后 `K=100` 个展示区域**（fold run 工具组算 1 个区域），`regions_before` 计数声明更早未下发的区域数；往上滚动时经新端点**按区域段向旧方向取回**（段边界对齐 fold run，永不劈开工具组）。

**与 2026-09-21「禁止迭代截断」铁律的关系**：那次禁的是「尾部截断且无取回通路 ⇒ 迭代永久不可见」。本方案**不删任何迭代**——任何响应窗口内迭代号序列完全连续无洞；`regions_before` 是服务端**显式声明的可取回窗口**（≠ 洞，gap 守卫不误触发）；每个 `tools_folded` 迭代都有确定取回通路。铁律演进为：

> **迭代「存在性」必须完整且线性一致**：任何响应窗口内迭代号连续、拼后连续、无静默缺失；「未下发」必须由计数显式声明且可完整取回。迭代「工具详情载荷」允许默认省略，条件：① 带 `tools_folded` 标记；② 提供按 `(turn_id, iteration)` 的详情取回端点；③ 轻字段永不覆盖已加载的完整数据。

**渲染层已核实零改动**：`progressStore.continuousIterations` 注释原文即 "does **NOT require iteration 1** — a contiguous sequence starting at any number (e.g. 12→13→14) is valid"（`progressStore.ts:145`）——尾部窗口（如 52..66）现有渲染层原生支持；`assertIterationContinuity` 只查序列**内部**断号。

---

## 1. 背景与目标

### 1.1 现状问题

- **体积**：`ConvertMessagesToHistoryWithIterations`（`channel/subscription.go:257`）把窗口内每个 turn 的全部 `iteration_history` 行装配进响应，工具详情（summary/args/detail）占绝对载荷。生产取证：单 turn 1,661 迭代 ≈ 3.6MB；`/api/history` 峰值 12.9MB ⇒ 切会话 `switchMs 7175ms`、50,633 DOM 节点。
- **消息行分页对巨型 turn 无效**：现有 `before_id` 游标按**消息行**计量（每页 100 行），一个巨型 turn = 1~2 条消息行 ⇒ 单页仍可能携带整包 3.6MB 迭代。分页计量必须下沉到**渲染块粒度**。
- **渲染真相**：折叠态下 pill 行只需要轻字段；`summary/args/detail` 只有点开浮层（`LazyPillPopover` → `ToolPopoverDetail`）才被读取（explore 实证）。
- 曾经的「三处截断」（每 turn 尾部 60 迭代）因**缺 iter 即 bug、无取回通路**被 `99ff8460` 删除并钉死守护——新方案必须先回答「如何不重蹈覆辙」（§0 已回答）。

### 1.2 需求（用户三轮输入的完整意图）

1. **完全连续且线性一致**（铁律）：任何情况下前端历史消息迭代号序列完全连续、无洞。
2. **分页从最后往前**：第一次加载**最后一个 turn 的最后 100 个展示区域**（折叠的 tool 组算一个区域），然后**动态往上**加载。
3. **折叠对象是工具组**：被折叠省略的是工具组的详情载荷；pill 行（默认展示形态）照常渲染。
4. **head 的坑**（v1 已确认，继续成立）：折叠块的第一个工具可能来自带 content/reasoning 的迭代——该迭代的文本/思考与工具轻字段照常完整下发。
5. **无感**：用户看前端与现状基本无感，交互（浮层/+N 溢出/复制/思考折叠/滚动）完全没区别。
6. 计数：简化的历史窗口视图需返回被折叠工具数量，或返回能让前端计算出这个数字的信息（轻字段数组长度 + `regions_before` 即满足）。

### 1.3 目标

- G1 首屏/翻页 payload 从 MB 级降到 ~10²KB 级（以实测校准）。
- G2 **无感验收**：默认视图与现状像素级一致；交互路径零变化；唯一新增可见物 = turn 顶部「更早区域」分隔条（与现有 loadMore 哨兵同族的渐进加载指示器）与浮层详情的极短加载态。
- G3 完整性与线性一致：见 §0 铁律演进。
- G4 不破坏任何既有不变量：连续前缀渲染、`unreachableGapSig`、`history_replaced` 幂等短路、live/SSE 路径、压缩点定位、虚拟列表/窗口化。

---

## 2. 现状分析（探索结论，含 v2 补充核实）

### 2.1 渲染折叠规则（`TurnBody.mergeToolRuns`，`TurnBody.tsx:941-956`）

```
hasTools(it) = it.tools.length > 0
absorbs(it)  = hasTools(it) && !it.content && !it.reasoning      // 纯工具迭代
head = 首个 hasTools 迭代（可带 content/reasoning —— 用户点名的坑）
贪心吸收 head 之后连续 absorbs 成员；块 = {...head, tools: 合并}，保留 head 迭代号
```

- head 也可本身是纯工具迭代；head/带文本迭代/GenUI 迭代不吸收不省略文本。
- **「展示区域」= mergeToolRuns 的输出块**：每个未被吸收的迭代 1 个区域；fold run（工具组）1 个区域。区域数 = `mergeToolRuns(iters).length`。
- GenUI 工具（uiMode 非空）由 `FoldedToolGroup` 分流渲染为顶层卡片。

### 2.2 历史加载链路（现状）

```
POST /api/history {channel, chat_id, limit=30(默认)/100(前端), before_id=0}
  → web_api.go:90 → serverapp/callbacks.go:292 HistorySnapshot
      ├─ GetHistoryBeforeForDisplay(beforeID, limit)   // 消息行窗口
      │    └─ ★ turn 边界对齐（history.go:1283-1304）：cursor 必在 turn 边界
      ├─ GetIterationHistoryByTurns(tenantID, turnIDs)  // 全量、无范围查询
      └─ ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)
  ← {messages, active_progress, last_seq, has_more, oldest_id}
```

- 前端 `loadMore`：`before_id=oldest_id` 游标 + IO 哨兵 arm/disarm（「一次手势=一次请求」）+ `noExactDups.length===0` 停机判据 + `ΔscrollTop==ΔtotalSize` 视口锚定——**全部保留不动**。
- `history_replaced` 幂等短路（`reduce.ts:1425`）：新字段必须参与引用稳定纪律。

### 2.3 v2 关键核实（方案成立的前提）

| 核实点 | 结论 |
|---|---|
| `continuousIterations`（`progressStore.ts:145`） | 注释原文 "does NOT require iteration 1 … A contiguous sequence starting at any number (e.g. 12→13→14) is valid" ⇒ **尾部窗口（52..66）渲染零改动** ✓ |
| `extendContiguous`/`fullScan`（`TurnBody.tsx:318-352`） | 从数组首元素扫起、断号即停——**不假设起点为 1** ✓ |
| `assertIterationContinuity`（`useChatMessages.ts:172`→`progressStore.ts:83`） | 只查序列内部 `curr !== prev+1` 断号——窗口内部连续即通过 ✓ |
| `unreachableGapSig`（`reduce.ts:133`） | 只对「洞部分落在 incoming（权威）窗口**之外**」触发 reload；窗口自身 [a..N] 无内部洞 ⇒ 不触发；`regions_before` 声明段永远可追赶 ✓ |
| 压缩点 `compactionByIter`（`TurnBody.tsx:981`） | anchor fallback（找不到已加载迭代 ≤ afterIteration 时 anchor=0）⇒ 压缩点渲染在窗口顶，区域段到位后自然归位——**现有 fallback 已兼容** ✓（Compactions 全量随 turn 下发，轻量） |
| `mergeIterations`（`reduce.ts:97`） | 同号权威覆盖（union by iteration 号）——「完整覆盖轻字段」与现状同向 ✓；需补「轻字段不覆盖完整」象限 |
| 存储 | `iteration` 列 = turn 内序号（1 起、续跑续接）；无范围查询、无 `(tenant_id,turn_id,iteration)` 复合索引——需新增 |

---

## 3. 核心设计（v2）

### 3.1 D1：展示区域（region）——分页计量单位

- **定义**：与前端渲染块 1:1 对齐的下发计量单位 = `mergeToolRuns` 输出块。**折叠的 tool 组算 1 个区域**（用户原话）。
- **同构判定**：后端 Go 侧 `RegionRuns(recs)` 与前端 `mergeToolRuns` 必须产出**相同的区域边界**——单一规范（§2.1 伪代码为唯一来源）、双实现、**共享 fixture 契约测试**钉死（§6-T4）。
- 判定基准：`Content == "" && Reasoning == ""`（DB 空串 vs 前端空串同构，不做 TrimSpace）。

### 3.2 D2：两级分页

**外层（消息行）——现状照旧**：`before_id` 游标从最新往最旧；`GetHistoryBeforeForDisplay` 的 **turn 边界对齐不动**（`history.go:1283`，测试守护）——它正是「区域整体性」的既有结构保证：一页永不切分 turn。

**内层（区域窗口，新增）**：

- 装配点：`ConvertMessagesToHistoryWithIterations`（`subscription.go:257`）对每个 turn：
  1. `GetIterationHistoryByTurns` 全量取迭代（数据面不变）；
  2. `RegionRuns(recs)` 划分区域；
  3. **从尾部往回数 `REGION_WINDOW = 100` 个区域**，窗口 = 第 100 区域的起始迭代..最后迭代（区域原子 ⇒ 窗口是**连续迭代号区间 [a..N]**）；
  4. 窗口内迭代经 D3 轻字段化后下发；
  5. `RegionsBefore = max(0, 区域总数 - 100)`，挂到 `HistoryMessage`。
- 协议：`HistoryMessage` 新增
  ```go
  // RegionsBefore = 该 turn 更早未下发的展示区域数（0 = 该 turn 已完整下发）。
  // 每个区域 = 前端渲染块（mergeToolRuns 同构判定；折叠的工具组算 1 个）。
  // 它是「可取回窗口」的显式声明 —— 绝不构成 gap；前端按需经
  // POST /api/regions 向更旧方向整段取回（段边界对齐区域，永不劈开工具组）。
  RegionsBefore int `json:"regions_before,omitempty"`
  ```
- turn 区域数 ≤ 100 时全量下发（`RegionsBefore=0`）——普通 turn 完全不受影响。

**内层取回端点 `POST /api/regions`**（REST `authenticatedPOST`，与 `/api/history` 同鉴权/属主校验；**不走 SSE envelope**——per-route seq 与 per-Run seq 不可混用）：

```jsonc
// 请求：向更旧方向取段
{ "channel": "web", "chat_id": "xxx", "turn_id": 47,
  "before_iteration": 52,        // 当前窗口最早迭代号（前端从 state 读）
  "region_limit": 100 }          // 服务端上限（如 100）
// 响应
{ "ok": true,
  "iterations": [ /* 迭代 [b..51]，轻字段形态 —— 段边界对齐区域 */ ],
  "regions_before": 12 }         // 仍剩更早区域数（0 = 该 turn 到顶）
```

- 服务端：`GetIterationHistoryBeforeRange(tenantID, turnID, beforeIter)` 取 `iteration < before_iteration` 的迭代（倒序量 + 正序回）→ `RegionRuns` 划分 → 从窗口边界往旧方向数 `region_limit` 个区域 → 返回段（`iteration < boundary` 的最小迭代即响应最小号）。
- 触发：前端在 turn 顶部渲染「更早区域」分隔条（`RegionsBefore > 0` 时）+ IO 哨兵——**复用 `loadMoreArmedRef` 的 arm/disarm 模式**（IO 驱动触发权，observer deps 只含哨兵存在性，loading/回调走 ref 现读——防「一次手势 11 次请求」风暴复辟）。
- 拼接：段 union 后窗口变为 [b..N]，迭代号连续；`RegionsBefore` 更新；归零后分隔条消失。
- 新 DomainEvent：`{ type: 'regions_loaded', turnID, iterations, regionsBefore }` —— reduce 新 case（模板 = `text_final` 的 committed 增量分支 `reduce.ts:940` + `phase_done` 的 `mergeIterations`）：union + 幂等短路 + 不触碰 `activeTurn/lastSeq/gapReloadToken`。
- 失败降级：请求失败 → 分隔条「重试」态；连续失败复用 `replay_gap{force_reload}` → `reload()` 既有通路（响应本身自洽，reload 安全）。

### 3.3 D3：工具组载荷瘦身（`tools_folded`）——「折叠对象 = 工具组」

- **窗口内所有迭代照常下发**（迭代号/content/reasoning/指标/subAgents 保序连续）；**所有工具只带轻字段**，迭代级打标：
  ```go
  // ToolsFolded = 该迭代的工具详情载荷（summary/args/detail/tool_hints）已按
  // 折叠视图省略，pill 轻字段（name/label/status/elapsed_ms/exit_code/
  // call_id/ui_mode/ui_libs/ui_surface/iteration）完整保留 —— 默认渲染与现状
  // 像素级一致（pills 全渲染、+N 溢出照旧）。浮层（LazyPillPopover →
  // ToolPopoverDetail）打开时按 (turn_id, iteration) 拉取完整数据，
  // mergeIterations 同号覆盖、迭代号不变。
  ToolsFolded bool `json:"tools_folded,omitempty"`
  ```
- 省略字段：`Summary / Args / Detail / ToolHints`（**payload 绝对大头**）；保留：pill 渲染与 `+N` 溢出菜单所需的全部轻字段（label 内含参数摘要——pill 第二段即取自 label）。
- **GenUI 豁免**：含 `uiMode != ""` 工具的迭代不瘦身（顶层卡片默认渲染可能消费详情，保守全量）。
- **浮层详情端点 `POST /api/iteration_detail`**：`{channel, chat_id, turn_id, iteration}` → 该迭代完整 `HistoryIteration`（全部工具完整 ToolProgress）。前端 `ToolPopoverDetail` 打开时若 `toolsFolded` → 拉取 → **mergeIterations 同号覆盖**（完整胜轻字段——与现状「同号权威覆盖」同向）→ 详情渲染。交互形态不变：浮层立即打开（标题/状态来自轻字段），详情区一个极短加载态（弱网下 skeleton）。
- **合并四象限**（`mergeIterations` 唯一语义新增——「轻」= toolsFolded）：

| incoming \ prev | prev 轻 | prev 完整 |
|---|---|---|
| **incoming 完整** | incoming 胜（浮层/区域段 hydrate） | incoming 胜（现状语义） |
| **incoming 轻** | prev 胜（引用稳定，幂等零渲染） | **prev 胜**（★轻字段永不覆盖已加载完整数据——reload/`active_progress` 不得抹掉已展开数据） |

- 「让前端计算出被折叠工具数量」：pill 行 `>8 工具 → 前 7 + +N` 的 N = 轻字段数组长度（数据全在）；区域数 = 轻字段迭代序列经 mergeToolRuns 后的块数；`regions_before` 由服务端显式下发。**无需任何额外计数字段**。

### 3.4 D4：连续性与线性一致性（铁律落点）

1. **窗口内连续**：内层窗口是连续迭代号区间 [a..N]（区域原子保证不劈迭代序列）⇒ 无内部洞 ⇒ `assertIterationContinuity` 通过、`continuousIterations`（任意起点，已核实）全渲染。
2. **regions_before ≠ 洞**：服务端显式声明的可取回窗口；`unreachableGapSig` 判据（洞部分落在权威窗口外）对它恒为「可追赶」——**永不触发整会话 reload**。
3. **拼接连续**：区域段 union 后 [b..N] 连续；段边界对齐区域 ⇒ 段与窗口衔接处迭代号天然相邻。
4. **详情 hydrate 不动迭代号**：同号覆盖 ⇒ 无新洞。
5. **外层消息分页 turn 原子**（既有）⇒ 跨 turn 不会出现「半拉子窗口」。
6. **live/SSE 路径不变**：流式迭代完整下发；turn 提交后的下一次历史 reload 才是区域窗口视图。live 迭代与历史窗口在 `mergeIterations` 处交汇，四象限收口。
7. 压缩点：全量随 turn 下发（轻量）；`AfterIteration` 落在窗口外时现有 anchor fallback（anchor=0 → 窗口顶）已兼容，区域段到位后自然归位（已核实）。

### 3.5 D5：`active_progress`（busy 恢复视图）—— 阶段 P1

切 busy 会话时 `active_progress.iteration_history`（FetchAll 全量）同样绕过瘦身收益。P1 将同一套「区域窗口 + tools_folded」接入 `GetActiveProgress`（`agent/agent_backend_methods.go`）的快照与 `fromIter` 增量两条路径（投影层做，不碰引擎内存结构；live 进行中迭代完整；`resync_required` 语义不变）。CLI 兼容决策见 §7-R5。

### 3.6 D6：「为纯工具迭代加 index」

- **逻辑 index**：turn 内 `iteration` 列（1 起单调、续跑续接）即唯一寻址键——区域段/详情端点都以 `(turn_id, iteration)` 定位，**无需新列**。
- **物理 index**：新增复合索引 `(tenant_id, turn_id, iteration)`（现状 `idx_iter_history_turn` 只到 turn_id，无范围查询）——`iteration < ? ORDER BY iteration` 倒序段查询的支撑；`CREATE INDEX IF NOT EXISTS` 幂等迁移。
- `iteration_history.id`（全表自增）不引入：跨 turn 不连续、与前端任何 key 无关联。

---

## 4. 关键边界与不变量（实施检查表）

1. **无感验收（G2）**：默认视图与现状像素级一致——文本、思考折叠、全部 pills、`+N` 溢出徽标、失败 chip、复制菜单照旧；E2E 以「同一会话全量视图 vs 窗口视图的 DOM 等价断言」钉死（§6-T11）。
2. head/带文本/思考/GenUI 迭代的文本字段照常完整下发；GenUI 迭代不做工具瘦身。
3. 窗口/段边界**永远对齐区域（fold run）**——工具组永不劈开（用户「整体处理」要求）。
4. 任何响应窗口内迭代号连续；`regions_before` 恒与实际未下发区域数一致（fixture 校验）。
5. 轻(ToolsFolded)永不覆盖完整（D3 四象限）——覆盖 `history_replaced` 重放、`active_progress` 水合、`text_final` 并路等全部 union 入口。
6. 幂等与引用稳定（`reduce.ts:1425`）：`regions_loaded` 重放返回原 state；新字段进 `reuseIfSame` 纪律。
7. `history_replaced` 对同一 turn 的**窗口变化**（region 段到达后下一次 reload 会带同一窗口）保持 union 语义——窗口不会因 reload 回退（DB 侧窗口只增不减：reload 的窗口 ≥ 已加载窗口，`regions_before` 权威覆盖）。
8. i18n 三语新 key 避开禁词（`collapseAll*`/`collapseLevel*`/`mergeTools*`/`processed`）——建议 `agent.regions.loadEarlier`/`agent.regions.loading`。
9. 虚拟列表/迭代窗口化：新分隔条与区域段插入由既有高度机制兜住（`record` 高度变化自动 unsettle）；E2E 覆盖「加载前后滚动总高一致性」。
10. `CommittedPayload` 非空约束（`NonEmpty<WebIteration>`）：窗口内至少含最后迭代（turn 的最终回复迭代）⇒ 非空天然满足。
11. live 迭代流（SSE）完整——正在进行 turn 的迭代不经历史路径。

---

## 5. 实施阶段

### P0：区域窗口 + 工具瘦身 + 按需取回（核心闭环）

| # | 文件 / 函数 | 改动 |
|---|---|---|
| 0.1 | `protocol/events.go` | `HistoryIteration` + `ToolsFolded`；`HistoryMessage` + `RegionsBefore`（omitempty ⇒ 旧客户端忽略，向后兼容） |
| 0.2 | `channel/region_view.go`（新） | `RegionRuns(recs)` 同构区域划分 + `RegionWindow(recs, K)` 尾部窗口裁剪 + `FoldToolDetails(iter)` 轻字段映射（GenUI 豁免）；导出供契约测试 |
| 0.3 | `channel/subscription.go:257` | 结构化装配处（`:345-378`）接入：区域窗口裁剪 + `RegionsBefore` + 轻字段化。⚠️ CLI `get_history` RPC 走同一转换函数——加 `foldView bool` 参数，REST 路径 true、RPC 路径 false（CLI 零影响，实施前跑一次 CLI 手测回归） |
| 0.4 | `storage/sqlite/session.go` | `GetIterationHistoryBeforeRange(tenantID, turnID, beforeIter)` + 详情单查 `GetIterationHistoryByNumber(tenantID, turnID, iteration)` |
| 0.5 | `storage/sqlite/sessiondb.go` + 迁移 | `CREATE INDEX IF NOT EXISTS idx_iter_history_turn_iter ON iteration_history(tenant_id, turn_id, iteration)` |
| 0.6 | `serverapp/callbacks.go` | `HistoryRegions(senderID, sel, turnID, beforeIter, regionLimit)` + `IterationDetail(senderID, sel, turnID, iteration)`（属主校验 + 会话库收口） |
| 0.7 | `channel/web/web_api.go` + `web.go` | `handleRegions` / `handleIterationDetail` + 路由 `POST /api/regions`、`POST /api/iteration_detail` |
| 0.8 | `web/src/types/shared.ts` + `normalize.ts` | `WebIteration.toolsFolded` + `HistoryResponse.regions_before` 解析收口 |
| 0.9 | `web/src/chat/types.ts` + `reduce.ts` | `regions_loaded` 事件 + case（union+幂等）；`mergeIterations` 四象限（轻不覆盖完整） |
| 0.10 | `chat/integrate.ts` + `derive.ts` + `AssistantMessage.tsx` | `regionsBefore` 透传到行/组件 |
| 0.11 | `web/src/components/agent/`（新 `RegionsDivider.tsx`） | 「⬆ 更早的 N 个区域」分隔条 + IO 哨兵（arm/disarm 复用）+ 段加载编排（`useChatMessages` 或新 hook，`ChatStore.dispatch` 单通道） |
| 0.12 | `FoldedToolGroup.tsx` / `ToolPopoverDetail` | 浮层打开时 `toolsFolded` → `fetchIterationDetail` → 同号覆盖渲染（交互形态不变） |
| 0.13 | `web/src/components/agent/api.ts` | `fetchRegions` / `fetchIterationDetail` |
| 0.14 | i18n zh/en/ja | 分隔条/加载/重试文案 |
| 0.15 | 守护测试 | §6 |

### P1：`active_progress` 区域窗口（busy 恢复）

| # | 文件 | 改动 |
|---|---|---|
| 1.1 | `agent/agent_backend_methods.go` | `GetActiveProgress` 快照/增量投影接入 `RegionWindow` + 轻字段化（投影层；live 迭代完整） |
| 1.2 | 契约测试 | `active_progress_snapshot_complete_test.go` 语义演进 |

### P2：参数调优与收尾

| # | 改动 |
|---|---|
| 2.1 | 外层初始页 `limit:100 → 50`（候选，以 T12 实测定）；（可选）loadMore/分隔条哨兵 `rootMargin` 预取提前量 |
| 2.2 | `AssistantMessage.tsx:97` 的 `iterationsTruncated` 死钩子移除（语义由 regions_before 取代） |
| 2.3 | gotchas/web-consistency-design 文档同步（§6-D） |

---

## 6. 守护测试（判别力优先：还原旧实现必红）

### 演进既有

| 测试 | 演进为 |
|---|---|
| T1 `channel/history_iterations_complete_test.go` | 窗口内迭代号**连续**（任意起点）+ `regions_before` 与实际一致 + `tools_folded` 轻字段形状正确（GenUI 豁免）+ **`/api/regions` + `/api/iteration_detail` 可完整取回**（拼回 1..N 全量） |
| T2 `web/src/chat/iterationBound.test.ts` | 保留「迭代号不截断」+ 新增「窗口任意起点合法」「轻不覆盖完整」「regions_before 声明段不触发 gap reload」 |
| T3 `agent/active_progress_snapshot_complete_test.go`（P1） | 同 T1 模式 |

### 新增

| # | 测试 | 断言要点 |
|---|---|---|
| T4 同构契约（Go `region_view_test.go` + TS `mergeToolRuns` 单测） | **同一 fixture**（head 带文本/head 纯工具/混合 run/GenUI 豁免/单迭代 run/全纯工具 turn）两侧区域边界**完全一致** |
| T5 区域窗口（Go） | 尾部 100 区域边界、区域原子（窗口最小迭代 = 区域起始）、区域数 ≤ K 全量、`regions_before` 计数 |
| T6 SQL（Go） | `GetIterationHistoryBeforeRange`：倒序边界、turn/tenant 隔离、与窗口拼接连续 |
| T7 `mergeIterations` 四象限（TS） | 完整胜轻 / **轻不覆盖完整** / 轻 vs 轻引用稳定 |
| T8 `reduce regions_loaded`（TS） | union + 幂等重放返回原 state + 三态路径 + 不触碰 lastSeq/gapReloadToken |
| T9 REST（Go） | `/api/regions`、`/api/iteration_detail`：属主校验（非属主 404）、越界、上限、轻字段化豁免 |
| T10 gap 守卫（TS） | 窗口 + regions_before 声明段 ⇒ `unreachableGapSig` 恒空；段到达后连续 |
| T11 E2E `web/e2e/region-window.spec.ts` | 真实浏览器：**无感断言**（默认视图 DOM 与全量视图等价：pill 数、+N 徽标数字、文本、思考条）→ 上滚至分隔条自动加载（请求数=1/手势）→ 段插入后迭代号连续、总高稳定 → 浮层打开 → 详情加载 → 完整渲染 → 幂等（重放零请求）→ 失败重试降级 |
| T12 性能守护（Go） | fixture 1,661 迭代 turn：窗口化+轻字段响应体积 < 全量的 10%（阈值一次校准后钉死） |

### 文档同步（落地 PR 内）

- `docs/agent/gotchas-web-frontend.md`：「迭代历史禁止截断」条目 → §0 演进版（保留历史事故原文 + 演进说明）；`subscription.go:1377` 注释同步。
- `docs/agent/web-consistency-design.md`：Snapshot 语义补区域窗口；新端点入清单。
- AGENTS.md warnings 索引「三处压体积各截一刀」条目追加演进指针。
- docs-site：内部历史投影优化，不涉及公共 API/配置/工具面，不更新。

---

## 7. 风险与缓解

| # | 风险 | 缓解 |
|---|---|---|
| R1 | **两端区域判定漂移** ⇒ `regions_before`/窗口边界错 ⇒ 前端拼接断号 | T4 同构 fixture 契约；规范唯一来源 §2.1；改任一侧必跑双测 |
| R2 | **reload 抹掉已展开数据** | T7 四象限「轻不覆盖完整」+ T2；reload 的窗口 ≥ 已加载窗口（`regions_before` 权威覆盖声明） |
| R3 | 巨型区域段单次过大 | 段请求带 `region_limit`（上限 100）+ 服务端硬上限 |
| R4 | `history_replaced` 幂等击穿 ⇒ 卡顿回归 | 新字段进 `reuseIfSame`；T8 幂等断言；`turn_perf_pipeline.test.tsx` 照跑 |
| R5 | **CLI/RPC 兼容**：RPC `get_history`（CLI TUI 全量渲染）走同一转换函数 | 0.3 的 `foldView` 参数隔离（RPC 路径 false = 全量，CLI 零影响）；CLI 跟进另立评估 |
| R6 | E2E mock 漂移（历史教训） | T11 用真实 handler（`page.route` 转发真服务或 go test 起服务）；字段以真机载荷探针为准 |
| R7 | 浮层详情加载态在弱网下「有感」 | 详情 RPC 极小（~KB）；skeleton 先行；「基本无感」边界已与用户对齐 |
| R8 | turn 内迭代窗口起点变化（`resolveResumeTurnID` 续跑续接迭代号） | iteration 号 turn 内天然连续（既有不变量）；窗口边界由服务端每次响应自洽声明 |

## 8. 验证方案

1. §6 全清单先红后绿（每条判别力自证）。
2. 体积量测：1,661 迭代 fixture 对比（全量 vs 窗口+轻字段）→ T12 阈值校准 + PR 记录。
3. 手测脚本：长会话（`chat_07B68B101679` 类）切会话 switchMs 对比；上滚区域段加载瀑布（DevTools 确认 1 手势 = 1 请求）；浮层打开延迟体感。
4. 回归面：`go test ./...` + lint + 前端全量 vitest + 既有 E2E（`turn-iter-perf`/`loadmore-pagination`/`msg-actions`）不红；pre-commit hook 全绿。

## 9. 回滚策略

- 全字段 omitempty + `foldView` 参数 ⇒ **零成本开关**：`foldView=false` 即回滚全量视图；新端点保留无害。
- 只影响渲染历史投影；**LLM 上下文零触碰**（replay 走 `session_messages` 独立路径）。
- DB 零迁移风险（只加索引）。

## 10. 待用户确认的开放问题

1. **区域窗口大小 K**：默认 100（用户原话）；是否需要可配置（config）还是硬编码常量？
2. **`REGION_LIMIT` 段大小**：默认 100；与 K 相同即可？
3. **`active_progress` 骨架化阶段**：排 P1（切 busy 大会话收益明显）；是否首版闭环？
4. **CLI 跟进**：P0 先隔离（`foldView=false`），CLI TUI 的区域分页后续单独评估——确认？
5. **浮层详情的缓存策略**：同一迭代多工具的浮层多次打开 → 端点按迭代返回全部工具详情（一次拉全，迭代内复用）——确认此粒度？
6. 分隔条触发：仅滚动哨兵自动触发（无手动点击按钮），与 loadMore 同族——确认？

## 修订记录

- **v1（同日，已废弃）**：把「折叠」建模为「迭代骨架 + `folded_tool_count` + 新增『+N 工具』点击展开徽标」+ 消息行分页。用户指出三点错误：折叠对象应为**工具组**（默认 pills 必须全渲染、交互零变化）；分页计量应为**展示区域**；历史必须完全连续线性一致。v1 的骨架方案会改变默认视图（pills 缺失）并新增交互，违背无感要求，全部废弃。
- **v2（本版）**：折叠 = 工具组详情载荷省略（`tools_folded` 轻字段，浮层按需）；分页 = 展示区域（尾部 100 区域 + `regions_before` + `/api/regions` 段取回）；连续性以「任意起点连续 + 显式可取回声明」保证，渲染层已核实零改动。

## 自审记录

- ✅ 用户三轮输入逐条落点：区域分页（D2）、工具组折叠（D3）、无感（G2+§4-1+T11）、连续线性一致（D4+T10）、head 坑（§2.1+T4 fixture）、计数可计算（§3.3 末段）。
- ✅ 与 2026-09-21 铁律冲突已显式演进（§0），且比 v1 更强：窗口连续性有已核实的前端注释背书。
- ✅ 关键前提全部代码核实（§2.3 六项），不是假设。
- ✅ 无感验收有判别力测试（T11 DOM 等价断言）——「像素级一致」不是口号而是 CI 门禁。
- ✅ 唯一动语义处（mergeIterations 四象限）配 T7；其余全部为纯增量（新字段/新端点/新事件）。
- ✅ 风险含真实链路陷阱（R5 CLI、R6 mock 漂移、R7 弱网边界），无防御式兜底——设计从数据模型根上定义（显式声明 + 取回通路）。
