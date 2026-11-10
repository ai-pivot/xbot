# time-representation.md — 时间表示统一铁律（2026-10-14「压缩分隔条堆一摞」事故定稿）

> 事故：用户亲报 web 前端「turn-154 后连续 4 个 `compaction-divider` 堆一摞」，且确认
> 「这些压缩都发生在很早的迭代上、位置根本不对」。第一轮误诊为「同锚点多次压缩 ⇒
> latest-wins 只留一条」（web 仓 `3ab7e332`，**用户亲判完全错误、已整体撤销**）——
> 症状在前端、根因在**后端两种时间表示混存 + 读侧按本地时区猜裸串**。

## 事故链（live API + 生产 DB 实证）

1. **写**：`iteration_history.created_at` 从未被 Go 显式写 —— `AppendIterationHistory` 的
   INSERT 不含这一列 ⇒ 落库值全部来自 DDL 默认 **`CURRENT_TIMESTAMP` = UTC 裸串**
   （`"2026-10-14 09:10:31"`，生产 DB 实测 607 行全为此形态）。
2. **写**：`session_messages.created_at` 由 Go 写 `time.RFC3339(Nano)` = **带本地偏移**
   （`"2026-10-14T17:22:20.601938+08:00"`；生产 DB 实测 58004 行 100% 此形态、0 条 Z 串、0 条裸串）。
3. **读**：`parseSQLiteTime → internal.ParseTimestamp` 的 case 2/3 把「裸串 / 驱动改写的 Z 串」
   按 **`time.Local`(+08:00) 墙钟**解析 —— 对 session_messages 的历史裸串（老 Go 本地写入）**恰好正确**，
   但对 iteration_history 的 **UTC 裸串 = 整体早 8 小时**。
4. **算法退化**：压缩标记 ts（形态 2，真实 instant）“晚于”**全部** turn 迭代区间 ⇒
   `channel.compactionAnchor`（时间线归属）退化成「最后一个 turn + 该 turn 最大迭代号」
   ⇒ **同窗全部压缩点拿到同一个 (turnID, afterIteration)** ⇒ 前端把 N 条分隔渲染在
   「最后一个 turn 的末尾」堆一摞。live API 实测：5 条标记全部 `after_iteration=30`
   （= turn155 最大迭代号），而其真实位置在 turn152 的**很早**迭代上。
5. **为什么纯 struct 测试全绿、线上却错**：`compaction_anchor_test.go` 全部用**已解析好的
   `time.Time`** 构造 `turnIterMap` —— 解析层（错位发生处）从未被任何判据走过。

## 铁律（新增代码必须全部遵守）

- **写**：`session_messages` / `iteration_history` 的 `created_at` **只许经 `canonicalTimestamp(ts)`
  **（`storage/sqlite/db.go`：`ts.Local().Format(time.RFC3339Nano)`）—— 任何时区的 `time.Time`
  进、恒带偏移出；`ts.Location()==UTC` 的值在 host TZ=UTC 下产出 "Z" 形态，但此时
  local≡UTC、读侧按本地猜 ≡ 字面 UTC —— **构造上无害**（错位组合「写 Z 串 & host local≠UTC」
  被本写法永久排除）。⛔ 禁止任何写点绕过它直接 `ts.Format(time.RFC3339*)` 落这两张表
  （含 `time.Now().UTC().Format(...)`）。
- **写**：⛔ 新表/新列**禁止依赖** `DEFAULT CURRENT_TIMESTAMP`（= UTC 裸串）作为「会被与
  Go 时间值比较」的时间列的值来源 —— 必须显式声明并在 Go 侧显式写 `canonicalTimestamp`。
  （历史遗留：iteration_history 全表、schema 里大量其它表的 created_at 仍为默认值 —— 读取
   侧按表逐一审查，见下。）
- **读**：解析**只许走两个专用函数**，⛔ 禁止裸 `time.Parse` / 自造 layout：
  - `internal.ParseTimestamp`：**session_messages 及同源表**。Z 串按**本地墙钟**猜
    （驱动对裸串的改写形态）—— 该猜测**只对「本地写入的裸串」正确**，靠写侧
    canonicalTimestamp 保证新数据不再产生歧义面。
  - `internal.ParseIterationTimestamp`（返回 error）：**iteration_history 专用**。
    RFC3339 带显式时区 ⇒ 直接解析；裸串/Z 串 ⇒ **UTC**（本表裸串唯一来源就是
    `CURRENT_TIMESTAMP`）；⛔ 无法识别 ⇒ **报错**，调用方（`parseIterationTime`）记
    ERROR + 按零值处理 —— 下游 `buildTurnSpans` 跳过 / `compactionIteration` 回落
    standalone 标记行（信息不丢，**绝不静默猜错**）。
  - ⛔ 两函数**不可互换**：session_messages 的历史裸串是**本地**墙钟、iteration_history 的
    历史裸串是 **UTC** —— 表级语义由该表历史上唯一的写方决定。混用 = 本事故复发。
- **比较**：不同时间表示的**字符串**之间禁止任何比较/推算；必须先经对应解析函数化成
  `time.Time`（instant）再比较。任何「时间戳相关逻辑」的判据测试必须以
  **`TZ=Asia/Shanghai` 与 `TZ=UTC` 双跑**（同一批数据必须产出同一批 instant 与同一批
  归属判定 —— 时区只许影响墙钟显示，绝不许影响时序事实）。
- **测试数据的形态保真**：给迭代/消息表 seed 时间时，**必须复刻生产数据形态**
  （裸 UTC 串 / 带偏移 RFC3339），不许只给 `time.Time` —— 第 5 点教训：纯 struct
  fixture 就是漏判的根源。

## 守护判据（全部 mutation 自证过 RED→GREEN）

- `storage/sqlite/iteration_created_at_tz_test.go`：
  - `TestIterationCreatedAt_LegacyBareIsUTC` —— CURRENT_TIMESTAMP 裸串按 UTC 读回
    （四条读路径 byte 级一致）；
  - `TestIterationCreatedAt_CanonicalWriteAndRoundTrip` —— 写侧必须显式带偏移
    canonical（stored 串含 `T` + 偏移/Z）且 instant 往返；
  - `TestIterationCreatedAt_GarbageFailsSafe` —— 未知格式零值 + 邻行不受牵连。
- `channel/compaction_anchor_tz_test.go`（**生产形态端到端**，真实 SQLite）：
  - `TestCompactionAnchor_LegacyUTCBareSpansZonedMarker` —— 裸 UTC 迭代时间线 +
    `+08:00` 压缩标记 ⇒ 必须归到**真实在跑的 turn** 的**真实迭代边界**（修复前：
    归到最后一个 turn 的最大迭代号 = 用户 DOM 看到的堆叠形态，本条必红）；
  - `TestCompactionAnchor_SpansFromDBDontDependOnHostTZ` —— 同一批数据任意宿主
    时区下 instant / 锚点判定一致。
- 前端语义钉（`web/src/chat/compactMarkerOrder.test.tsx`）：`同锚点多条压缩全部渲染`
  —— 每条分隔都是一次真实发生的独立压缩，**禁止 latest-wins 类去重**（第一轮错误
  修复的教训写进用例注释）；`不同迭代边界各自渲染在所属块`。

## 事故后仍需注意的存量风险（记录在案，未处理）

- schema 里大量**其它表**的 `created_at` 仍依赖 `DEFAULT CURRENT_TIMESTAMP`（= UTC 裸串）。
  与 Go 时间值发生**比较**的读取点目前只有 session_messages / iteration_history（本次已修）；
  其余表多为「仅展示/仅排序（ORDER BY id）」用途。动到任何一张表的时间比较逻辑时，
  必须先查它的历史写方是 SQL 默认还是 Go 显式，再选解析函数。
- `gotchas-misc.md`（注入型工具对一节）早在 2026-10-02 就注意到了「UTC 裸串与 Go 写的
  消息行不同源，非本时区下时间戳比较是地雷」并因此把合成工具锚定改成「注入时记录、
  读取不猜」—— 与本文件同一根因的两个平行出口；改时间比较逻辑前**两处都要读**。
