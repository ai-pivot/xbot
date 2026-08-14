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

### 4.2 reduce 转移表（全部业务规则，穷尽）

```typescript
export function reduce(s: ChatState, ev: DomainEvent): ChatState {
  switch (ev.type) {
    case 'turn_started':     // 活动 turn 有未 finalize 内容 → fold commit（数据保全）
                             // 新 Turn 进 live 态；requestID 精确绑定 user
    case 'iteration':        // ev.turnID !== activeTurn → return s（迟到/旧 turn 丢弃）
                             // iter > 当前 → append iterationsDelta（dedup by iter#）
    case 'stream':           // ev.turnID !== activeTurn → return s
                             // 全量替换 content/reasoning（无追加/回退歧义）
    case 'phase_done':       // finalIteration 非 null → fold 进 iterations（append-only）
                             // streaming=false（不冻结数据）
    case 'text_final':       // activeTurn(live|frozen) → committed（via text/fold）
                             // cancelled=true 时先 freeze 再 commit
    case 'session':          // busy=false 且 live 无产出 → 清空壳（idle 幽灵行灭绝）
    case 'history_replaced': // 全量替换（rewind/reload/hydration 是同一个转移）
  }
}
```

**迟到事件语义统一为一条规则**：`ev.turnID ≠ activeTurn`（且非 history_replaced）→ `return s`。旧设计的 4 个 guard、finalizedTurnID 全局残留、chatID 切换漏重置——全部被这一个显式比较取代（guard 与状态脱节的 bug 类别**结构性消失**）。

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

### 5.2 不变量（每条标注保证机制）

- **I1（槽位唯一）**：∀s∈S, ∀t∈s.turns：t 是唯一的，t.phase ∈ {live, frozen, committed} **恰居其一**。
  *保证*：`ReadonlyMap` key 语义 + 判别联合（TS 编译期）。
- **I2（committed 可渲染）**：phase.kind='committed' → payload.via='text' ∧ |content|≥1 ∨ payload.via='fold' ∧ |iterations|≥1。
  *保证*：`CommittedPayload` 判别联合 + `NonEmptyS`/`NonEmpty<T>` 构造函数（编译期 + 唯一运行时检查点）。
- **I3（活动唯一）**：∀s：|{t ∈ s.turns : t.phase.kind='live'}| ≤ 1，且 = activeTurn 所指。
  *保证*：reducer 的 turn_started 转移（构造性：新 live 产生前旧 active 必须 freeze/commit —— 转移函数 8 个 case 逐一验证，见 §5.4 归纳）。
- **I4（迭代 append-only）**：sᵢ → sᵢ₊₁ 过程中，任一 turn 的已完成迭代序列只允许 append（按 iter# dedup）与槽位迁移（live.iterations → committed/frozen 数据），**永不删除**。
  *保证*：reducer 中只有 3 处写 iterations（iteration append / phase_done fold / commit 拷贝），均为只增；TS readonly 阻止原地修改。
- **I5（seq 单调）**：s.lastSeq 严格递增；携带 seq 的事件若 ≤ lastSeq → δ 返回原状态。
  *保证*：reducer 显式比较（迟到/重放丢弃）。
- **I6（无 null）**：∀s∈S：任何数组字段非 null；∀e∈E：同。
  *保证*：`normalizeEvent` 是唯一入口，返回类型不含 null 数组；渲染层 ESLint 禁 `as`/禁访问未声明字段。

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
*证明*：归纳于事件序列。迭代 k 进入 `turn.iterations` 的唯一路径是 append（I4）；后续可能的事件中，phase_done 只增（fold finalIteration），text_final/turn_started 诱导的 commit/freeze 转移**整体拷贝** iterations 至新 phase 数据；无任何 δ 分支删除迭代。I4 保持 ⇒ 结论。∎

（Bug 5"最后 iter 闪烁"的根治：phase_done 在 δ 内 fold finalIteration，text_final 到达前迭代已在 committed 路径的数据里——不再依赖"text 重建"这个补丁路径。）

**T4（无 ghost 行）**
∀s, ∀帧：每个 turn 至多渲染一行 assistant。
*证明*：ρ 对每 turn 恰调用一次 assistantRow（flatMap 一对一）；由 I1，该 turn 的 phase 恰居其一 ⇒ 输出一行。ghost 需要 live 行与 committed 行并存 ⇒ 需要 turn 同时处于两 phase ⇒ 与 I1 矛盾 ⇒ 不可构造。∎

**T5（顺序线性一致）**
ρ 输出的行序 = (legacy 前缀) ⊕ turnID 升序 ⊕ (turn 内 user < assistant)。
*证明*：ρ 显式排序；turnID 由后端 chatProcessLoop 单调分配（DB 恢复，见 web-linearizability.md 已证引理）；optimum user 经 requestID 绑定进 turn（V2 语义保留为 turn_started 转移的构造规则）⇒ 顺序是纯函数 ⇒ 同快照同序。∎

### 5.4 归纳骨架（I1∧I3∧I4 在 δ 下保持）

对 8 个 case 逐一 case analysis（下面列出关键两个，其余同型）：

- **turn_started(ev)**：设 s 合法。若 s.activeTurn=t* 存在且 phase=live：δ 构造 t*'（committed, via:'fold'，`nonEmpty(iterations)` 成功——若失败则 fallback via 不存在 ⇒ 构造 committed 需 content 非空；两者皆空时保持 frozen 并清 active（该状态 ρ 不出空壳行，由 T1/I2）。新 turn tₙ 进 live；turns' = turns ⊕ {tₙ}，activeTurn'=tₙ.id。I1（Map 语义 + 判别联合）、I3（唯一 live）、I4（拷贝不减）保持。∎
- **text_final(ev)**：ev.turnID ≠ activeTurn → return s（全部不变量平凡保持）。相等且 phase∈{live,frozen} → 构造 committed：content 非空→via:'text'；否则 `nonEmpty(iterations)`（I4 保证 fold 前已含本 turn 全部迭代，含 phase_done fold 的 finalIteration）→ via:'fold'。I2 由构造函数签名强制。∎

### 5.5 诚实边界（不可证项）

| 项 | 状态 |
|---|---|
| normalizeEvent 自身的正确性 | 防御性解析 + 性质测试覆盖（raw 生成器），但类型证明止于"返回值满足类型" |
| 渲染组件（MessageList 内部） | ρ 之后的 DOM 层由组件测试/E2E 覆盖，不在本证明范围 |
| GenUI iframe 生命周期 | 独立子系统（本轮未动），仅保证 genui 字段随 T3 保全 |

---

## 6. 历史 Bug → 消灭机制映射表

| Bug | 消灭机制 | 层 |
|---|---|---|
| 1 cancel guard 污染新 turn | activeTurn 唯一指针；非 active 事件 → return s（零渲染） | reducer |
| 2 切会话 turn_id 残留 | ChatState per-chat 实例化；切换 = history_replaced 全量替换 | 架构 |
| 3 迟到 text 被拦截丢 content | 无"拦截"概念——text_final 对 active turn 永远 commit（I2 构造） | reducer |
| 4 null.tools 整树卸载 | normalizeEvent 唯一 null 处理点；类型层无 null 数组 | 类型+入口 |
| 5 最后 iter 闪烁 | phase_done fold 进 δ；T3 append-only | reducer+证明 |
| 6 ghost live 行 | Map 槽位唯一 + 判别联合；T4 | 类型 |
| 7 思考中位置错 | Row.kind 判别；isPartial 语义收窄至 live/frozen | 类型 |
| 8 发新消息旧 turn 重复 | turn_started 的 fold-commit 是唯一转移；单槽位 | reducer |

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

## 10. 评审决策点

1. `frozen → committed` 的触发：仅 text_final，还是新 turn_started 也可以 fold（现设计：两者都可以，后者 `via:'fold'`）—— 影响 commit 时机语义。
2. `legacy` 前缀行是否在 M1 就并入 Turn 模型（现设计：保留独立前缀，M5 后续再统一）。
3. shadow 运行期长度：一个迭代周期 vs 一周（M3 风险预算）。
