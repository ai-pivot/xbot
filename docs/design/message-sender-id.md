# 每条消息记录发送人 ID —— 详细设计（v2）

> 状态：设计稿（**不含实现代码**）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`
> 关联：`sendmessage-structured.md`（①）、`group-hardening.md`（④）、`court-reporting.md`（⑤）
> **版本：v2（2026-10-19）** —— 按用户拍板决策修订：sender 三列**只加在会话分库**，主库不承载任何消息。

---

## 修订记录（v1 → v2）

| # | v1 | v2 |
|---|---|---|
| S1 | 迁移 = 主库 v74 + 会话库 v4 | **只改会话库 schema（v3 → v4）**；主库不加列、不建表 |
| S2 | 群组消息以主库中心表为权威 | **不存在群级消息表**；权威 = 每个收件方会话库的 inbound 行 |
| S3 | 旧行 `sender_id` 由主库回填作业批量填 | 回填作业改为**逐会话库**执行（主库只提供 owner 映射） |
| S4 | 群组场景用中心表 `from_*` 索引 | 群历史用会话库 `group_id` 索引（④） |

---

## 0. 一页纸结论

1. **新增三列**（**仅会话分库**）：`sender_type`（`user|agent|system`）、`sender_id`（用户 id 或 `role:instance`）、`sender_name`（展示名，denormalized）。
2. **不采用「单一 sender_id 带命名空间」**：类型过滤是高频操作（群发/审计/UI），显式枚举列可索引、可约束、无解析歧义；`agent` 的 `role:instance` 本身含 `:`/`/`，拼进命名空间会引入歧义。
3. **`role` 列保持 LLM 语义不变**（`user`/`assistant`/`tool`/`system`）；`sender_*` 是正交的「谁说的」，两者不可混用。
4. **迁移只碰会话库**：`sessionSchemaVersion 3 → 4`，三列 `NOT NULL DEFAULT ''`，纯加法；旧数据按 `role` 回填 `sender_type`，`sender_id` 能确定才填（会话属主），否则留空（legacy）。
5. **写入收口在 `appendMessageWith`**（`storage/sqlite/history.go:291-337`）：所有插入点通过 `llm.ChatMessage` 的新字段传入，禁止各调用点手拼 SQL。
6. **主库零改动**：主库仅新增 `groups`/`group_members` 两张群组小表（④），**不加消息列、不建消息表**。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 每条消息可回答「谁说的」 | 新写入的 `session_messages` 行 `sender_type` 非空；system 行 `sender_id='system'` |
| G2 | 群发场景可区分发言者 | 同一群的不同成员会话里，`sender_id` 与 `from.id` 一致 |
| G3 | 旧数据不丢、不猜错 | 迁移后旧行 `sender_type` 按 role 分类；`sender_id=''` 明确表示 legacy |
| G4 | 查询可索引 | `(tenant_id, sender_type, sender_id)` 索引；按发送者过滤不全表扫 |
| G5 | UI 可显示「谁说的」 | Web/CLI 历史行展示发送者徽标；legacy 行不显示 |
| G6 | 与 ① 的信封字段同构 | `sender_*` 与 `structured_json.from.*` 一致（由 MessageService 统一填充） |

### 1.2 非目标

- 不改变消息排序/turn 语义。
- 不引入用户表/权限表（`sender_id` 仍是自由字符串，与现有 `user_chats.sender_id`、memory 的 user_id 一致）。
- 不把 `sender_id` 作为外键（跨会话库无法 FK）。
- 不实现「消息级已读/未读」。
- 不做「群级发送者索引表」（群历史靠跨库聚合，见 ④）。
- **不迁移**任何历史消息到别处；旧行原地加列。

---

## 2. 现状调研（事实 + 证据）

### 2.1 存储结构（v71 拆库后）

**会话库**（消息的真实归属）：

- DDL：`storage/sqlite/sessiondb.go:51-90`（`sessionSchema`）
- 版本：`storage/sqlite/sessiondb.go:45`（`sessionSchemaVersion = 3`）
- 路径：`storage/sqlite/sessiondb.go:211-222`（`SessionDBRelPath`）
- 惰性迁移：`storage/sqlite/sessiondb.go:296-320` —— **显式列清单** `INSERT ... SELECT`，新列自动取默认值（v1 文档误写为 `SELECT *`，本版更正）

**主库**（只有注册表与全局表）：

- `tenants`（含 `db_path`/`migrated`/`preview`）：`storage/sqlite/schema.go:17-38`
- v71 之前的 `session_messages` 表仍残留在 DDL 中（`storage/sqlite/schema.go:40-74`），仅服务 `migrated=0` 的会话；**不属于新设计的一部分**，收尾处置见开放问题 Q1。

### 2.2 写入路径

| 写入点 | 证据 | 角色 |
|---|---|---|
| 通用单条插入 | `storage/sqlite/history.go:291-337`（`appendMessageWith`） | 所有普通消息的收口点 |
| 批量插入 | `storage/sqlite/history.go:382-405`（`AppendMessages`） | 调用 `appendMessageWith` |
| 命令行 | `storage/sqlite/history.go:355-372`（`AppendCommandMessage`） | 单独 INSERT |
| 控制记录 | `storage/sqlite/history.go:446-525`（`AppendControl`） | 单独 INSERT |
| 上下文快照 | `storage/sqlite/history.go:527-600` | 单独 INSERT |
| 惰性迁移复制 | `storage/sqlite/sessiondb.go:296-320` | 显式列清单，列序敏感 |

### 2.3 上层调用点（消息来源）

| 调用点 | 证据 | 当前 sender 信息 |
|---|---|---|
| 主 Agent 用户消息 eager-save | `agent/agent.go:3985-4010` | `msg.SenderID` 可用（`bus.MessagePayload`） |
| 子代理 interactive 用户消息 | `agent/interactive.go:1191-1200,1985-2025` | 有 `senderID` 变量 |
| 一次性 SubAgent task | `agent/engine_wire.go:1654-1660` | 父 agent 身份 |
| SubAgent 初始 task | `agent/engine_wire.go:785` | 父 agent 身份 |
| 卡片回答 | `agent/card_handler.go:38-42` | 有 `msg.SenderID` |
| view_image 注入 | `agent/engine_run.go:2453-2462` | Internal 行，system |
| 取消批次 | `agent/agent_process.go:520` | 混合 |
| 持久化桥 | `agent/persist_bridge.go:58,78` | 批量 |

### 2.4 读取/展示路径

| 路径 | 证据 | 当前字段 |
|---|---|---|
| 历史查询 SQL | `storage/sqlite/history.go:857-899` | 显式列，无 sender |
| 协议 HistoryMessage | `protocol/events.go:258-330` | 无 sender |
| 转换函数 | `channel/subscription.go:1408-1490` | 构造 HistoryMessage，无 sender |
| Web 类型 | `web/src/types/shared.ts:575-615` | 无 sender |
| Web user 行 | `web/src/components/agent/UserMessage.tsx:1-80` | 只渲染 content |
| WS 信封 | `protocol/ws.go:97-128` | 已有 `SenderID`/`SenderName`（入站方向） |

### 2.5 入站消息已有 sender，但没落库

`bus.MessagePayload` 已含 `Channel`/`SenderID`/`SenderName`/`ChatID`（`bus/bus.go:13-32`），但主 Agent 只把它用于路由/settings/usage（`agent/engine_wire.go:146-147`）。这是本次要补的缺口。

### 2.6 user_chats 已有会话属主

```sql
CREATE TABLE user_chats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    sort_order INTEGER DEFAULT 0,
    UNIQUE(channel, sender_id, chat_id)
);
```

证据：`storage/sqlite/schema.go:288-298`；查询接口 `storage/sqlite/chat.go:419-437`（`GetSenderForChat`）。主库保留该表——回填作业需要它提供 owner 映射。

---

## 3. 字段设计

### 3.1 三列定义（会话库）

```sql
ALTER TABLE session_messages ADD COLUMN sender_type TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_name TEXT NOT NULL DEFAULT '';
```

| 列 | 类型 | 取值 | 说明 |
|---|---|---|---|
| `sender_type` | TEXT | `user` / `agent` / `system` | `tool` 消息归 `system`（工具由系统执行）；控制记录归 `system` |
| `sender_id` | TEXT | 用户 id / `role:instance` / `system` | `agent` 用 `role:instance`（与 `agent:<role>/<instance>` 地址一致） |
| `sender_name` | TEXT | 展示名 | 用户昵称 / 角色名；可空，UI 回退到 id |

> 这三列是 ① §7.2 的 12 列中的 3 列；其余列（`message_id`/`group_id`/`kind`/`correlation_id`/`reply_to`/`idempotency_key`/`direction`/`expires_at`/`structured_json`）同批迁移，定义以 ① 为准。本文只负责 sender 部分。

### 3.2 为什么不用「单一 sender_id + 命名空间」

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| A. `sender_id` 单列，值如 `user:u123` / `agent:hubu/main` | 列少 | 每次过滤要 `LIKE 'user:%'` 或用不到索引；`role:instance` 自带 `:`/`/`，解析规则脆弱；类型无法约束 | ❌ |
| B. `sender_type` + `sender_id` 两列 | 可索引、可约束、无语义解析；与 `role` 正交 | 多一列 | ✅ 采纳 |
| C. 仅 `sender_id`，另建 users/agents 表 | 规范化 | 跨库无法 FK；agent 是动态实例；成本高 | ❌（本期） |

### 3.3 `role` 与 `sender_type` 的关系

| `role` | `sender_type` | 典型 `sender_id` |
|---|---|---|
| `user` | `user` | `u_10086` |
| `assistant` | `agent` | `hubu/main` |
| `tool` | `system` | `system` |
| `system` | `system` | `system` |
| `user`（Internal 注入，如 view_image） | `system` | `system` |
| `user`（另一个 agent 发来的消息） | `agent` | `hubu/main` |

**关键规则**：`role` 只决定 LLM 如何看这条消息；`sender_*` 只决定「谁产生」。两者不一致是合法的（例如 agent 发来的消息以 `role=user` 进入对方上下文）。

---

## 4. 迁移策略

### 4.1 版本

- **会话库**：`sessionSchemaVersion 3 → 4`（`storage/sqlite/sessiondb.go:45`）。
- **主库**：sender 变更**不涉及**主库版本；v74 仅用于建 `groups`/`group_members`（④）。

### 4.2 迁移 DDL（会话库 v4，sender 部分）

```sql
-- 纯加列，不改既有列
ALTER TABLE session_messages ADD COLUMN sender_type TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_name TEXT NOT NULL DEFAULT '';

-- 按 role 回填 sender_type（旧数据无法知道具体 id，遵循最小猜测原则）
UPDATE session_messages SET sender_type = 'user'
 WHERE sender_type = '' AND role = 'user' AND internal_only = 0 AND record_type = 'message';
UPDATE session_messages SET sender_type = 'agent'
 WHERE sender_type = '' AND role = 'assistant';
UPDATE session_messages SET sender_type = 'system'
 WHERE sender_type = '' AND (role IN ('tool','system') OR record_type <> 'message' OR internal_only = 1);

-- system 行直接落 'system'；user/agent 留空表示 legacy
UPDATE session_messages SET sender_id = 'system', sender_name = 'system'
 WHERE sender_type = 'system' AND sender_id = '';

CREATE INDEX IF NOT EXISTS idx_sm_sender
    ON session_messages(tenant_id, sender_type, sender_id);
```

执行位置：`initSessionSchema` 的幂等 DDL 重放之后（`storage/sqlite/sessiondb.go:51-90`），逐库执行；`ALTER TABLE` 前用 `pragma table_info` 判断列是否已存在。**版本号不参与分支判断**（与现有 v2 索引迁移同一模式，见 `sessiondb.go:38-45` 注释）。

### 4.3 回填 sender_id（逐会话库）

会话库不能访问主库的 `user_chats`，因此由持有主库的进程执行一次性回填：

```text
for each tenant in tenants where db_path != '':
    owner = SELECT sender_id FROM user_chats
             WHERE channel = tenant.channel AND chat_id = tenant.chat_id
    if owner == '': continue
    open <main_db_dir>/<tenant.db_path>
    UPDATE session_messages
       SET sender_id = ?, sender_name = ?
     WHERE sender_id = '' AND sender_type IN ('user','agent')
```

判据：只填空值，绝不覆盖非空；失败只记录日志、不阻塞启动；作业幂等（可重复运行）。

对 `sender_type='agent'` 的旧 assistant 行：**无法知道是主 agent 还是哪个子代理**，统一留空（不猜）。新写入的 assistant 行由 `ChatMessage.SenderID` 填当前 agent 身份。

### 4.4 回填口径（明确）

| 旧行类型 | `sender_type` | `sender_id` | 理由 |
|---|---|---|---|
| `role=user` 普通消息 | `user` | 会话属主（回填作业）；失败留空 | 旧模型是 1:1 会话，属主即发送者 |
| `role=user` + `internal_only=1` | `system` | `system` | 引擎注入的模型载体 |
| `role=assistant` | `agent` | 留空（无法知道 agent 身份） | 旧行没有 agent 身份 |
| `role=tool` | `system` | `system` | 工具结果 |
| 控制记录（`compress`/`mask`/`context_edit`…） | `system` | `system` | 控制面 |
| `display_only=1` 的合成消息 | `system` | `system` | 合成 |
| `record_type='command'` | 按 `role` | 留空/`system` | 命令行输入输出 |
| `record_type='outbound'`（新增） | 按发送者 | 发送者 | 新数据，无旧行 |

> **绝不猜测**：无法确定的 `sender_id` 一律留空，读取层当作 legacy，不显示发送者徽标；不用 `'unknown'` 之类假值污染数据。

### 4.5 索引

| 索引 | 用途 |
|---|---|
| `idx_sm_sender (tenant_id, sender_type, sender_id)` | 按发送者查消息 |
| 现有 `idx_session_messages_tenant_history` | 主查询（id 顺序）不变 |
| 现有 `idx_sm_tenant_role_id` | 预览查询不变 |
| `ix_sm_group (group_id, created_at)`（①） | 群历史聚合（④） |
| `ux_sm_message_id`（①） | 投递去重 |

### 4.6 惰性迁移的列顺序契约

现有迁移使用**显式列清单**（`storage/sqlite/sessiondb.go:296-320`），因此：

- 会话库新增列不会破坏 `INSERT ... SELECT`；
- 新列在目标行取默认值 `''`；
- 若未来改动列清单，必须同步 `sessionSchema` 与迁移 SQL（现有注释 `sessiondb.go:51-76` 已声明该契约）。

### 4.7 回滚

| 场景 | 回滚 |
|---|---|
| 新列导致写入失败 | 代码回滚到旧版本；列保留（`NOT NULL DEFAULT ''` 对旧代码透明） |
| 回填错误 | `UPDATE ... SET sender_id='' WHERE sender_type='user'`（只清空，不破坏原 role/content） |
| 索引拖慢写入 | `DROP INDEX idx_sm_sender` |
| 会话库 v4 不兼容 | 旧二进制读新库：多出的列被忽略；SQLite 显式列 SELECT 不会失败 |

---

## 5. 写入路径改造

### 5.1 数据模型

`llm.ChatMessage` 新增（`llm/types.go:21-64`）：

```go
SenderType string // "user" | "agent" | "system"；空 = 未标注（legacy/内部）
SenderID   string // user id 或 "role:instance"
SenderName string // 展示名，可空
```

① 同时新增 `MessageID`/`BroadcastID`/`GroupID`/`Kind`/`CorrelationID`/`ReplyTo`/`IdempotencyKey`/`Direction`/`StructuredJSON`（定义见 ① §7.2）。`session_messages` 的写入统一由 `appendMessageWith` 读取并落库；新增列加入 INSERT 列表。

### 5.2 各写入点赋值规则

| 写入点 | `SenderType` | `SenderID` | `SenderName` |
|---|---|---|---|
| 主 Agent 用户消息（`agent/agent.go:3985`） | `user` | `msg.SenderID` | `msg.SenderName` |
| 子代理 interactive 用户消息（`agent/interactive.go:1191`） | `user` | `senderID` | 从 role/会话解析 |
| 子代理 task（`engine_wire.go:1654,785`） | `agent` | `parentAgentID`（`role:instance`） | role 名 |
| 主 Agent assistant 消息（`engine_run.go`/`interactive.go`） | `agent` | 当前 agent 身份 | role 名 |
| tool 消息（`engine_run.go:2661` 等） | `system` | `system` | `system` |
| view_image 注入（`engine_run.go:2453`） | `system` | `system` | `system` |
| 取消/中断批次（`agent_process.go:520`） | `system` | `system` | `system` |
| 结构化消息 inbound（①） | 由 `from.type` 映射 | `from.id` | `from.name` |
| 结构化消息 outbound（①） | 发送者 | 发送者 | 发送者 |
| 命令行（`AppendCommandMessage`） | `user`（输入）/`system`（输出） | `msg.SenderID`/`system` | 可空 |

### 5.3 入口收口

```text
llm.NewUserMessageFrom(senderType, senderID, senderName, content)
llm.NewAssistantMessageFrom(agentID, agentName, content)
llm.NewSystemMessage(content)
```

`NewUserMessage(content)` 保留（sender 为空 = legacy/内部）；新代码必须用 `From` 版本。

### 5.4 防回退

- 静态检查：`session_messages` 的 INSERT 只允许出现在 `storage/sqlite/history.go`（现有 7 处），其它包禁止直接 SQL。
- 单元测试：构造每种消息，断言三列值。
- 集成测试：跑一轮真实对话，断言 user 行 `sender_type=user` 且 `sender_id=msg.SenderID`。
- 群发测试：两个成员的会话库各自断言 `sender_id` = 发起者。

---

## 6. 读取与展示

### 6.1 SQL

`getHistoryFromWith`（`history.go:857-899`）SELECT 增加三列（以及 ① 的 9 列），Scan 到 `HistoryRecord`。`HistoryRecord.Message` 已携带 `llm.ChatMessage`，无需新增结构字段（`history.go:33-42`）。

### 6.2 协议

`protocol.HistoryMessage`（`protocol/events.go:258-330`）新增：

```go
SenderType string `json:"sender_type,omitempty"`
SenderID   string `json:"sender_id,omitempty"`
SenderName string `json:"sender_name,omitempty"`
```

`ConvertHistoryRecords`（`channel/subscription.go:1408-1490`）赋值。

### 6.3 Web 类型与渲染

- `web/src/types/shared.ts:575-615` 的 `ChatMessage` 增加同名可选字段。
- `UserMessage.tsx`：当 `senderType === 'agent'` 时，气泡上方显示「🤖 {senderName || senderId}」徽标，并改用左对齐/不同底色。
- `MessageItem.tsx` / `TurnBody.tsx`：透传字段。
- `sender_type === 'system'` 且 `display_only` 的行不渲染。
- legacy（三列空）不显示徽标，视觉与现状完全一致。

### 6.4 CLI

`channel/cli` 的历史渲染增加可选前缀（默认关闭，配置 `cli.show_sender=true` 时显示）。

### 6.5 会话列表 preview

`tenants.preview`（`storage/sqlite/schema.go:28-36`）仍取最新 user/assistant 内容，不需要 sender；本期不做「最后发言者」列（需要跨会话库再查一次，代价高）。

---

## 7. 群发场景的身份标识

### 7.1 统一标识

| 场景 | `sender_type` | `sender_id` 格式 |
|---|---|---|
| 1:1 用户消息 | `user` | `user_chats.sender_id` |
| agent → agent（`SendMessage`） | `agent` | `role:instance` |
| 群发副本（inbound 行） | `agent` | 发起者 `role:instance` |
| 群发副本（`group_id` 标记） | 同上 | 同上；`group_id` 列区分群 |
| 用户批红 | `user` | 用户 id |
| 系统事件 | `system` | `system` |

### 7.2 权威源分工（无中心表）

| 数据 | 位置 |
|---|---|
| 某条消息的发送者 | 该消息所在会话库那一行的 `sender_*` |
| 群发的一次逻辑发言 | 各收件方会话库中 `message_id` 相同的 inbound 行 |
| 发送者的完整发送记录 | 发送方会话库 `direction='out'` 的行 |
| 群成员清单 | 主库 `group_members`（④） |

- **没有中心表 `from_*`**，因此不存在"两表字段映射"问题。
- 写入时由 `MessageService` 用同一个信封同时填 `structured_json.from.*` 与 `sender_*`，保证同构。
- 群历史聚合（④ §6）直接读各库的 `sender_*`，不需要 join。

### 7.3 一致性约束

```text
structured_json.from.type  == sender_type
structured_json.from.id    == sender_id
structured_json.from.name  == sender_name
```

由写入收口保证；读取层发现不一致时以 `sender_*` 列为准（索引与 SQL 都用列），并把 `structured_json` 视为附带信息。

---

## 8. 查询/功能影响

| 功能 | 影响 | 处理 |
|---|---|---|
| 历史加载 | SELECT 多 3 列（+ ① 的 9 列） | 无行为变化 |
| 区域段取回 | 仅取 content/iteration | 不需要 sender |
| 会话导出 | `protocol/session_export.go:117` 的 RecordType | 增加 sender 字段（向后兼容） |
| rewind | 按 id 删除/重放 | 不受影响 |
| 压缩 | 只读 content/role | 不受影响 |
| 搜索 | 按内容搜索 | 可新增按 sender 过滤（后续） |
| 统计 | token usage 按 session | 不受影响 |

---

## 9. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 旧数据 `sender_id` 留空被误判为「无发送者」 | UI 不显示徽标 | 明确 legacy 语义；读取层可回退到会话属主 | 回填作业 |
| 回填把「agent 代发」的 user 行误标为属主 | 错误归属 | 只回填 `sender_type IN ('user','agent') AND sender_id='' AND role='user'`；outbound 行不参与 | 清空 sender_id |
| 会话库 ALTER 在大量文件上耗时 | 首次打开慢 | 惰性迁移 + 逐库执行；可后台预热 | 按访问触发，不批量迁移 |
| 迁移期间并发写 | 锁竞争 | 现有 `historyLock` + WAL；迁移在打开库时串行 | 重试 |
| 索引增加写放大 | 写入变慢 | 复合索引，成本可接受；观察 p99 | `DROP INDEX` |
| `role`/`sender_type` 混用 | 语义错误 | 文档 + 测试；`role` 不进 sender 逻辑 | — |
| 主库遗留 `session_messages` 无 sender 列 | 旧会话读不到 sender | 该表只服务 `migrated=0`；迁移完成后按 Q1 处置 | 保留空列/空表 |

---

## 10. 验收判据

### 10.1 迁移

1. 用 v3 会话库启动新版本：自动升级到 v4，`pragma table_info(session_messages)` 含三列（以及 ① 的 9 列）。
2. 会话库 10 万行迁移耗时 < 5s（本地基准）；无数据丢失（行数前后一致）。
3. 旧行 `sender_type` 分布符合 §4.4；`sender_id=''`（user/agent）或 `'system'`（system）。
4. 回填作业运行后，1:1 会话的旧 user 行 `sender_id` = `user_chats.sender_id`。
5. 重复运行迁移/回填幂等（不报错、不改变结果）。
6. **主库 `pragma table_info(session_messages)` 不含 sender 三列**（若有遗留表，保持原样）。

### 10.2 新写入

7. 主 Agent 收到用户消息：`sender_type='user'`, `sender_id=msg.SenderID`。
8. assistant 回复：`sender_type='agent'`, `sender_id` 为当前 agent 身份。
9. tool 结果：`sender_type='system'`。
10. SubAgent task 行：`sender_type='agent'`, `sender_id=父 agent`。
11. 群发副本：三列与 `structured_json.from.*` 完全一致。

### 10.3 读取/UI

12. `GET /api/history` 返回 `sender_type/sender_id/sender_name`。
13. Web 群发场景中，不同 agent 的气泡显示不同发送者徽标。
14. legacy 行不显示徽标，视觉与升级前一致。
15. CLI 默认输出不变；开启 `show_sender` 后显示前缀。

### 10.4 回归

16. `go test ./storage/... ./agent/... ./channel/...` 通过。
17. 前端 `npm test`（history/normalize/UserMessage 相关）通过。

---

## 11. 受影响代码清单（file:line）

### 11.1 迁移/存储（只在会话库）

| 文件:行 | 改动 |
|---|---|
| `storage/sqlite/sessiondb.go:45` | `sessionSchemaVersion 3 → 4` |
| `storage/sqlite/sessiondb.go:51-90` | 会话库 DDL 增加三列 + 索引（与 ① 的 9 列同批） |
| `storage/sqlite/sessiondb.go:296-320` | 惰性迁移保持显式列清单（新列取默认值） |
| `storage/sqlite/history.go:291-337` | `appendMessageWith` INSERT 三列 |
| `storage/sqlite/history.go:355-372` | `AppendCommandMessage` 三列 |
| `storage/sqlite/history.go:446-525` | `AppendControl` 三列（system） |
| `storage/sqlite/history.go:527-600` | 快照三列（system） |
| `storage/sqlite/history.go:857-899` | SELECT/Scan 三列 |
| `storage/sqlite/schema.go` | **不改**（主库不承载消息） |

### 11.2 模型/协议

| 文件:行 | 改动 |
|---|---|
| `llm/types.go:21-64` | `ChatMessage` 三字段 |
| `llm/types.go:280-299` | 新增 `*From` 构造函数 |
| `protocol/events.go:258-330` | `HistoryMessage` 三字段 |
| `channel/subscription.go:1408-1490` | 转换赋值 |
| `protocol/session_export.go:117` | 导出记录带 sender |

### 11.3 写入点

| 文件:行 | 改动 |
|---|---|
| `agent/agent.go:3985-4010` | 用户消息用 `NewUserMessageFrom` |
| `agent/interactive.go:1191-1200` | interactive 用户消息 |
| `agent/interactive.go:1985,2013` | 续跑消息 |
| `agent/engine_wire.go:785,1654` | SubAgent task |
| `agent/engine_run.go:2453-2462` | view_image（system） |
| `agent/agent_process.go:520` | 取消批次（system） |
| `agent/card_handler.go:38-42` | 卡片回答 |
| `agent/persist_bridge.go:58,78` | 批量持久化 |

### 11.4 前端

| 文件:行 | 改动 |
|---|---|
| `web/src/types/shared.ts:575-615` | 三字段 |
| `web/src/components/agent/UserMessage.tsx` | 发送者徽标 |
| `web/src/components/agent/MessageItem.tsx` | 透传 |
| `web/src/components/agent/TurnBody.tsx` | 透传 |
| `web/src/components/agent/normalize.ts` | 映射字段 |

### 11.5 测试

| 文件 | 内容 |
|---|---|
| `storage/sqlite/session_migration_v4_test.go`（新） | DDL/回填/幂等 |
| `storage/sqlite/history_sender_test.go`（新） | 写入/读取三列 |
| `agent/sender_identity_test.go`（新） | 各写入点 |
| `channel/subscription_test.go` | 转换 |
| `web/src/components/agent/UserMessage.test.tsx` | 徽标/legacy |

---

## 12. 开放问题

全局仅 3 条，见 `sendmessage-structured.md` §17（本文不新增）。其中与本文件相关的是 Q1（主库遗留消息表处置）——本设计默认**不动主库遗留表**。
