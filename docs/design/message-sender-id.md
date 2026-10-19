# 每条 user message 记录发送人 ID —— 详细设计

> 状态：设计稿（**不含实现代码**）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`
> 关联：`sendmessage-structured.md`（①）、`group-hardening.md`（④）、`court-reporting.md`（⑤）

---

## 0. 一页纸结论

1. **新增三列**：`sender_type`（`user|agent|system`）、`sender_id`（用户 id 或 `role:instance`）、`sender_name`（展示名，denormalized）。
2. **不采用「单一 sender_id 带命名空间」**：类型过滤是高频操作（群组/审计/UI），显式枚举列可索引、可约束、无解析歧义；`agent` 的 `role:instance` 本身含 `:`/`/`，拼进命名空间会引入歧义。
3. **`role` 列保持 LLM 语义不变**（`user`/`assistant`/`tool`/`system`）；`sender_*` 是正交的「谁说的」，两者不可混用。
4. **迁移 `v74`（主库）+ 会话库 schema v4**：三列 `NOT NULL DEFAULT ''`，纯加法；旧数据按 `role` 回填 `sender_type`，`sender_id` 留空（legacy）并在读取层按会话属主兜底。
5. **写入收口在 `appendMessageWith`**（`storage/sqlite/history.go:291-343`）：所有插入点通过 `llm.ChatMessage` 的新字段传入，禁止各调用点手拼 SQL。
6. **群组消息与 1:1 消息共用同一套 sender 标识**：群组 transcript 以 `agent_messages` 为权威，投影行带同样三列。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 每条消息可回答「谁说的」 | 新写入的 `session_messages` 行 `sender_type`/`sender_id` 非空（system 行 `sender_id='system'`） |
| G2 | 群组/多 agent 会话可区分发言者 | 同一会话内不同 agent 的投影行 `sender_id` 不同 |
| G3 | 旧数据不丢、不猜错 | 迁移后旧行 `sender_type` 按 role 分类；`sender_id=''` 明确表示 legacy |
| G4 | 查询可索引 | `(tenant_id, sender_type, sender_id)` 索引；按发送者过滤不全表扫 |
| G5 | UI 可显示「谁说的」 | Web/CLI 历史行展示发送者徽标；legacy 行不显示 |
| G6 | 与 ① 的结构化消息、④ 的群组共享字段 | `agent_messages.from_*` 与 `session_messages.sender_*` 同构 |

### 1.2 非目标

- 不改变消息排序/turn 语义。
- 不引入用户表/权限表（sender_id 仍是自由字符串，与现有 `user_chats.sender_id`、memory 的 user_id 一致）。
- 不把 `sender_id` 作为外键（跨会话库/主库，无法 FK）。
- 不实现「消息级已读/未读」。
- 不迁移历史消息到 `agent_messages`（旧控制记录只读）。

---

## 2. 现状调研（事实 + 证据）

### 2.1 表结构

主库 `session_messages`：

```sql
CREATE TABLE session_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_arguments TEXT,
    tool_calls TEXT,
    detail TEXT,
    reasoning_content TEXT DEFAULT '',
    reasoning_items TEXT DEFAULT '',
    display_only INTEGER DEFAULT 0,
    internal_only INTEGER DEFAULT 0,
    context_tokens INTEGER DEFAULT 0,
    turn_id INTEGER DEFAULT 0,
    iteration INTEGER NOT NULL DEFAULT 0,
    record_type TEXT NOT NULL DEFAULT 'message',
    target_history_id INTEGER,
    record_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
```

证据：`storage/sqlite/schema.go:40-74`。

会话库同一张表的子集（v71 拆库后实际写入目标），列序必须与主库一致：

- `storage/sqlite/sessiondb.go:51-76`（`sessionSchema`）
- `storage/sqlite/sessiondb.go:27`（`sessionSchemaVersion = 3`）
- 注释明确：`sessionSchema` 与 `schema.go` 逐列一致，惰性迁移 `INSERT INTO ... SELECT *` 依赖列序（`sessiondb.go:46-50`）

### 2.2 写入路径

| 写入点 | 证据 | 角色 |
|---|---|---|
| 通用单条插入 | `storage/sqlite/history.go:291-343`（`appendMessageWith`） | 所有普通消息的收口点 |
| 批量插入 | `storage/sqlite/history.go:364-400`（`AppendMessages`） | 调用 `appendMessageWith` |
| 命令行 | `storage/sqlite/history.go:351-378`（`AppendCommandMessage`） | 单独 INSERT |
| 控制记录 | `storage/sqlite/history.go:446-525`（`AppendControl`） | 单独 INSERT |
| 上下文快照 | `storage/sqlite/history.go:527-600` | 单独 INSERT |
| Ask 问题/回答 | `storage/sqlite/history.go:602-760` | 单独 INSERT |
| 惰性迁移复制 | `storage/sqlite/sessiondb.go:300+` | `INSERT INTO ... SELECT`，列序敏感 |

### 2.3 上层调用点（用户/agent 消息来源）

| 调用点 | 证据 | 当前 sender 信息 |
|---|---|---|
| 主 Agent 用户消息 eager-save | `agent/agent.go:3985-4010` | `msg.SenderID` 可用（`bus.MessagePayload`） |
| 子代理 interactive 用户消息 | `agent/interactive.go:1191-1200,1985-2025` | 有 `senderID` 变量（`interactive.go:1822,2852`） |
| 一次性 SubAgent task | `agent/engine_wire.go:1654-1660` | 父 agent 身份 |
| SubAgent 初始 task（cfg.Messages） | `agent/engine_wire.go:785` | 父 agent 身份 |
| 卡片回答 | `agent/card_handler.go:38-42` | 有 `msg.SenderID` |
| view_image 注入 | `agent/engine_run.go:2453-2462` | Internal 行，system |
| 取消批次 | `agent/agent_process.go:520` | 混合 |
| 持久化桥 | `agent/persist_bridge.go:58,78` | 批量 |

### 2.4 读取/展示路径

| 路径 | 证据 | 当前字段 |
|---|---|---|
| 历史查询 SQL | `storage/sqlite/history.go:857-899` | 18 列，无 sender |
| 协议 HistoryMessage | `protocol/events.go:258-330` | 无 sender |
| 转换函数 | `channel/subscription.go:1408-1490` | 构造 HistoryMessage，无 sender |
| Web 类型 | `web/src/types/shared.ts:575-615` | 无 sender |
| Web user 行 | `web/src/components/agent/UserMessage.tsx:1-80` | 只渲染 content |
| WS 信封 | `protocol/ws.go:97-128` | 已有 `SenderID`/`SenderName`（入站方向） |

### 2.5 入站消息已有 sender

`bus.MessagePayload` 已含：

```go
Channel    string
SenderID   string
SenderName string
ChatID     string
...
```

证据：`bus/bus.go:13-32`。

但该信息**没有落进 `session_messages`**：主 Agent 只把它用于路由/settings/usage（`agent/engine_wire.go:146-147`），消息行本身只有 `role`。这是本次要补的缺口。

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

证据：`storage/sqlite/schema.go:288-298`；查询接口 `storage/sqlite/chat.go:419-437`（`GetSenderForChat`）。

---

## 3. 字段设计

### 3.1 三列定义

```sql
ALTER TABLE session_messages ADD COLUMN sender_type TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_name TEXT NOT NULL DEFAULT '';
```

| 列 | 类型 | 取值 | 说明 |
|---|---|---|---|
| `sender_type` | TEXT | `user` / `agent` / `system` | `tool` 消息归 `system`（工具由系统执行）；控制记录归 `system` |
| `sender_id` | TEXT | 用户 id / `role:instance` / `system` | `agent` 用 `role:instance`（与 `agent:<role>/<instance>` 地址的 `role:instance` 部分一致） |
| `sender_name` | TEXT | 展示名 | 用户昵称 / 角色名；可为空，UI 回退到 id |

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
| `user`（结构化消息投影，来自另一个 agent） | `agent` | `hubu/main` |

**关键规则**：`role` 只决定 LLM 如何看这条消息；`sender_*` 只决定「谁产生」。两者不一致是合法的（例如 agent 发来的消息以 `role=user` 进入对方上下文）。

---

## 4. 迁移策略

### 4.1 版本

- 主库：`schemaVersion 73 → 74`（`storage/sqlite/db.go:67`）。
- 会话库：`sessionSchemaVersion 3 → 4`（`storage/sqlite/sessiondb.go:27`）。

### 4.2 迁移 DDL（主库 v74）

> **版本协调**：① 的 `agent_messages` 建表与 `session_messages.structured_json` 也在 v74。二者是同一发布批次的消息层变更，**合并为一个 v74 迁移函数**（先加列，再建表），避免两个 PR 争抢版本号。本文只描述 sender 相关部分。

```sql
-- v74: sender identity on every message row (pure additive, no rewrite)
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

-- sender_id 先统一写 'system' 仅对 system 行；user/agent 留空表示 legacy
UPDATE session_messages SET sender_id = 'system', sender_name = 'system'
 WHERE sender_type = 'system' AND sender_id = '';

CREATE INDEX IF NOT EXISTS idx_sm_sender
    ON session_messages(tenant_id, sender_type, sender_id);
```

### 4.3 会话库 v4

会话库不能访问主库的 `user_chats`，因此：

1. 会话库迁移只做 `ALTER TABLE ... ADD COLUMN` + 同样的 `role → sender_type` 回填；
2. `sender_id` 对 user/agent 旧行留空；
3. 主库启动后运行一次**回填作业**（可选、幂等）：

```text
for each tenant in tenants where db_path != '':
    owner = SELECT sender_id FROM user_chats WHERE channel=tenant.channel AND chat_id=tenant.chat_id
    if owner == '': continue
    open tenant.db_path
    UPDATE session_messages
       SET sender_id = ?, sender_name = ?
     WHERE sender_id = '' AND sender_type = 'user' AND role = 'user'
```

判据：该作业只填空值，绝不覆盖非空；失败只记录日志不阻塞启动。

### 4.4 回填口径（明确）

| 旧行类型 | `sender_type` | `sender_id` | 理由 |
|---|---|---|---|
| `role=user` 普通消息 | `user` | 会话属主（回填作业）；失败留空 | 旧模型是 1:1 会话，属主即发送者 |
| `role=user` + `internal_only=1` | `system` | `system` | 引擎注入的模型载体，不是人说的 |
| `role=assistant` | `agent` | 留空（无法知道是主 agent 还是子代理） | 旧行没有 agent 身份 |
| `role=tool` | `system` | `system` | 工具结果 |
| 控制记录（`ask_question`/`ask_answer`/`compress`/`mask`/`context_edit`） | `system` | `system` | 控制面 |
| `display_only=1` 的合成消息 | `system` | `system` | 合成 |
| `record_type=command` | 按 `role` | 留空/`system` | 命令行输入输出 |

> **绝不猜测**：无法确定的 `sender_id` 一律留空，读取层把它当作 legacy，不显示发送者徽标；不用 `'unknown'` 之类假值污染数据。

### 4.5 索引

| 索引 | 用途 |
|---|---|
| `idx_sm_sender (tenant_id, sender_type, sender_id)` | 按发送者查消息、群组统计 |
| 现有 `idx_session_messages_tenant_history` | 主查询（id 顺序）不变 |
| 现有 `idx_sm_tenant_role_id` | 预览查询不变 |

不加 `sender_id` 前缀索引：1:1 会话下基数低，收益小；群组场景用 `agent_messages.from_*` 索引。

### 4.6 迁移执行注意

- 主库迁移沿用现有 `migrateV73ToV74` 模式（参考 `storage/sqlite/migrations.go:524-560`）：幂等检查列是否存在。
- 会话库是**惰性迁移**：打开会话库时重放 `sessionSchema`（`sessiondb.go:34-50` 注释）；`ALTER TABLE` 需按 `pragma table_info` 判断列是否已存在，避免重复执行报错。
- 迁移期间**不重写数据**（只 UPDATE 空值），3.4GB 主库的迁移时间可控；会话库逐个小文件执行。
- 迁移前整库备份沿用现有逻辑：`storage/sqlite/db.go:338-363`（版本变更加 `VACUUM INTO` 备份）。

### 4.7 回滚

| 场景 | 回滚 |
|---|---|
| 新列导致写入失败 | 代码回滚到旧版本；列保留（`NOT NULL DEFAULT ''` 对旧代码透明） |
| 回填错误 | `UPDATE ... SET sender_id='' WHERE sender_type='user'`（只清空，不破坏原 role/content） |
| 索引拖慢写入 | `DROP INDEX idx_sm_sender`（不影响功能） |
| 会话库 v4 不兼容 | 旧二进制读新库：多出的列被忽略；不会失败（SQLite SELECT 显式列） |

---

## 5. 写入路径改造

### 5.1 数据模型

`llm.ChatMessage` 新增（`llm/types.go:21-64`）：

```go
SenderType string // "user" | "agent" | "system"；空 = 未标注（legacy/内部）
SenderID   string // user id 或 "role:instance"
SenderName string // 展示名，可空
```

`session_messages` 的写入统一由 `appendMessageWith`（`history.go:291-343`）读取这三个字段并落库；新增列加入 INSERT 列表。

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
| AskUser 回答 user 行（`agent.go:3933`） | `user` | `msg.SenderID` | `msg.SenderName` |
| 结构化消息投影（①） | 由 `from.type` 映射 | `from.id` | `from.name` |
| 命令行（`AppendCommandMessage`） | `user`（输入）/`system`（输出） | `msg.SenderID`/`system` | 可空 |

### 5.3 入口收口

在 `agent` 层提供单一构造函数，避免每个调用点重复：

```text
llm.NewUserMessageFrom(senderType, senderID, senderName, content)
llm.NewAssistantMessageFrom(agentID, agentName, content)
llm.NewSystemMessage(content)
```

`NewUserMessage(content)` 保留（sender 为空 = legacy/内部）；新代码必须用 `From` 版本。

### 5.4 防止回退

- 增加静态检查：`session_messages` 的 INSERT 只允许出现在 `storage/sqlite/history.go`（现有 7 处），其它包禁止直接 SQL。
- 单元测试：构造每种消息，断言三列值。
- 集成测试：跑一轮真实对话，断言 user 行 `sender_type=user` 且 `sender_id=msg.SenderID`。

---

## 6. 读取与展示

### 6.1 SQL

`getHistoryFromWith`（`history.go:857-899`）SELECT 增加三列，Scan 到 `HistoryRecord`。

`HistoryRecord`（`history.go:33-42`）的 `Message` 已经携带 `llm.ChatMessage`，无需新增字段。

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
- `UserMessage.tsx`：当 `senderType === 'agent'` 时，气泡上方显示「🤖 {senderName || senderId}」徽标，并改用左对齐/不同底色（群组场景）。
- `MessageItem.tsx`：把字段透传给 `TurnBody`/`UserMessage`。
- `sender_type === 'system'` 且 `display_only` 的行不渲染。
- legacy（三列空）不显示徽标，视觉与现状完全一致。

### 6.4 CLI

`channel/cli` 的历史渲染增加可选前缀（默认关闭，配置 `cli.show_sender=true` 时显示）。避免影响现有单用户使用。

### 6.5 会话列表 preview

`tenants.preview`（`storage/sqlite/schema.go:28-36`）仍取最新 user/assistant 内容，不需要 sender；如需显示「最后发言者」，从对应会话库再查一次（代价高，本期不做）。

---

## 7. 与群组消息的统一

### 7.1 统一标识

| 场景 | `sender_type` | `sender_id` 格式 |
|---|---|---|
| 1:1 用户消息 | `user` | `user_chats.sender_id` |
| agent → agent（`SendMessage`） | `agent` | `role:instance` |
| 群组会议成员发言 | `agent` | `role:instance` |
| 主持人群发 | `agent` | `moderator role:instance` |
| 用户批红 | `user` | 用户 id |
| 系统事件 | `system` | `system` |

### 7.2 权威源分工

- `agent_messages`（①）：结构化消息的**权威事件流**，含 `from_type/from_id/from_name`、`group_id`、`correlation_id`。
- `session_messages`：每个收件方会话的**投影**，含同样三列 + `structured_json`。
- 二者通过 `agent_messages.id == session_messages.structured_json.id` 关联（或新增 `message_ref` 列；见 ① §13）。
- 群组 transcript 查询直接走 `agent_messages WHERE group_id=?`；单会话历史走 `session_messages`。

### 7.3 字段映射

```text
agent_messages.from_type  ↔ session_messages.sender_type
agent_messages.from_id    ↔ session_messages.sender_id
agent_messages.from_name  ↔ session_messages.sender_name
```

必须一致；写入投影时由 MessageService 统一填充，禁止调用方各写各的。

---

## 8. 查询/功能影响

| 功能 | 影响 | 处理 |
|---|---|---|
| 历史加载 | SELECT 多 3 列 | 无行为变化 |
| 区域段取回 | 仅取 content/iteration | 不需要 sender；可选补充 |
| 会话导出 | `protocol/session_export.go:117` 的 RecordType | 增加 sender 字段（向后兼容） |
| rewind | 按 id 删除/重放 | 不受影响 |
| 压缩 | 只读 content/role | 不受影响 |
| 搜索 | 若按内容搜索 | 可新增按 sender 过滤（后续） |
| 统计 | token usage 按 session | 不受影响 |

---

## 9. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 旧数据 `sender_id` 留空被误判为「无发送者」 | UI 不显示徽标 | 明确 legacy 语义；读取层可回退到会话属主 | 回填作业 |
| 回填把「agent 代发」的 user 行误标为属主 | 错误归属 | 只回填 `sender_type=user AND sender_id='' AND role=user`；结构化消息投影不参与 | 清空 sender_id |
| 会话库 ALTER 在大量文件上耗时 | 首次打开慢 | 惰性迁移 + 逐库执行；可后台预热 | 不自动迁移，按访问触发 |
| 迁移期间并发写 | 锁竞争 | 现有 `historyLock` + WAL；迁移在打开库时串行 | 重试 |
| 索引增加写放大 | 写入变慢 | 单列复合索引，成本可接受；观察 p99 | `DROP INDEX` |
| `role`/`sender_type` 混用 | 语义错误 | 文档 + 测试；`role` 不进 sender 逻辑 | — |

---

## 10. 验收判据

### 10.1 迁移

1. 用 v73 数据库启动新版本：自动迁移到 v74，`pragma table_info(session_messages)` 含三列。
2. 旧库 10 万行迁移耗时 < 5s（本地基准）；无数据丢失（行数前后一致）。
3. 旧行 `sender_type` 分布符合 §4.4；`sender_id=''`（user/agent）或 `'system'`（system）。
4. 回填作业运行后，1:1 会话的旧 user 行 `sender_id` = `user_chats.sender_id`。
5. 重复运行迁移/回填幂等（不报错、不改变结果）。

### 10.2 新写入

6. 主 Agent 收到用户消息：`sender_type='user'`, `sender_id=msg.SenderID`。
7. assistant 回复：`sender_type='agent'`, `sender_id` 为当前 agent 身份。
8. tool 结果：`sender_type='system'`。
9. SubAgent task 行：`sender_type='agent'`, `sender_id=父 agent`。
10. 结构化消息投影：三列与 `agent_messages.from_*` 完全一致。

### 10.3 读取/UI

11. `GET /api/history` 返回 `sender_type/sender_id/sender_name`。
12. Web 群组会话中，不同 agent 的气泡显示不同发送者徽标。
13. legacy 行不显示徽标，视觉与升级前一致。
14. CLI 默认输出不变；开启 `show_sender` 后显示前缀。

### 10.4 回归

15. 全量 `go test ./storage/... ./agent/... ./channel/...` 通过。
16. 前端 `npm test`（history/normalize/UserMessage 相关）通过。

---

## 11. 受影响代码清单（file:line）

### 11.1 迁移/存储

| 文件:行 | 改动 |
|---|---|
| `storage/sqlite/db.go:67` | `schemaVersion 73 → 74` |
| `storage/sqlite/migrations.go:497-525` | 增加 `migrateV73ToV74` 分支 |
| `storage/sqlite/migrations.go:524-560` | 新增迁移函数（DDL + 回填） |
| `storage/sqlite/schema.go:40-74` | 新建库 DDL 增加三列 + 索引 |
| `storage/sqlite/sessiondb.go:27` | `sessionSchemaVersion 3 → 4` |
| `storage/sqlite/sessiondb.go:51-76` | 会话库 DDL 增加三列 + 索引 |
| `storage/sqlite/sessiondb.go:300+` | 惰性迁移列序 |
| `storage/sqlite/history.go:291-343` | `appendMessageWith` INSERT 三列 |
| `storage/sqlite/history.go:351-378` | `AppendCommandMessage` 三列 |
| `storage/sqlite/history.go:446-525` | `AppendControl` 三列（system） |
| `storage/sqlite/history.go:527-600` | 快照三列（system） |
| `storage/sqlite/history.go:602-760` | Ask 记录三列 |
| `storage/sqlite/history.go:857-899` | SELECT/Scan 三列 |
| `storage/sqlite/history.go:33-42` | 无需改（Message 携带） |

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
| `agent/agent.go:3933-3945` | AskUser 回答 user 行 |
| `agent/interactive.go:1191-1200` | interactive 用户消息 |
| `agent/interactive.go:1985,2013` | 续跑消息 |
| `agent/engine_wire.go:785,1654` | SubAgent task |
| `agent/engine_run.go:2453-2462` | view_image（system） |
| `agent/agent_process.go:520` | 取消批次（system） |
| `agent/card_handler.go:38-42` | 卡片回答 |
| `agent/persist_bridge.go:58,78` | 批量持久化 |
| `session/tenant.go:44-52` | 透传（若需要） |

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
| `storage/sqlite/migration_v74_test.go`（新） | DDL/回填/幂等 |
| `storage/sqlite/history_sender_test.go`（新） | 写入/读取三列 |
| `agent/sender_identity_test.go`（新） | 各写入点 |
| `channel/subscription_test.go` | 转换 |
| `web/src/components/agent/UserMessage.test.tsx` | 徽标/legacy |

---

## 12. 开放问题（需用户拍板）

1. **`sender_id` 的 agent 标识用 `role:instance` 还是全局 `agent_id`？** 本设计用 `role:instance`（与现有地址一致）；若未来允许 agent 改名/迁移，会失去历史连续性。
2. **旧数据 `sender_id` 是否强制回填？** 本设计「能确定才回填，否则留空」；若要求 100% 非空，需要接受 `legacy:<tenant>` 这类合成 id。
3. **`sender_name` 是否冗余存储？** 本设计存（避免每次 join/查角色）；代价是改名后历史名字不变。
4. **是否给 `role=tool` 记录 `tool_name` 作为 sender_id？** 本设计统一 `system`；若审计需要「哪个工具」，应新增 `tool_name` 已有列，不必占用 sender。
5. **群组内 user 消息的 `sender_id` 与 1:1 是否同轴？** 本设计同一个用户 id（`user_chats.sender_id`），不引入群内昵称。
