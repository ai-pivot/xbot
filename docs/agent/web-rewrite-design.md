# Web 前端消息层重写设计：类型驱动状态机（TDSM）

> 状态：设计稿（待评审）
> 范围：替换 `progressStore.ts`(1055 行) + `messageStore.ts`(622 行) + `useProgressStream.ts`(1742 行) 的消息一致性核心；`MessageList`/渲染组件改读新 derive 层，视觉不变。
> 核心方法：**把全部运行时不变量提升为 TypeScript 类型约束 —— 非法状态不可表示（illegal states unrepresentable）**。

---

## 1. 现状诊断：为什么 bug 无法根除

### 1.1 度量

| 指标 | 数值 | 含义 |
|---|---|---|
| 核心文件行数 | 3617 | progressStore + messageStore + useProgressStream |
| ref guard 引用 | **70** | finalizedRef / phaseDoneRef / turnCommittedRef / finalizedTurnIDRef |
| mutation 入口 | **46** | store.set* / ms.update* / freeze / reset 分散调用 |

### 1.2 历史 P0 的分类学（近 8 个）

| # | Bug | 非法状态被"表示"出来了 |
|---|---|---|
| 1 | cancel guard 污染新 turn → SSE 卡死 | guard（全局）与 turn 状态（每 chat）生命周期脱钩 |
| 2 | 切会话后 turn_id 残留拦截 | finalizedTurnIDRef 是全局标量，turn_id 是 per-chat 序 |
| 3 | 迟到 text 被拦截 → content 消失 | "已 commit"与"commit 内容完整"是两个状态，被一个 bool 折叠 |
| 4 | PhaseDone 跳过 normalize → null.tools → 整树卸载 | raw JSON 直接进入渲染层，类型断言 `as` 骗过 tsc |
| 5 | 最后 iter 闪烁（消失再出现） | 最后迭代的归属（live 字段 vs iterationHistory）无单一事实源 |
| 6 | ghost `turn-N-live` 行 + iter 全消失 | MessageStore live 槽与 committed 槽**并存**，derive 合并靠启发式 |
| 7 | cancel 后"思考中"渲染在 user 上方 | liveId 匹配"第一个 isPartial"，多个 isPartial 时语义歧义 |
| 8 | 发新消息后旧 turn 重复渲染 | frozen/committed/live 三态用两个 boolean 标志组合表达（2 bits 表示 3 态 = 有非法组合） |

### 1.3 结构性根因

```
SSE 事件 → useProgressStream 的 switch 分支（1742 行命令式协调）
             ├─ ProgressStore（live 流字段 + rAF throttle + 9 个流字段互相同步）
             ├─ MessageStore（turn 槽位 + live/committed 双轨 + toRows 合并启发式）
             └─ 4 个 ref guard（手工重建"状态机已经知道的信息"）
```

**70 处 guard 的存在本身就是设计缺陷**：每个 guard 都在尝试从外部推断状态机的当前状态，而状态机本身应该显式持有这些信息。guard 与真实状态脱节的每一个瞬间，就是一个 P0。

---

## 2. 设计原则

- **P0 非法状态不可表示**：用判别联合 + Brand + NonEmpty 类型，让历史 bug 类别在编译期无法构造。
- **P1 单一事实源**：全部状态在一个纯函数 reducer 管辖的 immutable `ChatState` 里。**零 ref guard**。
- **P2 事件先规范化**：raw JSON（Go nil slice → `null`、snake_case）在入口处一次性转换为闭合的 `DomainEvent` 联合；**渲染层永远不再见到 `null` 或 `as` 断言**。
- **P3 渲染零逻辑**：`deriveRows(state)` 是纯函数 + 穷尽 switch（`never` 检查）。渲染组件只做数据→DOM 映射。
- **P4 编译期 > 运行时**：每条不变量标注"由什么保证"（类型 / Map 语义 / reducer 转移前提），运行时检查只允许出现在唯一构造函数内。

---

## 3. 类型系统设计（类型体操层）

### 3.1 Brand：ID 防混淆 + 非空字符串

```typescript
declare const __brand: unique symbol
type Brand<T, B extends string> = T & { readonly [__brand]: B }

export type TurnID    = Brand<number, 'TurnID'>     // per-chat 单调
export type IterNum   = Brand<number, 'IterNum'>    // per-turn 1-based
export type EventSeq  = Brand<number, 'EventSeq'>   // per-run 单调

/** 非空字符串：只能通过 nonEmpty() 构造（唯一运行时检查点）。 */
export type NonEmptyS = Brand<string, 'NonEmpty'>
export function nonEmpty(s: string): NonEmptyS | null {
  return s.length > 0 ? (s as NonEmptyS) : null    // 唯一合法 as（构造函数内）
}

/** 非空数组：committed 渲染数据的下界保证。 */
export type NonEmpty<T> = readonly [T, ...T[]]
export function nonEmpty<T>(xs: readonly T[]): NonEmpty<T> | null {
  return xs.length > 0 ? (xs as NonEmpty<T>) : null
}
```

**保证**：`NonEmptyS` / `NonEmpty<T>` 一旦构造（immutable），非空性**永真**——渲染层拿到类型即拿到保证，无需任何 `if (empty)` 检查（检查缺失导致的"消失/闪烁"类 bug 在类型层灭绝）。

### 3.2 Turn 状态机：三态判别联合（核心）

```typescript
/** 一个 turn 的生命周期。live/frozen/committed 互斥 —— 由判别联合保证，
 *  取代旧设计"live 槽 + committed 槽并存 + frozen:boolean"（2 bits 3 态的非法组合）。*/
export type TurnPhase =
  | { readonly kind: 'live'
      readonly iter: IterNum                       // 当前迭代号
      readonly streaming: boolean
      readonly reasoning: string                   // 流式累积（全量替换，非 append）
      readonly content: string
      readonly iterations: readonly WebIteration[] // 已完成迭代（append-only，见 I5）
      readonly activeTools: readonly WebToolProgress[]
      readonly genui: NonEmptyS | null
      readonly subAgents: readonly WebSubAgent[]
      readonly todos: readonly TodoItem[] }
  | { readonly kind: 'frozen'                      // cancel：数据全保留，只是不再接受事件
      readonly data: LiveSnapshot }                // = live 分支的全部字段快照
  | { readonly kind: 'committed'
      readonly payload: CommittedPayload }

/** committed 的可渲染性：text 或 iterations 至少其一非空 —— 类型层表达。 */
export type CommittedPayload =
  | { readonly via: 'text'; readonly content: NonEmptyS
      readonly iterations: readonly WebIteration[] }
  | { readonly via: 'fold'; readonly iterations: NonEmpty<WebIteration>
      readonly content: string }
// 不存在 { content: "", iterations: [] } 的组合 —— 构造函数签名不接受。
```

**保证**：
- `committed` 的 payload 必然可渲染（`via:'text'` 的 content 非空 / `via:'fold'` 的 iterations 非空）→ **"iter 全消失只剩空壳"不可构造**（Bug 6/8）。
- `frozen` 是独立状态而非 live 的标志 → cancel 后的渲染语义（保留数据 + `isPartial` 传递 activeTools）不再依赖 `find(isPartial)` 启发式（Bug 7）。

### 3.3 ChatState：槽位唯一性

```typescript
export interface ChatState {
  readonly chatID: string
  /** 每 turn 恰好一个槽位。Map key 语义 = 唯一 → live 与 committed 并存（ghost 行）不可表示。*/
  readonly turns: ReadonlyMap<TurnID, Turn>
  /** legacy 无 turn_id 的历史消息（前缀段，只读）。 */
  readonly legacy: readonly LegacyRow[]
  readonly activeTurn: TurnID | null     // 唯一的"当前 turn"指针 —— 取代全部 ref guard
  readonly lastSeq: EventSeq | null
  readonly busy: boolean                 // session busy/idle
}

export interface Turn {
  readonly id: TurnID
  readonly user: UserRow | null          // optimistic user 先于 turn_started 存在
  readonly phase: TurnPhase
  readonly requestID: string | null      // turn_started 精确绑定乐观 user（V2 语义）
}
```

**保证**：
- `activeTurn` 是**唯一**的当前指针：任何事件 `e` 对非 active turn 的适用性由 reducer 显式判断（迟到/旧 turn 事件 → `return state`，引用不变 → React 零渲染）——**4 个 ref guard、70 处引用全部删除**（Bug 1/2/3 的根因消失）。
- `turns: ReadonlyMap<TurnID, Turn>` 的 key 唯一性由 Map 语义保证 → **ghost 行（live 行 + committed 行同时渲染）在数据结构上不可表示**（Bug 6）。

### 3.4 DomainEvent：闭合的事件联合 + 唯一 normalize 层

```typescript
export type DomainEvent =
  | { type: 'turn_started'; turnID: TurnID; requestID: string | null; trigger: 'user' | 'resume' | 'notify' }
  | { type: 'iteration'; turnID: TurnID; iter: IterNum; seq: EventSeq
      content?: string; reasoning?: string
      activeTools: readonly WebToolProgress[]; completedTools: readonly WebToolProgress[]
      iterationsDelta: readonly WebIteration[]      // 已 normalize（null→[]）
      todos?: readonly TodoItem[]; subAgents?: readonly WebSubAgent[] }
  | { type: 'stream'; turnID: TurnID; seq: EventSeq
      content?: string; reasoning?: string          // 全量累积文本
      streamingTools?: readonly WebToolProgress[]; genui?: NonEmptyS }
  | { type: 'phase_done'; turnID: TurnID; seq: EventSeq
      finalIteration: WebIteration | null          // 后端 recordFinalIteration 补记（已 normalize）
      todos?: readonly TodoItem[] }
  | { type: 'text_final'; turnID: TurnID; content: NonEmptyS | null
      progressHistory: readonly WebIteration[]; cancelled: boolean }
  | { type: 'session'; busy: boolean }
  | { type: 'history_replaced'; legacy: readonly LegacyRow[]
      turns: readonly Turn[]                        // rewind / reload / hydration：全量替换
      active: { turnID: TurnID; snapshot: LiveSnapshot } | null }

/** 唯一入口：raw SSE/WS JSON → DomainEvent | null。
 *  职责：Go nil slice → []；snake_case → camel；数字字段校验；非法事件 → null（丢弃）。
 *  全项目的 null 处理集中在这一个文件 —— Bug 4（null.tools 整树卸载）在入口灭绝。*/
export function normalizeEvent(raw: unknown, chatID: string): DomainEvent | null
```

**保证**：normalize 之后的世界里**不存在 null 数组字段**（类型未声明 null 即不存在）；渲染层禁止 `as`（ESLint rule），断言只允许在构造函数内。

---

## 4. 架构：事件 → 规范化 → Reducer → Derive → 渲染

```
SSE/WS raw
  → normalizeEvent()            // 唯一 null/格式处理点；非法 → null 丢弃
  → ChatStore.dispatch(ev)      // 同步 reduce；immutable 替换；rAF 合并通知
      reduce(state, ev) → state'   // 纯函数，全部业务规则集中于此（~300 行）
  → useSyncExternalStore(subscribe, getSnapshot)   // immutable 引用相等 → React 自动跳过
  → deriveRows(state) → readonly Row[]              // 纯函数 + 穷尽 switch
  → <MessageList rows/>          // 纯渲染（虚拟列表、折叠、GenUI 挂载点不变）
```

### 4.1 ChatStore（唯一可变点：一个 state 引用）

```typescript
class ChatStore {
  private state: ChatState
  private listeners = new Set<() => void>()
  private raf = 0

  dispatch(ev: DomainEvent): void {
    const next = reduce(this.state, ev)
    if (next === this.state) return            // 无变化 → 零渲染（迟到事件路径）
    this.state = next
    if (!this.raf) this.raf = requestAnimationFrame(() => {
      this.raf = 0; this.listeners.forEach(l => l())
    })
  }
  getSnapshot = (): ChatState => this.state     // 引用稳定（immutable）
  subscribe = (l: () => void) => { this.listeners.add(l); return () => this.listeners.delete(l) }
}
```

- **turn 边界原子性**：commit 是 `reduce` 内的一次对象替换（live→committed 同一 `Turn` 的新实例），下一次 rAF 快照里 live 与 committed **天然同帧** —— 取代 `flushSync` 补丁（T2 无闪烁的机制基础）。
- **流式节流**：rAF 合并通知（≤60Hz），高频 stream 事件合并为一次渲染。

### 4.2 reduce 转移表（全部业务规则，穷尽 —— 与实现同步）

```typescript
export function reduce(s: ChatState, ev: DomainEvent): ChatState {
  switch (ev.type) {
    case 'turn_started':     // ① 收尸旧 active live（fold commit，数据保全）
                             // ② 已存在同 ID turn（重放/lazy 后补）：
                             //    live 保留 / 空壳升级 live / committed 嫁接 user 不动 phase（指针回滚）
                             // ③ 新 turn 进 live；requestID→turnHint 双键绑定 user
    case 'iteration':        // 非 active：已有其它 active → 丢弃；无槽且无 active → lazy 采纳；
                             //  空壳 → 升级 live；committed/有输出 frozen → 丢弃
                             // seq ≤ lastSeq → 丢弃（I5）；iter 前进 append delta（I4）
                             // activeTools 到达 → 清 streamingTools 同名（I7）；迭代前进清空流式工具
    case 'stream':           // turnID 缺失回退 activeTurn；无槽/空壳 → lazy 采纳/升级（同上）
                             // ① seq 豁免：累积全量推送，不按 seq 排序（打字机语义）
                             // 全量替换 content/reasoning；incoming streamingTools 对 activeTools 过滤（I7）
    case 'phase_done':       // finalIteration fold 进 iterations（I4，T3 根治点）；streaming=false
    case 'text_final':       // live/frozen → committed（via text | via fold，I2 构造）
                             // 无产出分支 → frozen + 清 activeTurn（I3 修复：性质测试抓出）
                             // progressHistory 权威覆盖同号迭代（append-only union）
    case 'session':          // idle：live 有产出 → frozen 定格（text 迟到仍可 commit）
                             //      无产出 → 删槽（幽灵行灭绝）
    case 'history_replaced': // ⚠️ merge 语义（非全量替换）：
                             //  ① incoming（DB 权威）：同 ID 状态机 live 胜（SSE 比快照新）+ 嫁接 DB user；
                             //     空壳不覆盖状态机 committed/frozen-with-output（数据保全）
                             //  ② 状态机独有 turn：live/committed/有输出 frozen 保留（post-fetch SSE 数据）
                             //  ③ active：保留的 live 优先；否则 ev.active 升级空壳/建槽（快照恢复）
                             //  ④ lastSeq 随 active 保留（per-run 连续）；pendingUsers 剔除已绑定
    case 'user_sent':        // 乐观行入 pendingUsers
    case 'user_echo':        // 权威回声：turn 已存在且无 user → 挂载；否则去重后入 pending（turnHint）
    case 'user_ack':         // requestID 匹配回填 dbID（pending 或已绑定 turn.user）
  }
}
```

**迟到事件语义（修订）**：事件对**非 active turn** 且不满足 lazy 采纳前提（无 active 或目标是空壳）→ `return s`（引用不变，React 零渲染）。旧设计的 4 个 guard、finalizedTurnID 全局残留、chatID 切换漏重置——被"显式指针比较 + 受控 lazy 采纳"取代。lazy 采纳是**显式建模的宽容**（切回会话时 turn_started 不会重来），不是启发式：前提（activeTurn=null / 空壳）由状态机自身判定，性质测试穷举验证其不破坏 I3/I4。

### 4.3 deriveRows（渲染模型，穷尽 + 无启发式）

```typescript
export type Row = UserRow | { kind:'live'; ... } | { kind:'frozen'; ... } | { kind:'committed'; ... }

export function deriveRows(s: ChatState): readonly Row[] {
  return [
    ...s.legacy,
    ...[...s.turns.values()]                     // Map 插入序 = turnID 升序
      .sort((a, b) => a.id - b.id)
      .flatMap(t => t.user ? [userRow(t), assistantRow(t)] : [assistantRow(t)]),
  ]
}

function assistantRow(t: Turn): Row {
  switch (t.phase.kind) {
    case 'live':      return { kind:'live', id:`turn-${t.id}-live`, isPartial:true, ...liveFields(t.phase) }
    case 'frozen':    return { kind:'frozen', id:`turn-${t.id}`, isPartial:true,    ...t.phase.data }
    case 'committed': return { kind:'committed', id:t.committedID, ...committedFields(t.phase.payload) }
  }
}
```

- **每 turn 恰好一行 assistant**：类型穷尽 switch，三态各出一行，**不可能同时出两行**（ghost 行灭绝，Bug 6）。
- **`isPartial` 只有 live/frozen 为 true**："liveProgress 传给谁"不再靠 `find` 扫描——`MessageList` 按 `kind` 匹配行（Bug 7 灭绝）。
- **committed 渲染零分支**：payload 的 `via` 判别联合直接映射渲染路径，不存在"content 为空时怎么办"（类型保证非空）。

---

## 5. 形式化模型与证明

### 5.1 模型

迁移系统 **M = (S, E, δ)**：
- S = 合法 `ChatState` 值的集合（类型 `ChatState` 的 inhabitants，排除 any 逃逸）
- E = `DomainEvent` 值的集合
- δ = `reduce : S × E → S`（纯、全）

渲染函数 **ρ = deriveRows : S → Row\***（纯、全）。

事件序列 σ = e₁e₂…eₙ，状态轨迹 s₀ → s₁ → … → sₙ（sᵢ₊₁ = δ(sᵢ, eᵢ₊₁)）。

### 5.2 不变量（每条标注保证机制 —— 与实现同步修订）

- **I1（槽位唯一）**：∀s∈S, ∀t∈s.turns：t 唯一，t.phase ∈ {live, frozen, committed} **恰居其一**。
  *保证*：`ReadonlyMap` key 语义 + 判别联合（TS 编译期）。
- **I2（committed 可渲染）**：phase.kind='committed' → payload.via='text' ∧ |content|≥1 ∨ payload.via='fold' ∧ |iterations|≥1。
  *保证*：`CommittedPayload` 判别联合 + `NonEmptyS`/`NonEmpty<T>` 构造函数（编译期 + 唯一运行时检查点）。
- **I3（活动唯一）**：∀s：|{t ∈ s.turns : t.phase.kind='live'}| ≤ 1，且 activeTurn ≠ null 时指向的 turn 必为 live；activeTurn = null 时无 live。
  *保证*：全部产生/消除 live 的转移点共同维护——turn_started（收尸旧 active；对已存在 turn：live 指向/空壳升级/其它指针回滚）、lazy 采纳（仅 activeTurn=null 时）、history_replaced（保留 live 优先，ev.active 升级空壳或建槽）、text_final/session（commit/freeze/删槽时清指针，含无产出分支）。归纳见 §5.4。
- **I4（迭代 append-only）**：任一 turn 的已完成迭代序列在任意转移下只允许 append（按 iter# dedup，同号权威覆盖）与整体拷贝（live → committed/frozen/merge），**永不删除**。
  *保证*：写 iterations 的全部 4 处（iteration append / phase_done fold / commit·freeze 拷贝 / history_replaced merge union）均为只增；性质测试 P5 以观察集单调不减逐步验证。
- **I5（seq 单调，修订）**：s.lastSeq 严格递增；iteration/phase_done 携带 seq ≤ lastSeq → δ 返回原状态。**stream 豁免**：stream 是累积全量推送（delta_push 默认关闭），不按 seq 排序——旧实现明确的语义（"stream deltas are cumulative, not ordered by seq"），按到达序 gate 会误杀打字机帧。
  *保证*：reducer 显式比较（iteration/phase_done 两个 case）；豁免仅限 stream case。
- **I6（无 null）**：∀s∈S：任何数组字段非 null；∀e∈E：同。
  *保证*：`normalizeEvent` 是唯一入口，返回类型不含 null 数组；渲染层 ESLint 禁 `as`/禁访问未声明字段。
- **I7（工具不相交，新增）**：∀s，对 active live turn：`streamingTools ∩ activeTools = ∅`（按 name 判等）。
  *保证*：reduce 双向维护——iteration case：新 activeTools 到达清除 prev.streamingTools 同名条目（迭代前进时全清）；stream case：incoming streamingTools 对当前 activeTools 名字过滤。双向时序（generating 先到被 running 清除；running 后迟到 generating 被过滤）均覆盖，测试 H 验证。
- **I8（空壳占位语义，新增）**：定义 hollow(t) ≡ t.phase.kind='frozen' ∧ ¬hasOutput(t.phase.data)。hollow turn 是"DB 有 user 行"的占位符而非终态数据；任何权威活动信号（turn_started / ev.active / stream / iteration lazy）将 hollow 升级为 live。**升级无损**：hollow 的 data 无任何输出（content/reasoning/iterations/tools/genui 皆空），升级为 live 不丢失数据。
  *保证*：`isHollowFrozen` 判定 + 各转移点的升级分支；非 hollow（committed / 有输出 frozen）绝不被降级覆盖（Bug 12/13 根治）。

### 5.3 定理

**T1（渲染函数 total —— DOM 永不消失）**
ρ 对所有 s∈S 定义且不抛异常。
*证明*：ρ 的每一步只做（a）Map 迭代，（b）穷尽 switch（`kind` 判别联合 + `never` 检查保证编译期穷尽），（c）读取 I2/I6 保证存在的字段。TS 类型 soundness（无 any/断言逃逸，由 lint 强制）⇒ 字段访问的类型即运行时形态 ⇒ 无 TypeError 可达。∎

（这正是 Bug 4 的根治：`null.tools.map()` 需要"null 通过类型检查"才可能发生——新设计里该状态的类型不可构造。）

**T2（turn 边界原子性 —— 无闪烁）**
live→committed 的迁移在**单次 React 渲染提交**内对用户可见，不存在中间帧。
*证明*：迁移发生在 δ 内部（同步、单次 state 替换）；ρ 读同一快照 sᵢ₊₁ 时该 turn 恰处 committed（I1），live 行与 committed 行是**同一 turn 槽位的两种 phase**（非两个并存对象）⇒ 任意快照中该 turn 恰出一行 ⇒ 帧间不存在"旧行消失、新行未现"的间隙。rAF 合并保证一个快照一帧。∎

**T3（已渲染迭代永不消失）**
若迭代 k 在帧 f₁ 已由 ρ 输出（live 态），则 ∀f>f₁：ρ 输出仍含迭代 k（同 turn）。
*证明*：归纳于事件序列，分类所有可能接触该 turn 的转移：(a) iteration append（I4 只增）；(b) phase_done fold（只增）；(c) text_final / session 诱导的 commit/freeze **整体拷贝** iterations；(d) history_replaced merge——incoming 同 ID 时若状态机侧是 live 则 live 胜（含其 iterations），否则 DB 版本含该 turn 的 DB 迭代（DB 是 append-only 权威，见 web-linearizability.md），状态机独有的 committed/有输出 frozen **保留**（merge 第 2 步）；(e) turn_started 重放——对已存在 turn：live 保留原 phase（含 iterations）、committed/有输出 frozen 不动 phase（性质测试 seed=1/42 曾抓出违反，修复后覆盖）；(f) lazy 采纳/空壳升级仅作用于**无输出**槽（I8），不可能携带已输出迭代。所有分支迭代集单调不减 ⇒ 结论。∎

（Bug 5"最后 iter 闪烁"的根治：phase_done 在 δ 内 fold finalIteration，text_final 到达前迭代已在 committed 路径的数据里——不再依赖"text 重建"这个补丁路径。）

**T4（无 ghost 行）**
∀s, ∀帧：每个 turn 至多渲染一行 assistant。
*证明*：ρ 对每 turn 恰调用一次 assistantRow（flatMap 一对一）；由 I1，该 turn 的 phase 恰居其一 ⇒ 输出一行。ghost 需要 live 行与 committed 行并存 ⇒ 需要 turn 同时处于两 phase ⇒ 与 I1 矛盾 ⇒ 不可构造。∎

**T5（顺序线性一致）**
ρ 输出的行序 = (legacy 前缀) ⊕ turnID 升序 ⊕ (turn 内 user < assistant)。
*证明*：ρ 显式排序；turnID 由后端 chatProcessLoop 单调分配（DB 恢复，见 web-linearizability.md 已证引理）；user 经 requestID→turnHint 双键绑定进 turn（V2 语义，含 echo 先于 turn_started 的时序颠倒）⇒ 顺序是纯函数 ⇒ 同快照同序。∎

**T6（活动恢复活性 —— 工具长停机下的 live 可见性，新增）**
设后端 turn τ 正在执行（含工具长时间无输出）。客户端经任意一条恢复路径后存在 live 槽且后续 τ 事件被接纳：(a) 收到 τ 的 stream/iteration 事件且 activeTurn=null（或 τ 为空壳）→ lazy 采纳/升级（I8：无损）；(b) fetchHistory 返回 active_progress 声明 τ → history_replaced ev.active 分支建槽/升级空壳并置 activeTurn；(c) 收到 turn_started(τ) → 标准 live 创建。
*证明*：(a) stream case：turnID 非空 → target=τ；τ 无槽且 activeTurn=null → lazyAdoptLive 置 activeTurn=τ（I3 保持：此前无 live）；τ 为空壳 → 升级（I8）。iteration case 同型（含 seq 门控在其后，不影响采纳）。(b) merge 第 3 步：existing 为空槽/空壳 → 写入 live(snapshot)，activeTurn=τ；existing 已是 live → 指向。(c) 标准。三路径后 turns(τ).phase=live ∧ activeTurn=τ，后续事件通过 `target===s.activeTurn` 主路径写入。∎

（Bug 11/12 的根治："切回会话/刷新后只显示 history"—— 恢复不依赖新 SSE 事件，active_progress 快照与事件 lazy 采纳双通道。）

**T7（工具单渲染，新增）**
∀帧：live 行的工具区域中，同一 name 的工具至多渲染一个条目。
*证明*：渲染读 (activeTools, streamingTools) 两个数组；I7 保证二者按 name 不相交 ⇒ 并集无重名 ⇒ 单渲染。I7 的保持：iteration case 写 activeTools=ev.activeTools 后，streamingTools' = 旧值过滤掉与 ev.activeTools 同名者（迭代前进时为 ∅）；stream case 写 streamingTools' = incoming 过滤掉与当前 activeTools 同名者。两 case 后 I7 成立，其余 case 不写这两个字段（frozen 的 markError 保 name）。归纳 ⇒ 全轨迹成立。∎

（Bug 13"同一工具双渲染（executing+generating）"的根治；旧前端在 mergeProgressState 的同名过滤语义被提升为状态机不变量。）

### 5.4 归纳骨架（I1∧I3∧I4∧I7 在 δ 下保持）

对全部 case 做 case analysis（列关键的五个，其余同型或平凡）：

- **turn_started(ev.τ)**：设 s 合法。
  - *收尸*：s.activeTurn=t* 为 live → fold（有产出/user → committed via fold；否则 frozen 空壳）并清指针。I4（拷贝不减）、I3（live 数不增）保持。
  - *已存在 τ 槽*（lazy 后补/重放）：live → 保留原 phase、指针指向（I3：恰一 live）；hollow → 升级 live（I8：无输出故无损，live 数 0→1 且指针同置）；committed/有输出 frozen → 仅嫁接 user，phase 不变，**指针与 lastSeq 显式回滚**为 s 原值（I3：不产生第二个 live；I4：不动 iterations）。
  - *新槽*：EMPTY_LIVE 写入，指针指向。三路均 I1（Map set 单键）。
- **stream(ev.τ)**：τ 缺失回退 activeTurn。τ 无槽且 activeTurn=null → lazy 采纳（live 0→1，指针置 τ；I3）。τ 为空壳 → 升级（I8）。否则非 live → 丢弃（引用不变）。写入路径只替换 content/reasoning/streamingTools/genui——streamingTools' 对 activeTools 过滤（I7 保持）；iterations 不动（I4 平凡）；seq 不 gate（I5 豁免，累积语义）。
- **iteration(ev.τ)**：非 active 的处理同 stream 前置（lazy/升级/丢弃三分支，I3/I8 同上）。seq ≤ lastSeq → 丢弃（I5）。写入：iterations 只增（I4）；activeTools=ev.activeTools 且 streamingTools' 清同名/前进清空（I7）；lastSeq 推进。
- **text_final(ev.τ)**：τ 非 active 且非目标槽 → 丢弃。live/frozen → committed（via text/fold，I2 由构造签名强制；iterations = union(live, progressHistory) 同号权威覆盖——I4：并集不减）。无产出分支 → frozen + **清 activeTurn**（I3；性质测试曾抓出指针残留指向 frozen 的违反，修复后纳入）。committed → 丢弃（幂等）。
- **history_replaced(ev)**：merge 四步——① incoming：同 ID 状态机 live 胜（I3/I4：live 数据新于快照）+ 嫁接 DB user；incoming 空壳不覆盖状态机 committed/有输出 frozen（I4：不丢已有输出）。② 状态机独有：live/committed/有输出 frozen 保留（I4）。③ active：保留 live 优先，否则 ev.active 建槽/升级空壳（I8；I3：恰一 live 且指针一致）。④ lastSeq/pending 随属主保真。全步 I1（重建 Map，键唯一）。∎

### 5.5 诚实边界（不可证项）

| 项 | 状态 |
|---|---|
| normalizeEvent 自身的正确性 | 防御性解析 + 性质测试覆盖（raw 生成器），但类型证明止于"返回值满足类型" |
| 渲染组件（MessageList 内部） | ρ 之后的 DOM 层由组件测试/E2E 覆盖，不在本证明范围 |
| GenUI iframe 生命周期 | 独立子系统，仅保证 genui 字段随 T3 保全 |
| SSE 传输层（coalescing/gap/replay） | 状态机假设事件可能乱序/重放/丢失并以 I5+lazy+merge 容忍，但传输本身的送达性不在证明内（弱网最终一致由 reload→DB 权威兜底，见 web-consistency-design.md） |
| SW/HTTP 缓存层 | bundle 新鲜度由 sw2.js no-store + index.html no-cache + assets immutable 保证（部署面），不在状态机证明内 |
| history 属主门控（hook 层） | resolvedChatID ≠ chatID 时跳过 dispatch 是 hook 的前置条件，非 δ 的一部分；违反它不会破坏 I1-I8（只会延迟灌入） |

---

## 6. 历史 Bug → 消灭机制映射表

| Bug | 消灭机制 | 层 |
|---|---|---|
| 1 cancel guard 污染新 turn | activeTurn 唯一指针；非 active 事件 → return s（零渲染） | reducer |
| 2 切会话 turn_id 残留 | ChatState per-chat 实例化 + 属主门控（resolvedChatID≠chatID 跳过灌入）+ merge | 架构+hook |
| 3 迟到 text 被拦截丢 content | 无"拦截"概念——text_final 对目标 turn 永远 commit（I2 构造） | reducer |
| 4 null.tools 整树卸载 | normalizeEvent 唯一 null 处理点；类型层无 null 数组 | 类型+入口 |
| 5 最后 iter 闪烁 | phase_done fold 进 δ；T3 append-only | reducer+证明 |
| 6 ghost live 行 | Map 槽位唯一 + 判别联合；T4 | 类型 |
| 7 思考中位置错 | Row.kind 判别；isPartial 语义收窄至 live/frozen | 类型 |
| 8 发新消息旧 turn 重复 | turn_started 的 fold-commit 是唯一转移；单槽位 | reducer |
| 9 打字机帧被误判为 iteration（Web channel 无独立 stream_content 消息类型） | normalizeProgress 按 phase=''+stream 载荷分流到 stream 事件 | normalize |
| 10 跨会话脏灌（旧会话 messages 灌入新 store 被 merge 保留） | 属主门控：resolvedChatID ≠ chatID → 跳过 history dispatch | hook |
| 11 切回会话看不到 live（turn_started 已过、事件无槽可写） | lazy 采纳（T6 路径 a）；I8 空壳升级 | reducer+证明 |
| 12 刷新/切回只显示 history（active_progress 恢复被空壳挡住） | merge ev.active 升级空壳/建槽（T6 路径 b）；I8 | reducer+证明 |
| 13 同一工具双渲染（executing + generating 并存） | I7 streamingTools ∩ activeTools = ∅ 双向过滤；T7 | reducer+证明 |

另：部署层根因（sw.js 无 Cache-Control → SW 启发式缓存 → 用户永远跑旧 bundle）由 serveStaticFile（no-store/no-cache/immutable）+ sw2.js 改名根治——属传输/缓存层，不在状态机证明内（§5.5）。

---

## 7. 文件结构

```
web/src/chat/
├── types.ts          // §3 全部类型 + 构造函数（nonEmpty/brand）～200 行
├── normalize.ts      // normalizeEvent（唯一 null/格式点）～250 行
├── reduce.ts         // 状态机转移表（全部业务规则）～300 行
├── derive.ts         // deriveRows + Row 联合 ～150 行
├── store.ts          // ChatStore（rAF 合并通知）～80 行
└── useChat.ts        // useSyncExternalStore 绑定 hook ～60 行
```

总计 ~1040 行替换 3617 行；`useProgressStream`/`progressStore`/`messageStore`/4 个 ref guard 全部删除。渲染组件（MessageList/AssistantMessage/LiveIteration/GenUIBlock）改读 `Row` 联合，props 收窄。

## 8. 迁移策略（4 步，每步全绿）

| 步 | 内容 | 风险控制 |
|---|---|---|
| M1 | 新建 `web/src/chat/`（types/normalize/reduce/derive/store），**不动旧代码**；reducer 转移表单元测试 + 历史 bug 全映射为红灯测试 | 零风险（纯新增） |
| M2 | 性质测试：随机事件序列（fast-check 生成器）→ I1-I6 断言 + ρ total + T4 行数 = turn 数 | 证明落地为可执行检查 |
| M3 | AgentPanel 并行接入：`useChat` 与旧 store **shadow 运行**（同事件双写，仅旧 store 驱动渲染，diff 断言 rows 等价）；E2E 全量跑 | 可对比回滚 |
| M4 | 切换渲染数据源到 deriveRows；删除旧三文件 + 70 guard；E2E + 视觉回归 | 单 PR，git revert 可退 |

## 9. 测试策略

1. **转移表测试**：8 case × (正常/迟到/乱序/空/满) 矩阵。
2. **性质测试**（证明的可执行化）：`∀ 随机 σ: I(δ*(s₀,σ)) ∧ ρ 不抛 ∧ 每 turn 一行`。
3. **历史回归**：§6 表格 8 个 bug 各一个测试（先红后绿）。
4. **E2E**：保留现有全部（cancel/切换/闪烁/GenUI escape）+ 新增 ghost 行断言。

---

## 10. 评审决策点（已确认）

1. `frozen → committed` 的触发：仅 text_final，还是新 turn_started 也可以 fold（现设计：两者都可以，后者 `via:'fold'`）—— 影响 commit 时机语义。**已确认：两者皆可。**
2. `legacy` 前缀行是否在 M1 就并入 Turn 模型（现设计：保留独立前缀，M5 后续再统一）。**已确认：保留独立只读前缀。**
3. shadow 运行期长度：一个迭代周期 vs 一周（M3 风险预算）。**已确认：直接切 M4（旧系统 bug 太多，shadow 对比无意义）。**

---

## 11. 完整形式化证明（与实现同步，含 M4 后全部修订）

### 11.1 模型（修订）

迁移系统 **M = (S, E, δ, ρ, ι)**：
- S = 合法 `ChatState` 值的集合
- E = `DomainEvent` 值的集合（11 个判别分支：turn_started / iteration / stream / phase_done / text_final / session / history_replaced / user_sent / user_echo / user_ack / user_fail）
- δ = `reduce : S × E → S`（纯、全）
- ρ = `deriveRows : S → Row[]`（纯、全）
- ι = `rowsToChatMessages : Row[] → ChatMessage[]`（纯、全，适配层）

事件序列 σ = e₁e₂…eₙ，状态轨迹 s₀ → s₁ → … → sₙ（sᵢ₊₁ = δ(sᵢ, eᵢ₊₁)）。
渲染轨迹 rᵢ = ι(ρ(sᵢ))。

### 11.2 不变量（完整修订，I1-I9）

- **I1（槽位唯一）**：∀s∈S, ∀t∈s.turns：t 唯一，t.phase ∈ {live, frozen, committed} **恰居其一**。
  *保证*：`ReadonlyMap` key 语义 + 判别联合（TS 编译期）。
- **I2（committed 可渲染）**：phase.kind='committed' → payload.via='text' ∧ |content|≥1 ∨ payload.via='fold' ∧ |iterations|≥1。
  *保证*：`CommittedPayload` 判别联合 + `NonEmptyS`/`NonEmpty<T>` 构造函数。
- **I3（活动唯一）**：∀s：|{t ∈ s.turns : t.phase.kind='live'}| ≤ 1，且 activeTurn ≠ null 时指向的 turn 必为 live；activeTurn = null 时无 live。
  *保证*：全部产生/消除 live 的转移点共同维护——turn_started（收尸旧 active；对已存在 turn：live 指向/空壳升级/committed 遮蔽解除/其它指针回滚）、lazy 采纳（仅 activeTurn=null 时）、history_replaced（保留 live 优先，ev.active 升级空壳或建槽）、text_final/session（commit/freeze/删槽时清指针，含无产出分支）。归纳见 §11.4。
- **I4（迭代 append-only）**：任一 turn 的已完成迭代序列在任意转移下只允许 append（按 iter# dedup，同号权威覆盖）与整体拷贝（live → committed/frozen/merge），**永不删除**。
  *保证*：写 iterations 的全部 5 处（iteration append / phase_done fold / commit·freeze 拷贝 / history_replaced merge union / committed 遮蔽解除升级 live 时拷贝）均为只增；性质测试 P5 以观察集单调不减逐步验证。
- **I5（seq 单调，修订）**：s.lastSeq 严格递增；iteration/phase_done 携带 seq ≤ lastSeq → δ 返回原状态。**stream 豁免**：stream 是累积全量推送，不按 seq 排序。
  *保证*：reducer 显式比较（iteration/phase_done 两个 case）；豁免仅限 stream case。
- **I6（无 null）**：∀s∈S：任何数组字段非 null；∀e∈E：同。
  *保证*：`normalizeEvent` 是唯一入口，返回类型不含 null 数组；渲染层 ESLint 禁 `as`。
- **I7（工具不相交）**：∀s，对 active live turn：`streamingTools ∩ activeTools = ∅`（按 name 判等）。
  *保证*：reduce 双向维护——iteration case 清同名/前进清空；stream case incoming 对 activeTools 名字过滤。
- **I8（空壳占位语义）**：hollow(t) ≡ t.phase.kind='frozen' ∧ ¬hasOutput(t.phase.data)。hollow turn 是占位符；任何权威活动信号将 hollow 升级为 live。**升级无损**：hollow 的 data 无任何输出，升级为 live 不丢失数据。
  *保证*：`isHollowFrozen` 判定 + 各转移点的升级分支；非 hollow（committed / 有输出 frozen）绝不被降级覆盖。
- **I9（committed 遮蔽解除，新增）**：committed turn 收到比其已有最大迭代号更大的 iteration 事件，或携带实质载荷的 stream 事件 → 升级回 live（迭代 union 保留）。
  *保证*：iteration case 的 `ev.iter > maxIter` 分支 + stream case 的 `hasStreamEvidence` 分支。后端不会对已结束的 turn 发新迭代——迭代事件是活动的权威证据。

### 11.3 定理（完整修订，T1-T9）

**T1（渲染函数 total —— DOM 永不消失）**
ρ ∘ ι 对所有 s∈S 定义且不抛异常。
*证明*：ρ 的每一步只做（a）Map 迭代，（b）穷尽 switch（`kind` 判别联合 + `never` 检查保证编译期穷尽），（c）读取 I2/I6 保证存在的字段。ι 的每一步只做 Row → ChatMessage 的字段映射（判别联合穷尽 switch）。TS 类型 soundness（无 any/断言逃逸，由 lint 强制）⇒ 字段访问的类型即运行时形态 ⇒ 无 TypeError 可达。∎

**T2（turn 边界原子性 —— 无闪烁）**
live→committed 的迁移在**单次 React 渲染提交**内对用户可见，不存在中间帧。
*证明*：迁移发生在 δ 内部（同步、单次 state 替换）；ρ 读同一快照 sᵢ₊₁ 时该 turn 恰处 committed（I1），live 行与 committed 行是**同一 turn 槽位的两种 phase**（非两个并存对象）⇒ 任意快照中该 turn 恰出一行 ⇒ 帧间不存在"旧行消失、新行未现"的间隙。rAF 合并保证一个快照一帧。∎

**T3（已渲染迭代永不消失）**
若迭代 k 在帧 f₁ 已由 ρ 输出（live 态），则 ∀f>f₁：ρ 输出仍含迭代 k（同 turn）。
*证明*：归纳于事件序列，分类所有可能接触该 turn 的转移：(a) iteration append（I4 只增）；(b) phase_done fold（只增）；(c) text_final / session 诱导的 commit/freeze **整体拷贝** iterations；(d) history_replaced merge——incoming 同 ID 时若状态机侧是 live 则 live 胜（含其 iterations），否则 DB 版本含该 turn 的 DB 迭代（DB 是 append-only 权威），状态机独有的 committed/有输出 frozen **保留**（merge 第 2 步）；(e) turn_started 重放——对已存在 turn：live 保留原 phase（含 iterations）、committed/有输出 frozen 不动 phase（性质测试 seed=1/42 曾抓出违反，修复后覆盖）；(f) lazy 采纳/空壳升级仅作用于**无输出**槽（I8），不可能携带已输出迭代；(g) committed 遮蔽解除（I9）——升级回 live 时 `mergeIterations(committed.iterations, [])` = committed.iterations（不减）。所有分支迭代集单调不减 ⇒ 结论。∎

**T4（无 ghost 行）**
∀s, ∀帧：每个 turn 至多渲染一行 assistant。
*证明*：ρ 对每 turn 恰调用一次 assistantRow（flatMap 一对一）；由 I1，该 turn 的 phase 恰居其一 ⇒ 输出一行。ghost 需要 live 行与 committed 行并存 ⇒ 需要 turn 同时处于两 phase ⇒ 与 I1 矛盾 ⇒ 不可构造。∎

**T5（顺序线性一致）**
ρ 输出的行序 = (legacy 前缀) ⊕ turnID 升序 ⊕ (turn 内 user < assistant) ⊕ pendingUsers 沉底。
*证明*：ρ 显式排序（`[...s.turns.values()].sort((a,b) => a.id - b.id)` + `flatMap(t => t.user ? [user, assistant] : [assistant])`）；pendingUsers 追加在末尾（`turnID = MAX_SAFE_INTEGER`）；turnID 由后端 chatProcessLoop 单调分配（DB 恢复）；user 经 requestID→turnHint 双键绑定进 turn（V2 语义，含 echo 先于 turn_started 的时序颠倒）⇒ 顺序是纯函数 ⇒ 同快照同序。ι 保持 Row 顺序（顺序遍历 push）。∎

**T6（活动恢复活性 —— 工具长停机下的 live 可见性）**
设后端 turn τ 正在执行。客户端经任意一条恢复路径后存在 live 槽且后续 τ 事件被接纳：(a) 收到 τ 的 stream/iteration 事件且 activeTurn=null（或 τ 为空壳）→ lazy 采纳/升级（I8：无损）；(b) fetchHistory 返回 active_progress 声明 τ → history_replaced ev.active 分支建槽/升级空壳并置 activeTurn；(c) 收到 turn_started(τ) → 标准 live 创建；(d) committed 遮蔽解除（I9）：committed(τ) 收到 ev.iter > maxIter → 升级回 live。
*证明*：(a) stream case：target=τ；τ 无槽且 activeTurn=null → lazyAdoptLive（I3）；τ 为空壳 → 升级（I8）。iteration case 同型。(b) merge 第 3 步：existing 为空槽/空壳 → 写入 live(snapshot)，activeTurn=τ；existing 已是 live → 指向。(c) 标准。(d) iteration case：committed(τ) + ev.iter > maxIter → 升级 live（iterations 拷贝，I4 不减）。四路径后 turns(τ).phase=live ∧ activeTurn=τ，后续事件通过 `target===s.activeTurn` 主路径写入。∎

**T7（工具单渲染）**
∀帧：live 行的工具区域中，同一 name 的工具至多渲染一个条目。
*证明*：渲染读 (activeTools, streamingTools) 两个数组；I7 保证二者按 name 不相交 ⇒ 并集无重名 ⇒ 单渲染。I7 的保持：iteration case 写 activeTools 后清 streamingTools 同名（迭代前进时全清）；stream case 写 streamingTools' 对 activeTools 名字过滤。两 case 后 I7 成立，其余 case 不写这两个字段。归纳 ⇒ 全轨迹成立。∎

**T8（乐观行单渲染 —— 无双 user 行）**
∀帧：同一 requestID 的 user 行至多渲染一行。
*证明*：user 行有两个来源——(a) `pendingUsers`（user_sent/user_echo 事件，deriveRows 末尾）；(b) `turns[].user`（turn_started/user_echo 绑定进 turn 槽，deriveRows 按 turnID 排序）。reducer 保证：user_echo 到达时先按 requestID 去重 pendingUsers（`filter(u => u.requestID !== ev.row.requestID)`），再 append；turn_started 按 requestID/turnHint 从 pendingUsers 绑定进 turn（移出 pending）。history_replaced 的 pendingUsers 过滤（step 5）剔除已被 turns 绑定的（`turns.values().some(t => t.user?.requestID === u.requestID)`）。ι(historyToReplaced) 过滤 `persisted !== true || dbID === undefined` 的行（useChatMessages 的乐观/echo 副本不进渲染）。三道防线保证同一 requestID 的 user 行不会同时出现在 pendingUsers 和 turns.user ⇒ ρ 输出至多一行。∎

**T9（busy 活性 —— 发送成功后"思考中"必显示）**
设 user_sent 后 REST 成功（user_ack 到达）。若 turn_started 尚未到达（activeTurn=null），则 busy 仍为 true。
*证明*：busy = `currentSession.running ∨ progressSnapshot.streaming ∨ agentChat.busyFallback`。`busyFallback = state.activeTurn !== null`。user_ack 不改变 activeTurn（只清 pendingUser.sending）。若 turn_started 已到 → activeTurn ≠ null → busyFallback=true。若 turn_started 未到 → activeTurn=null → busyFallback=false，但 `onSendSuccess` 回调设置 `currentSession.running=true`（AgentPanel 的 `store.setStatus(selector, 'running')`）→ busy=true。两条路径覆盖。∎

### 11.4 归纳骨架（I1∧I3∧I4∧I7∧I9 在 δ 下保持）

对全部 11 个 case 做 case analysis：

- **turn_started(ev.τ)**：设 s 合法。
  - *收尸*：s.activeTurn=t* 为 live → fold（有产出/user → committed via fold；否则 frozen 空壳）并清指针。I4（拷贝不减）、I3（live 数不增）保持。
  - *已存在 τ 槽*（lazy 后补/重放）：live → 保留原 phase、指针指向（I3）；hollow → 升级 live（I8：无输出故无损）；committed/有输出 frozen → 仅嫁接 user，phase 不变，**指针与 lastSeq 显式回滚**（I3：不产生第二个 live；I4：不动 iterations）。
  - *新槽*：EMPTY_LIVE 写入，指针指向。三路均 I1（Map set 单键）。
- **stream(ev.τ)**：τ 缺失回退 activeTurn。τ 无槽且 activeTurn=null → lazy 采纳（I3）。τ 为空壳 → 升级（I8）。committed(τ) + hasStreamEvidence → 升级回 live（I9：iterations 拷贝不减）。否则非 live → 丢弃。写入路径只替换 content/reasoning/streamingTools/genui——streamingTools' 对 activeTools 过滤（I7 保持）；iterations 不动（I4 平凡）；seq 不 gate（I5 豁免）。
- **iteration(ev.τ)**：非 active 的处理同 stream 前置（lazy/升级/丢弃/committed 遮蔽解除四分支，I3/I8/I9 同上）。seq ≤ lastSeq → 丢弃（I5）。写入：iterations 只增（I4）；activeTools=ev.activeTools 且 streamingTools' 清同名/前进清空（I7）；lastSeq 推进。
- **phase_done(ev.τ)**：仅 active turn；finalIteration fold 进 iterations（I4，T3 根治点）；streaming=false。
- **text_final(ev.τ)**：τ 非 active 且非目标槽 → 丢弃。live/frozen → committed（via text/fold，I2；iterations = union(live, progressHistory) 同号权威覆盖——I4）。无产出分支 → frozen + 清 activeTurn（I3）。committed → 丢弃（幂等）。
- **session**：idle：live 有产出 → frozen 定格；无产出 → 删槽（幽灵行灭绝）。busy → s.busy=true。
- **history_replaced**：merge 五步——① incoming：同 ID 状态机 live 胜 + 嫁接 DB user；incoming 空壳不覆盖状态机 committed/frozen-with-output（I4）。② 状态机独有：live/committed/有输出 frozen 保留（I4）。③ active：保留 live 优先，否则 ev.active 建槽/升级空壳（I8；I3）。③.5 ev.active 与保留 live 同 ID → 快照迭代 union（I4）。④ lastSeq/pending 随属主保真。⑤ pendingUsers 剔除已绑定（T8 防线）。全步 I1（重建 Map，键唯一）。
- **user_sent**：pendingUsers append。I1 平凡（pendingUsers 不在 turns 里）。
- **user_echo**：turn 已存在且无 user → 挂载；否则按 requestID 去重 pending 后 append（T8 防线）。I1 平凡。
- **user_ack**：pendingUsers 按 requestID 匹配 → 清 sending、赋 queued、补 turnHint。或已绑定 turn.user → 同。I1 平凡。
- **user_fail**：pendingUsers 按 requestID 移除。I1 平凡。∎

### 11.5 诚实边界（不可证项）

| 项 | 状态 |
|---|---|
| normalizeEvent 自身的正确性 | 防御性解析 + 性质测试覆盖（raw 生成器），但类型证明止于"返回值满足类型" |
| 渲染组件（MessageList 内部） | ρ ∘ ι 之后的 DOM 层由组件测试/E2E 覆盖，不在本证明范围 |
| GenUI iframe 生命周期 | 独立子系统，仅保证 genui 字段随 T3 保全 |
| SSE 传输层（coalescing/gap/replay） | 状态机假设事件可能乱序/重放/丢失并以 I5+lazy+merge+I9 容忍，但传输本身的送达性不在证明内（弱网最终一致由 reload→DB 权威兜底） |
| SW/HTTP 缓存层 | bundle 新鲜度由 sw2.js no-store + index.html no-cache + assets immutable 保证（部署面），不在状态机证明内 |
| history 属主门控（hook 层） | resolvedChatID ≠ chatID 时跳过 dispatch 是 hook 的前置条件，非 δ 的一部分；违反它不会破坏 I1-I9（只会延迟灌入） |
| busy 的 currentSession.running 来源 | 来自 SSE session(busy) 事件或 onSendSuccess 的 setStatus（hook 层），非 δ 的一部分；T9 证明覆盖两条路径 |
