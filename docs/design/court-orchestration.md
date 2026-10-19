# 朝廷式全自动组织结构（Court Orchestration）架构方案

> 状态：设计稿（不含实现代码）
> 基线：`origin/master@619ec6c3`（2026-10-19）
> 工作分支：`feat/court-orchestration`（隔离工作树 `/tmp/xbot-court`）
> 作者：taizi
> **版本：v2（2026-10-19）** —— 按用户拍板决策修订：消息只落会话分库；群组只有群发/单发 + 两张小表；ask_user 完全异步；旧群数据直接删除；批红规则引擎保留。

---

## 文档索引（详细设计系列）

本文件是总体方案。以下四份是**可批红的详细设计**：

| 文档 | 覆盖 | 核心内容 |
|---|---|---|
| [`sendmessage-structured.md`](./sendmessage-structured.md) | ① SendMessage 强化 + ② 删除 AskUser | 信封 schema、kind/payload、correlation/幂等/超时、**消息只落会话分库**、异步 ask_user 与插队队列、群发/单发语义、AskUser 删除清单 |
| [`message-sender-id.md`](./message-sender-id.md) | ③ 每条消息记录发送人 | `sender_type`/`sender_id`/`sender_name`（**仅会话库**）、会话库 v3→v4 迁移与回填、写入/读取/UI 改造 |
| [`group-hardening.md`](./group-hardening.md) | ④ 群组（群发/单发） | 只有群发与单发；`groups`/`group_members` 两张小表；跨会话库群历史聚合；D1–D17 修复；hop/fanout/限流；`grp_<nanoid>`；旧群数据删除清单 |
| [`court-reporting.md`](./court-reporting.md) | ⑤ 批红 + 汇报详细设计 | state schema、report/decision 消息、确定性规则引擎 schema（白名单/阈值/证据/矛盾）、朝会 cron、端到端时序 |

阅读顺序建议：① → ③ → ④ → ⑤（消息模型 → 身份 → 群组 → 朝廷流程）。

**三条不可动摇的基线**（后续任何实现 PR 都不得违反）：

1. **主库不放任何消息**：所有消息只存在于 `sqlite.SessionDBRelPath(channel, chatID)` 指向的会话库。
2. **没有「群组消息」**：群发 = 遍历成员各发一份、各写各的会话库；群历史 = 跨会话库按 `group_id` 聚合。
3. **`ask_user` 完全异步**：投递即返回；用户回复是插队消息，在下一个 iteration 边界注入。

**决策基线（用户拍板 A–E）**：

| # | 决策 |
|---|---|
| A | 消息绝不碰主库：只落会话分库，主库不放任何消息表 |
| B | 只有群发和单发；群状态 = `groups` + `group_members` 两张小表；无群级消息表 |
| C | `ask_user` 完全异步；旧 AskUser 工具与旧交互直接删除，无兼容窗口、无只读回放 |
| D | 旧群数据（`peer_groups.json`、内存成员表）直接删除，不迁移不兼容 |
| E | 批红规则引擎保留：确定性 YAML + 脚本，不由 LLM 判定 |

---

## 0. 一页纸结论

「朝廷」不是一套新的业务流程代码，而是**把现有原语按组织语义组合起来的使用规范**：

- **部门 = interactive SubAgent**（`tools/subagent.go` + role `.md`）
- **部门会议 = 群发 + 单发**（`SendMessage(to="group:grp_*")` 群发；成员回给发起者用单发）
- **朝会 = cron 注入主会话**（`cron/scheduler.go` + `tools/cron.go`）
- **奏折/工作日志 = 工作区普通文件**（Read/Write/Grep，零核心改动）
- **批红 = 确定性校验脚本（规则）+ 汇总 agent（只做机械格式化）**
- **持久化 = per-session SQLite + memory + role 注册表（磁盘 md）+ 主库两张群组小表**
- **审计 = hooks 的 SubAgentStart/Stop（现有事件）**

消息层的三条硬约束（详见 ①）：

1. **消息只落会话分库**：`session_messages` / `iteration_history`，主库无消息表。
2. **只有群发和单发**：群发 = 遍历成员各发一份；群历史 = 跨会话库按 `group_id` 聚合。
3. **`ask_user` 完全异步**：投递即返回，用户回复是插队消息，在下一个 iteration 边界注入。

核心缺口只有四个，全部是**通用原语**，不含任何「朝廷」业务语义：

1. `WakeInteractiveSession`：从 DB 重建内存中的子代理会话（当前完全缺失）。
2. 子代理 TTL 策略化：`destroy` → `hibernate`/`retain`，并把「自然完成时间」写对。
3. spawn 时的 `Clear()` 语义拆分：新增/续跑/重置三种意图分开。
4. 群组通用缺陷修复：两张小表 + 群发/单发语义 + 成员校验 + 限流（不是新建群组引擎）。

---

## 1. 目标与非目标

### 1.1 目标

1. **用现有功能组合出组织结构**：subagent / CreateChat(group) / SendMessage / cron / session DB / memory / hooks / plugins，每一层都能在代码里指出对应实现。
2. **组织形态是数据，不是代码**：
   - 部门名、职责、工具白名单 → `.xbot/agents/*.md`（已有）
   - 汇报关系、会议周期、批红规则 → 工作区 `court/org.yaml` + skill 脚本
   - 任务进度、阻塞、证据 → `court/state/**.yaml`（唯一事实源）
3. **子代理可跨长时间唤醒**：进程重启、数天/数周后仍能恢复对话与记忆。
4. **上级校验不依赖转述**：首辅直接读原始状态文件做 diff/哈希校验，汇总 agent 只做机械层。
5. **全链路可审计、可回滚**：每次写状态有 append-only 日志；每个 Phase 有可执行验收判据。

### 1.2 非目标（明确边界）

| 不做 | 原因 |
|---|---|
| 不新增 `court/` Go 业务包、不写死部门名单/流程/轮次/模型 | 违反「不定制逻辑」；组织变化应由数据驱动 |
| 不在 core 解析奏折/朝会格式 | 格式属于使用约定，属于 skill / prompt / 脚本 |
| 不实现通用审批工作流引擎 | 批红是**确定性规则校验**，不是 BPM |
| 不让 LLM 做「批红判定」 | 批红只做机械层：schema、存在性、单调性、时效性；判断留给首辅/用户 |
| 不新增消息总线 / 不改 Dispatcher 模型 | 现有 `SendMessage` + `AgentChannel` 已够用 |
| 不做多租户权限体系 | 当前是单 operator（v63），超出本方案 |
| 不替换 memory provider | 只修正 SubAgent tenant 派生与整合 no-op |

### 1.3 「不写死逻辑」的可操作判据

一个改动是否合规，用三问检验：

1. 换一个部门名/换一条汇报线，需要改 Go 代码吗？需要 → 不合规。
2. 换一种会议节奏（每日/每周/事件触发），需要改 Go 代码吗？需要 → 不合规。
3. 换一套批红规则（新增字段校验），需要改 Go 代码吗？需要 → 不合规。

允许进 core 的只有「与组织语义无关的通用能力」：唤醒、休眠、持久化、群组生命周期、状态机。它们对任何多代理编排场景都成立。

---

## 2. 架构图

```
                                ┌──────────────────────────────────┐
                                │ 皇帝（用户）                       │
                                │ 下旨 / 批红 / 随时插入            │
                                └───────────────┬──────────────────┘
                                                │ IM / Web / CLI 消息
                                ┌───────────────▼──────────────────┐
                                │ 内阁首辅（主 Agent 常驻 session）   │
                                │ · 唯一调度者          │
                                │ · 拆旨 → 派发 → 验收 → 上奏        │
                                └───┬───────────┬───────────┬──────┘
                    CreateChat(group)│           │cron       │SendMessage(agent:)
                                    │           │           │
              ┌─────────────────────▼──┐   ┌────▼─────┐  ┌──▼──────────────────────┐
              │ 部务会议 group:grp_*    │   │ 朝会/例会 │  │ 六部（interactive        │
              │ 群发 = 逐成员各发一份     │   │ 定时触发  │  │ SubAgent，长寿命会话）    │
              │ 成员回发起者 = 单发       │   └────┬─────┘  │ 工部/户部/刑部/礼部/...   │
              └────────────┬───────────┘        │        └──┬────┬────┬────┬──────┘
                           │                    │           │    │    │    │
                           └────────────────────┴───────────┴────┴────┴────┘
                                                │ 读写（Read/Write/Grep）
                                ┌───────────────▼──────────────────┐
                                │ 唯一事实源（工作区文件，可 git）    │
                                │ court/state/departments/*.yaml    │
                                │ court/log/<dept>/<date>.md        │
                                │ court/meetings/<id>.md            │
                                │ court/evidence/**                 │
                                └───────────────┬──────────────────┘
                                                │ 读原始文件（不读转述）
                                ┌───────────────▼──────────────────┐
                                │ 司礼监批红                         │
                                │ ① 确定性校验脚本（规则引擎）        │
                                │ ② 汇总 agent（只做去重/格式化）     │
                                └───────────────┬──────────────────┘
                                                │ 奏折
                                ┌───────────────▼──────────────────┐
                                │ 皇帝：批红（准/驳/改）              │
                                └──────────────────────────────────┘

  消息/持久层（不在图里但支撑一切）：
  每会话一个 session DB（消息的唯一归属）
  + 主库两张群组小表 groups / group_members
  + memory（user_id 隔离） + role md + cron_jobs + hooks
```

**为什么不是 N×N**：群发只由发起者显式执行一次；成员回复默认单发给发起者，不再群发。每次群发带 `hop`，超过 `max_hops` 拒绝；单次 fanout ≤128 且有令牌桶（见 ④ §9）。

---

## 3. 角色 → 现有基础设施映射

| 朝廷角色 | 现有功能 | 代码位置 | 关键约束 |
|---|---|---|---|
| 皇帝 | 用户消息 + IM/Web/CLI session | `channel/`、`bus/` | 唯一最终裁决；不直接进工具循环 |
| 内阁首辅 | 主 Agent 常驻会话 | `agent/agent.go`、`session/multitenant.go:293` | 唯一调度者；负责汇总与上奏 |
| 六部（部门） | interactive SubAgent（role md） | `tools/subagent.go:125`、`tools/subagent_roles.go:12`、`agent/interactive.go:970` | 长寿命、独立工具白名单、独立记忆 |
| 部门内司/局 | 嵌套 SubAgent | `agent/engine.go:1026`、`agent/interactive.go:2719` | `spawn_agent` capability 控制 |
| 部务会议 | `CreateChat(type=group)` + `SendMessage(to=group:)` | `tools/create_chat.go:164-259`、`tools/send_message.go:240-353` | 群发 = 遍历成员各发一份；成员回发起者用单发；群历史跨库聚合 |
| 公文传递 | `SendMessage(to=agent:/session:/group:)` | `tools/send_message.go:76-121` | 投递即返回（写各自会话库），回复走异步通知 |
| 朝会/例会 | cron job → bg notification pipeline | `tools/cron.go:127`、`cron/scheduler.go:205`、`agent/agent.go:2860-2873` | job 归属主会话；持久化在 `cron_jobs` 表 |
| 工作日志/奏折 | 普通文件工具 | `tools/read.go`、Write/Grep 工具 | 可 git 版本化、可人审 |
| 司礼监批红（规则） | skill 脚本 / plugin 工具 | skill 目录、`plugin/plugin.go:162-171` | **确定性**，不调 LLM |
| 司礼监批红（格式） | 汇总 SubAgent | `tools/subagent.go`（role=`sili`） | 只做去重/校验证据/排版 |
| 组织编制 | `court/org.yaml` + role md | 工作区文件、`tools/subagent_loader.go:14` | 数据驱动，改编制不改代码 |
| 档案库 | session DB（每会话一个库） | `session/sessiondb.go:3-20`、`session/tenant.go:259` | 只有 `DestroySession` 才删库 |
| 长期记忆 | memory provider（xbot/letta） | `memory/xbot/xbot.go:105-156`、`agent/engine_wire.go:1340` | 按 user_id + tenant 隔离 |
| 起居注/审计 | hooks：SubAgentStart / SubAgentStop | `agent/hooks/event.go:53-54`、`agent/engine_wire.go:1677,1805` | 父侧发事件；子侧无 hook |
| 令牌/权限 | role capabilities（memory/send_message/spawn_agent） | `tools/subagent_roles.go:22-27` | 只有三个开关 |
| 定时/常驻任务扩展 | plugin `CronScheduler` | `plugin/context.go:99-102`、`plugin/integration.go:295-336` | 插件可贡献 cron，但无托管后台循环 |

**结论**：除了「唤醒 / 休眠 / 群组生命周期 / 状态文件校验」这四个通用原语，其它全部已有。

---

## 4. 现状调研（事实 + 缺陷）

### 4.1 子代理生命周期

**注册表**：纯内存 `sync.Map`，key = `channel:chatID/roleName[:instance]`（`agent/agent.go:560-563`、`agent/interactive.go:664-670`）。全仓只有两处 `Store`：fg spawn 完成（`agent/interactive.go:1759`）和 one-shot（`agent/engine_wire.go:1622`）。**没有任何从 DB 重建的代码**。

**spawn 全链路**（`agent/interactive.go:970-1767`）：

1. `982` 生成 key；`986` 惰性 TTL 清理；`989-1004` `LoadOrStore` 占位符。
2. `1070` 构建 cfg；`1105` `GetOrCreateSession("agent", key)`（DB 侧有会话）。
3. **`1130` 无条件 `Clear()`**：清空该 tenant 的全部消息（`session/tenant.go:291-297`）。
4. `1191-1203` 先落 user 消息；`1227-1641` bg Run / `1644-1767` fg Run。
5. 完成：fg 成功保留（`1759`），失败 destroy（`1683`）；bg 自然完成保留（`1516-1626`）。

**销毁漏斗**：`destroyInteractiveSession`（`agent/interactive.go:557-617`）是唯一出口，做六件事：

- 发 `subagent_stopped(removed=true)`（`567`）
- 关 bgTask（`572-585`）
- 从 group 移除成员（`588-593`，**读 `ia.groupID` 未加锁**）
- 删内存进度/迭代历史（`598-600`）
- 删 offload（`604-607`）
- **`multiSession.DestroySession("agent", key)`**（`612-616`）→ 删会话库文件 + `tenants` 行（`session/multitenant.go:795-838`）

**TTL 清理**：

- 常量 `interactiveSessionTTL = 30 * time.Minute`（`agent/interactive.go:156`）。
- `cleanupExpiredSessions`（`162-196`）：跳过 `running`（`178-180`），其余 `now-lastUsed > 30m` → `destroy`（`182-192`）。
- **只在两个入口惰性触发**：spawn（`986`）、`ListInteractiveSessions`（`2958`）。**没有后台 ticker**。
- `CleanupInteractiveSessions`（`2904-2920`）与 `GetActiveInteractiveRoles`（`2884`）**全仓零调用者**（死代码）。

**关键缺陷（按严重度）**：

| # | 缺陷 | 证据 |
|---|---|---|
| L1 | 每次同 key spawn 都 `Clear()`，重启后历史被静默抹除 | `agent/interactive.go:1130`（注释 `1127-1129` 自陈「server restart 时 DB 保留旧 tenant 数据」）；`session/tenant.go:291-297` |
| L2 | bg 自然完成**不刷新 `lastUsed`**，长任务刚跑完就满足 30min TTL，下一次 list/spawn 立即删库 | 赋值点仅 `989/891/1714/1961`；完成段落 `1517-1605` 无赋值 |
| L3 | TTL 只惰性触发：无人 list 时永久残留；list 一次可能批量删库 | `986`、`2958`；`182-192` |
| L4 | 重启后无法恢复：`Continue`/`dump`/`messages` 全只查内存 map | `1773-1799`、`3149-3178`、`3182-3197` |
| L5 | one-shot 注释说「保留供 TTL 清理」，实际立即 destroy | `agent/engine_wire.go:1612-1613` vs `1794` |
| L6 | 占位符窗口 `cfg==nil`，send 报「still initializing」，Unload 只删 map 不删 DB | `989-1004`、`1897-1904`、`2785-2789` |
| L7 | fg Run 期间迭代历史不可见（`OnIterationSnapshot` 只在 bg 设置） | `1270-1303` vs `1644-1767` |
| L8 | 父会话清理无入口；级联只覆盖 bg 父子链 | `2904`（零调用）、`2719-2757` |

**销毁的连带后果**：`DestroySession` 删 `tenants` 行；`core_memory_blocks` / `archival_memory` / `tenant_state` 都有 `ON DELETE CASCADE`（`storage/sqlite/schema.go:76-127`、`storage/sqlite/migrations.go:1810-1819`）→ **子代理的 letta memory 也一起被删**。这是「持久化让路」必须改 TTL 的根本原因。

### 4.2 子代理状态管理

`interactiveAgent`（`agent/interactive.go:44-75`）把四类状态混在一个结构体：

| 类别 | 字段 | 能否从 DB 重建 |
|---|---|---|
| durable（真消息） | `messages`（48） | 能（`TenantSession.GetMessages`），但 spawn 会先 `Clear` |
| 迭代快照 | `iterationHistory`（49） | 只能从最后一条 assistant 的 `Detail` JSON 还原（`1583-1587`/`1739-1743`），无读取代码 |
| runtime | `running`、`cancelCurrent`、`interrupted`、`pendingMessages`、`bgTask` | 不能（也不需要） |
| config | `cfg`、`systemPrompt`、`userCtx`、`subID` | 能用 role 定义 + `buildSubAgentRunConfig` 重建，但需记录 spawn 时的 model/subID |
| presentation | `lastReply`、`lastError`、`promptTokens`、`completionTokens` | 不能（可从消息重算部分） |
| 组织关系 | `groupID`、`parentKey`、`task`、`instance` | 不能（无持久化） |

其它问题：

- 会话内消息真相在 DB，但「谁是活跃会话」全在内存；两者没有任何重建桥梁。
- `lastUsed` 纯内存且 bg 完成不更新（L2）。
- 占位符不是状态机：`cfg` 为 nil 时读方各自写错误文案（`1897-1904`、`1803-1807`）。
- 锁纪律靠注释维持：不得持 `ia.mu` 调 `Run()`（`2030-2032`），否则嵌套 spawn 的 `cleanupExpiredSessions` 取 `ia.mu` 死锁。
- TOCTOU：`bgTaskMgr` 可被替换（`575-577`、`2796-2798`）；resolve → Load 之间会话可能被 TTL/unload 摘除（`1869-1877`）。
- `GetSessionMessages` / `GetAgentSessionDump` 只读内存 → 重启后 UI 也读不到（`3149`、`3182`）。
- fg 分支没有 `OnIterationSnapshot`（L7）。

### 4.3 群组（旧的两套模型，v2 删除）

v1 基线里存在两套互不相通的群组模型：

| | `GroupMembership`（`group:`） | `PeerGroup`（`peer:`） |
|---|---|---|
| 定义 | `tools/group_state.go:19-26` | `tools/group_state.go:149-152` |
| 存储 | 进程内 `sync.Map`（`:29`） | `$XBOT_HOME/peer_groups.json`（`:162-215`） |
| 持久化 | **无** | 有（100ms 防抖，`:265-279`） |
| 成员 | `agent:role/instance` | `channel:chatID` |
| 发送方校验 | **无**（`tools/send_message.go:240-258`） | 有（`:390-394`） |
| 消息历史 | **无**（`tools/group_state.go:12-18` 自陈） | 无 |

**v2 处置：两套都删**（决策 D），统一为「群发 / 单发 + 两张小表」（见 ④）。这里保留表格仅作审计对照，不代表要保留实现。

**旧群组缺陷清单（修复要求保留）**：

| # | 缺陷 | 证据 |
|---|---|---|
| G1 | 无讨论历史/无「谁说过什么」；成员互相看不到彼此发言 | `group_state.go:19-26`、`send_message.go:240-353` |
| G2 | 「主持人串行」只存在于描述；实现是并发 goroutine | 描述 `create_chat.go:29-43`；实现 `send_message.go:280-297,327-341` |
| G3 | `max_rounds` 是空参数，`Closed` 是死字段 | `create_chat.go:57,69`（无读取点）、`group_state.go:61-65` |
| G4 | 无持久化，重启即失忆 | `group_state.go:29`；`send_message.go:241-244` |
| G5 | ID `g<N>` 进程内自增，重启复用；`CreateGroup` 无条件覆盖 | `create_chat.go:14,170`；`group_state.go:32-40` |
| G6 | 无租户/成员校验，知道 `group:g1` 就能群发 | `group_state.go:29`；`send_message.go:240-258` |
| G7 | 广播是 N 个并发 full-turn RPC；成员 prompt 鼓励再广播 → N×N 放大器 | `send_message.go:280-297`；`agent/engine_wire.go:735-739` |
| G8 | 广播等全部成员完成（30s），与 `sendToAgent` 的 2s 即返回策略矛盾 | `send_message.go:265-268,297`；`tools/limits.go:61,65` |
| G9 | @ 解析过窄，`@agent:qa`（无 instance）被丢弃 → 变全员广播；前缀不带发送者 | `send_message.go:431-447`、`:292,336` |
| G10 | 成员 roster 是创建时快照；spawn 失败仍入册；`isInGroup` 用快照 | `create_chat.go:189-242`；`send_message.go:223-230` |
| G11 | peer 组自动 ID 只有 15×15=225 种，撞名即共享 | `tools/peer_group.go:196-200`；`group_state.go:228-231` |
| G12 | peer 组单文件全局、无 GC、`ListGroupMembers` 不校验成员、`FlushPeerGroups` 零调用 | `group_state.go:162-168,283,296-327,360-386` |
| G13 | 文档与代码漂移（history/round/sequential 均不存在） | `docs/agent/tools.md:98,107-112` |

### 4.4 会话存储 / 跨重启

- 每会话一个 SQLite（`sessions/<channel>/<bucket>/<name>.db`）：`session/sessiondb.go:3-20`、`storage/sqlite/sessiondb.go:201-216`。
- `tenants` 注册表在主库（`storage/sqlite/schema.go:17-38`），消息在会话库（`:40-74`），迭代历史在会话库（`:314-333`）。
- 主库还存：`cron_jobs`（`:252-267`）、`pending_resumes`（`:300-307`）、memory 表（`:84-135`）。
- 会话缓存 24h 空闲驱逐：`session/multitenant.go:708-757`、默认值 `agent/agent.go:2227-2228` / `config/config.go:1156-1157`。驱逐只关库，**不删数据**（`evictSessionDB`，`multitenant.go:776-785`）。
- 主 Agent 的重启恢复已有先例：`pending_resumes` 收集（`serverapp/server.go:1027-1070`）→ 启动后重放（`1075-1128`）。**子代理没有对应机制**。
- Web 侧栏的 agent 会话列表**从 DB 读**（`serverapp/callbacks.go:637-661`，`listTenantsByChannel("agent")`），所以重启后「历史可见」，但「不可继续」（`ContinueInteractiveSession` 只查内存）。

### 4.5 memory 能力与边界

- 主库表：`xbot_short_term_memories` + FTS5（`memory/xbot/xbot.go:70-89`）、`xbot_long_term_memories` + FTS5（`:105-133`，`scope=global|session`，`:122`）。
- 隔离轴是 **user_id**，不是 tenant_id（`memory/xbot/xbot.go:239-261`，注释 `:153-156`）。
- 注入路径：主 Agent 走 `MemoryMiddleware`，贴在 user 消息后的 `<system-reminder>`（`agent/middleware_builtin.go:541-566`）；SubAgent 手动 `Recall` 拼进 system prompt（`agent/engine_wire.go:977-991`）。
- SubAgent tenant：`deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName)`（`agent/subagent_tenant.go:20-33`）→ **不含 instance**；同一角色的多个实例共享 tenant；且依赖父会话 tenant → **同一部门在不同主会话中记忆不连续**。
- `consolidateSubAgentMemory` 构造的 `MemorizeInput` 没有 `ArchiveAll`，三家 provider 在 `!ArchiveAll` 时直接返回 → **退出时整合是 no-op**（`agent/engine_wire.go:1472-1476`、`memory/xbot/xbot.go:800-802`、`memory/letta/letta.go:153-155`、`memory/flat/flat.go:128-130`）。
- SubAgent 的 `cfg.ChatID` 是父会话 chatID（`agent/engine_wire.go:807`）→ session-scope 记忆与父会话串。

**对朝廷的结论**：memory 适合沉淀「部门经验/事实」，**不能作为任务进度的唯一事实源**（无 schema、无单调性、无证据路径）。进度必须落文件。

### 4.6 cron / hooks / plugins

**cron**：

- job 持久化在主库 `cron_jobs`（`storage/sqlite/schema.go:252-267`），字段含 channel/chat_id/sender/message/next_run（`:255-267`）。
- 调度循环 `checkAndFire`（`cron/scheduler.go:205-248`）→ `notifyCronFn`（`:244-245`）。
- 注入统一走 bg notification pipeline（`agent/agent.go:2860-2873`）；目标会话空闲时直接作为 user message 处理，**不需要会话先存在**（`agent/agent.go:4984-4997` 注释 + `5081-5105`）。
- job 是**会话级隔离**的：`listJobs`/`removeJob` 按 channel+chatID（`tools/cron.go:170-180,207-220`）；SubAgent 创建的 job 归属父会话。
- 结论：**朝会天然可用**，且进程重启后 job 仍会触发——这是比子代理 TTL 更可靠的「唤醒源」。

**hooks**：

- 14 个事件（`agent/hooks/event.go:45-60`），含 `SubAgentStart`/`SubAgentStop`（`:53-54`）。
- 执行器：command / http / mcp_tool / callback / plugin bridge（`agent/hooks/types.go:100`、`executor_command.go:32`、`executor_http.go:54`、`executor_mcp.go:27`、`plugin_bridge.go:17`）。
- 三层配置：`~/.xbot/hooks.json` → `<proj>/.xbot/hooks.json` → `hooks.local.json`（`agent/hooks/config.go:27-57`），启动期载入，改配置要重启。
- command hook 默认关闭，需 `enable_command_hooks: true`（`agent/hooks/config.go:18-20`、`manager.go:226`）。
- 局限：`SubAgentStart/Stop` 由**父** hookManager 发出且**返回值被丢弃**（`agent/engine_wire.go:1677-1686,1805-1814`）；SubAgent 内部 `cfg.HookManager=nil`（`engine_wire.go:1019`）→ 子代理工具循环没有 hook。
- 结论：hooks 适合做**审计/记账**（append-only 起居注），不适合做实时纪律阻断。

**plugins**：

- `PluginContributes` 支持 tools / hooks / context_enrichers / commands / **crons** / themes / UI / configuration（`plugin/plugin.go:162-171`）。
- `PluginContext` 聚合 `CronScheduler`（`plugin/context.go:99-102`）、`HookSubscriber`、`StorageProvider`（`~/.xbot/plugins/<id>/storage.json`）、`RegisterCommand`、Config 热更新（`:108-154`）。
- 插件 cron 由 `WirePluginCrons` 写入 sqlite cron 表并随启动重放（`plugin/integration.go:295-336`）。
- 结论：如果批红校验想「可安装/可替换」，做成 plugin 是正解；如果只想零部署，做成 skill 脚本 + hooks command 更轻。

**roles**：

- role 是磁盘 md + frontmatter，不是 DB：`tools/subagent_roles.go:12-20`、`tools/subagent_loader.go:14-33`。
- 加载优先级 embed → 全局 → 用户 → 项目（`agent/agents.go:50-78`），每次目录重扫（`:40,95-98`）。
- 无 role 级持久状态字段；`capabilities` 只有 memory/send_message/spawn_agent（`subagent_roles.go:22-27`）。
- 结论：**编制本身就是数据**；部门清单不需要新代码。

---

## 5. 朝廷运行模型（怎么组合）

### 5.1 组织编制：`court/org.yaml`

```yaml
# court/org.yaml —— 组织结构（数据）
schema: court/v1
court:
  name: "xbot 朝廷"
  emperor: "user:main"                 # 用户
  chancellor: "agent:neige/main"       # 首辅（主 agent 的固定角色声明，不是新进程）
departments:
  - id: gongbu
    role: gongbu                       # 对应 .xbot/agents/gongbu.md
    instance: main
    objective: "PPU 集群部署与运维"
    reports_to: chancellor
    meeting: daily                     # 朝会参与频率
    state: court/state/departments/gongbu.yaml
  - id: hubu
    role: hubu
    instance: main
    objective: "资源、成本、配额"
    reports_to: chancellor
    meeting: weekly
meetings:
  morning:
    cron: "0 9 * * *"                  # 每日 09:00
    attendees: [gongbu, hubu, xingbu]
    agenda_template: court/templates/morning.md
red_pen:                               # 批红规则（脚本读取，不是代码分支）
  validator: court/bin/validate_state.py
  rules:
    - schema_required
    - evidence_exists
    - progress_monotonic
    - freshness
    - blocked_has_next_action
```

**注意**：`org.yaml` 只被 **prompt / skill 脚本 / 首辅** 读取；core 不解析它。这样换编制不改 Go 代码。

### 5.2 部门（SubAgent）工作协议

部门是被 `CreateChat(type=agent)` 或 `SubAgent(interactive=true)` 拉起的长期会话。它的 system prompt（role md）必须规定：

1. **只改自己的状态文件**：`court/state/departments/<id>.yaml`。
2. 每次工作结束前：更新 `updated_at`、`progress`、`evidence`、`next_action`，并向 `court/log/<id>/<date>.md` 追加一条日志。
3. **进度单调**：只能增；回退必须把任务置 `blocked` 并写原因。
4. 证据必须是**可验证路径**（日志/文件/命令输出），不能只写结论。
5. 收到会议 `@` 时，只回主持人，不向其他部门广播。
6. 不直接向用户汇报（除非首辅明确授权）。

### 5.3 部门会议（群发 + 单发）

- 首辅 `CreateChat(type=group, members=[agent:gongbu/main, agent:hubu/main, ...])` → 主库写入 `groups` + `group_members`，返回 `group:grp_<nanoid>`。
- **通知开会 = 一次群发**：`SendMessage(to="group:grp_...", kind=text, message="...")`。平台遍历 `status='alive'` 的成员，**每人各得一份独立副本**（各写各的会话库）。
- **成员发言 = 单发回发起者**：`SendMessage(to="agent:shoufu/main", reply_to=<群发消息 id>)`。prompt 明确「会议中默认只回发起者，不群发」。
- **需要全员知晓的结论 = 发起者再群发一次**（显式动作，受 hop/fanout/令牌桶限制）。
- 会议纪要由首辅通过 `GroupHistory(group_id)` 聚合查询（只读视图，跨成员会话库）后写入 `court/meetings/<date>-<topic>.md`。平台不建群级消息表 表。
- 群历史是「读视图」而不是共享上下文：成员不会自动看到别人的回答；首辅在纪要里汇总后，通过下一次群发把结论发回全员。

### 5.4 朝会（cron + 汇总）

- 首辅创建 cron job：message 是一段**朝会指令**（读所有 state + 会议纪要 + 运行校验脚本 + 生成奏折）。
- cron 触发 → bg notification → 首辅会话（空闲则作为 user message 处理，`agent/agent.go:4984-4997`）。
- 首辅的朝会流程（prompt 约定）：
  1. 列出 `court/state/departments/*.yaml`，检查 `updated_at`。
  2. 运行 `court/bin/validate_state.py --all`，拿机器可读 verdict。
  3. 对 `stale`/`violations` 的部门，`SendMessage(agent:<dept>)` 追问（一次一个）。
  4. 生成 `court/reports/<date>.md`（奏折），只包含：结论、证据路径、风险、请批事项。
  5. 把奏折发送给用户。
- **cron 是唤醒源**：即使子代理 TTL 把会话销毁，朝会也能通过 `@` 重新拉起部门（Phase 1 的兜底）。

### 5.5 司礼监批红（确定性规则 + 机械汇总）

**两层分离**：

| 层 | 谁做 | 允许做什么 | 禁止做什么 |
|---|---|---|---|
| 规则层 | `validate_state.py`（skill/plugin 脚本） | schema 校验、字段枚举、证据路径存在性、进度单调、时间新鲜度、阻塞项完整性、重复 task id | 调 LLM、做业务判断 |
| 汇总层 | `sili` SubAgent | 去重、把 violations 排版成奏折、附证据链接、标注「需人工批红」 | 修改原文事实、替首辅下结论 |

**校验脚本输出（machine-readable）**：

```json
{
  "ok": false,
  "violations": [
    {"dept": "gongbu", "task": "ppu-deploy-03", "rule": "evidence_exists",
     "detail": "court/evidence/ppu-deploy-03.log not found"},
    {"dept": "gongbu", "task": "ppu-deploy-04", "rule": "progress_monotonic",
     "detail": "progress 70 -> 55 without blocked status"}
  ],
  "stale": [{"dept": "hubu", "updated_at": "2026-10-17T09:00:00Z"}]
}
```

**上级校验原始文件，不读转述**：

- 首辅在朝会中直接 `Read` 每个 state 文件与日志尾部，计算 `sha256`/`git diff`（普通工具）。
- 汇总 agent 的输出必须带 `evidence.path`；首辅抽样 `Read` 验证路径存在。
- 规则：**任何没有证据路径的进度，视为 0**。

### 5.6 唯一事实源协议（共享状态文件）

**目录**：

```
court/
  org.yaml                         # 编制
  state/departments/<dept>.yaml    # 每部门一个，唯一事实源
  log/<dept>/<YYYY-MM-DD>.md       # append-only 工作日志
  meetings/<YYYY-MM-DD>-<topic>.md # 会议纪要
  reports/<YYYY-MM-DD>.md          # 奏折
  evidence/**                      # 证据（日志、截图、命令输出、导出）
  bin/validate_state.py            # 确定性校验
  templates/morning.md             # 朝会议程模板
```

**state schema（v1）**：

```yaml
schema: court/v1
department: gongbu
owner_agent: "agent:gongbu/main"
updated_at: "2026-10-19T10:00:00Z"   # RFC3339，必须每次写入刷新
phase: executing                      # planning|executing|blocked|done
progress: 62                          # 0-100，单调不减
objective: "PPU 集群部署"
tasks:
  - id: ppu-deploy-03
    title: "部署 prefill 节点"
    status: doing                     # todo|doing|blocked|done
    progress: 40
    owner: "agent:gongbu/node-1"
    updated_at: "2026-10-19T09:58:00Z"
    evidence:
      - path: "court/evidence/ppu-deploy-03.log"
        kind: log                     # log|file|command|url
        sha256: "…"                   # 可选，写入时计算
    blocked_by: []                    # 阻塞项 id 列表
    next_action: "等待 3 个节点就绪后执行 smoke test"
    deadline: "2026-10-20T18:00:00Z"
risks:
  - id: r1
    desc: "spot 实例可能被回收"
    mitigation: "预留 on-demand 节点"
handoffs: []                          # 交接记录
```

**写纪律**：

1. 只有 `owner_agent` 能写自己的 state 文件；首辅只读。
2. 写入必须原子：临时文件 + `os.replace`（脚本或 Write 工具保证）。
3. 每次写入追加一条 log（时间、变更摘要、证据路径）。
4. `progress` 只能增；回退必须 `phase: blocked` 且 `blocked_by` 非空。
5. `evidence.path` 必须存在且非空；`sha256` 可选但推荐。
6. 同一 task id 在一个部门内唯一。
7. `updated_at` 超过 N 小时（默认 24h）视为 stale。

**为什么用文件而不是新 DB 表**：

- 所有 agent 已有 Read/Write/Grep，零新工具。
- 跨 session/跨进程/跨重启天然持久。
- 可 git diff、可人审、可回滚。
- 不需要在主库新增任何**消息**表；唯一的主库改动是两张群组小表（`groups`/`group_members`，④ §4）。

---

## 6. 需要改动清单

> 原则：以下改动全部是**通用原语**，不含部门名/流程名。每项给出「现状行为 → 改动理由 → 方案 → 风险 → 迁移」。

### 6.1 ① 子代理过期清理逻辑（最高风险，先设计后动代码）

#### 现状行为（精确）

| 项 | 行为 | 证据 |
|---|---|---|
| TTL | 30 分钟，硬编码常量 | `agent/interactive.go:156` |
| 触发 | 仅在 spawn / ListInteractiveSessions 惰性调用 | `:986`、`:2958` |
| 判定 | `now - lastUsed > 30m` 且 `!running` | `:178-192` |
| 动作 | `destroyInteractiveSession` → 删 map / 进度 / offload / **会话库文件 + tenants 行** | `:557-617`、`session/multitenant.go:795-838` |
| 连带 | `core_memory_blocks`/`archival_memory`/`tenant_state` 随 `tenants` CASCADE 删除 | `storage/sqlite/schema.go:76-127`、`migrations.go:1810-1819` |
| lastUsed | bg 自然完成不更新 | 赋值点 `989/891/1714/1961`，完成段 `1517-1605` 无赋值 |
| 父会话清理 | 死代码（零调用） | `:2904-2920` |

#### 改动理由

1. 持久化要求「长时间后可唤醒」，而 30min TTL 会**物理删除**会话与记忆。
2. 当前清理是「内存独占判定」：只要不在 map 里就等于可删，但 DB 里可能还有有用历史。
3. L2 让「刚跑完的长任务」立即被删，行为非线性且难解释。
4. 清理与唤醒必须是一对：有 hibernate 才允许删除内存态。

#### 方案：TTL 策略化（配置驱动，默认行为不变）

```yaml
# config.json → agent.subagent
subagent:
  session_ttl: 30m          # 内存态休眠阈值
  ttl_policy: destroy       # destroy | hibernate | retain；过渡期默认 destroy
  destroy_after: 720h       # 仅 hibernate/retain 下生效：30 天后才真删
  sweep_interval: 5m        # housekeeping 周期；0 = 仅惰性
  refresh_last_used_on_finish: true
```

三种策略：

| 策略 | 内存 | DB | 记忆 | 用途 |
|---|---|---|---|---|
| `destroy` | 删 | 删 | 删（CASCADE） | 现状；临时/一次性 agent |
| `hibernate` | 删 | **保留** | 保留 | 默认目标：部门 |
| `retain` | 删（可选） | 保留 | 保留 | 调试/审计；靠 `destroy_after` 兜底 |

**`hibernate` 的具体动作**（新函数，替换 `cleanupExpiredSessions` 里的 destroy 分支）：

1. 从 `interactiveSubAgents` 删除（同现状）。
2. 清 `lastProgressSnapshot` / `iterationHistories`（同现状）。
3. **不** `DestroySession`；改为 `multiSession` 的缓存驱逐（`evictSessionDB`，`session/multitenant.go:776-785`）——只关库，保留文件与 tenant 行。
4. 记录 `hibernated_at` / `finished_at` 到 sidecar：`~/.xbot/agents-runtime/<hash>.json`（或复用 `tenant_state`，见 6.4）。
5. 发 `subagent_stopped(removed=false, hibernated=true)`（新增字段，前端可显示「休眠」而不是消失）。
6. 不删 offload（offload 与 root session 共享，`docs/agent/agent-core` 有纪律）；由 `destroy_after` 或 `CleanSession` 兜底。

**`finished_at` 修正**：bg 自然完成（`agent/interactive.go:1517` 附近）写 `finishedAt=now`；TTL 判定用 `finishedAt`（无则 `lastUsed`）。这修复 L2。

**housekeeping**：不新起 goroutine，挂到现有 `MultiTenantSession.StartCleanupRoutine`（5min ticker，`session/multitenant.go:669-690`）的同一节奏，或 `Agent.periodicCleanup`（6h，`agent/agent.go:5202-5215`）。推荐后者：TTL 不要求分钟级精度，复用 6h ticker 最省资源；同时在 spawn/list 保留惰性清理作为快速路径。

#### 迁移策略（三阶段，禁止一步到位）

1. **M0（只加不改）**：
   - 增加配置读取与 `finished_at` 写入。
   - `ttl_policy` 默认 `destroy`，行为与现在**完全一致**。
   - 加指标：活跃子代理数、DB 文件数、磁盘占用、因 TTL 销毁次数。
   - 验收：现有测试全绿；生产观察 1 周。
2. **M1（默认切 hibernate）**：
   - `cleanupExpiredSessions` 按策略分支；`destroy` 保留为回滚开关。
   - `destroy_after` 清理长期休眠会话（复用同一 reaper，先 consolidation 再删）。
   - 验收：30min 后侧栏消失、DB 仍在；`destroy_after` 到点后文件消失。
3. **M2（收口）**：
   - 删除/接线死代码 `CleanupInteractiveSessions`（`agent/interactive.go:2904`）到父会话删除入口。
   - 若某类 agent（如 one-shot）仍要立即删，用 `ttl_policy=destroy` 的角色标记或调用点显式 destroy。

#### 风险与缓解

| 风险 | 表现 | 缓解 |
|---|---|---|
| 资源泄漏 | 休眠会话 DB/offload 无界增长 | `destroy_after` + 指标告警 + 定期 `du` 巡检 |
| 僵尸代理 | `running=true` 卡死永不回收 | 现有 `running` 跳过逻辑保留；加 `running_since` 超时（如 2h）强制 hibernate，并记 `lastError` |
| 清理/唤醒竞态 | send 成功解析后会话被摘除 | 复用 `sessionOperationGate`（`agent/agent.go:945`）；hibernate 与 wake 都拿同一 gate |
| 记忆误删 | `DestroySession` 级联删 core/archival | `hibernate` 路径禁止调用 `DestroySession`；`destroy_after` 前先 `consolidateSubAgentMemory`（并修 6.4 的 `ArchiveAll`） |
| 侧栏语义变化 | 用户以为 agent 消失 | `subagent_stopped` 增加 `hibernated` 字段；前端显示「休眠」 |
| 旧数据不兼容 | 旧 DB 没有 `finished_at` | 回退到 `lastUsed`；sidecar 缺失视为 `lastUsed` |

#### 回滚

- 配置切回 `ttl_policy: destroy`，重启即恢复现状。
- hibernate 期间未删任何数据，回滚无数据损失。
- `destroy_after` 是唯一不可逆动作，默认 30 天，且只在 M1 后启用。

### 6.2 ② 子代理状态管理

#### 现状问题

见 §4.2。根因是**没有「状态机 + 单一真相」**：内存结构体同时承担 durable/runtime/config/presentation，且消息真相在 DB 却没有重建入口。

#### 方案

1. **状态机**（显式枚举，不用散落 bool）：
   ```
   initializing → running → idle → hibernated → destroyed
                    ↑         │
                    └─ send ──┘
   ```
   - 所有转换在一个 mutex 内完成；非法转换返回明确错误。
   - `send` 在 `initializing` 时返回 `retry_later`（现状文案保留），在 `hibernated` 时先 wake。
2. **状态分层**（不要求一次重构完，先加注释与访问器）：
   - `DurableSession`：`TenantSession`（唯一消息真相）+ `role/instance/groupID/parentKey/task/model/subID/userCtx`（spawn spec，落 sidecar）。
   - `Runtime`：`running/cancelCurrent/pendingMessages/bgTask/hook closure`。
   - `View`：`lastReply/lastError/tokens/iterationHistory`（可从 DB + Detail 重算）。
3. **`WakeInteractiveSession(fullKey)`**（新增通用原语）：
   ```
   if map hit → return
   tenant, err := GetOrCreateSession("agent", key)
   msgs, err := tenant.GetMessages()
   if len(msgs)==0 → return not_found
   role := loadRole(roleName)                  // 磁盘 md，热更新
   cfg := buildSubAgentRunConfig(...)          // 用 sidecar 里的 model/subID/userCtx 覆盖默认
   ia := &interactiveAgent{state: idle, messages: msgs, ...}
   iterationHistory := decodeDetail(msgs)      // 从最后 assistant.Detail 还原
   map.Store(key, ia)
   ```
   - 入口：`SendToInteractiveSession` map miss 时先尝试 wake（这样 `SendMessage(agent:dept)` / `SubAgent(action=send)` 自动恢复）。
   - 显式 `SubAgent(action=wake)` 可选，但**不需要**：send 语义天然是「唤醒并投递」。
4. **spawn 的 `Clear()` 语义拆分**：
   - `mode: new`（默认，保持现状）：确认 key 无活跃会话后 `Clear()`，全新开始。
   - `mode: resume`：不 `Clear`；若 DB 有消息则加载并追加新任务。
   - `mode: reset`：显式 `Clear`（管理员动作）。
   - `SubAgent`/`CreateChat` 的默认仍为 `new`，避免行为突变；朝廷部门在 `org.yaml`/prompt 里用 `resume`。
5. **fg 分支补 `OnIterationSnapshot`**：与 bg 对齐（`agent/interactive.go:1270-1303` 的接线搬到 fg 分支）。
6. **持久化 spawn spec**：`~/.xbot/agent-specs/<sha256(key)>.json`（role/instance/model/subID/userCtx/groupID/parentKey/task/created_at/updated_at）。不新增 DB 表，不碰 schema；文件损坏时降级为 role 定义默认值并记 warning。
7. **锁纪律代码化**：把「不得持 `ia.mu` 调 Run()」写成 `runtime` 子结构 + 注释 + 单测断言（现有 `agent/interactive.go:2030-2032`）。

#### 风险

- 唤醒时 model/subID 可能已失效 → sidecar 记录失败则回退到 role 默认 model，并在 session 里留一条系统提示。
- role 定义在休眠期间被改名/删除 → wake 返回 `role_not_found`，提示首辅重新编制。
- `iterationHistory` 从 `Detail` 还原可能不完整（历史消息 Detail 被压缩）→ 允许为空，不影响消息真相。

### 6.3 ③ 群组（群发 / 单发）

#### 现状问题

见 §4.3（G1–G13）。核心是**两套模型语义重叠、group 无持久化无历史、广播无刹车**。

#### 方案（v2：只有一个模型）

**A. 通用修复（进 core，但仍是通用能力）**：

| 修复 | 做法 | 落点 |
|---|---|---|
| 模型唯一 | 删除 `GroupMembership` 与 `PeerGroup`；只有「群发 = 遍历成员各发一份」与「单发」 | `tools/group_state.go`（整文件）、`tools/send_message.go:380-428` |
| 持久化 | 主库两张小表：`groups(id,name,owner,created_at)` + `group_members(group_id,member_addr,status,joined_at)` | `storage/sqlite/migrations.go`（v74） |
| ID 唯一 | `g<N>` → `grp_<nanoid(12)>`；`CreateGroup` 冲突报 `ID_TAKEN` | `tools/create_chat.go:170`、新 `tools/group_ids.go` |
| 发送方校验 | 群发前查 `group_members(status='alive')`；否则 `MEMBERSHIP_REQUIRED` | `tools/send_message.go:240-258` |
| 发送者标识 | 每份副本带 `sender_*` + `group_id` + `message_id`；不再用 `[group:x]` 字符串前缀 | `send_message.go:292,336` |
| 广播刹车 | 信封 `hop/max_hops`；fanout ≤128；群/发送者令牌桶；成员 prompt 默认「回复单发」 | `send_message.go:280-297`、`agent/engine_wire.go:736-740` |
| @ 解析容错 | `@agent:role`（无 instance）按 role 唯一匹配；歧义报 `AMBIGUOUS_MENTION` | `send_message.go:431-447` |
| 删轮次 | `max_rounds`/`rounds_used`/`Closed`/`group_rounds` 全部从 schema 与代码删除 | `create_chat.go:57,69`、`group_state.go:61-65` |
| 成员状态 | 只有 `alive/left`；`left` 不再投递；leave 广播 `event(member.left)` | 新 `tools/group_store.go` |
| 群历史 | `GroupHistory(group_id)` 跨会话库聚合（只读，按 `message_id` 合并） | 新 `tools/group_history.go` |

**B. 朝廷用法（不进 core）**：

- **开会通知 = 一次群发**；成员回发起者 = 单发；结论回全员 = 发起者再群发一次。
- 会议纪要 = 首辅用 `GroupHistory` 聚合后写 `court/meetings/<date>-<topic>.md`，平台不建消息表。
- 不存在主持人/发言权/轮次：这些是 prompt 约定，core 不建模。

#### 迁移（决策 D：不迁移）

- `peer_groups.json`、内存 `group:` 群**直接删除**，不导入、不兼容；升级后旧群不存在（文档/CHANGELOG 说明）。
- 新代码只认 `group:grp_*`；`peer:` 地址返回 `NOT_FOUND`。
- 回滚：DROP 两张表即回到"无群组"状态（消息不受影响）。

### 6.4 ④ 持久化 / 唤醒

#### 现状

- 会话库：`session/sessiondb.go:3-20`，每会话一个 DB；`DestroySession` 才删（`session/multitenant.go:795-838`）。
- memory：xbot 按 user_id（`memory/xbot/xbot.go:239-261`）；SubAgent tenant 由 `(parentTenantID, parentAgentID, roleName)` 派生（`agent/subagent_tenant.go:20-33`）。
- 主 Agent 有 `pending_resumes` 重启恢复（`serverapp/server.go:1027-1128`）；子代理无。
- Web 侧栏从 DB 列出 agent 会话（`serverapp/callbacks.go:637-661`），但不能继续。

#### 方案

1. **`WakeInteractiveSession`**（见 6.2）。
2. **durable tenant 派生**：
   - 现状 `deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName)` 依赖父会话 → 部门在不同主会话中记忆不连续；且不含 instance。
   - 新增 `deriveDurableAgentTenantID(ownerUserID, role, instance)`（SHA-256，负号位 + 另一个命名空间位），用于 `durable: true` 的 role。
   - role frontmatter 增加可选 `durable: true`（`tools/subagent_loader.go` 的 frontmatter 解析器加一个字段；向后兼容，缺省 false）。
   - 迁移：durable role 首次启动时，若新 tenant 无数据且旧 tenant 有数据，复制 `core_memory_blocks` 的 persona/working_context（不复制 human，human 本来就共享）；复制后写 marker 防止重复。老数据不删。
3. **`finished_at` / `hibernated_at`**：sidecar `~/.xbot/agent-specs/<hash>.json`；不在 `xbot.db` 加表。
4. **记忆整合 no-op 修复**：`consolidateSubAgentMemory` 的 `MemorizeInput` 增加 `ArchiveAll: true`、`SessionID: key`（`agent/engine_wire.go:1472-1476`）。这会让「退出/休眠前整合」真正生效。
5. **重启后的懒唤醒**：
   - `SendMessage(agent:dept)` / `SubAgent(action=send)` → `SendToInteractiveSession` miss → `WakeInteractiveSession` → 投递。
   - 朝会 cron 作为主动唤醒源：首辅在朝会里给未更新的部门发消息。
6. **启动预热（可选，Phase 3）**：
   - 首辅启动时读 `court/org.yaml`，对 `meeting: daily` 的部门发一条「晨间点名」；不新增 core 代码，纯 prompt/工具调用。
7. **审计**：
   - hooks `SubAgentStart/Stop` → command hook 追加 `court/log/_audit/<date>.jsonl`（需 `enable_command_hooks: true`）。
   - 这是现有 hooks 能力（`agent/hooks/config.go:18-20`），不需要改 core。

#### 风险

- tenant 派生变更导致「旧记忆看不见」→ dual-read 迁移 + 不删旧数据；`org.yaml` 可回退 `durable: false`。
- wake 时 DB 锁竞争 → 复用 `sessionOperationGate` 与 `GetOrCreateSession` 的既有锁序（`session/sessiondb.go:59-61`）。
- 多进程同时 wake 同一 key → 单 operator 部署下不成立；若未来多进程，需要文件 lease（`O_EXCL`），本方案标注为 Phase 4。

### 6.5 ⑤ 共享状态文件协议

见 §5.6。补充**实施约束**：

1. **写入者唯一**：每部门 state 只由该部门写；首辅只读。避免并发写冲突。
2. **原子写**：临时文件 + rename；validator 检查 `mtime` 与 `sha256`。
3. **校验脚本位置**：`court/bin/validate_state.py`，作为 **skill 脚本**被调用（`Skill` 或 `Shell`），不进 core。
4. **规则可插拔**：`org.yaml` 的 `red_pen.rules` 是规则名列表；脚本按名注册函数。新增规则只改脚本。
5. **批红不可被 LLM 绕过**：首辅的奏折必须附 validator 的 `ok=false/true`；`ok=false` 时不得标 `done`。
6. **证据路径规范**：相对工作区根，禁止 `..` 越界；validator 解析后必须落在 `court/` 或显式允许的目录。
7. **版本**：`schema: court/v1`；validator 拒绝未知版本。

---

## 7. 分阶段实施计划

### Phase 1 — 最小闭环（1 个部门 + 1 个汇总 + 1 次每日朝会）

**目标**：不写任何 Go 代码，用现有工具跑通「部门干活 → 写状态 → 校验 → 朝会 → 奏折」。

**真实场景**：PPU 部署进度跟踪。

**步骤**：

1. 新建 role：`.xbot/agents/gongbu.md`（工部，tools: Read/Write/Grep/Shell/SubAgent，capabilities: memory/send_message/spawn_agent）。
2. 新建 role：`.xbot/agents/sili.md`（司礼监，tools: Read/Grep/Write/Shell，只做机械汇总）。
3. 新建 `court/org.yaml`、`court/state/departments/gongbu.yaml`、`court/bin/validate_state.py`、`court/templates/morning.md`。
4. 首辅用 `CreateChat(type=agent, role=gongbu, instance=main)` 拉起工部，投递部署任务。
5. 工部每次汇报前更新 state + log；首辅抽查 `Read` 原始文件。
6. 首辅创建 cron：每日 09:00 朝会（message 指向 `court/templates/morning.md`）。
7. 朝会：首辅读 state → 跑 validator → 生成 `court/reports/<date>.md` → 发用户。

**验收判据（可执行）**：

- [ ] `court/state/departments/gongbu.yaml` 通过 `validate_state.py`（`ok=true`）。
- [ ] 至少 3 个 task 有 `evidence.path` 且路径存在。
- [ ] 人为制造 3 类违规（删证据、进度回退、`updated_at` 改旧），validator 全部报出（`violations` 长度 ≥ 3）。
- [ ] cron 在重启后仍触发（`cron_jobs.next_run` 正确；朝会报告落盘）。
- [ ] 工部会话被 TTL 销毁后，次日朝会仍能通过 state 文件恢复上下文（验证「文件是事实源」）。
- [ ] 奏折中每条结论都能追到 evidence 路径（抽查 3 条）。

**Phase 1 明确不做**：不改 TTL、不改 group、不加唤醒代码。

### Phase 2 — 多部门 + 部门会议（1–2 周）

**目标**：3 个部门（工部/户部/刑部）+ group 会议 + 通用 group 修复。

**步骤**：

1. 新增 `hubu.md`、`xingbu.md`，`org.yaml` 增加编制。
2. 首辅 `CreateChat(type=group, members=[gongbu,hubu,xingbu])`。
3. 会议按 prompt 串行 `@`；首辅写 `court/meetings/<date>-<topic>.md`。
4. 修复 group 通用缺陷：ID、发送方校验、前缀带 sender、hop 限制、`max_rounds` 诚实、roster 实时校验。
5. 加入「跨部门依赖」：`state.handoffs`，validator 校验被依赖 task 存在。

**验收判据**：

- [ ] 3 个部门 state 均通过 validator。
- [ ] 一次会议产生纪要，含每个部门的「结论 + evidence 路径 + 待办」。
- [ ] 非成员向 `group:gN` 发送被拒（单测或手工）。
- [ ] 一次广播只触发 1 跳；成员不回广播（日志可见）。
- [ ] `grp_*` 重启后不冲突（新格式，无进程内计数器）。

### Phase 3 — 持久化与跨时间唤醒（2–3 周）

**目标**：进程重启/数天后，部门可被 `SendMessage` 唤醒，消息与记忆完整。

**步骤**：

1. 实现 `WakeInteractiveSession` + `SendToInteractiveSession` miss 时自动 wake。
2. 实现 TTL `hibernate`/`retain` + `finished_at` + reaper（配置默认过渡期 `destroy`）。
3. spawn `mode=new|resume|reset`；`resume` 不 `Clear`。
4. durable tenant 派生 + `durable: true` role 标记 + 旧记忆迁移（dual-read）。
5. `consolidateSubAgentMemory` 补 `ArchiveAll/SessionID`。
6. fg 分支补 `OnIterationSnapshot`。
7. 审计 hook（command hook 写 jsonl）。
8. 切换默认 `ttl_policy=hibernate`，观察 1–2 周后启用 `destroy_after=720h`。

**验收判据**：

- [ ] 重启进程后 `SendMessage(agent:gongbu/main, ...)` 成功唤醒；`GetMessages` 返回 ≥ 20 条历史。
- [ ] 唤醒后 `memory_search` 能查到部门休眠前的关键事实（durable tenant）。
- [ ] TTL 到期后：侧栏不再显示活跃、DB 文件仍在、`tenants` 行仍在。
- [ ] `destroy_after` 到期后：DB 文件与 tenant 行删除，且删除前有 consolidation 日志。
- [ ] 30 分钟长任务完成后不会被立即回收（`finished_at` 生效）。
- [ ] 非法状态转换返回明确错误；并发 send/wake 无重复 Run（单测 + race detector）。
- [ ] 旧数据（未标记 durable 的 role）行为不变。

---

## 8. 风险与回滚总表

| 风险 | 触发点 | 影响 | 缓解 | 回滚 |
|---|---|---|---|---|
| TTL 改 hibernate 后资源泄漏 | 休眠会话不删 | 磁盘/DB 增长 | `destroy_after` + 指标 + 定期 `du` | 切回 `destroy` |
| 僵尸 `running` | Run 卡死 | 永不回收 | `running_since` 超时强制 hibernate | 同上 |
| 唤醒重建失败 | role 删除/model 失效 | 部门失联 | sidecar spec + 明确错误 + 首辅重编制 | 关闭 `resume`，用 `new` |
| 记忆迁移丢数据 | durable tenant 变更 | 部门经验丢失 | 不删旧 tenant + dual-read + marker | 回退派生函数 |
| group 成员校验变严 | 现有跨组用法 | 消息被拒 | CHANGELOG + 先告警后拒绝 | 配置开关 `groups.strict_membership=false` |
| 群发部分失败 | 成员漏消息 | 逐成员 receipt | 返回 recipients 明细 + 同 key 重试只补失败者 | 重发 |
| 广播 hop 限制 | 现有广播链 | 深层广播被截 | hop 默认 4 + 日志 | 调高 max_hops |
| 状态文件并发写 | 多 agent 写同一文件 | 内容损坏 | 单写者 + 原子 rename + validator | git 回滚 |
| 校验脚本误报 | 规则过严 | 奏折全红 | 规则分级（error/warn）+ 先 warn | 调整 `red_pen.rules` |
| cron 朝会噪音 | 频繁触发 | 打扰用户 | 每日一次 + 只报异常 | 删除 cron job |
| hooks command 安全 | 任意命令 | 注入风险 | 默认关闭；仅项目层启用；脚本路径固定 | 关闭 `enable_command_hooks` |

**总回滚原则**：

1. 所有新行为都有配置开关，默认值尽量保持现状。
2. 所有删除动作（`destroy_after`）默认 30 天，且先 consolidation。
3. 状态文件进 git；任何组织数据可 `git revert`。
4. 代码回滚按 commit 粒度；Phase 3 的唤醒/休眠必须独立 commit。

---

## 9. 验证命令（提交前自查）

```bash
# 分支与隔离
git -C /tmp/xbot-court status --short
git -C /tmp/xbot-court log --oneline -1

# 清理逻辑证据
grep -n "interactiveSessionTTL\|cleanupExpiredSessions\|destroyInteractiveSession" agent/interactive.go
grep -rn "CleanupInteractiveSessions" --include=*.go .        # 应仅定义
grep -n "Clear()" agent/interactive.go                       # 1130 / 1648
grep -n "lastUsed =" agent/interactive.go                    # 989/891/1714/1961

# 状态/持久化
grep -rn "func (.*WakeInteractive\|rehydrat\|restoreInteractive" --include=*.go .  # 应为空（现状）
grep -n "func (m \*MultiTenantSession) DestroySession" -A 45 session/multitenant.go
grep -n "func (s \*TenantSession) GetMessages\|func (s \*TenantSession) Clear" session/tenant.go

# 群组
grep -rn "FlushPeerGroups" --include=*.go .                  # 仅定义
grep -n "groupCounter\|CreateGroup" tools/create_chat.go tools/group_state.go
grep -n "for _, m := range members" tools/send_message.go    # 并发广播

# memory
grep -n "deriveSubAgentTenantID" -A 15 agent/subagent_tenant.go
grep -n "ArchiveAll" agent/engine_wire.go                    # 现状缺失

# hooks / cron
grep -n "EventSubAgentStart\|EventSubAgentStop" agent/hooks/event.go
grep -n "SetNotifyCronFunc" -A 12 agent/agent.go
```

**测试建议**（不提交验证脚本）：

- `go test ./agent/ -run 'Interactive|SubAgent'`
- `go test ./tools/ -run 'Group|SendMessage|Cron'`
- `go test ./session/`
- 手工：重启后 `SendMessage(agent:...)` 唤醒；TTL 到期后 `ls sessions/`。

---

## 10. 附录：关键位置索引

| 主题 | 位置 |
|---|---|
| 子代理注册表 | `agent/agent.go:560-563` |
| 子代理结构体 | `agent/interactive.go:44-75` |
| spawn | `agent/interactive.go:970-1767` |
| `Clear()` | `agent/interactive.go:1130`、`:1648`；实现 `session/tenant.go:291-297` |
| 销毁漏斗 | `agent/interactive.go:557-617` |
| TTL | `agent/interactive.go:156,162-196` |
| TTL 调用点 | `agent/interactive.go:986,2958` |
| 死代码 | `agent/interactive.go:2904-2920` |
| 自然完成 | `agent/interactive.go:1516-1626` |
| Continue（仅内存） | `agent/interactive.go:1773-1842` |
| dump/messages（仅内存） | `agent/interactive.go:3149-3197` |
| 会话销毁 | `session/multitenant.go:795-838` |
| 会话缓存驱逐 | `session/multitenant.go:708-785` |
| 每会话 DB | `session/sessiondb.go:3-20`、`storage/sqlite/sessiondb.go:201-216` |
| 主库 schema | `storage/sqlite/schema.go:17-135,252-333` |
| 主 Agent 重启恢复 | `serverapp/server.go:1027-1128` |
| 侧栏 agent 列表（DB） | `serverapp/callbacks.go:637-661` |
| SubAgent tenant 派生 | `agent/subagent_tenant.go:20-33` |
| SubAgent memory 构建 | `agent/engine_wire.go:1340-1364` |
| SubAgent memory 整合（no-op） | `agent/engine_wire.go:1457-1489` |
| memory 隔离 | `memory/xbot/xbot.go:105-156,239-261` |
| memory 注入 | `agent/middleware_builtin.go:541-566`；SubAgent `agent/engine_wire.go:977-991` |
| group 模型（旧，已删） | `tools/group_state.go:12-152`（审计对照） |
| group 创建 | `tools/create_chat.go:164-259` |
| group 发送 | `tools/send_message.go:232-353` |
| peer 组（旧，已删） | `tools/group_state.go:133-387`、`tools/peer_group.go`（审计对照） |
| group prompt | `agent/engine_wire.go:729-741` |
| group 历史聚合 | 新 `tools/group_history.go` |
| group 清理 | `agent/interactive.go:588-593` |
| cron 调度 | `cron/scheduler.go:205-248` |
| cron 工具 | `tools/cron.go:127-180,207-220` |
| cron 注入 | `agent/agent.go:2860-2873,4984-4997,5081-5105` |
| hooks 事件 | `agent/hooks/event.go:45-60` |
| hooks 配置 | `agent/hooks/config.go:27-57` |
| hooks 调度 | `agent/hooks/manager.go:131-171` |
| SubAgentStart/Stop | `agent/engine_wire.go:1677-1686,1805-1814` |
| plugin contributes | `plugin/plugin.go:162-171` |
| plugin cron | `plugin/context.go:99-102`、`plugin/integration.go:295-336` |
| role 定义/加载 | `tools/subagent_roles.go:12-27`、`tools/subagent_loader.go:14-33` |
| role 优先级 | `agent/agents.go:40-114` |

---

## 11. 决策记录（ADR 摘要）

| 决策 | 理由 | 替代方案（否决） |
|---|---|---|
| 组织结构用文件而非 DB 表 | 零 schema 变更；所有 agent 已有文件工具；可 git | 新增 `court_*` 表（违反「不定制」） |
| 会议不新增 core 引擎 | 只有群发/单发两个动作；通知用群发、回复用单发，prompt 约定即可 | 写一个会议执行器 / 轮次抽象（业务逻辑进 core） |
| 批红规则用脚本 | 确定性、可测试、可插拔 | 让 LLM 判定（不可复现） |
| TTL 默认先保持 destroy | 零行为突变，可观察 | 直接切 hibernate（风险高） |
| durable tenant 用 sidecar 记录 spec | 不碰 schema | 加 `agent_specs` 表（迁移成本） |
| 朝会靠 cron 而非常驻 goroutine | 已有持久化 + 重启可用 | 新后台调度器（重复造轮子） |
| 群历史靠跨会话库聚合 | 消息只落会话库（决策 A）；不建群级消息表 | 给 group 建消息表 / 群组独立库（与 session 重复，且违反 A） |
| 群状态只建两张小表 | 只有成员关系是跨会话数据；消息不是 | 建群组独立库、存群消息、存轮次/主持人 |
| 旧群数据直接删除 | 用户拍板 D；旧数据本来不可靠（内存/JSON 双轨） | 迁移 `peer_groups.json` + legacy 认领（复杂度高、价值低） |
| ask_user 完全异步 + 插队队列 | 不阻塞 run；用户回复应立即被看到 | 保留等待态/兼容窗口（双份状态、跨重启恢复复杂） |

---

## 12. 交付物清单

- 本方案：`docs/design/court-orchestration.md`
- 分支：`feat/court-orchestration`（基于 `origin/master@619ec6c3`）
- 不包含：任何实现代码、任何验证脚本（按任务约束）
- 后续若进入实现：Phase 3 的唤醒/休眠应拆成独立 commit，且先补测试再改默认配置。
