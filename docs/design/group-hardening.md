# 群组强化（Group Hardening）—— 详细设计

> 状态：设计稿（**不含实现代码**）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`
> 关联：`sendmessage-structured.md`（①）、`message-sender-id.md`（③）、`court-reporting.md`（⑤）
> 前置：本设计逐条解决审计缺陷 D1–D17。

---

## 0. 一页纸结论

1. **分层模型**：`Meeting`（`group:`，有主持人、有轮次、有发言权）与 `PeerBus`（`peer:`，无主持人、异步广播）共享一个 **Conversation Core**（成员、权限、transcript、投递、ID、持久化）；差异只在「谁能触发谁」的策略层。
2. **一切落库**：新增 `groups` + `group_members`（主库），消息 transcript 复用 ① 的 `agent_messages`（`group_id` 索引）。进程重启不丢群、不丢纪要。
3. **ID 改 nanoid**：`grp_<12>`；创建时查重；彻底消除 `g<N>` 重启复用与 15×15 撞名。
4. **成员状态机 + 心跳**：`alive/busy/left/dead`，`last_seen_at` 心跳；leave 广播 `event`；roster 实时查询，不再用创建时快照。
5. **轮次真正实现**：`rounds_used` 持久化；`max_rounds` 生效；达到上限自动 `closed` 并广播收尾事件。
6. **广播控流**：信封 `hop/max_hops`、扇出上限、按群令牌桶、消息 ID 去重；成员回复默认定向，不自动广播。
7. **权限/租户**：`owner_scope` + 成员校验；知道 ID 不等于能发；跨租户返回 `NOT_FOUND`。
8. **与 ①③ 整合**：群消息 = 结构化消息；`from_*` = `sender_*`；会议纪要 = `agent_messages WHERE group_id=?` 的事件流。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 统一模型，消除两套实现 | 一个 `ConversationService` 接口，两个策略实现 |
| G2 | 群组持久化、可回放 | 重启后 `groups`/`group_members`/transcript 完整 |
| G3 | 轮次语义真实 | `max_rounds=3` 时第 4 次触发被拒绝并自动关闭 |
| G4 | 防 N×N | hop 限制 + 扇出上限 + 回复定向；压测无指数增长 |
| G5 | 成员状态实时 | join/leave/busy 变化 1s 内可查；leave 广播 |
| G6 | ID 全局唯一 | 重启/多进程不重复；创建 100 万次无碰撞（概率论证） |
| G7 | 权限与租户隔离 | 非成员/跨租户发送被拒绝，且不泄露存在性 |
| G8 | 与结构化消息统一 | 群消息有 `message_id/correlation_id/from_*` |

### 1.2 非目标

- 不做分布式共识/多节点群组（单进程多会话；多进程部署不在本期）。
- 不做群公告/置顶/表情等 IM 功能。
- 不做「成员互相私聊」的额外通道（仍走 `session:`）。
- 不把群组做成完整 IM（无已读回执、无输入中状态）。
- 不迁移旧 `group:`（内存态无法恢复）；只迁移 `peer_groups.json`。

---

## 2. 审计缺陷 D1–D17 与解决方案

| # | 缺陷 | 证据（file:line） | 解决方案 | 验收 |
|---|---|---|---|---|
| **D1** | 两套模型互不相通：`group:` 是内存成员集合，`peer:` 是 JSON 文件 | `tools/group_state.go:19-26,149-152`；`tools/send_message.go:85-100` | 抽出 `Conversation Core`；`Meeting`/`PeerBus` 只实现策略接口；共用存储/路由/权限 | 同一套 `GET /api/groups` 能列出两种类型 |
| **D2** | `max_rounds` 是空参数，从未读取 | `tools/create_chat.go:57,61-68,170-242`；`tools/group_state.go:61-65` | `groups.max_rounds`/`rounds_used` 落库；每次触发计数；达上限自动关闭 | `max_rounds=2` 第 3 次发送返回 `ROUND_LIMIT` |
| **D3** | 群组无消息历史/无 transcript | `tools/group_state.go:11-18`（自陈无 store） | transcript 落 `agent_messages(group_id)`；纪要可查询 | `GET /api/groups/{id}/messages` 返回全部发言 |
| **D4** | 广播 = N 个并发 full-turn，无 hop/depth，成员可再广播 → N×N | `tools/send_message.go:280-308,327-350`；`create_chat.go:247-248` | `hop/max_hops`、扇出上限 32、群令牌桶、回复默认定向、去重 | 压力测试消息数线性增长，不爆炸 |
| **D5** | ID `g<N>` 进程内自增，重启复用；`CreateGroup` 无条件覆盖 | `tools/create_chat.go:13-14,170,242`；`tools/group_state.go:32-40` | `grp_<nanoid(12)>`；插入冲突重试；主键唯一 | 重启后新建群 ID 不重复 |
| **D6** | 发送者无成员校验：知道 `group:g1` 就能群发 | `tools/send_message.go:240-258`（未校验 ctx/成员） | 发送前校验 `from` ∈ `group_members(status != left)`；否则 `MEMBERSHIP_REQUIRED` | 非成员发送返回 403/错误码 |
| **D7** | 群组无持久化，重启即失忆 | `tools/group_state.go:29`（`sync.Map`） | `groups`/`group_members` 落主库；启动加载 | 重启后群与成员仍在 |
| **D8** | roster 是创建时快照；spawn 失败仍入册；`isInGroup` 用快照 | `tools/create_chat.go:189-242`；`tools/send_message.go:223-230` | roster 实时查库；spawn 成功才置 `alive`，失败 `dead`；`isInGroup` 改为查询 | 成员崩溃后 roster 显示 `dead` |
| **D9** | `FlushPeerGroups` 零调用者 | `tools/group_state.go:283`；全局 grep 无调用 | 迁入 DB 后删除该函数；优雅停机不再需要 | 代码中无 `FlushPeerGroups` |
| **D10** | peer 自动 ID 仅 15×15=225 种，撞名即共享 | `tools/peer_group.go:12-31,196-200` | 统一 `grp_<nanoid(12)>`；显式 ID 也校验唯一 | 连续创建 10 万群无碰撞 |
| **D11** | peer 单文件全局、无 GC、无租户隔离 | `tools/group_state.go:161-215,242-260,360-386` | DB 行 + `owner_scope`；空群 7 天后 `archived`；按 scope 查询 | 跨租户不可见 |
| **D12** | peer 广播串行 O(n)，无控流/去重 | `tools/send_message.go:380-428` | 批量投递（并发度可配）+ 令牌桶 + `message_id` 去重 | 100 成员广播 < 1s 入队 |
| **D13** | 无 round/turn 真实语义；`Closed` 是死字段 | `tools/group_state.go:61-65`；描述 `create_chat.go:29-43` | `Meeting` 显式轮次状态机；`Closed` 由 DB 状态替代 | 关闭后发送返回 `GROUP_CLOSED` |
| **D14** | 无成员状态机/心跳/leave 广播 | `tools/group_state.go:296-327`（仅增删） | `status/last_seen_at`；心跳 sweep；leave 广播 event | leave 后其他成员收到事件 |
| **D15** | 无 correlation/idempotency/消息 ID，无法回放去重 | `tools/send_message.go:292,336`（字符串前缀） | 复用 ① 信封；`agent_messages.id/idempotency_key` | 重发同 key 不重复 |
| **D16** | 无权限模型；跨组目标校验仅 `sendToAgent` 有 | `tools/send_message.go:123-132,240` | 统一权限中间件：租户 + 成员 + 角色能力 | 跨组/跨租户拒绝 |
| **D17** | 成员互相看不到发言；@ 解析过窄（`@agent:qa` 被丢弃→变广播） | `tools/send_message.go:431-447` | `@` 支持 `role`/`role/instance`；歧义返回错误；transcript 全员可见（按 visibility） | `@agent:qa` 能定向到唯一实例或报歧义 |

> 补充：D2/D5/D7/D13 是「文档声称有、代码没有」的典型漂移（`docs/agent/tools.md:98,107-112`），本设计以**代码可验证行为**为准，文档同步更新。

---

## 3. 统一模型

### 3.1 分层

```text
┌──────────────────────────────────────────────────────────────┐
│                     Conversation Core                        │
│  · ID / owner_scope / members / permissions                  │
│  · transcript (agent_messages)                               │
│  · delivery (broadcast/directed, hop, fanout, rate limit)    │
│  · idempotency / receipts                                    │
└───────────────┬───────────────────────────┬──────────────────┘
                │                           │
      ┌─────────▼─────────┐       ┌─────────▼─────────┐
      │  MeetingPolicy     │       │  PeerBusPolicy     │
      │  group:            │       │  peer:             │
      │  · moderator       │       │  · 无主持人         │
      │  · rounds/max      │       │  · 任意成员广播      │
      │  · 发言权/@定向     │       │  · 无轮次           │
      │  · 自动关闭         │       │  · 长期存在          │
      └────────────────────┘       └────────────────────┘
```

### 3.2 职责对比

| 能力 | Meeting (`group:`) | PeerBus (`peer:`) |
|---|---|---|
| 创建者 | 主持人（agent/user） | 任意成员（JoinGroup） |
| 成员加入 | 创建时指定，不可自助 | 自助 Join/Leave |
| 谁可触发成员 | 仅主持人（`@`/directed） | 任意成员（broadcast/directed） |
| 轮次 | 有，`max_rounds` | 无 |
| 自动关闭 | 达到轮次/显式关闭 | 永不自动关闭（归档） |
| Transcript | 有 | 有 |
| 典型用途 | 朝会/部务会 | 部门间异步总线 |

### 3.3 共享接口（设计级）

```text
ConversationService
  Create(ctx, CreateSpec) (Conversation, error)
  Get(ctx, id) (Conversation, error)
  List(ctx, scope, filter) ([]Conversation, error)
  Join(ctx, id, member) error
  Leave(ctx, id, memberID, reason) error
  Send(ctx, id, Envelope) (SendResult, error)
  Messages(ctx, id, afterID, limit) ([]Envelope, error)
  Close(ctx, id, reason) error
  Heartbeat(ctx, id, memberID) error

Policy (per kind)
  CanSend(conv, sender, envelope) error
  CanTrigger(conv, sender, target) error
  OnSend(conv, envelope) (postActions, error)   // 轮次计数/关闭
  OnJoin/OnLeave/OnClose(...)
```

---

## 4. 存储 schema（主库 v75）

### 4.1 `groups`

```sql
CREATE TABLE groups (
    id                  TEXT PRIMARY KEY,              -- grp_<nanoid(12)>
    kind                TEXT NOT NULL,                 -- meeting | peer
    name                TEXT NOT NULL DEFAULT '',      -- 展示名
    owner_scope         TEXT NOT NULL,                 -- 租户/用户隔离轴
    moderator_id        TEXT NOT NULL DEFAULT '',      -- meeting: role:instance / user id
    max_rounds          INTEGER NOT NULL DEFAULT 10,   -- meeting only
    rounds_used         INTEGER NOT NULL DEFAULT 0,
    round_started_at    DATETIME,
    status              TEXT NOT NULL DEFAULT 'open',  -- open | closed | archived
    close_reason        TEXT NOT NULL DEFAULT '',
    history_visibility  TEXT NOT NULL DEFAULT 'since_join', -- none | since_join | all
    max_hops            INTEGER NOT NULL DEFAULT 4,
    max_fanout          INTEGER NOT NULL DEFAULT 32,
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL,
    closed_at           DATETIME,
    archived_at         DATETIME
);
CREATE INDEX ix_groups_scope_kind ON groups(owner_scope, kind, status);
CREATE UNIQUE INDEX ux_groups_id ON groups(id);
```

### 4.2 `group_members`

```sql
CREATE TABLE group_members (
    group_id      TEXT NOT NULL,
    member_type   TEXT NOT NULL,                 -- user | agent | session
    member_id     TEXT NOT NULL,                 -- user id / role:instance / channel:chatID
    display_name  TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL DEFAULT 'member',-- moderator | member | observer
    status        TEXT NOT NULL DEFAULT 'alive', -- alive | busy | left | dead
    joined_at     DATETIME NOT NULL,
    last_seen_at  DATETIME,
    left_at       DATETIME,
    left_reason   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (group_id, member_type, member_id)
);
CREATE INDEX ix_group_members_lookup ON group_members(group_id, status);
CREATE INDEX ix_group_members_member ON group_members(member_type, member_id);
```

### 4.3 transcript

复用 ① 的 `agent_messages`：

- 每条群消息 `group_id = groups.id`；
- `to_type` 为 `group` 或 `agent`/`user`（定向）；
- `delivery_mode` 建议在 v74 建 `agent_messages` 时**直接包含**（本文档把它列为群组层字段，是为了明确语义归属）；若 v74 已发布未含该列，则 v75 用 `ALTER TABLE` 补齐：

```sql
ALTER TABLE agent_messages ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'directed';
CREATE INDEX ix_agent_messages_group_mode ON agent_messages(group_id, delivery_mode, created_at);
```

会议纪要查询：

```sql
SELECT * FROM agent_messages
 WHERE group_id = ? AND kind NOT IN ('receipt')
 ORDER BY created_at, id;
```

### 4.4 轮次记录（可选）

```sql
CREATE TABLE group_rounds (
    group_id     TEXT NOT NULL,
    round_no     INTEGER NOT NULL,
    started_at   DATETIME NOT NULL,
    ended_at     DATETIME,
    trigger_message_id TEXT NOT NULL,
    participants TEXT NOT NULL DEFAULT '[]',      -- JSON array
    PRIMARY KEY (group_id, round_no)
);
```

用途：审计「第几轮谁被点名」；不参与实时判定（实时用 `groups.rounds_used`）。

### 4.5 为什么放主库

- 群跨会话/跨进程，会话库只属于一个会话；
- 与 `agent_messages` 同库，transcript join 简单；
- 主库已有 `tenants`/`cron_jobs` 等全局表，迁移路径成熟。

---

## 5. 生命周期与状态机

### 5.1 群状态

```text
create ──▶ open ──(max_rounds/显式 Close)──▶ closed ──(7d 无活动)──▶ archived
              │
              └──(成员全 left)──▶ closed(reason=empty)
```

规则：

1. `closed` 后拒绝新消息（`GROUP_CLOSED`），但 transcript 仍可读。
2. `archived` 只读；可显式 `restore`（本期不做）。
3. `rounds_used` 只在 `open` 且 meeting 时递增。
4. 关闭时广播 `event(group.closed)` 给所有 `alive` 成员。

### 5.2 成员状态

```text
join ──▶ alive ⇄ busy
           │
           ├──(leave)──▶ left
           └──(心跳超时/agent unload)──▶ dead
```

| 状态 | 含义 | 触发 |
|---|---|---|
| `alive` | 在线空闲 | join / 心跳恢复 / 任务完成 |
| `busy` | 正在处理 turn | 发言被投递且目标 run 忙碌 |
| `left` | 主动退出 | LeaveGroup |
| `dead` | 失联 | 心跳 > 90s 或 agent unload |

- 心跳：成员每次发送/接收/处理消息时更新 `last_seen_at`；后台 sweep 每 30s 把 `alive/busy` 且 `last_seen_at < now-90s` 置 `dead`。
- `dead` 不自动移除（保留历史）；`roster` 显示状态；主持人可 `RemoveMember`。
- `join/leave/dead` 广播 `kind=event`（`event_type=member.*`）。

### 5.3 轮次语义（meeting）

- **一轮的定义**：主持人发出一次「触发消息」（带 `@` 或 `directed` 给至少一个成员）→ 被触发成员各产生一次回复（或超时）→ 轮结束。
- `rounds_used += 1` 在触发消息持久化时原子递增；`group_rounds` 记录参与人。
- `rounds_used >= max_rounds` 时：
  1. 拒绝新的触发消息（`ROUND_LIMIT`）；
  2. `status=closed`，`close_reason=max_rounds`；
  3. 广播 `event(group.closed)`，附带 `summary_ref`（若有）。
- 普通成员之间的定向/广播不计轮（peer 无轮；meeting 内成员广播默认禁止，除非主持人显式授权）。

---

## 6. 投递、hop 与控流

### 6.1 投递矩阵

| 发送方角色 | broadcast | directed（成员） | directed（非成员） |
|---|---|---|---|
| moderator | ✅（计入 1 轮） | ✅（计入 1 轮） | ❌ |
| member | ❌（默认） | ✅（回复，不计轮） | ❌ |
| member（被授权） | ✅（需 `allow_member_broadcast`） | ✅ | ❌ |
| user | ✅ | ✅ | ❌ |
| system | ✅ | ✅ | ✅（内部） |

### 6.2 hop / depth

- 信封 `hop` 初始 0；每次「agent 收到后转发给另一个 agent」+1。
- `hop >= max_hops` 拒绝投递，回执 `HOP_LIMIT`。
- 防止 A→B→A 循环：`causation_id` 链上出现过的 `message_id` 不再转发（环检测）。
- 广播消息的子消息默认 `hop+1`；成员回复不自动转发。

### 6.3 扇出与限流

| 限制 | 默认 | 可配 |
|---|---|---|
| 单次广播最大收件人 | 32 | `groups.max_fanout` |
| 每群每秒消息数 | 20 | 全局配置 |
| 每发送者每秒消息数 | 5 | 全局配置 |
| 单条 payload | 256 KiB | 固定 |
| 群成员上限 | 128 | 全局配置 |

超限返回 `RATE_LIMITED`/`FANOUT_LIMIT`，不静默丢弃。

### 6.4 去重

- `agent_messages.id` 主键 + `(owner_scope, idempotency_key)` 唯一索引。
- 投递到会话时以 `message_id` 为幂等键，重复投递只更新状态。
- 同一 `correlation_id` 内按 `created_at,id` FIFO。

### 6.5 N×N 防护（关键）

旧实现：主持人广播给 N 个成员；成员 prompt 鼓励「向全员汇报」→ 每个成员再广播 N 次 → O(N²) full-turn。

新规则：

1. **成员回复默认 `directed` 给主持人/触发者**，不自动广播。
2. 成员要广播必须显式 `delivery.mode=broadcast`，且受 `allow_member_broadcast` 与限流约束。
3. 广播产生的消息 `hop+1`；达到 `max_hops` 不再转发。
4. 同一 `causation_id` 链上的重复消息 ID 直接丢弃（环检测）。
5. 群级令牌桶；超限返回错误而非排队爆炸。

---

## 7. ID 设计

### 7.1 格式

```text
grp_<nanoid(12)>     例：grp_V1StGXR8_Z5j
```

- 字母表：`0-9A-Za-z_-`（64 字符），12 位 ≈ 64^12 ≈ 4.7×10^21 组合。
- 生日碰撞：创建 100 万个群，碰撞概率 ≈ 1 - exp(-n²/2N) ≈ 1.1×10^-10，可忽略。
- 显式 ID（`JoinGroup(group_id=...)`）：校验 `^[A-Za-z0-9][A-Za-z0-9_-]{1,63}$` 且全局唯一；冲突返回 `ID_TAKEN`，不自动复用。

### 7.2 与旧 ID 的兼容

- 旧 `group:g1` / `peer:swift-team` 字符串地址**仍可解析**；内部映射到 `grp_*` 需要迁移（见 §9）。
- 新 API 返回 `id`（`grp_*`）与 `address`（`group:grp_*`/`peer:grp_*`）两个字段；旧调用方用 `address`。
- 不再生成 `g<N>`；`groupCounter` 删除。

---

## 8. 权限与租户隔离

### 8.1 校验顺序

```text
1. 解析 from（服务端上下文，不信 payload）
2. 查 groups.id，校验 owner_scope 与调用者一致 → 否则 NOT_FOUND
3. 查 group_members(from) → 不存在/left → MEMBERSHIP_REQUIRED
4. Policy.CanSend/CanTrigger → 否则 PERMISSION_DENIED
5. hop/fanout/rate/idempotency
6. 持久化 + 投递
```

### 8.2 角色能力

| 角色 | 读 transcript | 发消息 | 触发成员 | 关闭群 | 移除成员 |
|---|---|---|---|---|---|
| moderator | ✅ | ✅ | ✅ | ✅ | ✅ |
| member | ✅（按 visibility） | ✅（定向） | ❌ | ❌ | ❌ |
| observer | ✅ | ❌ | ❌ | ❌ | ❌ |
| user（owner） | ✅ | ✅ | ✅ | ✅ | ✅ |
| system | ✅ | ✅ | ✅ | ✅ | ✅ |

### 8.3 租户隔离

- `owner_scope` = 会话 owner 的稳定 id（与 ③ 的 sender 轴一致）。
- 所有读写 SQL 强制 `owner_scope = ?`；群 ID 猜测返回 `NOT_FOUND`。
- 跨租户成员邀请：本期不支持（返回 `CROSS_TENANT_DENIED`）。

---

## 9. 迁移策略

### 9.1 阶段

| 阶段 | 内容 | 兼容 | 回滚 |
|---|---|---|---|
| M1 | 文档（本设计） | — | — |
| M2 | 主库 v75：`groups`/`group_members`/`group_rounds` + `agent_messages.delivery_mode`；`ConversationService` 双写（内存 + DB） | 旧路径仍读内存 | 关 flag，删表 |
| M3 | 读路径切 DB；`peer_groups.json` 一次性导入 | 旧文件保留只读 | 切回文件 |
| M4 | 删除内存 `groupStore`/`peerGroupStore`/`FlushPeerGroups` | 需要重启 | 恢复旧代码 |
| M5 | 清理 `g<N>`/词表 ID 生成 | — | — |

### 9.2 `peer_groups.json` 导入

```text
for each key "peer:<id>" in peer_groups.json:
    newID = "grp_" + nanoid(12)
    INSERT groups(id=newID, kind='peer', name=<id>, owner_scope='legacy', status='open', ...)
    for each member {session_key, name}:
        INSERT group_members(group_id=newID, member_type='session',
                             member_id=session_key, display_name=name, status='alive')
    -- 旧地址 "peer:<id>" 建立别名映射（内存/DB alias 表或 groups.name 索引）
```

- `owner_scope='legacy'` 的群在首次被非 legacy 用户访问时提示「需要认领」；本期只读。
- 导入完成后文件重命名 `peer_groups.json.imported`，不删除。
- 幂等：以 `name + kind` 判重；重复导入跳过。

### 9.3 内存 group 无法迁移

`GroupMembership` 只在进程内（`tools/group_state.go:29`），重启即失；迁移只保证「升级后新群持久化」，不承诺恢复旧群。文档需明确告知。

### 9.4 数据回填

- `groups.rounds_used=0`；`max_rounds=10` 默认。
- `group_members.status='alive'`（导入时）；`last_seen_at=now`。
- `agent_messages.delivery_mode`：旧行默认 `directed`（无法判断）。

---

## 10. 与 ①③ 的整合

### 10.1 消息 = 信封

- 群消息必须是 `xbot.message/v1` 信封；`to.type=group`，`group_id` 必填。
- `from` 由服务端从会话上下文解析，写入 `agent_messages.from_*` 与投影行 `sender_*`。
- `ask_user` 在群内强制 `directed` 给单个成员/用户。
- `report`/`decision` 的 `correlation_id` 贯穿群内。

### 10.2 会议纪要

```text
纪要 = agent_messages
        WHERE group_id = ?
          AND kind IN ('text','ask_user','answer','report','decision','event')
          AND (visibility = 'group' OR viewer 是 from/to)
        ORDER BY created_at, id
```

- 可按 `round_no` 分段（`group_rounds`）。
- 导出为 `court/meetings/<date>-<topic>.md`（⑤）。

### 10.3 sender 统一

| 概念 | `agent_messages` | `session_messages` |
|---|---|---|
| 发送者类型 | `from_type` | `sender_type` |
| 发送者 ID | `from_id` | `sender_id` |
| 显示名 | `from_name` | `sender_name` |
| 群 | `group_id` | `structured_json.group_id` |

写入投影时由 `ConversationService` 统一填充，禁止各渠道自行拼装。

---

## 11. Rolodex / roster 实时性

### 11.1 查询

```text
ListMembers(groupID):
  SELECT member_type, member_id, display_name, role, status, last_seen_at
    FROM group_members WHERE group_id = ?
    ORDER BY role DESC, joined_at
```

- 不再从 `ctx.GroupMembers` 快照读（`create_chat.go:183-184` 的字段保留但仅作为「创建时提示」）。
- `isInGroup` 改为 `ConversationService.IsMember(ctx, groupID, from)`。

### 11.2 心跳来源

| 事件 | 更新 |
|---|---|
| 成员发送消息 | `last_seen_at=now`, `status=alive`（若 was dead） |
| 成员被投递且忙碌 | `status=busy` |
| 成员 turn 结束 | `status=alive` |
| agent unload | `status=dead`, `left_reason=unloaded` |
| Join/Leave | 插入/更新 |
| 后台 sweep | `status=dead` if stale |

### 11.3 事件

```json
{
  "kind": "event",
  "payload": {
    "event_type": "member.left",
    "subject": "group:grp_...",
    "data": {"member_type": "agent", "member_id": "qa/main", "reason": "unloaded"}
  }
}
```

广播给所有非 `left` 成员；observer 也收。

---

## 12. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 迁移后旧 `peer:` 地址失效 | 现有 agent 找不到群 | 地址别名映射（`groups.name`/alias 表）；旧地址解析到新 ID | 保留 JSON 读路径 |
| 轮次计数过于严格 | 正常讨论被提前关闭 | 默认 10；可配；`ROUND_LIMIT` 返回明确；主持人可 `extend`（后续） | 提高 max_rounds |
| 心跳误判 dead | 成员被错误标记 | 90s 阈值 + 只在明确 unload/超时时置 dead；可手动恢复 | 放宽阈值 |
| 扇出/限流影响性能 | 广播被拒 | 默认值足够朝廷规模（<32 成员）；可配 | 提高上限 |
| transcript 膨胀 | 主库变大 | 按群归档；receipt 不落 transcript；7 天归档 | 清理任务 |
| 权限过严 | agent 无法沟通 | 错误码 + 白名单；灰度 | 放宽为同租户 |
| 双写不一致 | 内存/DB 状态分裂 | M2 以 DB 为权威；内存只做缓存；定期 reconcile | 关闭双写 |

---

## 13. 验收判据

### 13.1 功能

1. 创建 meeting 群：返回 `grp_*`；重启后 `GET /api/groups/{id}` 仍存在。
2. `max_rounds=2`：第 3 次触发返回 `ROUND_LIMIT`，群 `status=closed`，成员收到 `event(group.closed)`。
3. 非成员发送返回 `MEMBERSHIP_REQUIRED`；跨租户返回 `NOT_FOUND`。
4. 成员 leave 后，其他成员在 1s 内收到 `event(member.left)`，roster 状态 `left`。
5. 成员心跳超时 90s 后 roster 显示 `dead`。
6. `@agent:qa`（无 instance）若唯一则定向成功；多实例返回 `AMBIGUOUS_MENTION`。
7. 广播 32 人：每人恰好收到 1 条；重复 `idempotency_key` 不重复。
8. hop 达到 `max_hops` 的消息被拒绝并回执。
9. transcript 查询返回完整发言，含 `from_*`/`correlation_id`。

### 13.2 迁移

10. `peer_groups.json` 导入后，旧 `peer:<id>` 地址仍可发送。
11. 导入幂等：重复执行不产生重复群/成员。
12. 重启后无内存依赖（`groupStore` 为空也能工作）。

### 13.3 压测

13. 50 成员群，主持人连续 10 轮广播：消息总数 ≤ 50×10 + 回执，无 N×N。
14. 1000 条群消息写入 p99 < 50ms（本地 SQLite）。
15. 广播 32 成员入队 < 500ms（不含成员处理）。

### 13.4 回归

16. 现有 `group:`/`peer:` 工具调用语义不破坏（text 消息）。
17. `go test ./tools/... ./storage/... ./agent/...` 通过。

---

## 14. 受影响代码清单（file:line）

### 14.1 新增

| 文件 | 作用 |
|---|---|
| `group/core.go`（或 `tools/group_core.go`） | Conversation Core 接口与实现 |
| `group/meeting.go` | MeetingPolicy（轮次/关闭） |
| `group/peerbus.go` | PeerBusPolicy |
| `group/store_sqlite.go` | DB 读写 |
| `group/ids.go` | nanoid、ID 校验 |
| `storage/sqlite/migrations.go`（v75） | `groups`/`group_members`/`group_rounds` |
| `tools/group_tools.go` | 工具适配（CreateChat/Join/Leave/List/SendMessage） |

### 14.2 修改

| 文件:行 | 改动 |
|---|---|
| `tools/group_state.go:19-40` | 删除内存 `GroupMembership`/`groupStore`，改适配器 |
| `tools/group_state.go:137-215` | 删除 `PeerGroup` 文件存储，改 DB |
| `tools/group_state.go:281-290` | 删除 `FlushPeerGroups` |
| `tools/group_state.go:296-386` | Join/Leave/IsMember/GetMembers/List 改查库 |
| `tools/create_chat.go:13-14` | 删除 `groupCounter` |
| `tools/create_chat.go:57,61-68` | `max_rounds` 真正传递 |
| `tools/create_chat.go:170-242` | 创建走 `ConversationService`；spawn 失败标 dead |
| `tools/send_message.go:240-353` | group 走结构化投递 + 轮次 + 权限 |
| `tools/send_message.go:380-428` | peer 走结构化投递 + 限流 |
| `tools/send_message.go:431-447` | `parseMentions` 支持 role-only/歧义 |
| `tools/peer_group.go:12-31,196-200` | 删除词表 ID；改 nanoid |
| `tools/peer_group.go:168-177` | ListGroupMembers 校验成员 |
| `tools/subagent_roles.go:23-27` | 可选新增 `group_admin` 能力 |
| `tools/registry_helpers.go:242-247` | 工具注册保持 |
| `docs/agent/tools.md:90-115` | 更新群组语义 |
| `tools/limits.go` | 新增 fanout/rate 常量 |

### 14.3 删除

| 文件/片段 | 时机 |
|---|---|
| `tools/group_state.go` 内存实现 | M4 |
| `tools/peer_group.go` 词表生成 | M5 |
| `peer_groups.json` 读写 | M4（保留导入后只读） |

---

## 15. 开放问题（需用户拍板）

1. **Meeting 的「一轮」定义**：本设计 = 主持人一次触发 + 成员回复；若希望「全体成员各发言一次才算一轮」，需要改为屏障式轮次（实现复杂度更高）。
2. **peer 组是否允许自助 Join**：本设计保留（兼容现状）；若朝廷场景要求「只有首辅能拉人」，可关闭自助加入。
3. **`owner_scope` 的粒度**：本设计用「用户/租户」；若未来多用户共享群，需要新增 `visibility`/邀请模型。
4. **旧 `peer_groups.json` 的 `legacy` 群如何认领**：本设计只读；需要确认是否要提供「认领到当前用户」的命令。
5. **transcript 保留期**：本设计 7 天归档到 `audit` 表/文件；需要确认朝廷纪要是否要求永久保留（建议永久，归档而非删除）。
