# Blackboard — 跨 agent 共享黑板

> 一句话：**一块板，多个 agent**。主 agent、它的全部 SubAgent（以及加入同一命名板的独立会话）共享一份**持久**工作面，条目带 revision（CAS）、租约认领（可自愈）与依赖边（自动解锁），变更实时推给 UI 并**唤醒关注它的会话**。

## 1. 它是什么 / 不是什么

**是**：宿主**通用**的协作底座 —— 只认 `board` + `key`，`kind`/`body`/`status` 由产出方解释（与 `shared_artifacts` 同一哲学：宿主零领域语义）。核心代码里**没有** team / workflow / task 之类的领域分支。

**不是**：
- 不是会话私有的 TODO（那是 `todoManager`，见 §6 对照）——黑板是**跨会话**的；
- 不是流水线引擎（没有 DAG、没有步骤定义）——依赖只是"这些条目 close 之前它不能开工"；
- 不是新的事件通道（推送与唤醒**复用**既有 SSE 与 bg 通知管线）。

## 2. 数据模型（`storage/sqlite/blackboard.go`，schema v71）

`blackboard_entries`（全局表，不属于 tenant）：

| 列 | 语义 |
|---|---|
| `board` | 作用域：**会话板** = 会话键（`web:chat_abc` / `cli:/repo`）或**命名板** `@name`（`@` 是分隔符——会话键永不含 `@`，两个命名空间不可能撞车） |
| `key` | 板内唯一（`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`） |
| `kind` | **产出方声明**的内容类型，宿主不解释（task / finding / decision / note…） |
| `title` | 一行摘要（必填；列表与 UI 的主字段） |
| `body` | **不透明**载荷（markdown/JSON，≤64KB）——列表**不返回** body |
| `status` | 调用方自定义标签（宿主透明；惯例 open/working/done）。**不参与依赖判定** |
| `closed` | 结构性"已了结"位：依赖门控只看它 |
| `blocked_by` | JSON 数组：纯结构依赖边（允许前向引用） |
| `revision` | **CAS 令牌**：每次写入 +1 |
| `claimed_by` / `claim_token` / `claim_expires_at` | 租约：持有者标签 / **租约句柄** / 到期（unix ms） |
| `created_by` / `created_at` / `updated_at` | 审计 |

**派生（不落库，读时计算）**：`claimed_by` 仅当租约未过期才对外可见（过期即时视为空闲，**无后台清扫**）；`blocked` = 任一依赖缺失或未 closed；`ready` = `!closed && !blocked && 空闲`。

## 3. 三个机制（这是它比"共享 KV"强的地方）

1. **revision CAS**：`update/close/reopen/delete` 必须带 `expected_revision`（工具层强制；RPC 层可省略 → 由 handler 先读再写，仍走 CAS）。冲突**回带权威条目**，落后者一眼知道该用哪个 revision 重试——绝不静默覆盖同伴的成果。
2. **租约认领（claim token）**：`claim` 是**单条 SQL UPDATE + `RowsAffected`**，原子；空闲/租约过期才抢得到；续租与释放都要 `claim_token`。**为什么用 token 而不是身份**：身份字符串不唯一——同一个 role 的两个 SubAgent 实例共享 `SessionKey`（`instance` 从不进 ToolContext，`engine_wire.go` 的 `subAgentID = parent/role`），用身份做租约的话第二个实例能"续租"别人的活。token 是 128-bit `crypto/rand`，只在**成功认领**时返回，且 `json:"-"` 永不随读接口泄漏（同 share token 的"凭据"模式）。
3. **依赖门控**：`blocked_by` 未满足时条目 `ready=false`，**且 `claim` 直接拒绝**（判据写在 claim 的 UPDATE 里，与写入同一事务语义，不是先读后判）。所以"ready"不是建议而是闸门——并行 agent 不会在沙地上开工。

## 4. 三个消费路径

| 路径 | 实现 | 语义 |
|---|---|---|
| **Agent 读写** | `tools.BlackboardTool`（单一工具多 action：post/get/list/update/claim/release/close/reopen/delete/watch/unwatch） | 列表默认**不带 body**、默认 50 条：列表是给模型看的摘要，正文用 `get` 取（上下文纪律） |
| **UI 实时** | 每次成功写入 → `tools.BlackboardEvent` → `Agent.broadcastBlackboardUpdate` → `channel.BlackboardUpdateSender` → `WebChannel.SendBlackboardUpdate` → **seq=0 全 web 客户端广播**（`broadcastSessionStateToWebClients`） | 载荷是"**变了，去重取**"信号（`protocol.BlackboardUpdatePayload`），不是第二份状态；前端 `blackboard_update` → `blackboard-update` window 事件 → 面板 120ms 去抖后重取 |
| **唤醒 agent** | `agent/blackboardHub`（watch 注册表 + 合并窗口）→ `Agent.injectAsyncMessage(..., tools.AsyncSourceBlackboard)` → 既有 bg 通知管线 | **busy ⇒ 作为 synthetic tool-result 注入当前迭代；idle ⇒ 开新 turn**。每 `(会话, 板)` 3 秒**至多一条**（leading + trailing：窗口内被压下的变更在窗口结束时补发，**不丢**）；**绝不通知写入者自己** |

`watch` 是显式、按会话的内存订阅：不 watch 就完全不会被打扰；重启丢订阅是正确行为（板本身是持久的）。

## 5. 不变量 / 铁律（改动前必读）

1. **默认板 = `RootSessionKey`**（fallback `SessionKey` → `Channel:ChatID`）——这是"主 agent 与它的全部 SubAgent 自动共享"的唯一实现方式。**绝不能用 `SessionKey`**：web 浏览 CLI 会话时它被 physicalChannel override 成 `web:chatID`，会把同一会话劈成两块板（与 `TodoManager.sessionKey` 同款事故，见 `tools/todo.go:285-293`）。
2. **通知只走既有 bg 管线**：绝不新造注入通道，绝不伪造 turn。busy 注入发生在**迭代边界**（工具执行之后），不打断正在跑的工具。
3. **写入者不通知自己**：否则"写一条就被自己唤醒"会自我循环。
4. **合并窗口必须 leading + trailing**：只做 leading 会把窗口内的变更**永久吞掉**（agent 以为板没动）。
5. **SSE 事件必须进 `SSE_EVENT_TYPES` 白名单**（`web/src/providers/sseConnection.ts`）：native `EventSource` 按事件名派发，漏注册 = 面板永远收不到（同 `ask_user_resolved`/`bg_task_output` 的坑）。
6. **`claim` 必须拒绝 closed / blocked 的条目**，判据在 SQL 里（`blackboardClaimableSQL`）；把闸门写在 Go 侧"先读后判"就重新引入竞态。
7. **列表不返回 body**（除非显式 `include_body`）：一块 500 条的板 × 64KB 正文会直接吃掉模型上下文。
8. **冲突必须回带权威条目**（`BlackboardConflictError.Entry`）：没有它，模型只能盲重试。
9. **黑板入口必须有上限**：板 ≤500 条、body ≤64KB、租约 TTL 30s…24h（超限**显式报错**，不静默截断/不 evict——丢掉同伴的条目比报错更糟）。
10. **Core 里不得出现 team/workflow 语义**：黑板只提供机制；"什么是一条 task""谁该做什么"由使用它的 skill/agent 决定。

## 6. 与邻近概念的对照

| | 作用域 | 持久 | 并发控制 | 变更如何到达 |
|---|---|---|---|---|
| `todoManager`（TodoWrite） | **单会话**（SubAgent 另算） | 文件 | 无（整体覆盖） | progress 事件（本会话 UI） |
| Goal | 单会话 | DB | 无 | TDSM |
| **Blackboard** | **跨会话/跨 agent** | SQLite（v71） | **revision CAS + 租约** | SSE 广播 + **bg 通知唤醒 agent** |
| `offload_store` | 主 + 子（共享目录） | 文件 | 无 | 无（只读召回） |
| PeerGroup | 独立会话之间 | JSON | 无 | 消息投递 |

## 7. 竞品对照（为什么值得做）

| 能力 | Claude Code Agent Teams（2026-02，实验开关） | Codex（并行 agent） | 本实现 |
|---|---|---|---|
| 共享工作面 | 有（**任务**列表） | 无（只有 worktree 隔离） | 有，且**通用**（kind/body 不透明） |
| 认领 | self-claim，**轮询** TaskList | 无 | **原子认领 + 租约**（无轮询；持有者崩溃自动可接管） |
| 并发正确性 | "file locking"（语义未公开） | 无 | **revision CAS**（冲突回带权威条目） |
| 依赖 | 有（blocked 自动 unblock） | 无 | 有，且**claim 直接拒绝未满足依赖** |
| 唤醒 | 无（必须轮询） | 无 | **推送**：busy 注入 / idle 开 turn |
| 持久性 | 官方承认 limit："resumed sessions refer to teammates that no longer exist" | 无共享态 | SQLite 持久，revision/租约/依赖全过重启 |
| 子代理参与 | 否（teammate 是独立会话） | 否 | **是**（默认板 = 根会话 ⇒ 主 + 全部 SubAgent 自动共享） |
| 人类可见 | tmux `Ctrl+T` 覆盖层 | 线程视图 | Web 面板：实时、跨会话、含租约倒计时与依赖 |

## 8. 关键文件

| 文件 | 职责 |
|---|---|
| `storage/sqlite/blackboard.go` | 表 DDL（`blackboardSchema`）+ `BlackboardService`：CAS/租约/TTL/依赖派生/配额/派发 `BlackboardConflictError` |
| `storage/sqlite/migrations.go` | `migrateV70ToV71`（幂等；新库走 `schema.go` 的同一份 DDL） |
| `tools/blackboard.go` | `BlackboardTool`（参数→服务调用→markdown 渲染）、`BlackboardHub` 接口、`BlackboardEvent` |
| `agent/blackboard.go` | `blackboardHub`：watch 注册表 + 3s 合并窗口（leading+trailing）+ SSE 扇出 + 唤醒投递 |
| `serverapp/rpc_blackboard.go` | 面板 RPC：`blackboard_list/boards/get/post/close/release/delete`（人类写入 actor=`human`，同一 publish 点） |
| `channel/web/web.go` | `SendBlackboardUpdate`（seq=0 全客户端广播） |
| `web/src/hooks/useBlackboard.ts` + `components/blackboard/BlackboardPanel.tsx` | 面板：重取是唯一权威、120ms 去抖、1s 本地倒计时 |
| `web/src/types/shared.ts` / `providers/sseConnection.ts` | 事件类型 + **白名单** + window 事件派发 |

## 9. 测试（判别力清单）

- `storage/sqlite/blackboard_test.go`：并发认领**恰好 1 胜**、租约过期自愈、CAS 无损、依赖门控（含 **claim 被拒**）、同身份不同 token 无法共享租约、release 幂等（无事件）、配额、板隔离、`List` 的派生标志（**守住"取切片元素地址"那类静默 bug**）、v70→v71 幂等迁移。
- `tools/blackboard_test.go`：主/子共享默认板、physicalChannel override 不分板、跨会话隔离、全生命周期（含重复 post 冲突回带原条目、陈旧 CAS 被拒且**不覆盖**）、同 role 双实例争抢、错误可操作、watch 幂等/前缀。
- `agent/blackboard_test.go`：watch 注册表、**写入者不通知自己**、突发合并（leading + trailing 不丢）、前缀过滤、退订后停止。
- `web/src/hooks/useBlackboard.test.tsx` + `components/blackboard/BlackboardPanel.test.tsx`：会话板读取、事件去抖、**陈旧响应丢弃**、写入后重取、面板状态/倒计时/展开正文/广播刷新。

## 10. 踩过的坑（写在这里省下一轮）

- **`List` 的"取切片元素地址"**：`out = append(out, *e); ptrs = append(ptrs, &out[len(out)-1])` —— append 扩容后指针指向旧数组，派生标志（blocked/ready）**写进了没人读的内存**，list 永远返回 `blocked=false`。改法：先收 `[]*Entry`，`applyDerived` 之后再解引用。**这类 bug 只有断言派生字段的测试能抓住**。
- **身份不是租约**：SubAgent 的 `instance` 不进 `subAgentID`，同 role 两个实例 `SessionKey` 相同 ⇒ 早期"同身份可续租"的实现会被第二个实例"续租"掉（同一时刻两个 worker 都以为自己在做）。token 修掉。
- **`claim` 忘了 publish**：工具里 `claim` 一开始没调 `publish`，于是"某人接手了活"这件**最重要的事**既不上 UI 也不唤醒任何人（单测抓住）。
- **schema 版本有三处**：`db.go:schemaVersion`、`schema.go` 的快照字面量 `INSERT INTO schema_version VALUES (n)`、以及迁移链 `from < n`；漏改快照字面量 ⇒ `TestSchema_CreationFromZero` 红。另外**手写 `version != n` 的旧测试**（`migration_v69_test.go`）必须改成 `schemaVersion`。
- **新表要持 `db.writeMu`**（`share.go` 早于写闸门 P0，别照抄它的"不持锁"）。
