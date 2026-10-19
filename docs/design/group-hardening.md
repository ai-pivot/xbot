# 群组（群发 / 单发）—— 详细设计（v2.1）

> 状态：设计稿（**不含实现代码**）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`
> 关联：`sendmessage-structured.md`（①）、`message-sender-id.md`（③）、`court-reporting.md`（⑤）
> **版本：v2.1（2026-10-19）** —— 在 v2 基础上按用户批注补回「群组展示」：**保留展示 API（用户视角有用），但不做 LRU、不建单独记录**；展示 = 事件流 + 按需查询当前会话库，后端零额外存储。
> **v2（2026-10-19）** —— 按用户拍板决策重写。v1 的双模型分层（会议模型 + 对等总线）、群组独立库、群级消息表、轮次/主持人/发言权抽象**全部作废**。

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
| G-7 | v2 初稿把「群历史」整体判死（无任何查询视图） | **补回「群组展示」**：`GET /api/groups/{id}/messages`（当前会话视角 + 游标分页）+ `group.message` 事件流；**不做跨库聚合、不做 LRU、不建聚合表/缓存、不做定时扫表**（§6） |

---

## 0. 一页纸结论

1. **用户模型只有两个动作**：
   - **单发** = `SendMessage(to="agent:x/y")` / `session:` / IM，写 1 条 outbound + 1 条 inbound。
   - **群发** = `SendMessage(to="group:grp_xxx")`，**遍历 `group_members(status='alive')`，对每人各发一份一样的 SendMessage**；各写各的会话库，消息带 `group_id` + `sender_*` 标记。
2. **没有「群组消息」实体**：不存在 `to.type=group` 的消息、不存在群级消息表、不存在群组独立库。群 ID 只是**标记 + 成员表主键**。
3. **群状态 = 主库两张小表**：`groups(id, name, owner, created_at)` + `group_members(group_id, member_addr, status, joined_at)`。
4. **不做群历史聚合，但保留群组展示**：平台**不跨会话库合并**、不建聚合表/缓存/LRU、不设时间/条数窗口、不做定时扫表。展示由两件事构成：① **按需查询**当前用户会话库（`GET /api/groups/{id}/messages`，游标分页，只返回当前会话视角）；② **事件流**（写入时推 `group.message`，前端实时渲染）。群主掌握全局仍靠 `SubAgent(action=inspect)` + 子代理主动汇报。
5. **保留必修 bug 修复**：每条消息带发送者标识；群发前校验成员身份；hop/fanout/令牌桶防 N×N；ID 用 `grp_<nanoid(12)>`；成员状态 `alive/left`（`left` 不再投递）。
6. **旧数据直接删**：`peer_groups.json`、内存 `GroupMembership`、`peer:` 分支、`groupCounter`、`FlushPeerGroups`，不迁移不兼容。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 群组语义唯一，消除双模型 | 代码里只有 `group:` 一种群地址；无双模型分支 |
| G2 | 群状态持久化 | 重启后 `groups`/`group_members` 完整；内存无群状态 |
| G3 | 群主可掌握成员状态，成员有可用的群组展示 | `SubAgent(action=inspect)` + 子代理主动汇报掌握全局；成员/群主可通过 `GET /api/groups/{id}/messages` 读取**自己会话视角**的群消息，并通过 `group.message` 事件实时渲染；不做跨库聚合 |
| G4 | 防 N×N | hop 限制 + fanout 上限 + 群/发送者令牌桶 + 回复默认单发 |
| G5 | 成员身份可校验 | 非成员群发返回 `MEMBERSHIP_REQUIRED` |
| G6 | ID 全局唯一 | `grp_<nanoid(12)>`；重启/多进程不重复；创建 100 万次无碰撞（概率论证） |
| G7 | 与 ①③ 统一 | 群发副本是结构化消息；`sender_*` = 信封 `from.*` |
| G8 | 旧数据干净退场 | 仓库内无 `peer_groups.json` 读写；无 `GroupMembership` |

### 1.2 非目标

- 不做分布式共识/多节点群组（单进程部署；多进程不在本期）。
- 不做群公告/置顶/表情/已读回执/输入中状态。
- 不做群级消息表、不做群组独立库。
- **不做跨会话库聚合**：不做 GroupHistory API、不建聚合结果表、不做 LRU 缓存、不设 7 天/2000 条窗口、不做定时扫表、不提供上帝视角（群主用 `SubAgent(action=inspect)` + 子代理主动汇报）。展示只读当前会话视角（§6）。
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
| **D3** | 群组无消息历史、无展示视图 | `tools/group_state.go:11-18` | **保留群组展示**：`GET /api/groups/{id}/messages`（当前会话视角 + 游标分页）+ `group.message` 事件流；**不做跨库聚合**（群主用 `SubAgent(action=inspect)` 看子代理状态，子代理主动汇报）；`group_id` 保留为查询键 | 群成员能在自己会话里看到群消息时间线；全仓无聚合表/LRU |
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
- **其他成员的原始回答不会自动进入你的 LLM 上下文**（除非发起者再次群发）。
- 但**展示层**可以看到当前会话视角的完整时间线：我发的（`direction=out`）+ 发给我的（`direction=in`），数据来自**自己的会话库**（§6）。这两者不矛盾：展示不等于上下文注入。
- 这与 IM 群聊不同，是用户模型（只有群发和单发）的直接推论。**平台不做群历史聚合**：群主通过 `SubAgent(action=inspect)` 查看子代理状态，子代理主动汇报。

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
  8. 写发送方会话库 outbound 行（message_id, group_id, envelope；`envelope.meta.recipients` = 本次展开的收件人清单，供流向图渲染，不新增表/记录）
  9. for addr in R (有界并发, 默认 8):
       写收件方会话库 inbound 行（message_id 相同, group_id, sender_*）
       成功 → 入队通知（busy/idle 见 ① §8）
       失败 → 记录 failed
 10. 失败者写 receipt(failed) 到发送方库
 11. 向实际写入的会话推送 `group.message` 事件（收件人 in / 发送方 out；不落盘、失败不影响投递）
 12. return {message_id, recipients:[{addr,status}], duplicate}
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

## 6. 群组展示：事件流 + 按需查询（零额外存储）

> **结论：保留群组展示 API（用户视角有用）；展示 = 读当前会话库 + 实时事件，后端不新增任何存储。**
> 不做 LRU、不加单独记录、不建聚合表/缓存、不做定时扫表、不设窗口。

### 6.1 为什么能做，且为什么不用聚合

消息本来就在各成员会话库里：`session_messages` 已带 `group_id` / `sender_*` / `kind` / `correlation_id`（① §7.2）。当前用户打开群组时要看的本来就是「**我发的 + 发给我的**」——这恰好是他自己那个会话库里已有的行：

| 视角 | 会话库里的行 | 展示含义 |
|---|---|---|
| 成员 | `direction=in` 的群发副本 + 自己 `direction=out` 的回复 | 收到的通知 + 我的发言 |
| 群主 / 发起者 | 每次群发的 `direction=out` 行（一条逻辑消息一行）+ 成员单发回来的 `direction=in` 行 | 会议通知 + 各部门回奏 |
| 旁观者 | 无行（非成员） | 不可见 |

因此查询是**单库、单索引、keyset 分页**，不是 N 路扫描。反过来，跨会话库合并需要枚举成员库、合并排序、处理跨 owner 越权；要提速就得建聚合表/缓存/LRU/窗口/定时扫表 —— 全部引入一致性、GC、越权面，而收益只是"看到别人的原始副本"，用户并不需要。**不做聚合 ≠ 没有展示。**

### 6.2 数据基底（已有字段，不新增）

| 字段 | 来源 | 展示用途 |
|---|---|---|
| `group_id` | ① §7.2 新增列 | 过滤键；唯一查询入口 |
| `sender_type` / `sender_id` / `sender_name` | ③ 新增列 | 头像/徽标/"谁说的" |
| `kind` | ① 新增列 | 选择卡片形态（text/ask_user/report/decision/event/receipt/answer） |
| `structured_json` | ① 新增列 | 卡片 `payload`（证据、选项、批红结论等） |
| `correlation_id` / `reply_to` | ① 新增列 | 线程视图、问答连线 |
| `direction`（`in`/`out`） | ① 新增列 | 当前视角左右分栏、"我发的"与"发给我的" |
| `structured_json.meta.recipients` | 信封内既有 `meta`（不新增记录） | 流向图：outbound 行的收件人清单与投递状态 |
| `id` / `created_at` | 既有列 | 游标分页与时间线排序 |

**不新增表、不新增列、不新增缓存。**

### 6.3 形态一：按需查询（事实源）

```http
GET /api/groups/{id}/messages?cursor=<msg_id>&limit=50
```

请求参数：

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `id`（path） | string | 是 | `grp_<nanoid(12)>` |
| `cursor` | string | 否 | 上一页最后一条的 `message_id`；缺省从最新开始 |
| `limit` | int | 否 | 默认 50，最大 200 |
| `kind` | string | 否 | 可选过滤（如 `ask_user` / `report` / `decision`） |
| `correlation_id` | string | 否 | 线程视图：只取该请求—响应链 |
| `unanswered` | bool | 否 | 收件箱：只取 `kind=ask_user AND answered=false` |

响应（示例）：

```json
{
  "group_id": "grp_V1StGXR8_Z5j",
  "viewer": {"session": "session:web:chat_123", "member_status": "alive"},
  "items": [
    {
      "message_id": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
      "direction": "in",
      "kind": "ask_user",
      "sender": {"type": "agent", "id": "hubu/main", "name": "户部"},
      "correlation_id": "cor_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
      "reply_to": "",
      "created_at": "2026-10-19T12:00:00.123Z",
      "text": "户部请旨：PPU 集群是否按计划今夜扩容？",
      "payload": {"questions": ["..."], "allow_cancel": true},
      "answered": false
    }
  ],
  "next_cursor": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "has_more": true
}
```

分页语义（设计级）：

- `cursor` 对外是不透明字符串，等于上一页最后一条的 `message_id`；服务端解析成行 `id` 做 keyset：
  `WHERE group_id = ? AND id < :cursor_row_id ORDER BY id DESC LIMIT :limit + 1`。
- 多取一条判断 `has_more`；`next_cursor` 取本页最后一条。
- 无 `cursor` = 第一页（最新）。**没有 offset 深翻页问题。**
- 返回顺序：`id DESC`（最新在前）；前端时间线可反转渲染。
- `answered` 由同一会话库内是否存在 `kind=answer AND reply_to=<id>` 派生，不建状态列（① §7.5）。

权限：

| 调用者 | 结果 |
|---|---|
| `groups.owner` | ✅ 只读**自己会话库**的群消息（自己发的 + 回给自己的） |
| `status='alive'` 成员 | ✅ 只读**自己会话库**的群消息 |
| `status='left'` 成员 | ❌ `403 MEMBERSHIP_REQUIRED` |
| 非成员 / 跨 owner | ❌ `404 NOT_FOUND`（不泄露群存在性） |

- `viewer.session` 由服务端会话上下文决定，**不接受客户端传 session 参数** → 没有跨库读的口子。
- **没有上帝视角**：群主也只能看到自己会话库里的行；全局掌握仍靠 `SubAgent(action=inspect)` + 子代理主动汇报。
- 查询不写库、不产生事件、不改变任何状态（纯只读）。

### 6.4 形态二：事件流（实时性）

- **触发点**：群发/单发写库成功之后（§5.2 第 8/9 步），沿**现有 WS/SSE 通道**推一条 `group.message`。不新增 broker、不建 outbox、不落盘。
- **推送范围**：只推给**实际写入了该消息的会话**——群发时给每个收件人会话推 `direction=in`，给发送方会话推 `direction=out`。每个连接只收到自己会话的事件，天然零越权。
- **事件是提示，不是事实源**：丢事件不影响正确性；客户端按 `message_id` 幂等去重，重连后用 `GET ...?cursor=<last_message_id>` 补齐。

核心 schema（完整字段与校验见 ① §11.1）：

```json
{
  "type": "group.message",
  "v": 1,
  "group_id": "grp_V1StGXR8_Z5j",
  "message_id": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "sender_type": "agent",
  "sender_id": "hubu/main",
  "sender_name": "户部",
  "kind": "report",
  "correlation_id": "cor_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "ts": "2026-10-19T12:00:00.123Z"
}
```

可选补充字段（前端需要时携带，不改变核心契约）：`direction`（`in|out`）、`reply_to`、`text` 预览（≤200 字）、`group_seq` 不提供（无额外记录，顺序由 `message_id`/`ts` 表达）。

- **顺序**：同一会话库内按行 `id`/`ts` 递增；跨会话库不承诺全局顺序（本来就不是聚合视图）。
- **大消息**：事件只带元数据 + 文本预览；完整 `payload` 由客户端按 `message_id` 调查询接口取，避免事件风暴。

### 6.5 明确不做（红线）

| 不做 | 原因 |
|---|---|
| **不做**跨会话库聚合 / `GroupHistory` API | N 路文件扫描 + 跨 owner 越权面；用户要的只是自己视角 |
| **不做** LRU / 内存缓存 / 聚合结果表 | 双写一致性与 GC；消息量本就是单会话库规模 |
| 7 天 / 2000 条窗口 | 无意义截断；游标分页本身解决数据量 |
| 定时扫描 / 后台物化 / outbox | 没有额外存储，就不需要维护它 |
| 上帝视角 / 跨成员原始副本 | 群主用 `SubAgent(action=inspect)` + 主动汇报 |
| 事件重放缓冲 / replay 表 | 客户端用 cursor 补齐，服务端不存事件 |

### 6.6 前端形态（展示层设计）

| 形态 | 数据来源 | 交互要点 |
|---|---|---|
| **实时时间线**（消息流式出现） | 事件流 `group.message` + 查询首屏 | 新消息入场动画；日期分隔吸顶；未读锚点；断线重连显示「正在补齐…」 |
| **结构化消息卡片** | `kind` + `payload` | `ask_user`→提问卡片（单选/多选/自定义/取消）；`report`→奏折卡片（状态/证据/待批）；`decision`→批红卡片（准/驳/改 + 朱批视觉） |
| **成员状态灯** | `group_members.status` + 状态事件 | 绿=`alive`/空闲，黄=`busy`（运行时态，不落库），灰=`left`；呼吸动画；hover 显示最近活动 |
| **消息流向图** | `sender` + recipients（outbound 行的投递明细） | 谁→群→谁 的拓扑：群发画扇形、单发画直线；点节点筛选时间线 |
| **未回答提问收件箱** | `kind=ask_user AND answered=false` 查询 | 集中列表、批量回复、按 `correlation_id` 展开线程 |
| **线程视图** | `correlation_id` | 提问→回答→后续动作连成线；可折叠；跨卡片高亮 |

**"高级/炫酷"的视觉与交互规格**（展示层，不引入数据依赖）：

- **视觉语言**：朝廷主题可选皮肤（奏折纸纹、朱批红、墨色）；默认仍遵循系统深浅色，不牺牲可读性。
- **动效**：消息入场 120–180ms ease-out；状态灯呼吸；流向图节点 hover 抬升；`prefers-reduced-motion` 自动降级为无动画。
- **性能预算**：首屏 ≤50 条；虚拟滚动支撑 10k 行；渲染 60fps；事件在 16ms 帧内合并，最多触发一次渲染。
- **状态完备**：空群、加载骨架、权限拒绝、断线重连、补历史失败都有明确空态/错误态。
- **可访问性**：键盘可达、焦点环、卡片语义角色；状态不能只靠颜色区分（同时用文字/图标）。
- **移动端**：时间线单列；流向图折叠为「谁发的 / 发给谁」标签；收件箱置顶。

### 6.7 性能与容量

- 单库单索引：`ix_sm_group_cursor ON session_messages(group_id, id DESC) WHERE group_id <> ''`（① §7.2），查询 O(log n + limit)。
- 50 条/页、keyset 分页，无深翻页退化；10 万行内本地 p99 目标 < 10ms。
- 事件只在写库后内存广播，**不增加持久化写**；推送失败不影响投递。
- 不做缓存的前提：消息规模 = 单会话库规模，SQLite 本地读足够快；缓存只会带来一致性负担。

### 6.8 与旧「不做聚合」结论的关系

- **保留**：不做跨会话库聚合、不建群级消息表、不提供上帝视角。
- **修正**：不是"没有展示"；展示 API 保留，但只读当前会话视角、无缓存、无额外记录。
- **命名**：接口统一叫 `GET /api/groups/{id}/messages`，不叫 `history`（避免暗示跨会话库历史）。

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
| **读群消息（当前会话视角）** | ✅ | ✅ | ❌ | ❌ |
| 读成员列表 | ✅ | ✅ | ❌ | ❌ |
| 加成员（`JoinGroup`/`AddMember`） | ✅ | ❌（自助加入需开关） | ❌ | ❌ |
| 移除成员 / leave | ✅ | ✅（仅自己） | — | ❌ |
| 删除群（硬删） | ✅ | ❌ | ❌ | ❌ |

- `groups.owner` 是唯一管理权来源；不引入 `role` 列（决策 B 只要四个字段）。
- 自助加入（`JoinGroup(group_id)`）：默认允许（与现状兼容）；若需要"只有 owner 能拉人"，用配置开关关闭。

### 7.3 归属隔离

- 隔离轴 = `groups.owner`（群宿主会话地址，`session:<channel>/<chatID>`）。
- 所有读写 SQL 强制 `owner = <caller session>` 或成员校验；ID 猜测返回 `NOT_FOUND`。
- **展示查询的隔离轴是「调用者自己的会话库」**：`viewer.session` 由服务端上下文决定，不接受客户端参数；查询不触达其他成员的库（§6.3）。
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
4. **没有 `busy`/`dead`/心跳**：投递失败由 receipt 表达，不改变成员状态。展示层的「忙」来自运行时是否正在跑 turn（内存态/事件），**不落 `group_members`、不新增列**。

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

（**不新增跨库群历史工具**：群组展示只读当前会话视角，接口为 `GET /api/groups/{id}/messages`，不做聚合，见 §6。）

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
| 展示查询慢（长历史/大群） | 单库查询 | 首屏卡顿 | `(group_id, id DESC)` 复合索引 + 50 条/页 + keyset 分页；无跨库扫描 | 调小 `limit` / 补索引 |
| 事件丢失或断线 | WS/SSE 断开 | 时间线缺消息 | 事件按 `message_id` 幂等；重连用 `cursor` 补齐；查询是事实源 | 无需回滚（无状态） |
| 展示被误解为上下文注入 | 用户看到时间线 | 误以为成员共享上下文 | UI 明确「仅当前会话视角」；§3.3 与 §6.1 说明 | — |

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
8. 群成员调用 `GET /api/groups/{id}/messages`：只返回自己会话视角（`direction` + `sender_*` 齐全）；`limit=50`、`cursor` 可续读，无重复/遗漏。
9. 非成员返回 `404 NOT_FOUND`；`left` 成员返回 `403 MEMBERSHIP_REQUIRED`；客户端伪造 `session` 参数无效（服务端以上下文为准）。
10. 全仓无 `GroupHistory` API、无聚合表、无 LRU/缓存/窗口/定时扫表；`group_id` 只用于单库查询。
11. 群主仍通过 `SubAgent(action=inspect)` + 子代理主动汇报掌握全局（无上帝视角）。
12. 事件流：写库后受影响会话收到 `group.message`；断线期间的消息可用 `cursor` 补齐；重复事件按 `message_id` 幂等。
13. 群成员上限/ fanout 超限返回 `FANOUT_LIMIT`；令牌桶超限返回 `RATE_LIMITED`。

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
| `channel/web/web_group_messages.go` | `GET /api/groups/{id}/messages`：成员校验 + 单库 keyset 分页 |
| `protocol/group_events.go` | `GroupMessageEvent`（`group.message`）传输事件定义 |

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
| `protocol/events.go` / `channel/subscription.go` | 注册并分发 `group.message` 事件（不落盘） |
| `tools/send_message.go`（写库成功后） | 向受影响会话推送 `group.message`（收件人 `in`、发送方 `out`） |
| `storage/sqlite/sessiondb.go` | 新增 `ix_sm_group_cursor(group_id, id DESC)`（① §7.2） |
| `web/src/components/group/*` | 时间线、结构化卡片、状态灯、流向图、收件箱、线程视图 |
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

无开放问题（Q1–Q3 已由用户拍板，见 `sendmessage-structured.md` §17）。与本文件相关的是 **Q2 的修订版：不做群历史聚合，但保留群组展示 API**——`GET /api/groups/{id}/messages`（当前会话视角 + 游标分页）+ `group.message` 事件流；不建聚合表/缓存/LRU/窗口/定时扫表（§6）。
