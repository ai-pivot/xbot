# 群组（群发 / 单发）—— 详细设计（v2）

> 状态：设计稿（**不含实现代码**）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`
> 关联：`sendmessage-structured.md`（①）、`message-sender-id.md`（③）、`court-reporting.md`（⑤）
> **版本：v2（2026-10-19）** —— 按用户拍板决策重写。v1 的双模型分层（会议模型 + 对等总线）、群组独立库、群级消息表、轮次/主持人/发言权抽象**全部作废**。

---

## 修订记录（v1 → v2）

| # | v1 | v2（已拍板） |
|---|---|---|
| G-1 | 「会议」模型（有主持人/轮次/发言权）与「对等总线」模型（无主持人）共享统一抽象 | **双模型删除**。只有两个动作：**群发**和**单发** |
| G-2 | 群消息落主库中心表（group_id） | **没有「群组消息」这种东西**。群发 = 遍历成员各发一份、各写各的会话库；**不做群历史聚合**（群主用 `SubAgent(action=inspect)`，子代理主动汇报） |
| G-3 | 新增轮次表、轮次计数、最大轮次、可见性、主持人字段、`kind` 类型 | **全部删除**。群状态只有两张小表：`groups` + `group_members` |
| G-4 | 成员状态 `alive/busy/left/dead` + 心跳 sweep | **只有 `alive/left`**（left 不再投递）；无心跳、无 busy/dead |
| G-5 | `peer_groups.json` 一次性导入迁移 + legacy 认领 | **直接删文件，不迁移、不导入、不兼容** |
| G-6 | `peer:` 与 `group:` 双地址轨 | **只保留 `group:`**；`peer:` 删除 |

---

## 0. 一页纸结论

1. **用户模型只有两个动作**：
   - **单发** = `SendMessage(to="agent:x/y")` / `session:` / IM，写 1 条 outbound + 1 条 inbound。
   - **群发** = `SendMessage(to="group:grp_xxx")`，**遍历 `group_members(status='alive')`，对每人各发一份一样的 SendMessage**；各写各的会话库，消息带 `group_id` + `sender_*` 标记。
2. **没有「群组消息」实体**：不存在 `to.type=group` 的消息、不存在群级消息表、不存在群组独立库。群 ID 只是**标记 + 成员表主键**。
3. **群状态 = 主库两张小表**：`groups(id, name, owner, created_at)` + `group_members(group_id, member_addr, status, joined_at)`。
4. **没有群历史聚合**：平台不提供群历史查询工具、不跨会话库查询、不按 `message_id` 合并。群主通过 `SubAgent(action=inspect)` 查看子代理状态，子代理主动汇报；消息上的 `group_id` 仅作标记（未来若真要做聚合时有锚点），成员只读自己的会话库。
5. **保留必修 bug 修复**：每条消息带发送者标识；群发前校验成员身份；hop/fanout/令牌桶防 N×N；ID 用 `grp_<nanoid(12)>`；成员状态 `alive/left`（`left` 不再投递）。
6. **旧数据直接删**：`peer_groups.json`、内存 `GroupMembership`、`peer:` 分支、`groupCounter`、`FlushPeerGroups`，不迁移不兼容。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 群组语义唯一，消除双模型 | 代码里只有 `group:` 一种群地址；无双模型分支 |
| G2 | 群状态持久化 | 重启后 `groups`/`group_members` 完整；内存无群状态 |
| G3 | 群主可掌握成员状态 | `SubAgent(action=inspect)` 返回子代理状态；子代理主动汇报；平台不提供群历史聚合 |
| G4 | 防 N×N | hop 限制 + fanout 上限 + 群/发送者令牌桶 + 回复默认单发 |
| G5 | 成员身份可校验 | 非成员群发返回 `MEMBERSHIP_REQUIRED` |
| G6 | ID 全局唯一 | `grp_<nanoid(12)>`；重启/多进程不重复；创建 100 万次无碰撞（概率论证） |
| G7 | 与 ①③ 统一 | 群发副本是结构化消息；`sender_*` = 信封 `from.*` |
| G8 | 旧数据干净退场 | 仓库内无 `peer_groups.json` 读写；无 `GroupMembership` |

### 1.2 非目标

- 不做分布式共识/多节点群组（单进程部署；多进程不在本期）。
- 不做群公告/置顶/表情/已读回执/输入中状态。
- 不做群级消息表、不做群组独立库。
- 不做轮次/主持人/发言权/点名顺序（prompt 约定即可，core 不建模）。
- 不做成员互相私聊的额外通道（仍走 `agent:` 单发）。
- **不迁移**旧群数据（决策 D）。

---

## 2. 现状与缺陷（D1–D17 重映射）

> 保留 v1 的缺陷编号，便于对照历史审计；「v2 解决」列按新模型重写。

| # | 缺陷 | 证据（file:line） | v2 解决 | 验收 |
|---|---|---|---|---|
| **D1** | 两套模型互不相通：`group:` 是内存成员集合，`peer:` 是 JSON 文件 | `tools/group_state.go:19-26,149-152`；`tools/send_message.go:85-100` | **两套都删**，只保留一套 DB 群组 + 一种群发语义 | 代码无 `PeerGroup`/`GroupMembership` |
| **D2** | `max_rounds` 是空参数，从未读取 | `tools/create_chat.go:57,61-68,170-242` | **删除轮次概念与参数**（不是实现它） | 工具 schema 无 `max_rounds` |
| **D3** | 群组无消息历史 | `tools/group_state.go:11-18` | **不做群历史**（拍板：群主用 `SubAgent(action=inspect)` 看子代理状态，子代理主动汇报）；消息保留 `group_id` 标记 | 无群历史工具/API；inspect 可用 |
| **D4** | 广播 = N 个并发 full-turn，无 hop/depth → N×N | `tools/send_message.go:280-308,327-350` | hop/max_hops + fanout ≤128 + 令牌桶 + 回复默认单发 + `message_id` 去重 | 压测消息数线性 |
| **D5** | ID `g<N>` 进程内自增，重启复用；`CreateGroup` 无条件覆盖 | `tools/create_chat.go:13-14,170,242` | `grp_<nanoid(12)>`；主键冲突重试；已存在则报错 | 重启后新建群 ID 不重复 |
| **D6** | 发送者无成员校验：知道 `group:g1` 就能群发 | `tools/send_message.go:240-258` | 群发前查 `group_members(status='alive')`；否则 `MEMBERSHIP_REQUIRED` | 非成员发送被拒 |
| **D7** | 群组无持久化，重启即失忆 | `tools/group_state.go:29`（`sync.Map`） | 主库两张表（§4） | 重启后群与成员仍在 |
| **D8** | roster 是创建时快照；spawn 失败仍入册 | `tools/create_chat.go:189-242`；`tools/send_message.go:223-230` | roster 实时查库；spawn 成功才 INSERT `alive` | 成员崩溃/退出后 roster 反映 `left` |
| **D9** | `FlushPeerGroups` 零调用者 | `tools/group_state.go:283` | 文件存储整体删除，函数消失 | 全仓无 `FlushPeerGroups` |
| **D10** | peer 自动 ID 仅 15×15=225 种，撞名即共享 | `tools/peer_group.go:12-31,196-200` | `grp_<nanoid(12)>`；显式 ID 校验唯一 | 连续创建 10 万群无碰撞 |
| **D11** | peer 单文件全局、无 GC、无租户隔离 | `tools/group_state.go:161-215,242-260,360-386` | DB 行 + `owner`；删除群时 CASCADE 清成员 | 跨 owner 不可见/不可删 |
| **D12** | peer 广播串行 O(n)，无控流/去重 | `tools/send_message.go:380-428` | 有界并发投递 + 令牌桶 + `message_id` 去重 | 100 成员广播 < 1s 入队 |
| **D13** | 无 round/turn 真实语义；`Closed` 是死字段 | `tools/group_state.go:61-65` | **删除 round/Closed**；群没有"轮次" | schema 无 rounds/closed |
| **D14** | 无成员状态机/心跳/leave 广播 | `tools/group_state.go:296-327` | `status ∈ {alive,left}`；leave 后不再投递；广播 `event(member.left)` | leave 后成员收不到新群发 |
| **D15** | 无 correlation/idempotency/消息 ID，无法回放去重 | `tools/send_message.go:292,336` | 复用 ① 信封：`message_id`/`idempotency_key` | 重发同 key 不重复 |
| **D16** | 无权限模型；跨组目标校验仅 `sendToAgent` 有 | `tools/send_message.go:123-132,240` | 统一校验：owner + 成员 + 能力位（§7） | 非成员/跨 owner 拒绝 |
| **D17** | 成员互相看不到发言；@ 解析过窄（`@agent:qa` 被丢弃→变广播） | `tools/send_message.go:431-447` | @ 支持 `role`/`role/instance`，歧义报错；成员只能看到「发给自己的那份」；跨成员信息由 subagent 主动汇报（inspect），平台不做聚合（§6） | `@agent:qa` 定向或报歧义 |

> D2/D5/D7/D13 是「文档声称有、代码没有」的典型漂移（`docs/agent/tools.md:98,107-112`）；v2 的立场是：**没有的能力不留在 schema 里**。

---

## 3. 模型：只有群发和单发

### 3.1 定义

```text
单发（direct）
  SendMessage(to = agent:<role>/<instance> | session:<channel>:<chatID> | <IM 地址>)
  → 1 条 outbound（发送方库）
  → 1 条 inbound （收件方库）

群发（broadcast）
  SendMessage(to = group:<grp_id>)
  → 1 条 outbound（发送方库，带 message_id + group_id）
  → N 条 inbound （每个 status='alive' 的成员各一条，带同一个 message_id + group_id + sender_*）
```

**群发不是消息类型，是路由展开**：信封的 `to.type` 只有 `user/agent/session/im`，不存在 `group`。`group_id` 只是标记字段。

### 3.2 明令删除的概念

| 概念 | 处置 |
|---|---|
| 双模型分层（会议 / 对等总线） | 删除 |
| 统一会话抽象层（v1 命名） | 删除（不需要统一抽象，因为只有一个模型） |
| 群级消息表 / 群组独立库 | 删除 |
| 主持人 / 观察者角色 / 发言权 | 删除 |
| `rounds_used` / `max_rounds` / `group_rounds` | 删除 |
| `history_visibility` / `visibility` | 删除（成员能读的 = 自己会话里收到的那一份） |
| `kind=meeting|peer` / `status=open|closed|archived` | 删除（群只有"存在"，删除即硬删） |
| `peer:` 地址 | 删除 |

### 3.3 成员能看到什么（诚实边界）

- 群发给 N 个成员 = **N 份独立副本**。每个成员在自己的会话上下文里看到「发起者发给我的一条消息」。
- **其他成员的回答不会自动进入你的上下文**（除非发起者再次群发）。
- 这与 IM 群聊不同，是用户模型（只有群发和单发）的直接推论。**平台不做群历史聚合**：群主通过 `SubAgent(action=inspect)` 查看子代理状态，子代理主动汇报；成员只读自己的会话库。

---

## 4. 存储 schema（主库 v74）

> 主库只加两张**小表**；不放任何消息。

### 4.1 `groups`

```sql
CREATE TABLE IF NOT EXISTS groups (
    id          TEXT PRIMARY KEY,              -- grp_<nanoid(12)>
    name        TEXT NOT NULL DEFAULT '',      -- 展示名
    owner       TEXT NOT NULL,                 -- 创建者会话地址 session:<channel>/<chatID>（见 §4.5）
    created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_groups_owner ON groups(owner, created_at);
```

### 4.2 `group_members`

```sql
CREATE TABLE IF NOT EXISTS group_members (
    group_id     TEXT NOT NULL,
    member_addr  TEXT NOT NULL,                -- agent:<role>/<instance> | session:<channel>/<chatID>
    status       TEXT NOT NULL DEFAULT 'alive' CHECK (status IN ('alive','left')),
    joined_at    DATETIME NOT NULL,
    PRIMARY KEY (group_id, member_addr),
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS ix_group_members_addr ON group_members(member_addr, status);
```

字段语义：

| 列 | 说明 |
|---|---|
| `member_addr` | **规范地址**（带前缀）。只允许两种：`agent:<role>/<instance>`（相对群 owner 会话解析）与 `session:<channel>/<chatID>`（绝对）。**不支持 `user:` 成员**（一个用户可能对应多渠道会话，无法唯一解析到会话库） |
| `status` | `alive` = 参与投递；`left` = 已退出，**不再投递**，但保留行用于审计 |
| `joined_at` | 首次加入时间；`left` 后再加入（`UPDATE status='alive'`）不刷新该值 |

### 4.5 地址解析规则（投递用）

```text
owner = session:<channel>/<chatID>            # 群的宿主会话（创建者所在会话）

agent:<role>/<instance>
  → session key = <owner.channel>:<owner.chatID>/<role>:<instance>
  → SessionDBRelPath("agent", key)

session:<channel>/<chatID>
  → SessionDBRelPath(channel, chatID)
```

约束：

1. **群必须由一个宿主会话创建**（main agent / user 会话）；**不允许子代理创建群**（子代理的 session key 含父会话前缀，作为 owner 会使相对解析二义）。
2. `member_addr` 的 `agent:` 形式是**相对于 owner 会话**的地址；跨 chat 的成员请用 `session:` 绝对地址。
3. `session:<channel>/<chatID>` 用 `SplitN(s, "/", 3)` 解析，`chatID` 可含 `/`。
4. 解析失败（会话不存在/agent 未注册）→ 投递记 `failed`；跳过该成员并记 warning。

### 4.3 为什么是两张小表

- 群是跨会话的**成员关系**，不属于任何一个会话库；
- 只有元数据（ID/名字/owner/成员），没有消息内容 —— 不违反「主库不放消息」；
- 迁移成本最低：两张表 + 三个索引，与现有 `cron_jobs`/`tenants` 同级。

### 4.4 迁移（主库 v73 → v74）

```text
migrateV73ToV74:
  CREATE TABLE groups ...
  CREATE TABLE group_members ...
  CREATE INDEX ...
```

- 幂等：`CREATE ... IF NOT EXISTS`；重复执行安全。
- **无数据迁移、无回填**（决策 D：旧群数据直接删）。
- 回滚：`DROP TABLE group_members; DROP TABLE groups;`（只影响群元数据，不碰消息）。

---

## 5. 群发流程（字段级）

### 5.1 创建群

```text
CreateChat(type=group, members=[addr...], name=...)
  id = "grp_" + nanoid(12)
  owner = <caller session address: session:<channel>/<chatID>>
  INSERT groups(id, name, owner, created_at=now)
  for each member:
      spawn/解析成功 → INSERT group_members(group_id, member_addr, 'alive', now)
      失败          → 不插入（返回 warnings[]）
  return {id, address:"group:"+id, members:[...], warnings:[...]}
```

- `owner` = 调用者的**会话地址**（由服务端会话上下文生成，不信任 payload）；群必须由宿主会话创建（§4.5）。
- 已存在的 `id` 冲突：重试生成（最多 3 次）；显式 ID 冲突返回 `ID_TAKEN`。
- spawn 失败**不入册**（修复 D8）。

### 5.2 群发

```text
Send(to="group:"+grpID, envelope)
  1. 解析 from（服务端上下文）
  2. SELECT owner FROM groups WHERE id=?           → 不存在: NOT_FOUND
  3. 成员校验：sender session == groups.owner ? 通过 :
       SELECT status FROM group_members
       WHERE group_id=? AND member_addr=?          → 非 alive: MEMBERSHIP_REQUIRED
  4. hop/idempotency 校验
  5. SELECT member_addr FROM group_members
       WHERE group_id=? AND status='alive'
       AND member_addr <> <from.addr>              → 收件人集合 R
     if len(R) > max_fanout(128): FANOUT_LIMIT
  6. token bucket(group, sender)                    → 超限: RATE_LIMITED
  7. message_id = "msg_" + ULID（本次逻辑消息的唯一 ID）
  8. 写发送方会话库 outbound 行（message_id, group_id, envelope）
  9. for addr in R (有界并发, 默认 8):
       写收件方会话库 inbound 行（message_id 相同, group_id, sender_*）
       成功 → 入队通知（busy/idle 见 ① §8）
       失败 → 记录 failed
 10. 失败者写 receipt(failed) 到发送方库
 11. return {message_id, recipients:[{addr,status}], duplicate}
```

### 5.3 原子性与部分失败

- **不承诺跨库原子性**（N 个独立 SQLite 文件，无分布式事务）。
- 语义 = **每个收件人 at-least-once**；收件方按 `message_id` 幂等（唯一索引，① §7.2）。
- 发送方重试同一 `idempotency_key`：outbound 行已存在 → 只补发 `failed` 的收件人，`duplicate=true`。
- 返回体必须列出每个收件人的状态，不允许"整体成功"的模糊结论。

### 5.4 群发副本的字段

| 字段 | 值 |
|---|---|
| `message_id` | 全副本共享同一个 `msg_<ULID>`（一条逻辑发言） |
| `group_id` | 群 ID |
| `sender_type`/`sender_id`/`sender_name` | 发起者 |
| `to.type`/`to.id` | `agent`/`session` + 该成员地址 |
| `structured_json.from.*` | 与 `sender_*` 一致 |

---

## 6. 群历史：不做聚合（拍板决策）

**结论：平台不提供群历史。**

- **不建工具/API**：没有群历史查询工具，没有对应的实现文件，没有 `GET /api/groups/{id}/history`。
- **不做跨会话库查询**：不按 `message_id` 合并、不设时间/条数窗口、不做 LRU 缓存、不定义跨库越权规则。
- **不注入任何成员会话**：成员只读自己的会话库（收到的那一份副本）。
- **群主怎么掌握全局**：通过 `SubAgent(action=inspect)` 查看子代理状态；子代理按 role prompt 主动向其汇报（`kind=report` / `kind=text` 单发）。
- **`group_id` 字段保留**：仅作消息标记（未来若真要聚合时有锚点），不参与路由，也不驱动任何查询。

> 理由：群主已有 inspect 能力，且子代理会主动汇报；再建一套跨库聚合会引入 N 路文件扫描、跨 owner 越权面和缓存一致性问题，收益为零。

---

## 7. 权限与归属

### 7.1 校验顺序

```text
1. 解析 from（服务端上下文，不信 payload）
2. 查 groups.id；owner 校验（群存在性）→ 不存在 NOT_FOUND
3. sender session == owner ? 通过 : 查 group_members(from.addr) → 非 alive：MEMBERSHIP_REQUIRED
4. 能力位（SubAgent 必须 capabilities.send_message=true）
5. hop/fanout/rate/idempotency
6. 写库 + 投递
```

### 7.2 操作权限

| 操作 | owner | alive 成员 | left 成员 | 其他 |
|---|---|---|---|---|
| 群发 | ✅ | ✅ | ❌ | ❌ |
| 读成员列表 | ✅ | ✅ | ❌ | ❌ |
| 加成员（`JoinGroup`/`AddMember`） | ✅ | ❌（自助加入需开关） | ❌ | ❌ |
| 移除成员 / leave | ✅ | ✅（仅自己） | — | ❌ |
| 删除群（硬删） | ✅ | ❌ | ❌ | ❌ |

- `groups.owner` 是唯一管理权来源；不引入 `role` 列（决策 B 只要四个字段）。
- 自助加入（`JoinGroup(group_id)`）：默认允许（与现状兼容）；若需要"只有 owner 能拉人"，用配置开关关闭。

### 7.3 归属隔离

- 隔离轴 = `groups.owner`（群宿主会话地址，`session:<channel>/<chatID>`）。
- 所有读写 SQL 强制 `owner = <caller session>` 或成员校验；ID 猜测返回 `NOT_FOUND`。
- 跨 owner 邀请：本期不支持（`CROSS_OWNER_DENIED`）。

---

## 8. ID 设计

```text
grp_<nanoid(12)>     例：grp_V1StGXR8_Z5j
```

- 字母表：`0-9A-Za-z_-`（64 字符），12 位 ≈ 64^12 ≈ 4.7×10^21。
- 生日碰撞：100 万个群，碰撞概率 ≈ 1 - exp(-n²/2N) ≈ 1.1×10^-10。
- 显式 ID：`^[A-Za-z0-9][A-Za-z0-9_-]{1,63}$`，全局唯一；冲突返回 `ID_TAKEN`。
- 旧 `g<N>` / `peer:<name>`：**不解析、不映射**（旧数据已删）。

---

## 9. hop / fanout / 限流

### 9.1 hop

- 信封 `hop` 初始 0（user/system 发起）。
- agent 收到消息后主动转发/群发：`hop = 收到消息的 hop + 1`。
- `hop >= max_hops`（默认 4）→ 拒绝，回执 `HOP_LIMIT`。
- 环检测：`causation_id` 链上出现过的 `message_id` 不再转发（保留最近 32 个 ID）。

### 9.2 fanout

| 限制 | 默认 | 可配 |
|---|---|---|
| 单次群发最大收件人 | 128 | 全局配置 |
| 群成员上限 | 128 | 全局配置 |
| 投递并发度 | 8 | 全局配置 |
| 单条 payload | 256 KiB | 固定 |

### 9.3 令牌桶

| 桶 | 速率 | 突发 |
|---|---|---|
| 每群 | 20 条/秒 | 40 |
| 每发送者 | 5 条/秒 | 10 |

- 计费单位 = **逻辑发送**（一次群发算 1 条，不是 N 条）。
- 超限返回 `RATE_LIMITED`，不静默丢弃。

### 9.4 N×N 防护（关键）

旧实现：主持人广播 N 个成员；成员 prompt 鼓励「向全员汇报」→ 每人再广播 N 次 → O(N²) full-turn。

新规则：

1. **成员回复默认单发给发起者**（prompt 约定 + 工具提示）。
2. 成员要群发必须显式 `to="group:..."`，受 fanout/hop/令牌桶约束。
3. 群发副本 `hop+1`；达到 `max_hops` 不再转发。
4. 同一 `causation_id` 链上的重复消息直接丢弃（环检测）。
5. `message_id` 保证同一逻辑发言不会因重试被重复计入历史。

---

## 10. 成员状态

```text
join ──▶ alive ──(leave)──▶ left
          ▲                  │
          └──(re-join)───────┘
```

| 状态 | 含义 | 触发 |
|---|---|---|
| `alive` | 参与投递 | 创建/加入/重新加入 |
| `left` | 已退出，不投递 | `LeaveGroup` / owner 移除 |

规则：

1. 群发只查 `status='alive'`。
2. `left` 保留行（历史审计），不物理删除。
3. 状态变更广播 `kind=event(event_type=member.left/joined)` 给其余 alive 成员（单发，不是群发；避免事件风暴）。
4. **没有 `busy`/`dead`/心跳**：投递失败由 receipt 表达，不改变成员状态。

---

## 11. 删除清单（决策 D：不迁移、不兼容）

| 对象 | 位置 | 处置 |
|---|---|---|
| `peer_groups.json` | `$XBOT_HOME/peer_groups.json` | 启动时不读取；提供一次性清理命令 `rm`（文档说明）；不导入 |
| 内存群存储 | `tools/group_state.go:19-40`（`GroupMembership`/`groupStore`） | 删除类型与全局变量 |
| peer 文件存储 | `tools/group_state.go:141-290`（`PeerGroup`/load/save/Flush） | 删除 |
| `FlushPeerGroups` | `tools/group_state.go:283` | 删除调用点与函数 |
| `peer:` 地址分支 | `tools/send_message.go:380-428`（`sendToPeerGroup`） | 删除 |
| `peer:` 解析 | `tools/send_message.go:85-100` | 删除分支 |
| `groupCounter` / `g<N>` | `tools/create_chat.go:13-14,170,242` | 删除；改用 `grp_<nanoid>` |
| `max_rounds` 参数 | `tools/create_chat.go:57,61-68` | 删除 |
| `Closed` 字段 | `tools/group_state.go:61-65` | 删除 |
| 词表 ID 生成 | `tools/peer_group.go:196-200` | 删除；`grp_<nanoid>` |
| 群组创建时的内存快照 | `tools/create_chat.go:189-242` | 改为 DB INSERT |
| `CreateGroup` 无条件覆盖 | `tools/create_chat.go:164-259` | 冲突报 `ID_TAKEN` |

保留并改造：

| 工具 | 改造 |
|---|---|
| `CreateChat(type=group)` | 写 `groups`/`group_members`；返回 `group:grp_*` |
| `SendMessage(to=group:)` | 群发展开（§5.2） |
| `JoinGroup` | `INSERT ... ON CONFLICT DO UPDATE status='alive'` |
| `LeaveGroup` | `UPDATE status='left'` |
| `ListGroupMembers` | 查 `group_members`（含 status）；校验调用者是成员 |

（**不新增群历史查询工具**：群历史不做聚合，见 §6。）

---

## 12. 迁移策略

| 阶段 | 内容 | 兼容 | 回滚 |
|---|---|---|---|
| M1 | 本文档 | — | — |
| M2（主库 v74） | 建 `groups`/`group_members`；新代码双写（DB + 旧内存，仅一个版本） | 旧路径仍可读内存群 | 关 flag，DROP 两张表 |
| M3 | 读路径切 DB；`peer:` 分支删除；`g<N>` 停止生成 | 旧 `g<N>` 群在重启后自然消失（本来也不持久） | 恢复旧二进制 |
| M4 | 删除 `group_state.go` 的旧实现与 `peer_group.go` 的 ID 生成 | 无 | 代码回滚 |

**没有数据迁移**（决策 D）：

- `peer_groups.json` 不导入；文档/CHANGELOG 明确"升级后旧 peer 组不存在"。
- 内存 `group:` 群本来就不持久化，升级后自然消失。
- 不提供 `legacy` 认领命令。

---

## 13. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 群发是 N 份副本，成员看不到别人回答 | 用户误以为"群聊" | 文档 + prompt 明确；群主用 `SubAgent(action=inspect)` 掌握成员状态，子代理主动汇报 | —（模型决定） |
| 部分投递失败 | 成员漏消息 | 逐成员 receipt + 同 key 重试只补失败者 | 重发 |
| 限流误伤 | 群发被拒 | 默认值足够 128 人规模；错误码明确 | 调高阈值 |
| 删除旧 peer 数据 | 用户旧群消失 | CHANGELOG 提前说明；文档明确 | 无法回滚（数据已删） |
| 成员校验变严 | 现有跨组用法被拒 | 发布说明 + 先 warn 日志后拒绝（一个版本） | 配置开关 `groups.strict_membership=false` |
| 群 owner 单点 | owner 退出后无人管理 | owner 可转移（后续）；本期 owner 永久 | — |

---

## 14. 验收判据

### 14.1 功能

1. 创建群：返回 `grp_*`；重启后 `GET /api/groups/{id}` 仍存在。
2. 群发 3 成员：每人会话库各 1 条 inbound（`group_id`/`message_id`/`sender_*` 齐全），发送方 1 条 outbound；`recipients[]` 长度 = 3。
3. 非成员群发返回 `MEMBERSHIP_REQUIRED`；跨 owner 返回 `NOT_FOUND`。
4. 成员 leave 后：不再收到新群发；其余成员收到 `event(member.left)`。
5. `@agent:qa`（无 instance）唯一则定向成功；多实例返回 `AMBIGUOUS_MENTION`。
6. 重复 `idempotency_key` 不重复投递；返回 `duplicate=true`。
7. hop 达到 `max_hops` 的消息被拒绝并回执。
8. 不存在群历史查询工具/API（全仓无对应实现文件）；群主通过 `SubAgent(action=inspect)` 获取子代理状态，子代理主动汇报。
9. 群成员上限/ fanout 超限返回 `FANOUT_LIMIT`；令牌桶超限返回 `RATE_LIMITED`。

### 14.2 删除/迁移

10. 全仓 grep 无 `peer_groups.json` 读写、无 `PeerGroup`、无 `GroupMembership`、无 `FlushPeerGroups`。
11. `peer:` 地址返回 `NOT_FOUND`。
12. 重启后无内存依赖（没有 `groupStore` 也能工作）。
13. 主库 `groups`/`group_members` 行数与创建/加入操作一致；无消息表。

### 14.3 压测

14. 50 成员群，连续 10 次群发：消息总数 = 50×10（每成员一份），无 N×N。
15. 1000 条群消息写入 p99 < 50ms（本地 SQLite）。
16. 128 成员群发入队 < 1s（并发度 8，不含成员处理）。

### 14.4 回归

17. 现有 1:1 `agent:`/`session:` 语义不破坏。
18. `go test ./tools/... ./storage/... ./agent/...` 通过。

---

## 15. 受影响代码清单（file:line）

### 15.1 新增

| 文件 | 作用 |
|---|---|
| `tools/group_store.go` | `groups`/`group_members` 读写（SQLite） |
| `tools/group_ids.go` | nanoid、ID 校验 |
| `storage/sqlite/migrations.go`（v74） | 建两张表 + 索引 |
| `tools/group_tools.go` | Create/Join/Leave/List 适配 |

### 15.2 修改

| 文件:行 | 改动 |
|---|---|
| `tools/group_state.go` | 整文件删除（旧实现）；新实现放 `group_store.go` |
| `tools/create_chat.go:13-14,170-259` | 删除 `groupCounter`；创建写 DB；spawn 失败不入册 |
| `tools/create_chat.go:57,61-68` | 删除 `max_rounds` |
| `tools/send_message.go:85-100` | 删除 `peer:` 分支 |
| `tools/send_message.go:240-353` | 群发：成员校验 + 展开 + 限流 + receipt |
| `tools/send_message.go:380-428` | 删除 `sendToPeerGroup` |
| `tools/send_message.go:431-447` | `parseMentions` 支持 role-only/歧义 |
| `tools/peer_group.go:56-140` | Join/Leave 改为 DB 操作 |
| `tools/peer_group.go:159-175` | ListGroupMembers 改为 DB + 成员校验 |
| `tools/peer_group.go:196-200` | 删除词表 ID |
| `agent/engine_wire.go:729-741` | 群组 prompt 改为「群发/单发」语义 |
| `docs/agent/tools.md:90-115` | 更新群组语义 |

### 15.3 删除

| 文件/片段 | 依据 |
|---|---|
| `tools/group_state.go` 全部 | 决策 D |
| `peer_groups.json` 读写 | 决策 D |
| `SendMessage` 的 `peer:` 分支 | 决策 D |
| `max_rounds`/`Closed`/`rounds_used` | 决策 B |
| `GroupMembership`/`PeerGroup` 类型 | 决策 D |

---

## 16. 开放问题

无开放问题（Q1–Q3 已由用户拍板，见 `sendmessage-structured.md` §17）。与本文件相关的是 **Q2：不做群历史聚合**（群主用 `SubAgent(action=inspect)`，子代理主动汇报）。
