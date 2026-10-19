# SendMessage 结构化消息 + AskUser 内建 —— 详细设计

> 状态：设计稿（**不含实现代码**，只含 schema / 接口 / 迁移 / 时序）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`（隔离工作树 `/tmp/xbot-court`）
> 关联：`message-sender-id.md`（③）、`group-hardening.md`（④）、`court-reporting.md`（⑤）
> 作者：taizi

---

## 0. 一页纸结论

1. **`SendMessage` 从「字符串 + 邮箱地址」升级为「结构化信封 + 地址」**：信封有 `kind`/`payload`/`correlation_id`/`reply_to`/`idempotency_key`/`hop`，`message` 字符串降级为 `kind=text` 的快捷方式，**完全向后兼容**。
2. **`SendMessage` 永远异步**：持久化成功即返回 `message_id`，不等待回复。回复以「带 `sender_id` 的消息」注入发送方会话，通过 `reply_to`/`correlation_id` 关联（复用现有 `AsyncMessageNotification` 注入管线）。
3. **`AskUser` 工具被删除**，其全部能力内建为 `kind=ask_user` + `kind=answer`。用户回答走异步回调，数据结构与现有 `ask_question`/`ask_answer` 控制记录**双向映射**，旧历史只读兼容。
4. **结构化消息落主库 `agent_messages` 表**（跨会话、可审计、可幂等），同时向收件方会话的 `session_messages` 写一条投影行（供 LLM 上下文与 UI 渲染），两表通过 `message_id` 关联。
5. **权限按「租户 + 群组成员 + 能力位」三重校验**，发送者身份只从服务端上下文解析，绝不信任 payload 里的 `from`。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 可验证判据 |
|---|---|---|
| G1 | 结构化消息 schema 稳定、可版本化 | `schema` 字段 + 单一 `kind` 注册表；未知 kind 拒绝并回执 |
| G2 | AskUser 能力不丢：问题/选项/单选/多选/自定义/取消/超时 | `kind=ask_user` payload 覆盖现有 `AskUserQuestion` 全部字段（`protocol/events.go:105-110`） |
| G3 | 完全异步 + 回调 | `SendMessage` 返回耗时 < 100ms（本地持久化）；回答通过 `reply_to` 注入发送方会话 |
| G4 | 幂等/重试/超时/取消语义明确 | 重复 `idempotency_key` 返回同一 `message_id`；超时产生 `kind=receipt(status=expired)` |
| G5 | 结构化消息可审计、可回放 | `agent_messages` 按 `group_id`/`correlation_id` 可查询完整 transcript |
| G6 | 群组内区分广播/定向 | `delivery.mode=broadcast|directed`，`ask_user` 强制 directed |
| G7 | 删除 AskUser 后旧数据不炸 | 旧 `ask_question`/`ask_answer` 只读回放，前端折叠展示 |

### 1.2 非目标

- 不实现通用消息队列/事务消息（不引入 Kafka/NATS）；持久化 + 注入即可。
- 不做端到端加密/签名（`decision.signature` 字段预留，本期不校验）。
- 不改变 IM 渠道（Feishu/Web/CLI）的入站消息模型；只新增「结构化消息」这一层。
- 不把 `SendMessage` 变成同步 RPC；需要同步语义的调用方自行等待回调事件。
- 不在本期实现「消息编辑/撤回」；只做追加式状态收据。
- 不删除旧 DB 列、不重写旧控制记录。

---

## 2. 现状调研（事实 + 证据）

### 2.1 SendMessage 现状

| 事实 | 证据 |
|---|---|
| 工具参数只有 `to` + `message` 两个字符串 | `tools/send_message.go:64-67` |
| 按地址前缀分派：`agent:` / `group:` / `peer:` / `session:` / IM | `tools/send_message.go:76-121` |
| `agent:` 目标在 2s 窗口内顺手拿回复，超时即返回「已投递」 | `tools/send_message.go:123-190`、`tools/limits.go:61-65` |
| 后台投递使用脱离工具 ctx 的独立上下文 | `tools/send_message.go:136-155` |
| group 目标无消息存储，只是成员集合；广播 = 对每个成员并发 full-turn 调用 | `tools/group_state.go:11-29`、`tools/send_message.go:240-353` |
| peer 目标通过 `PeerMessageFn` 异步注入；busy→fake tool result，idle→user message | `tools/send_message.go:380-428`、`agent/agent.go:5081-5142` |
| 异步注入统一走 `AsyncMessageNotification` → `BgTaskManager.NotifyCh` | `tools/task_manager.go:964-1005` |
| 注入消息当前没有结构化 sender 身份，只有 `Sid`（会话属主 user id） | `tools/task_manager.go:964-968` |

### 2.2 AskUser 现状

| 事实 | 证据 |
|---|---|
| 工具返回 `WaitingUser:true` + metadata（`ask_questions`/`request_id`），由引擎暂停 run | `tools/ask_user.go:98-125`、`agent/engine_run.go:2264-2286` |
| 工具注册在 core registry | `tools/registry_helpers.go:258` |
| 工具可用渠道 = cli/feishu/web | `tools/ask_user.go:127-131` |
| 问题结构：question/options/multi_select/allow_other | `tools/ask_user.go:38-68`、`protocol/events.go:105-110` |
| 等待态是「每 (channel, chatID) 至多一个」，内存 entry + DB 控制记录双份，DB 为权威 | `agent/agent.go:535,674,1242-1320,1390-1480` |
| 回答作为 inbound message 回来，metadata `ask_user_answered=true`，并写 `ask_answer` + 一条 normal user 行 | `agent/agent.go:3920-3985`、`storage/sqlite/history.go:735-760` |
| Web：WS 事件 `ask_user` / `ask_user_resolved` / `ask_user_response` | `protocol/ws.go:15-22,64,83-88,120-128` |
| Web 发布 pending prompt | `channel/web/web.go:1104-1135,1524-1560` |
| Web 回答分发 | `channel/web/web_inbound.go:444-480` |
| CLI 面板 / Feishu 卡片 | `channel/cli/cli_panel_askuser.go:305`、`channel/feishu/feishu.go:1920,2264,2401` |
| 引擎等待元数据拼装 | `agent/engine_run.go:2405,2427,2488` |
| 旧控制记录类型 | `storage/sqlite/history.go:16-30`（`ask_question`/`ask_answer`） |

### 2.3 关键缺口

1. **无 correlation**：`request_id` 只在 AskUser 的等待态里用，普通 `SendMessage` 的「发出去—回过来」没有关联键。
2. **无持久化信封**：`SendMessage` 投递内容只存在于目标会话的 `session_messages`（且被压成前缀字符串 `[group:g1] ...`），没有「谁发给谁、哪条消息、什么状态」的权威记录。
3. **异步是「投递即忘」**：投递失败只回字符串错误；没有重试、没有幂等、没有超时收据。
4. **AskUser 与 SendMessage 两套机制**：前者要求「目标会话正在等待」，后者要求「目标地址可寻址」；不能把「向另一个 agent 提问」表达成同一种消息。
5. **群组消息丢失发送者**：`[group:%s] %s` 前缀（`tools/send_message.go:292,336`）无法结构化还原「谁说的」。

---

## 3. 术语与常量

| 术语 | 定义 |
|---|---|
| **信封（envelope）** | 一条结构化消息的完整 JSON 对象 |
| **kind** | 信封的语义类型，决定 `payload` schema |
| **correlation_id** | 一次「请求—响应」会话的关联键；同一调查/提问的所有消息共享 |
| **message_id** | 单条消息的全局唯一 ID（ULID，可按时间排序） |
| **reply_to** | 本条消息直接回复的那条 `message_id` |
| **causation_id** | 触发本条消息的上一条消息（用于链路追踪，可空） |
| **hop** | 消息经过的 agent 跳数；每转发一次 +1 |
| **投影行（projection）** | 写入收件方 `session_messages` 的那条行，供 LLM/UI 使用 |
| **收据（receipt）** | `kind=receipt` 的状态消息（delivered/read/accepted/rejected/expired） |

常量：

```text
schema:              "xbot.message/v1"
default_max_hops:    4
default_ttl_ms:      86_400_000        # 24h；ask_user 默认 1h
max_payload_bytes:   256 KiB
id_prefix:           msg_ / cor_ / rcp_
id_format:           ULID（26 字符 Crockford Base32，时间有序）
```

---

## 4. 消息信封 schema（字段级）

### 4.1 顶层字段

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `schema` | string | 是 | 固定 `xbot.message/v1`；未来不兼容变更升 v2 |
| `id` | string | 是 | `msg_<ULID>`；服务端生成，调用方不可指定 |
| `kind` | string | 是 | 见 §5；未知 kind → `INVALID_KIND` 回执 |
| `from` | object | 是 | **服务端填充**，忽略调用方传入（防伪造） |
| `from.type` | enum | 是 | `user` / `agent` / `system` |
| `from.id` | string | 是 | user id 或 `role:instance` |
| `from.name` | string | 否 | 展示名 |
| `from.session` | string | 否 | `channel:chatID` 或 agent 会话 key |
| `to` | object | 是 | 目标地址 |
| `to.type` | enum | 是 | `user` / `agent` / `group` / `peer` / `session` / `im` |
| `to.id` | string | 是 | 解析后的目标 ID（去掉前缀的规范形式） |
| `group_id` | string | 否 | 群组场景必填，与 ④ 的群组主键一致 |
| `correlation_id` | string | 否 | 请求—响应链；`ask_user`/`report`/`decision` 必填 |
| `causation_id` | string | 否 | 触发消息 ID |
| `reply_to` | string | 否 | 直接回复的消息 ID |
| `idempotency_key` | string | 否 | 同一发送者在 TTL 内重复提交返回同一 `id` |
| `created_at` | string | 是 | RFC3339Nano，服务端时钟 |
| `expires_at` | string | 否 | 到期后状态机转 `expired` |
| `hop` | integer | 是 | 初始 0；每次 agent 间转发 +1 |
| `max_hops` | integer | 是 | 默认 4；`hop >= max_hops` 拒绝并回执 `HOP_LIMIT` |
| `delivery` | object | 否 | 投递语义 |
| `delivery.mode` | enum | 否 | `broadcast` / `directed`；默认：group=broadcast，其余=directed |
| `delivery.recipients` | string[] | 否 | directed 时的收件人列表；缺省 = `to` |
| `delivery.await_ms` | integer | 否 | **仅兼容旧行为**：>0 时工具最多等这么久拿即时回复；默认 0（完全异步） |
| `payload` | object | 是（除 text） | 按 kind 定义 |
| `text` | string | 否 | 人读渲染文本；`kind=text` 时必填；其他 kind 作为 fallback 展示 |
| `meta` | object | 否 | 扩展键值（topic/round/labels），不参与路由 |

### 4.2 示例（ask_user）

```json
{
  "schema": "xbot.message/v1",
  "id": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "kind": "ask_user",
  "from": {"type": "agent", "id": "hubu/main", "name": "户部", "session": "agent:hubu/main"},
  "to": {"type": "user", "id": "u_10086", "name": "皇帝"},
  "group_id": "grp_7F3K2M9Q",
  "correlation_id": "cor_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "idempotency_key": "hubu-report-20261019-q1",
  "created_at": "2026-10-19T12:00:00.123Z",
  "expires_at": "2026-10-19T13:00:00.123Z",
  "hop": 0,
  "max_hops": 4,
  "delivery": {"mode": "directed", "recipients": ["user:u_10086"]},
  "payload": {
    "questions": [
      {
        "id": "q1",
        "question": "PPU 集群是否按计划今夜扩容？",
        "options": [
          {"id": "approve", "label": "准", "description": "按 8 节点扩容"},
          {"id": "reject", "label": "驳", "description": "暂缓，等预算"},
          {"id": "modify", "label": "改", "description": "先扩 4 节点"}
        ],
        "multi_select": false,
        "allow_other": true,
        "required": true
      }
    ],
    "allow_cancel": true,
    "timeout_ms": 3600000,
    "context_refs": ["court/reports/2026-10-19.md#section-3"]
  },
  "text": "户部请旨：PPU 集群是否按计划今夜扩容？"
}
```

### 4.3 示例（answer）

```json
{
  "schema": "xbot.message/v1",
  "id": "msg_01J9Z9A1B2C3D4E5F6G7H8J9K0",
  "kind": "answer",
  "from": {"type": "user", "id": "u_10086", "name": "皇帝"},
  "to": {"type": "agent", "id": "hubu/main"},
  "group_id": "grp_7F3K2M9Q",
  "correlation_id": "cor_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "reply_to": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "created_at": "2026-10-19T12:04:11.000Z",
  "hop": 0,
  "max_hops": 4,
  "payload": {
    "answers": [
      {"question_id": "q1", "option_ids": ["modify"], "text": "先扩 4 节点，观察 24h"}
    ],
    "cancelled": false,
    "answered_by": "u_10086"
  },
  "text": "改：先扩 4 节点，观察 24h"
}
```

---

## 5. kind 注册表与 payload schema

### 5.1 总表

| kind | 方向 | 用途 | 是否进 LLM 上下文 |
|---|---|---|---|
| `text` | 任意 | 普通文本（= 旧 `message`） | 是 |
| `ask_user` | agent/system → user | 提问 | 是（渲染为「待回答的问题」） |
| `answer` | user/agent → 原发送方 | 回答 | 是 |
| `command` | user/agent → agent/system | 控制指令（spawn/kill/pause/resume/reload） | 是 |
| `report` | agent → user/agent | 奏折/汇报 | 是 |
| `event` | 任意 → 任意 | 状态事件（无强回复期望） | 可配置（默认是） |
| `decision` | user → agent/group | 批红（准/驳/改） | 是 |
| `receipt` | 系统/收件方 → 发送方 | 投递/读取/接受/拒绝/过期收据 | 否（仅审计/UI） |

### 5.2 `text`

```json
{"text": "string, 1..65536"}
```

兼容规则：`kind` 缺省且 `message` 非空 ⇒ 等价于 `kind=text, payload.text=message`。

### 5.3 `ask_user`

| 字段 | 类型 | 必填 | 约束 |
|---|---|---|---|
| `questions` | array | 是 | 1..20 项 |
| `questions[].id` | string | 是 | 消息内唯一；缺省服务端生成 `q1..qN` |
| `questions[].question` | string | 是 | 1..8192 |
| `questions[].options` | array | 否 | 0..50 项；无 options 表示纯文本回答 |
| `questions[].options[].id` | string | 是 | 消息内唯一 |
| `questions[].options[].label` | string | 是 | 展示文本 |
| `questions[].options[].description` | string | 否 | 副标题 |
| `questions[].multi_select` | bool | 否 | 默认 false |
| `questions[].allow_other` | bool | 否 | 默认 false；true 时允许 `text` |
| `questions[].required` | bool | 否 | 默认 true |
| `allow_cancel` | bool | 否 | 默认 true |
| `timeout_ms` | integer | 否 | 1_000..86_400_000；默认 3_600_000 |
| `context_refs` | string[] | 否 | 证据/文件引用（给 UI 展示） |

回答校验：
- `multi_select=false` 时 `option_ids` 长度 ≤ 1；
- `allow_other=false` 时 `text` 必须为空；
- `required=true` 时 `answers` 必须覆盖该 question；
- 校验失败 → `kind=receipt(status=rejected, error.code=INVALID_ANSWER)`，原问题保持 pending。

### 5.4 `answer`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `answers` | array | 是* | `cancelled=false` 时必填 |
| `answers[].question_id` | string | 是 | 对应 `ask_user` 的 question id |
| `answers[].option_ids` | string[] | 否 | 选项 id 列表 |
| `answers[].text` | string | 否 | 自定义输入 |
| `cancelled` | bool | 是 | true 表示用户取消；此时 `answers` 可为空 |
| `answered_by` | string | 是 | 回答者 id；服务端校验必须 = `from.id` |

### 5.5 `command`

```json
{
  "command": "spawn|kill|pause|resume|reload|ack",
  "args": {"role": "...", "instance": "...", "reason": "..."},
  "reason": "string"
}
```

`command` 是否需要二次确认由命令注册表决定；本期只定义 schema，不实现具体命令执行器。

### 5.6 `report`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `report_id` | string | 是 | 与 `court/state/reports/<date>.yaml` 的 id 一致 |
| `period` | object | 是 | `{"from": RFC3339, "to": RFC3339}` |
| `department` | string | 是 | 部门 id（对应 `court/org.yaml`） |
| `summary` | string | 是 | ≤ 4000 字 |
| `state_ref` | string | 是 | 唯一事实源文件相对路径 |
| `state_sha256` | string | 是 | 汇报时状态文件的哈希（防转述失真） |
| `evidence` | array | 是 | 至少 1 条；见下 |
| `evidence[].path` | string | 是 | 必须存在 |
| `evidence[].kind` | enum | 是 | `log`/`file`/`command`/`url`/`screenshot` |
| `evidence[].sha256` | string | 否 | 文件类建议填 |
| `blockers` | array | 否 | `{id, desc, blocked_by[]}` |
| `asks` | array | 否 | `{question_id, question, options[]}`；等价于内嵌 ask_user |
| `confidence` | number | 否 | 0..1，仅表达自评，不参与规则判定 |

### 5.7 `event`

```json
{
  "event_type": "state.changed|member.joined|member.left|group.closed|task.blocked",
  "subject": "group:grp_xxx | agent:... | court/state/...",
  "severity": "info|warn|error",
  "data": {}
}
```

### 5.8 `decision`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `decision_id` | string | 是 | 批红记录 id |
| `target_report_id` | string | 是 | 被批的 `report_id` |
| `target_message_id` | string | 是 | 被批的消息 `id` |
| `verdict` | enum | 是 | `approve`(准) / `reject`(驳) / `modify`(改) |
| `comments` | string | 否 | 批注 |
| `modifications` | array | `modify` 时必填 | `{path, op, value}`，op ∈ `set`/`append`/`remove` |
| `signature` | string | 否 | 预留：未来签名 |

### 5.9 `receipt`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `ack_of` | string | 是 | 被回执的 `message_id` |
| `status` | enum | 是 | `delivered`/`read`/`accepted`/`rejected`/`expired`/`cancelled` |
| `error` | object | 否 | `{code, message}` |
| `at` | string | 是 | RFC3339Nano |

错误码枚举：`INVALID_KIND`、`INVALID_PAYLOAD`、`INVALID_ANSWER`、`NOT_FOUND`、`PERMISSION_DENIED`、`MEMBERSHIP_REQUIRED`、`HOP_LIMIT`、`TTL_EXPIRED`、`IDEMPOTENCY_CONFLICT`、`DELIVERY_FAILED`、`CANCELLED`。

---

## 6. `SendMessage` 工具接口扩展

### 6.1 参数（向后兼容）

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `to` | string | 是 | 保持现有地址语法（`agent:`/`group:`/`peer:`/`session:`/IM） |
| `message` | string | 条件 | `kind` 缺省时必填；`kind=text` 时等价 `payload.text` |
| `kind` | string | 否 | 默认 `text` |
| `payload` | object | 条件 | 非 text 必填 |
| `correlation_id` | string | 否 | 缺省由服务端生成；若调用方指定则必须复用已有链（校验存在性） |
| `reply_to` | string | 否 | 回复时必填 |
| `causation_id` | string | 否 | 触发消息 id |
| `idempotency_key` | string | 否 | 发送者作用域内唯一（建议 `<role>/<instance>:<业务键>`） |
| `ttl_ms` | integer | 否 | 缺省见 §3 |
| `delivery` | object | 否 | `{mode, recipients, await_ms}` |
| `visibility` | enum | 否 | `group`/`private`；群组场景控制 transcript 可见性，默认 `group` |

### 6.2 返回（结构化）

```json
{
  "ok": true,
  "message_id": "msg_01J9...",
  "correlation_id": "cor_01J9...",
  "status": "queued",
  "persisted": true,
  "recipients": [
    {"type": "agent", "id": "hubu/main", "status": "queued"}
  ],
  "duplicate": false
}
```

失败时 `ok=false` + `error.code`/`error.message`，**不抛裸字符串**（现有工具返回 `error` 的行为保留，但错误体结构化）。

### 6.3 内部接口（设计级）

```text
MessageService
  Send(ctx, Envelope) (SendResult, error)
    ├─ 1. 规范化地址/kind/payload
    ├─ 2. 解析 from（ToolContext / session 上下文；忽略调用方 from）
    ├─ 3. 权限校验（§10）
    ├─ 4. hop/TTL/幂等校验
    ├─ 5. 持久化 agent_messages（status=queued）
    ├─ 6. 路由到 Delivery（§8）
    └─ 7. 更新 status=delivered/failed，返回 SendResult

  Resolve(ctx, correlationID) ([]Envelope, error)
  Cancel(ctx, messageID, reason) error
  ExpireDue(ctx, now) (int, error)      // 定时 sweeper
```

### 6.4 与旧行为差异

| 场景 | 旧 | 新 |
|---|---|---|
| `SendMessage(agent:, message)` | 2s 内可能同步返回回复 | 立即返回 `queued`；回复异步注入 |
| `SendMessage(group:, message)` | 同步等全部成员（30s）后返回拼接文本 | 立即返回；成员各自异步处理，收据记录 |
| 投递失败 | 字符串错误 | 结构化错误 + `receipt(failed)` |
| 重复点击/重试 | 重复发送 | `idempotency_key` 去重 |

---

## 7. 生命周期与状态机

```text
                 ┌──────────┐
   Send() ──────▶│  queued  │
                 └────┬─────┘
                      │ 路由成功
                      ▼
                 ┌──────────┐    收件方读取    ┌──────────┐
                 │delivered │───────────────▶│   read   │
                 └────┬─────┘                └────┬─────┘
        ask_user 回答 │                            │ 非 ask_user
                      ▼                            ▼
                 ┌──────────┐                ┌──────────┐
                 │ answered │                │ accepted │
                 └──────────┘                └──────────┘

  任意状态 ── TTL 到期 ──▶ expired
  任意状态 ── 显式 Cancel ──▶ cancelled
  任意状态 ── 投递失败 ──▶ failed
```

规则：

1. `queued → delivered` 在**投影行成功写入收件方会话**后落定；注入失败重试 3 次（指数退避 200ms/1s/5s），仍失败 → `failed` + 回执。
2. `ask_user` 的 `answered` 只能由合法 `answer` 触发；重复 answer 幂等（返回首次结果，不重复注入）。
3. `expired` 由 sweeper 每 30s 扫描 `expires_at <= now AND status IN (queued, delivered)`；产生 `receipt(expired)` 注入发送方。
4. `receipt` 本身不进入状态机（它是观察者）；但 `receipt(ack_of)` 必须指向存在的消息。
5. 状态迁移全部写 `agent_messages.status` + 一条 `event` 审计行（同一事务）。

---

## 8. 异步投递与回调

### 8.1 投递（发送方 → 收件方）

复用现有异步注入管线（`tools/task_manager.go:964-1005`、`agent/agent.go:5081-5105`），但扩展通知类型：

```text
AsyncMessageNotification {
  Key      string   // 目标 session key
  Sid      string   // 会话属主 user id（现有字段）
  Content  string   // 渲染文本（text 或 payload 的 human-readable 形式）
  Source   string   // 新增: "structured_message"
  MessageID string  // 新增
  Envelope  []byte  // 新增: 完整信封 JSON（投影行使用）
}
```

- **busy**：作为 synthetic tool call/result 注入当前迭代（现有行为，`drainAndInjectBgNotifications`）。
- **idle**：作为一条 user 消息注入并开启新 turn（现有 `injectInbound`）。
- **不阻塞**：`Send()` 只负责「持久化 + 入队」，不等待目标处理。

### 8.2 回调（收件方 → 发送方）

当收件方产生以下行为时，自动生成回程消息：

| 触发 | 回程 kind | reply_to | correlation_id |
|---|---|---|---|
| 用户回答 `ask_user` | `answer` | 原 `ask_user.id` | 原 correlation |
| agent 对 `ask_user` 的回答（agent 作为收件方） | `answer` | 原 id | 原 correlation |
| 用户批红 `report` | `decision` | 原 `report.id` | 原 correlation |
| 收件方显式 ack/拒绝 | `receipt` | 原 id | 原 correlation |
| 目标不存在/无权限 | `receipt(failed)` | 原 id | 原 correlation |

回程消息**注入发送方会话**（同样走 §8.1），因此发送方 agent 的下一轮 LLM 上下文里会看到：

```text
<from agent:hubu/main, kind=answer, correlation=cor_..., reply_to=msg_...>
Q1: 改 — 先扩 4 节点，观察 24h
</from>
```

并同时写一条 `session_messages` 投影行（`sender_type=user, sender_id=u_10086`，见 ③），保证 UI 能显示「谁回答的」。

### 8.3 顺序与去重

- **同一 correlation 内**：按 `created_at` + `id`（ULID 有序）FIFO 注入；注入器维护 `last_delivered_id`，乱序到达时缓冲。
- **跨 correlation**：无全局顺序承诺（best-effort）。
- **去重**：`idempotency_key` 唯一索引；投影注入以 `agent_messages.id` 为幂等键，重复投递只更新状态不重复插入。
- **重试**：仅投递失败重试（3 次）；LLM 处理失败不自动重试（避免重复副作用），由上层发起新消息并带 `causation_id`。

### 8.4 超时与取消

- `expires_at` 到期 → `expired` + 回执。
- 取消：发送方（或同租户用户）可发 `kind=command, command=cancel, args.target=msg_id`；或调用 `MessageService.Cancel`。
- 取消 `ask_user` 时，同步清理该 correlation 的 pending 状态（对齐现有 `resolvePendingAskUser`，`agent/agent.go:1454-1480`）。
- 取消是幂等的：已 answered/expired 的消息返回 `CANCELLED` 但不改变终态。

### 8.5 时序（ASCII）

```text
发送方 agent              MessageService            收件方会话/用户
    │                          │                         │
    │ SendMessage(ask_user)    │                         │
    ├─────────────────────────▶│                         │
    │                          ├─ persist(queued)        │
    │                          ├─ permission/hop/ttl     │
    │◀── {id, corr, queued} ───┤                         │
    │                          ├─ inject(projection) ───▶│
    │                          │◀── delivered ───────────┤
    │  (工具立即返回，继续跑)     │                         │
    │                          │      用户回答 answer      │
    │                          │◀────────────────────────┤
    │                          ├─ validate/answer         │
    │                          ├─ persist(answer)        │
    │                          ├─ inject(sender session)─▶│
    │◀── 异步 user message ─────┤                         │
    │  (下一轮迭代看到回答)       │                         │
```

---

## 9. AskUser 内建（复用现有 UI 管线）

### 9.1 映射关系

| 旧 AskUser 概念 | 新结构化消息 |
|---|---|
| `ask_questions` metadata | `payload.questions` |
| `request_id` | `correlation_id`（同时保留 `request_id` 兼容） |
| `WaitingUser` 暂停 | 不暂停；发送即返回，回答作为新消息注入 |
| `ask_question` 控制记录 | `agent_messages(kind=ask_user)` + 投影行 |
| `ask_answer` 控制记录 | `agent_messages(kind=answer)` + 投影行 |
| Web `ask_user` 事件 | 由 `MessageService` 在投影时发同一 `MsgTypeAskUser`，`RequestID=correlation_id` |
| Web `ask_user_response` | 转换为 `kind=answer` 消息 |
| CLI 面板 | 保留 UI，数据源改为 `ask_user` 结构化消息 |
| Feishu 卡片 | 保留卡片，回调转 `kind=answer` |

### 9.2 关键差异：不再暂停 Run

旧机制要求收到回答时目标 run 仍在等待；新机制下：

- 发送方 agent **继续执行**，回答到达时作为 user 消息进入下一轮（或 busy 时作为 synthetic tool result）。
- 对「必须等回答才能继续」的场景，agent 自行通过 `SendMessage(kind=ask_user)` 后结束本轮；回答到达自然开启新 turn。
- 这样避免「等待态内存 entry + DB 控制记录」双份状态和跨重启恢复问题（现有 `agent/agent.go:1242-1480` 的复杂度不再扩张）。

### 9.3 旧历史兼容

- `ask_question`/`ask_answer` 控制记录**保留读取**（`storage/sqlite/history.go:16-30,602-760`）。
- Replay 仍按现有逻辑折叠（`agent/agent.go:1261-1410`）。
- 新建消息不再写这两个 record type；只写 `agent_messages` + 投影行。
- 前端：遇到 `record_type=ask_question` 的历史行，渲染为**只读折叠块**「历史提问（已归档）」，不弹面板。

---

## 10. 权限模型

### 10.1 身份来源

| 发送方 | 身份解析来源 | 可信度 |
|---|---|---|
| 用户 | `ToolContext.OriginUserID` / inbound `SenderID` | 服务端鉴权后可信 |
| 主 agent | `AgentID` + `channel:chatID` | 服务端上下文 |
| 子 agent | `role:instance` + 父会话 tenant | 服务端上下文 |
| system | 定时器/hook 注入 | 内部调用 |

**禁止**从 `payload.from` 取值；该字段仅用于**输出**展示。

### 10.2 权限矩阵

| 发送方 \ 目标 | 自己的 user | 同租户 agent | 同组 agent | 跨组 agent | 同租户 group | 其他租户 |
|---|---|---|---|---|---|---|
| user | ✅ | ✅（自己的会话） | — | ❌ | ✅（成员/主持人） | ❌ |
| agent（主） | ✅（自己的 owner） | ✅ | ✅ | ❌ | ✅（成员） | ❌ |
| agent（子） | ✅（父会话 owner） | ✅（同组） | ✅ | ❌ | ✅（成员） | ❌ |
| system | ✅ | ✅ | ✅ | ✅（内部） | ✅ | ❌ |

附加规则：

1. SubAgent 必须 `capabilities.send_message=true`（`tools/subagent_roles.go:23-27`）。
2. `ask_user` 的 `to` 必须是 `user` 或 `agent`；不允许广播。
3. `decision` 的发送者必须是 `user`，且 `target_report_id` 属于同一租户。
4. `command` 的 `spawn/kill` 仅 user 或 system 可发；agent 只能发 `pause/resume/reload` 给自己的子 agent。
5. 群组消息要求 `from` 是群成员（见 ④ D6）；peer 组要求 `session_key` 在成员表内。

### 10.3 租户隔离

- `agent_messages.tenant_scope` = 会话 owner 的稳定 id（与 memory 的 user_id 轴一致）。
- 所有查询强制 `tenant_scope = ?`；跨租户 ID 猜测返回 `NOT_FOUND`（不泄露存在性）。

---

## 11. 群组场景的结构化语义

| 场景 | `delivery.mode` | 行为 |
|---|---|---|
| 主持人对全员讲话 | `broadcast` | 每个成员各得一条投影；各自独立处理；不要求回复 |
| 主持人 @单个成员 | `directed` | 仅该成员收到；其余成员收到 `event`（可选） |
| 成员回答主持人 | `directed`（`to=主持人`） | 不自动广播；transcript 记录 |
| 成员向全员汇报 | `broadcast` | 显式选择；受 hop/fanout 限制 |
| `ask_user` | `directed`（强制） | `recipients` 必须恰好 1 个 |
| `decision` | `directed` | 只回给 report 作者 |

Transcript：

- 每条结构化消息以 `group_id` 落 `agent_messages`；查询 `WHERE group_id=? ORDER BY created_at,id` 即会议纪要。
- 会议纪要 = 结构化事件流（与 ④ 的 `agent_messages` 共用表）。
- 新成员加入时是否回放历史：由群组 `history_visibility` 决定（`none`/`since_join`/`all`，默认 `since_join`）。

---

## 12. ② 彻底删除 AskUser 工具

### 12.1 删除策略（三阶段）

**阶段 A（同一 PR，代码删除 + 兼容读）**

1. 从 registry 注销：`tools/registry_helpers.go:258` 删除 `r.RegisterCore(&AskUserTool{})`。
2. 删除实现文件 `tools/ask_user.go`（整文件 1-132）。
3. 保留 `protocol.AskUserQuestion`/`AskUserEvent`/`AskUserResolvedEvent` 结构体（旧客户端兼容），标记 `Deprecated`，不再由新流程发出。
4. 保留 `storage/sqlite` 的 `ask_question`/`ask_answer` 读写（旧历史回放需要）。
5. 保留 Web/CLI/Feishu 的 **answer 入口**，但数据源改为结构化消息（阶段 B 完成前可双写）。

**阶段 B（下一 PR，UI 切换）**

6. Web `AskUserPanel` 改为渲染 `kind=ask_user` 投影（数据字段同构，改动小）。
7. CLI 面板 `cli_panel_askuser.go` 同上。
8. Feishu 卡片 `buildAskUserCard` 同上。
9. `WaitingUser` 字段保留但不再由新工具设置；旧会话恢复仍可读。

**阶段 C（再下一 PR，清理）**

10. 删除 `AskUserEvent`/`AskUserResolvedEvent`（确认无旧客户端）。
11. 删除 `buildWaitingUserOutbound` 的 AskUser 分支（`agent/agent_process.go:550-620`）。
12. 删除 `pendingAskUser*` 内存状态（`agent/agent.go:535,674,1242-1480`）。
13. 删除 `AppendAskQuestion`/`AppendAskAnswer*` 写方法（保留读）。

> 阶段 A 必须可独立回滚：把 `r.RegisterCore(&AskUserTool{})` 加回即可恢复旧行为（文件暂不删，先标记 deprecated 更稳）。

### 12.2 受影响点全清单（file:line）

| 层 | 文件:行 | 处理 |
|---|---|---|
| 工具实现 | `tools/ask_user.go:1-132` | 删除（阶段 A 标记 deprecated，阶段 C 删文件） |
| 工具注册 | `tools/registry_helpers.go:258` | 删除注册 |
| 合成提示 | `tools/synthetic_hints.go:69` | 保留（旧历史回放仍需要 `ask_user` hint） |
| 协议事件 | `protocol/events.go:105-110` | 保留结构体，标 deprecated |
| 协议事件 | `protocol/events.go:462-493` | 阶段 C 删除事件发送；结构体保留至 C |
| WS 类型 | `protocol/ws.go:15-22,64,83-88,120-128` | 保留（旧客户端） |
| Web 发布 | `channel/web/web.go:1104-1135` | 改为由 MessageService 发布 |
| Web 回答 | `channel/web/web.go:902`、`channel/web/web_inbound.go:444-480` | 转为 `kind=answer` |
| Web pending | `channel/web/web.go:1524-1570` | 阶段 B/C 改数据源 |
| CLI 面板 | `channel/cli/cli_panel_askuser.go:305`、`channel/cli/cli_helpers.go:270` | 阶段 B 改数据源 |
| CLI 会话 | `channel/cli/cli.go:873-919`、`channel/cli/cli_agent_msg.go:16-19,370` | 阶段 B/C 清理 |
| Feishu 卡片 | `channel/feishu/feishu.go:1920,1992,2010,2144,2243,2264,2401,2460,2490` | 阶段 B 改数据源 |
| Agent 等待态 | `agent/agent.go:535,674,1242-1480` | 阶段 C 删除 |
| Agent 回答处理 | `agent/agent.go:3920-3985` | 阶段 B 改走结构化消息 |
| 引擎等待 | `agent/engine_run.go:2264-2286,2405,2427,2488` | 阶段 B/C 简化 |
| 引擎工具 | `agent/engine_run_tools.go:692` | 阶段 C 删除 |
| 出站构造 | `agent/agent_process.go:550-620` | 阶段 C 删除 AskUser 分支 |
| 卡片处理 | `agent/card_handler.go:38-60` | 阶段 B 改数据源 |
| 后端查询 | `agent/agent_backend_methods.go:153-167` | 阶段 C 删除 |
| 存储 | `storage/sqlite/history.go:602,728,741` | 保留读；写方法阶段 C 删 |
| Prompt | `prompt/channels/cli.md:4`、`prompt/channels/feishu.md:29-30` | 改为「用 SendMessage(kind=ask_user)」 |
| 文档 | `docs/agent/tools.md:193-204` | 改写 |
| 前端 | `web/src/components/agent/AskUserPanel.tsx`、`web/src/hooks/useAskUser.ts`、`web/src/workspace/panels/AgentPanel.tsx`、`web/src/hooks/useSessionStore.ts` | 阶段 B 改数据源 |
| 测试 | `tools/ask_user_test.go`、`agent/*askuser*_test.go`、`channel/web/web_askuser_resolved_test.go`、`cmd/xbot-cli/askuser_resolved_test.go`、`web/src/components/agent/AskUserPanel.test.tsx`、`web/src/hooks/useAskUser.test.tsx` | 删除/改写 |

### 12.3 前端渲染兼容

| 数据 | 新渲染 | 旧渲染 |
|---|---|---|
| `record_type=ask_question` | 不渲染为面板；折叠「历史提问」 | 面板 |
| `kind=ask_user` 投影 | 面板（可回答） | — |
| `kind=answer` 投影 | 普通 user 行 + 「回答」徽标 | — |
| `ask_user_resolved` | 仅清理缓存 | 清理缓存 |

---

## 13. 持久化设计

### 13.1 主库新表 `agent_messages`

> 为什么在主库：结构化消息跨会话/跨 agent，放会话库会导致「收件方会话不存在时无处落库」；主库已有 `tenants` 注册表与 cron 等全局表。

```sql
CREATE TABLE agent_messages (
    id               TEXT PRIMARY KEY,             -- msg_<ULID>
    tenant_scope     TEXT NOT NULL,                -- 租户/用户隔离轴
    kind             TEXT NOT NULL,
    from_type        TEXT NOT NULL,                -- user|agent|system
    from_id          TEXT NOT NULL,
    from_name        TEXT NOT NULL DEFAULT '',
    from_session     TEXT NOT NULL DEFAULT '',
    to_type          TEXT NOT NULL,                -- user|agent|group|peer|session|im
    to_id            TEXT NOT NULL,
    group_id         TEXT NOT NULL DEFAULT '',
    correlation_id   TEXT NOT NULL DEFAULT '',
    causation_id     TEXT NOT NULL DEFAULT '',
    reply_to         TEXT NOT NULL DEFAULT '',
    idempotency_key  TEXT NOT NULL DEFAULT '',
    hop              INTEGER NOT NULL DEFAULT 0,
    max_hops         INTEGER NOT NULL DEFAULT 4,
    status           TEXT NOT NULL,                -- queued|delivered|read|answered|accepted|rejected|expired|cancelled|failed
    visibility       TEXT NOT NULL DEFAULT 'group',
    payload_json     TEXT NOT NULL DEFAULT '{}',
    text             TEXT NOT NULL DEFAULT '',
    error_code       TEXT NOT NULL DEFAULT '',
    error_message    TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL,
    delivered_at     DATETIME,
    expires_at       DATETIME,
    closed_at        DATETIME
);
CREATE UNIQUE INDEX ux_agent_messages_idem
    ON agent_messages(tenant_scope, idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX ix_agent_messages_corr
    ON agent_messages(tenant_scope, correlation_id, created_at);
CREATE INDEX ix_agent_messages_to
    ON agent_messages(tenant_scope, to_type, to_id, created_at);
CREATE INDEX ix_agent_messages_group
    ON agent_messages(group_id, created_at);
CREATE INDEX ix_agent_messages_status_exp
    ON agent_messages(status, expires_at);
CREATE INDEX ix_agent_messages_from
    ON agent_messages(tenant_scope, from_type, from_id, created_at);
```

### 13.2 会话投影行

在 `session_messages` 新增一列（与 ③ 的迁移合并，见 `message-sender-id.md`）：

```sql
ALTER TABLE session_messages ADD COLUMN structured_json TEXT NOT NULL DEFAULT '';
```

- 投影行：`role='user'`（或 `role='assistant'` 用于 agent 间消息？统一用 `user` 表示「外部输入」，由 sender 字段区分来源），`structured_json` = 信封 JSON，`record_type='message'`。
- LLM 看到的是 `text`（渲染文本），必要时可注入 `payload` 的摘要；完整结构不直接塞进 prompt（避免 token 爆炸）。
- UI 通过 `structured_json` 判断 kind 并渲染不同卡片。

### 13.3 状态审计

每次状态迁移追加一行 `kind=event, event_type=message.status`，payload 含 `{message_id, from_status, to_status, reason}`；与主表同事务写入。

---

## 14. 迁移策略

### 14.1 阶段划分

| 阶段 | 改动 | 兼容性 | 回滚 |
|---|---|---|---|
| M1（本设计） | 文档 only | 无 | — |
| M2 | 主库 v74：`agent_messages` + `session_messages.structured_json`（与 ③ 的 sender 三列合并为同一个 v74 迁移）；`SendMessage` 双写（旧字符串 + 新信封） | 旧客户端无感 | DROP 表/列（SQLite 需重建表；保留备份） |
| M3 | `SendMessage` 默认走结构化；`kind=text` 兼容旧调用 | 旧 agent 仍可发字符串 | 切回旧路径（feature flag） |
| M4 | AskUser 工具下线，Web/CLI/Feishu 切数据源 | 旧历史只读 | 重新注册工具 |
| M5 | 清理旧 wait 状态与协议事件 | 需要客户端升级 | 保留旧代码分支一个版本 |

### 14.2 Feature flag

```json
{
  "features": {
    "structured_messages": "off|shadow|on",
    "ask_user_via_message": "off|on"
  }
}
```

- `shadow`：新旧都写，读旧；用于生产验证。
- `on`：新路径权威。
- 默认 `off`，灰度后再开。

### 14.3 数据回填

- `agent_messages` 新表：无需回填。
- `session_messages.structured_json`：旧行默认 `''`，表示「非结构化」。
- 旧 `ask_question`/`ask_answer`：不回填为 `agent_messages`；历史只读。

---

## 15. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 异步化改变 agent 行为：原来同步拿到回复，现在拿不到 | agent 可能重复提问 | 文档/prompt 明确「回答异步到达」；提供 `delivery.await_ms` 兼容窗口 | 关闭 feature flag |
| 消息重复注入导致 LLM 上下文重复 | token 浪费/行为异常 | 幂等键 + 投影去重（以 message_id 唯一） | 关闭结构化投递 |
| 主库写入成为热点 | 吞吐下降 | 单条 INSERT + WAL；批量场景按会话分片 | 降级为会话库投影 only |
| 旧客户端收到未知 kind | UI 报错 | 未知 kind 渲染为 text；`structured_json` 可选 | 双写期保留旧事件 |
| AskUser 删除导致旧会话恢复失败 | 无法回答历史问题 | 保留读取/折叠；旧 pending 只读 | 重新注册工具 |
| 权限校验误判 | agent 无法通信 | 灰度 + 详细错误码；白名单开关 | 放宽到「同租户即可」 |
| hop 限制过严 | 多级转述被截断 | 默认 4，可配置；超限回执明确 | 提高 max_hops |

---

## 16. 验收判据

### 16.1 功能

1. `SendMessage(to="agent:x/y", message="hi")` 在 ≤100ms 返回 `{ok:true, status:"queued", message_id:"msg_..."}`。
2. `SendMessage(kind="ask_user", ...)` 返回后发送方 run 继续；用户回答后，发送方下一轮上下文出现 `kind=answer` 的消息，`reply_to` 正确。
3. 同一 `idempotency_key` 重复发送 10 次，`agent_messages` 只有 1 行，返回同一 `message_id`。
4. TTL 到期后 30s 内产生 `receipt(expired)` 并注入发送方。
5. 取消未回答的 ask_user 后，再次回答返回 `CANCELLED`。
6. 非法答案（multi_select=false 选 2 项）返回 `INVALID_ANSWER`，原问题仍 pending。
7. 跨组/跨租户发送返回 `PERMISSION_DENIED`/`NOT_FOUND`，不泄露目标存在性。
8. hop 达到 max_hops 时返回 `HOP_LIMIT`，消息不投递。

### 16.2 兼容

9. `GetBuiltinToolNames()` 不含 `AskUser`。
10. 载入含 `ask_question` 的旧历史：不报错、不弹面板、显示折叠块。
11. 旧 `SendMessage(to, message)` 调用 100% 成功（kind 缺省 = text）。

### 16.3 审计

12. `SELECT * FROM agent_messages WHERE correlation_id=?` 能还原完整问答链。
13. 群组会议纪要 = `WHERE group_id=? ORDER BY created_at,id` 的结构化事件流，含发送者。

### 16.4 测试清单

- 单元：envelope 校验、幂等、状态机、权限矩阵、hop。
- 集成：`SendMessage` → 注入 → 回答 → 回调（CLI/Web/Feishu 三渠道）。
- 回归：旧历史 replay、旧工具调用、`WaitingUser` 旧会话恢复。
- 前端：AskUserPanel 数据源切换、历史折叠、回答徽标。

---

## 17. 受影响代码清单（file:line）

### 17.1 新增（设计落地时）

| 文件 | 作用 |
|---|---|
| `tools/message_types.go` | 信封/kind/payload 类型与校验 |
| `tools/message_service.go` | Send/Resolve/Cancel/Expire |
| `tools/message_routes.go` | 地址解析、权限、投递 |
| `storage/sqlite/agent_messages.go` | 主库读写 |
| `storage/sqlite/migrations.go`（v74） | 建表/加列 |

### 17.2 修改

| 文件:行 | 改动 |
|---|---|
| `tools/send_message.go:64-121` | 参数扩展、分派到 MessageService |
| `tools/send_message.go:123-190` | 去掉 2s 等待默认语义（保留可选 await_ms） |
| `tools/send_message.go:240-353` | group 广播改为结构化投递 |
| `tools/send_message.go:380-428` | peer 广播改为结构化投递 |
| `tools/registry_helpers.go:258` | 删除 AskUser 注册 |
| `tools/task_manager.go:964-1005` | 增加 MessageID/Envelope 字段 |
| `agent/agent.go:5081-5142` | 注入携带结构化信封 |
| `agent/agent.go:3920-3985` | AskUser 回答路径改造 |
| `agent/engine_run.go:2405,2427,2488` | 等待元数据改造 |
| `agent/agent_process.go:550-620` | WaitingUser 出站改造 |
| `protocol/events.go:105-110,462-493` | 标 deprecated；新增 structured message 事件 |
| `protocol/ws.go:15-22,64,83-128` | 新增 `structured_message` 类型；旧类型保留 |
| `channel/web/web.go:902,1104-1135,1524-1570` | 数据源切换 |
| `channel/web/web_inbound.go:444-480` | answer 转换 |
| `channel/cli/cli_panel_askuser.go:305` | 数据源切换 |
| `channel/feishu/feishu.go:1920-2490` | 数据源切换 |
| `storage/sqlite/schema.go:40-74` | `structured_json` 列 |
| `storage/sqlite/sessiondb.go:51-76` | 同上（会话库） |
| `storage/sqlite/history.go:291-343,857-899` | 写入/读取新列 |
| `channel/subscription.go:1408-1490` | 投影转换带 sender/structured |
| `web/src/types/shared.ts:575-615` | 新增 structured 字段 |
| `web/src/components/agent/AskUserPanel.tsx` | 数据源 |
| `web/src/hooks/useAskUser.ts` | 数据源 |
| `prompt/channels/cli.md:4` | 改用 SendMessage |
| `prompt/channels/feishu.md:29-30` | 同上 |
| `docs/agent/tools.md:90-115,193-204` | 更新文档 |

### 17.3 删除

| 文件 | 时机 |
|---|---|
| `tools/ask_user.go` | 阶段 C |
| `agent/agent.go:535,674,1242-1480` 的 pending 逻辑 | 阶段 C |
| `channel/cli/cli_panel_askuser.go` | 阶段 C |
| `web/src/components/agent/AskUserPanel.tsx` | 阶段 C（先改造再删） |

---

## 18. 开放问题（需用户拍板）

1. **`agent_messages` 放主库还是会话库？** 本设计选主库（跨会话），代价是主库写入增加；若坚持「一会话一库」纯净性，可改为「收件方会话库 + 发送方收件箱旁路」。
2. **`ask_user` 是否允许 agent→agent？** 本设计允许（用于子代理向上级提问），但要求 directed + 同组；若只允许 agent→user，可删掉该分支。
3. **旧 `AskUser` 工具保留多久？** 本设计给三阶段（约两个版本）；需要确认兼容窗口。
4. **结构化消息是否进 LLM 全量 payload？** 本设计只进 `text` 摘要；若需要模型看到完整 JSON（例如 report 的 evidence 列表），需定义 token 预算。
5. **`decision.signature` 是否本期启用？** 预留字段，未实现校验。
