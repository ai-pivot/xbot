# SendMessage 结构化消息 + 异步 ask_user —— 详细设计（v2）

> 状态：设计稿（**不含实现代码**，只含 schema / 接口 / 迁移 / 时序）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`（隔离工作树 `/tmp/xbot-court`）
> 关联：`message-sender-id.md`（③）、`group-hardening.md`（④）、`court-reporting.md`（⑤）
> 作者：taizi
> **版本：v2（2026-10-19）** —— 按用户拍板决策全面修订，冲突处以本版为准。

---

## 修订记录（v1 → v2，作废项）

| # | v1 设计 | v2（已拍板） | 理由 |
|---|---|---|---|
| R1 | 结构化消息落主库一张中心消息表 + 会话投影行 | **消息只落会话分库**（`session_messages` / `iteration_history`），主库不放任何消息表 | A：绝不碰主库 |
| R2 | 群组有独立消息模型、群级消息表、投递模式字段（广播/定向） | **没有「群组消息」这种东西**：只有群发（遍历成员各发一份、各写各的会话库）与单发 | B |
| R3 | AskUser 三阶段兼容（保留旧事件、旧历史只读回放） | **旧 AskUser 工具与旧交互直接删除**，不做兼容窗口、不做只读回放 | C |
| R4 | `ask_user` 允许「等回答」，用同步等待参数兼容 | **完全异步**：投递即返回；用户回复走**插队优先队列**，在下一个 iteration 边界注入 | C |
| R5 | 幂等/状态机建在主库中心消息表的状态列上 | 幂等落在**发送方会话库**的唯一索引；状态由 `kind=receipt` 行表达 | A（无主库表） |

---

## 0. 一页纸结论

1. **`SendMessage` 是「结构化信封 + 地址」**：`kind`/`payload`/`correlation_id`/`reply_to`/`idempotency_key`/`hop`；`message` 字符串 = `kind=text` 的快捷方式。
2. **消息的权威副本永远在参与者的会话分库里**。一次投递 = 发送方会话库一条 `direction=out` 的审计行 + 收件方会话库一条 `direction=in` 的消息行。**主库（`xbot.db`）不新增任何消息表**。
3. **群发不是一种消息类型**：群发 = 遍历 `group_members(status='alive')`，对每个成员各发一份同样的 `SendMessage`；每份写各自的会话库，带 `group_id` 与 `sender_*`。群历史 = 跨会话库按 `group_id` 聚合查询（见 ④）。
4. **`SendMessage` 永远异步**：持久化成功即返回 `{message_id, status}`，不等待回复。
5. **`ask_user` 完全异步**：投递即返回并带 `correlation_id`。用户回复是**插队消息**（优先级高于普通排队消息），agent 忙则在**下一个 iteration 边界**注入（不打断当前工具调用），空闲则立即开启新 turn；回复带 `reply_to=<correlation_id>`。
6. **旧 `AskUser` 工具、等待态、旧 WS 事件、旧控制记录读写、旧历史只读回放全部删除**（无兼容窗口），前端换成新问题卡片（单选/多选/自定义/取消/超时）。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 可验证判据 |
|---|---|---|
| G1 | 结构化消息 schema 稳定、可版本化 | `schema` 字段 + 单一 `kind` 注册表；未知 kind 拒绝并回执 |
| G2 | ask_user 能力不丢 | payload 覆盖单选/多选/自定义/取消/超时四类交互；前端卡片可回答 |
| G3 | 完全异步 + 回调 | `SendMessage` 返回耗时 < 100ms（本进程本地写库）；回答经插队队列注入 |
| G4 | 幂等/去重语义明确 | 同一 `idempotency_key` 重复提交返回同一 `message_id`，收件方只有一行 |
| G5 | 消息只在会话库、可跨会话聚合 | 任意消息可由 `(channel, chatID)` + `message_id` 定位；主库 `SELECT` 不到任何消息内容 |
| G6 | 群发/单发语义唯一 | 地址只有 `agent:`/`session:`/`group:`/IM；`group:` 的投递 = N 份独立单发 |
| G7 | ask_user 回复高优先级且不打断工具 | 运行中收到回复：当前工具调用完整执行 → 持久化 → 下一 iteration 首条注入 |
| G8 | 旧机制彻底退场 | `GetBuiltinToolNames()` 无 `AskUser`；代码中无等待态/旧事件/旧回放分支 |

### 1.2 非目标

- 不实现通用消息队列/事务消息（不引入 Kafka/NATS）；本地 SQLite 持久化 + 进程内队列即可。
- 不做端到端加密/签名（`decision.signature` 字段预留，本期不校验）。
- 不改变 IM 渠道（Feishu/Web/CLI）的入站消息模型；只新增「结构化消息」这一层。
- 不把 `SendMessage` 变成同步 RPC；需要结果的调用方等异步回调。
- 不实现消息编辑/撤回；只做追加式收据（receipt 也是消息行）。
- **不保留**旧 `ask_question`/`ask_answer` 的写路径、等待态、只读回放；旧行按普通历史行展示（详见 §12）。

---

## 2. 现状调研（事实 + 证据）

### 2.1 SendMessage 现状

| 事实 | 证据 |
|---|---|
| 工具参数只有 `to` + `message` 两个字符串 | `tools/send_message.go:64-67` |
| 按地址前缀分派：`agent:` / `group:` / `peer:` / `session:` / IM | `tools/send_message.go:76-121` |
| `agent:` 目标在 2s 窗口内顺手拿回复，超时即返回「已投递」 | `tools/send_message.go:123-190`、`tools/limits.go:61-65` |
| 后台投递使用脱离工具 ctx 的独立上下文 | `tools/send_message.go:136-155` |
| group 目标无消息存储，只是内存成员集合；广播 = 对每个成员并发 full-turn 调用 | `tools/group_state.go:19-40`、`tools/send_message.go:240-353` |
| peer 目标走 `PeerMessageFn` → `AsyncMessageNotification`；busy→synthetic tool result，idle→user message | `tools/send_message.go:380-428`、`agent/agent.go:4941-4960` |
| 异步注入统一走 `AsyncMessageNotification` → `BgTaskManager` 队列 | `tools/task_manager.go:942-1005` |
| 注入消息当前没有结构化 sender 身份，只有 `Sid`（会话属主 user id） | `tools/task_manager.go:964-968` |
| 入站消息已有 `SenderID`/`SenderName`，但未落库 | `bus/bus.go:13-32`、`agent/engine_wire.go:146-147` |

### 2.2 AskUser 现状（将被整体删除）

| 事实 | 证据 |
|---|---|
| 工具返回 `WaitingUser:true` + metadata，由引擎暂停 run | `tools/ask_user.go:98-125`、`agent/engine_run.go:2264-2286` |
| 工具注册在 core registry | `tools/registry_helpers.go:258` |
| 等待态「每 (channel, chatID) 至多一个」，内存 entry + DB 控制记录双份 | `agent/agent.go:535,674,1242-1320,1390-1480` |
| 回答作为 inbound message 回来，写 `ask_answer` + 一条 normal user 行 | `agent/agent.go:3920-3985`、`storage/sqlite/history.go:728-760` |
| Web/CLI/Feishu 各有独立面板/卡片 | `channel/web/web.go:1104-1135,1524-1560`、`channel/cli/cli_panel_askuser.go:305`、`channel/feishu/feishu.go:1920,2264,2401` |

### 2.3 存储现状（v71 拆库后）

| 事实 | 证据 |
|---|---|
| 会话库路径由 `sqlite.SessionDBRelPath(channel, chatID)` 派生 | `storage/sqlite/sessiondb.go:211-222` |
| 会话库 schema = 主库子集（`session_messages` + `iteration_history` + 索引） | `storage/sqlite/sessiondb.go:20-90` |
| 会话库版本独立：`sessionSchemaVersion = 3` | `storage/sqlite/sessiondb.go:45` |
| 惰性迁移是**显式列清单** `INSERT ... SELECT`（不是 `SELECT *`），新增列天然拿到默认值 | `storage/sqlite/sessiondb.go:296-320` |
| 主库仍保留 v71 之前的 `session_messages` 表（`migrated=0` 的会话仍读它） | `storage/sqlite/schema.go:40-74`、`tenants.migrated`（`:34`） |
| 会话库写收口：`appendMessageWith`（显式 INSERT 列清单） | `storage/sqlite/history.go:291-337` |
| 主库 schema 版本 73 | `storage/sqlite/db.go:67` |

### 2.4 关键缺口

1. **无 correlation**：普通 `SendMessage` 的「发出去—回过来」没有关联键。
2. **无持久化信封**：投递内容只以 `[group:g1] ...` 前缀字符串存在于目标会话，无法还原「谁发给谁、哪条、什么状态」。
3. **异步是「投递即忘」**：没有重试、没有幂等、没有超时收据。
4. **AskUser 与 SendMessage 两套机制**：前者要求目标会话正在等待，后者要求地址可寻址。
5. **群组消息丢失发送者**：`[group:%s] %s` 前缀（`tools/send_message.go:292,336`）无法结构化还原。
6. **等待态跨重启恢复复杂**：内存 + DB 双份状态（`agent/agent.go:1242-1480`）。

---

## 3. 术语与常量

| 术语 | 定义 |
|---|---|
| **信封（envelope）** | 一条结构化消息的完整 JSON 对象，存于 `session_messages.structured_json` |
| **kind** | 信封语义类型，决定 `payload` schema |
| **message_id** | 一份具体投递的唯一 ID（`msg_<ULID>`）；群发时每个收件人各一份、ID 不同 |
| **correlation_id** | 一次「请求—响应」会话的关联键；ask_user/report/decision 必填 |
| **reply_to** | 本条消息直接回复的那条 `message_id` |
| **causation_id** | 触发本条消息的上一条消息（链路追踪，可空） |
| **hop** | 消息经过的 agent 跳数；每转发一次 +1 |
| **outbound 行** | 发送方会话库里 `direction='out'` 的审计行（不进 LLM 上下文，不渲染气泡） |
| **inbound 行** | 收件方会话库里 `direction='in'` 的消息行（进 LLM 上下文、可渲染） |
| **receipt** | `kind=receipt` 的状态消息（也是写进相关会话库的一行） |

常量：

```text
schema:              "xbot.message/v1"
default_max_hops:    4
default_ttl_ms:      86_400_000        # 24h；ask_user 默认 1h
max_payload_bytes:   256 KiB
max_fanout:          128               # 单次群发最大收件人
id_prefix:           msg_ / cor_ / rcp_
id_format:           ULID（26 字符 Crockford Base32，时间有序）
group_id_format:     grp_<nanoid(12)>
```

---

## 4. 消息信封 schema（字段级）

### 4.1 顶层字段

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `schema` | string | 是 | 固定 `xbot.message/v1` |
| `id` | string | 是 | `msg_<ULID>`；服务端生成，调用方不可指定 |
| `kind` | string | 是 | 见 §5；未知 kind → `INVALID_KIND` 回执 |
| `from` | object | 是 | **服务端填充**，忽略调用方传入（防伪造） |
| `from.type` | enum | 是 | `user` / `agent` / `system` |
| `from.id` | string | 是 | user id 或 `role:instance` |
| `from.name` | string | 否 | 展示名 |
| `from.session` | string | 否 | `channel:chatID` 或 agent 会话 key |
| `to` | object | 是 | 目标地址（单发解析后的规范形式） |
| `to.type` | enum | 是 | `user` / `agent` / `session` / `im`；**没有 `group`**（群发是 N 次单发） |
| `to.id` | string | 是 | 解析后的目标 ID |
| `group_id` | string | 否 | 群发场景的群 ID（仅作标记与聚合查询，不参与路由） |
| `correlation_id` | string | 否 | 请求—响应链；`ask_user`/`report`/`decision` 必填 |
| `causation_id` | string | 否 | 触发消息 ID |
| `reply_to` | string | 否 | 直接回复的消息 ID（ask_user 回复填 `correlation_id` 对应的原问题 `message_id`） |
| `idempotency_key` | string | 否 | 发送者作用域内唯一（建议 `<role>/<instance>:<业务键>`） |
| `created_at` | string | 是 | RFC3339Nano，服务端时钟 |
| `expires_at` | string | 否 | 到期后由 sweeper 产生 `receipt(expired)` |
| `hop` | integer | 是 | 初始 0；agent 间转发 +1 |
| `max_hops` | integer | 是 | 默认 4；`hop >= max_hops` 拒绝并回执 `HOP_LIMIT` |
| `priority` | integer | 否 | 0=普通；100=用户插队回复（仅入站队列使用，不随消息转发） |
| `payload` | object | 是（除 text） | 按 kind 定义 |
| `text` | string | 否 | 人读渲染文本；`kind=text` 时必填 |
| `meta` | object | 否 | 扩展键值，不参与路由 |

> `delivery` 对象（v1 的广播/定向/同步等待参数）**删除**：路由由地址决定，群发由 `group:` 地址展开，不存在"投递模式"字段。

### 4.2 示例（ask_user）

```json
{
  "schema": "xbot.message/v1",
  "id": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "kind": "ask_user",
  "from": {"type": "agent", "id": "hubu/main", "name": "户部", "session": "agent:hubu/main"},
  "to": {"type": "user", "id": "u_10086", "name": "皇帝"},
  "correlation_id": "cor_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "idempotency_key": "hubu-report-20261019-q1",
  "created_at": "2026-10-19T12:00:00.123Z",
  "expires_at": "2026-10-19T13:00:00.123Z",
  "hop": 0,
  "max_hops": 4,
  "payload": {
    "questions": [{
      "id": "q1",
      "question": "PPU 集群是否按计划今夜扩容？",
      "options": [
        {"id": "approve", "label": "准", "description": "按 8 节点扩容"},
        {"id": "reject",  "label": "驳", "description": "暂缓，等预算"},
        {"id": "modify",  "label": "改", "description": "先扩 4 节点"}
      ],
      "multi_select": false,
      "allow_other": true,
      "required": true
    }],
    "allow_cancel": true,
    "timeout_ms": 3600000,
    "context_refs": ["court/reports/2026-10-19.md#section-3"]
  },
  "text": "户部请旨：PPU 集群是否按计划今夜扩容？"
}
```

### 4.3 示例（answer，即用户回复 / 插队消息）

```json
{
  "schema": "xbot.message/v1",
  "id": "msg_01J9Z9A1B2C3D4E5F6G7H8J9K0",
  "kind": "answer",
  "from": {"type": "user", "id": "u_10086", "name": "皇帝"},
  "to": {"type": "agent", "id": "hubu/main"},
  "correlation_id": "cor_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "reply_to": "msg_01J9Z8Q3K7V2M4R6T8W0X2Y4Z6",
  "created_at": "2026-10-19T12:04:11.000Z",
  "hop": 0,
  "max_hops": 4,
  "payload": {
    "answers": [{"question_id": "q1", "option_ids": ["modify"], "text": "先扩 4 节点，观察 24h"}],
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
| `ask_user` | agent/system → user | 提问（异步，不阻塞 run） | 是（渲染为「待回答的问题」） |
| `answer` | user/agent → 原发送方 | 回答（插队优先级 100） | 是 |
| `command` | user/agent → agent/system | 控制指令（spawn/kill/pause/resume/reload） | 是 |
| `report` | agent → user/agent | 奏折/汇报 | 是 |
| `event` | 任意 → 任意 | 状态事件（无强回复期望） | 可配置（默认是） |
| `decision` | user → agent | 批红（准/驳/改） | 是 |
| `receipt` | 系统/收件方 → 发送方 | 投递/失败/过期收据 | 否（仅审计/UI） |

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
| `questions[].options` | array | 否 | 0..50 项；无 options = 纯文本回答 |
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
- 校验失败 → 发 `receipt(status=rejected, error.code=INVALID_ANSWER)`，原问题保持 pending。

### 5.4 `answer`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `answers` | array | 是* | `cancelled=false` 时必填 |
| `answers[].question_id` | string | 是 | 对应 `ask_user` 的 question id |
| `answers[].option_ids` | string[] | 否 | 选项 id 列表 |
| `answers[].text` | string | 否 | 自定义输入 |
| `cancelled` | bool | 是 | true = 用户取消；此时 `answers` 可为空 |
| `answered_by` | string | 是 | 回答者 id；服务端校验必须 = `from.id` |

### 5.5 `command`

```json
{"command": "spawn|kill|pause|resume|reload|ack", "args": {"role": "...", "instance": "...", "reason": "..."}, "reason": "string"}
```

`command` 是否需要二次确认由命令注册表决定；本期只定义 schema，不实现具体命令执行器。

### 5.6 `report`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `report_id` | string | 是 | 与 `court/reports/<date>/<dept>.yaml` 的 id 一致 |
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
| `confidence` | number | 否 | 0..1，仅自评，不参与规则判定 |

### 5.7 `event`

```json
{"event_type": "state.changed|member.joined|member.left|group.created|task.blocked", "subject": "group:grp_xxx | agent:... | court/state/...", "severity": "info|warn|error", "data": {}}
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
| `status` | enum | 是 | `delivered`/`rejected`/`expired`/`cancelled`/`failed` |
| `duplicate` | bool | 否 | true 表示重复提交，未重复投递 |
| `error` | object | 否 | `{code, message}` |
| `at` | string | 是 | RFC3339Nano |

错误码枚举：`INVALID_KIND`、`INVALID_PAYLOAD`、`INVALID_ANSWER`、`NOT_FOUND`、`PERMISSION_DENIED`、`MEMBERSHIP_REQUIRED`、`HOP_LIMIT`、`FANOUT_LIMIT`、`RATE_LIMITED`、`TTL_EXPIRED`、`IDEMPOTENCY_CONFLICT`、`DELIVERY_FAILED`、`CANCELLED`。

---

## 6. `SendMessage` 工具接口

### 6.1 参数

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `to` | string | 是 | `agent:<role>/<instance>` / `session:<channel>:<chatID>` / `group:<grp_id>` / IM 地址 |
| `message` | string | 条件 | `kind` 缺省时必填；`kind=text` 时等价 `payload.text` |
| `kind` | string | 否 | 默认 `text` |
| `payload` | object | 条件 | 非 text 必填 |
| `correlation_id` | string | 否 | 缺省由服务端生成；指定时复用已有链（校验存在性） |
| `reply_to` | string | 否 | 回复时必填 |
| `causation_id` | string | 否 | 触发消息 id |
| `idempotency_key` | string | 否 | 发送者作用域内唯一 |
| `ttl_ms` | integer | 否 | 缺省见 §3 |

> 地址前缀 `peer:` **删除**。`to="peer:x"` 返回 `NOT_FOUND`（旧 peer 数据不迁移，见 ④ / §12）。

### 6.2 返回（结构化）

```json
{
  "ok": true,
  "message_id": "msg_01J9...",
  "correlation_id": "cor_01J9...",
  "status": "delivered",
  "recipients": [
    {"addr": "agent:hubu/main", "message_id": "msg_01J9...", "status": "delivered"},
    {"addr": "agent:gongbu/main", "message_id": "msg_01J9...", "status": "delivered"}
  ],
  "duplicate": false
}
```

失败时 `ok=false` + `error.code`/`error.message`（结构化错误体）。

### 6.3 内部接口（设计级）

```text
MessageService
  Send(ctx, Draft) (SendResult, error)
    ├─ 1. 规范化地址/kind/payload
    ├─ 2. 解析 from（ToolContext / session 上下文；忽略调用方 from）
    ├─ 3. 权限校验（§10）
    ├─ 4. hop/TTL/幂等校验（幂等查询发送方会话库）
    ├─ 5. 展开目标：
    │      · 单发 → [1 个收件人]
    │      · group:<id> → SELECT member_addr FROM group_members
    │                    WHERE group_id=? AND status='alive'
    ├─ 6. 写发送方会话库 outbound 行（含 envelope）
    ├─ 7. 逐个收件人：写收件方会话库 inbound 行（按 message_id 去重）
    ├─ 8. 成功者投递通知（busy → 队列；idle → 新 turn）
    └─ 9. 失败者写 receipt(failed) 到发送方会话库；返回 SendResult

  Resolve(ctx, correlationID) ([]Envelope, error)   // 扫发送方会话库
  ExpireDue(ctx, now) (int, error)                  // 会话加载/活跃期定时结算
```

### 6.4 与旧行为差异

| 场景 | 旧 | 新 |
|---|---|---|
| `SendMessage(agent:, message)` | 2s 内可能同步返回回复 | 立即返回 `delivered`；回复异步注入 |
| `SendMessage(group:, message)` | 同步等全部成员（30s）后返回拼接文本 | 立即返回；每个成员各得一份，逐个记结果 |
| 投递失败 | 字符串错误 | 结构化错误 + `receipt(failed)` 行 |
| 重复点击/重试 | 重复发送 | `idempotency_key` 去重 |
| `peer:` 地址 | 可用 | 删除 |

---

## 7. 存储模型（核心）

### 7.1 铁律

1. **消息只在会话库**：`session_messages` / `iteration_history`，路径 `sqlite.SessionDBRelPath(channel, chatID)`（`storage/sqlite/sessiondb.go:211-222`）。
2. **主库不放任何消息表**：不建中心消息表、不建群级消息表、不建全局状态表。
3. **每条消息都是某个会话库的一行**：由 `tenant_id`（会话）唯一归属。
4. **跨会话的可观测性靠 ID 关联 + 聚合查询**，不靠中心表。

### 7.2 会话库 schema 变更（`sessionSchemaVersion 3 → 4`）

```sql
-- 会话库 v4：纯加列（NOT NULL DEFAULT ''），不改既有列
ALTER TABLE session_messages ADD COLUMN sender_type     TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_id       TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN sender_name     TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN message_id      TEXT NOT NULL DEFAULT '';  -- msg_<ULID>
ALTER TABLE session_messages ADD COLUMN group_id        TEXT NOT NULL DEFAULT '';  -- 群发标记
ALTER TABLE session_messages ADD COLUMN kind            TEXT NOT NULL DEFAULT '';  -- text/ask_user/answer/...
ALTER TABLE session_messages ADD COLUMN correlation_id  TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN reply_to        TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN direction       TEXT NOT NULL DEFAULT '';  -- in | out
ALTER TABLE session_messages ADD COLUMN expires_at      DATETIME;                  -- ask_user/report 的超时结算点
ALTER TABLE session_messages ADD COLUMN structured_json TEXT NOT NULL DEFAULT '';  -- 完整信封

-- 一份投递在同一个会话库里只能有一行（重试/重复注入幂等）
CREATE UNIQUE INDEX IF NOT EXISTS ux_sm_message_id
    ON session_messages(message_id) WHERE message_id <> '';
-- 发送者作用域幂等：只约束 outbound 行
CREATE UNIQUE INDEX IF NOT EXISTS ux_sm_idem
    ON session_messages(sender_id, idempotency_key)
    WHERE idempotency_key <> '' AND direction = 'out';
-- 每个 correlation 只允许一个 answer（用户重复提交不重复注入）
CREATE UNIQUE INDEX IF NOT EXISTS ux_sm_answer
    ON session_messages(correlation_id) WHERE kind = 'answer';
-- 群历史聚合 / correlation 链查询
CREATE INDEX IF NOT EXISTS ix_sm_group ON session_messages(group_id, created_at) WHERE group_id <> '';
CREATE INDEX IF NOT EXISTS ix_sm_corr  ON session_messages(correlation_id, created_at) WHERE correlation_id <> '';
CREATE INDEX IF NOT EXISTS ix_sm_kind  ON session_messages(kind, created_at) WHERE kind <> '';
```

字段语义：

| 列 | outbound 行（发送方） | inbound 行（收件方） |
|---|---|---|
| `role` | `assistant` | `user`（agent 发来的也当外部输入） |
| `direction` | `out` | `in` |
| `record_type` | **`outbound`**（新枚举） | `message` |
| `internal_only` | 不参与（由 `record_type` 排除） | 0 |
| `sender_*` | 发送者 | 发送者 |
| `message_id` | 本份投递 ID | 同一 ID |
| `structured_json` | 信封 | 信封 |

- `record_type='outbound'` 是新增枚举（`storage/sqlite/history.go:16-30`）：**不进 LLM 回放，不渲染聊天气泡**，只用于幂等、审计、pending 恢复。渲染与回放需显式跳过。
- `internal_only` 语义保持不变（LLM-only，永不渲染），**不复用**它表达 outbound。

### 7.3 一次发送写了什么

单发 A(agent) → B(agent)：

```text
A 的会话库：
  INSERT session_messages(role=assistant, record_type=outbound, direction=out,
                          message_id=msg_X, kind=..., structured_json=<envelope>,
                          sender_type=agent, sender_id=A, idempotency_key=...)
B 的会话库：
  INSERT session_messages(role=user, record_type=message, direction=in,
                          message_id=msg_X, kind=..., structured_json=<envelope>,
                          sender_type=agent, sender_id=A, group_id='')
```

群发 A → group G（成员 B、C、D）：

```text
A 的会话库：1 条 outbound（message_id=msg_X, group_id=G）
B/C/D 的会话库：各 1 条 inbound（message_id 同为 msg_X, group_id=G）
```

> 群发**不是**一条消息被广播到多个库，而是 N 份独立单发（用户模型的直接映射）。N 份副本共享同一个 `message_id`（同一条逻辑发言），靠 `group_id` + 收件方会话库区分副本；群历史按 `message_id` 去重。

### 7.4 幂等与去重

| 场景 | 机制 |
|---|---|
| 同一发送者重复提交 `idempotency_key` | 查发送方会话库 `ux_sm_idem`；命中则返回原 `message_id`，`duplicate=true`，不再投递 |
| 同一收件人重复收到同一 `message_id` | `ux_sm_message_id` 冲突 → `INSERT OR IGNORE`，不重复注入 |
| 用户重复回答同一问题 | `ux_sm_answer` 冲突 → 返回 `receipt(duplicate=true, status=delivered)`，不重复注入 |
| 投递后进程崩溃重放 | 收件方按 `message_id` 幂等，重放安全 |

> 幂等的边界是**发送方会话库**。跨会话的全局幂等（同一 key 从不同会话发出）不支持，也不需要：`idempotency_key` 的语义就是「我这个发送者的业务键」。

### 7.5 状态与收据（没有全局 status 表）

消息状态由**行本身 + receipt 行**表达：

| 状态 | 表达 |
|---|---|
| `delivered` | 收件方会话库存在该 `message_id` 的 inbound 行 |
| `failed` | 发送方会话库存在 `kind=receipt, ack_of=msg_X, status=failed` 行 |
| `expired` | 同上，`status=expired` |
| `answered` | 发送方会话库存在 `kind=answer, reply_to=msg_X` 行 |
| `duplicate` | 重复提交时新写的 receipt 行 `duplicate=true` |

`sweeper` 只处理 `ask_user`/`report` 等有 `expires_at` 的 outbound 行：

```sql
SELECT message_id, correlation_id, expires_at, structured_json
  FROM session_messages
 WHERE direction='out' AND kind IN ('ask_user','report')
   AND expires_at IS NOT NULL AND expires_at <= ?
   AND NOT EXISTS (SELECT 1 FROM session_messages r
                    WHERE r.kind='receipt' AND r.reply_to = session_messages.message_id)
```

触发时机：会话加载时（惰性）+ 活跃会话的定时 tick（复用 `Agent.periodicCleanup` 节奏，`agent/agent.go:5202-5215`）。**不新增后台全局扫描器**（主库没有消息表可扫）。

---

## 8. 异步投递

### 8.1 投递通道

复用现有异步注入管线（`tools/task_manager.go:942-1005`、`agent/agent.go:4941-4960`），扩展通知类型：

```text
AsyncMessageNotification {
  Key       string   // 目标 session key
  Sid       string   // 会话属主 user id（现有字段）
  Content   string   // 渲染文本
  Source    string   // 新增: "structured_message" / "ask_user_reply"
  MessageID string   // 新增
  Envelope  []byte   // 新增: 完整信封 JSON
  Priority  int      // 新增: 0 普通；100 用户插队回复
}
```

- **busy**：进入会话的插队/通知队列，在下一个 iteration 边界注入（§9）。
- **idle**：作为一条 user 消息注入并开启新 turn（`injectInboundWithMetadata`，`agent/agent.go:4959`）。
- **不阻塞**：`Send()` 只负责「写库 + 入队」，不等待目标处理。

### 8.2 回调（收件方 → 发送方）

| 触发 | 回程 kind | reply_to | correlation_id |
|---|---|---|---|
| 用户回答 `ask_user` | `answer` | 原问题 `message_id` | 原 correlation |
| agent 对 `ask_user` 的回答 | `answer` | 原 id | 原 correlation |
| 用户批红 `report` | `decision` | 原 `report` 的 `message_id` | 原 correlation |
| 收件方显式 ack/拒绝 | `receipt` | 原 id | 原 correlation |
| 目标不存在/无权限 | `receipt(failed)` | 原 id | 原 correlation |

回程消息**写发送方会话库**（inbound 行），因此发送方下一轮 LLM 上下文会看到：

```text
<from agent:hubu/main, kind=answer, correlation=cor_..., reply_to=msg_...>
Q1: 改 — 先扩 4 节点，观察 24h
</from>
```

### 8.3 顺序与去重

- **同一 correlation 内**：按 `created_at` + `message_id`（ULID 有序）FIFO 注入。
- **跨 correlation**：无全局顺序承诺（best-effort）。
- **去重**：见 §7.4。
- **重试**：仅投递失败重试（3 次，200ms/1s/5s 退避）；LLM 处理失败不自动重试。

### 8.4 超时与取消

- `expires_at` 到期 → 写 `receipt(expired)` 到发送方会话库，并注入一条用户可见提示。
- 取消：发送方发 `kind=command, command=cancel, args.target=<message_id>`；取消 `ask_user` 时写 `receipt(cancelled)`，并把该 correlation 标记为 closed（之后到的 answer 返回 `CANCELLED`）。
- 取消幂等：已 answered/expired 的消息返回 `CANCELLED` 但不改变终态。

### 8.5 时序

```text
发送方 agent            MessageService            收件方会话库 / 用户
    │                        │                         │
    │ SendMessage(ask_user)  │                         │
    ├───────────────────────▶│                         │
    │                        ├─ 权限/hop/ttl/幂等       │
    │                        ├─ 写 outbound 行          │
    │                        ├─ 写 inbound 行 ─────────▶│
    │◀── {id,corr,delivered}─┤                         │
    │  (工具立即返回，继续跑)  │                         │
    │                        │      用户回答 answer      │
    │                        │◀────────────────────────┤
    │                        ├─ 写 answer 行（发送方库） │
    │                        ├─ 入插队队列(priority=100) │
    │◀── 下一 iteration 首条 ─┤                         │
```

---

## 9. ask_user：完全异步 + 插队队列

### 9.1 语义

1. `SendMessage(kind=ask_user)` **只投递，不等待**；返回 `{message_id, correlation_id, status:"delivered"}`。
2. 发送方 agent 继续执行；「必须等回答」的场景由 agent 自行结束本轮，回答到达后自然开启新 turn。
3. 用户回复生成 `kind=answer`，写入提问 agent 的会话库，并作为**插队消息**进入该 agent 的入站队列。

### 9.2 插队队列语义（优先级）

| 优先级 | 消息 | 说明 |
|---|---|---|
| 100 | `ask_user` 的 `answer`（用户提交问题卡片） | 用户明确在回答一个待决问题 |
| 10 | 用户在 agent 运行中的普通插话（⚡ interject） | 现状已有 |
| 0 | 普通排队消息、通知、cron | 现状默认 |

规则：

1. **严格优先**：同一会话内，队列非空时优先取优先级最高的消息；同级 FIFO。
2. **防饿死**：连续处理 8 条高优先级消息后，强制取 1 条普通消息（普通消息不会被无限推后）。
3. **不打断工具**：优先级只影响「下一条消息何时被取」，**绝不中断正在执行的工具调用/LLM 请求**。
4. **合并注入**：同一 iteration 边界若有多条同 correlation 的消息，按 `created_at` 顺序、一次性注入。

### 9.3 两个注入点（file:line）

| 场景 | 代码位置 | 行为 |
|---|---|---|
| **agent 空闲** | `agent/agent.go:4943-4960`（`injectInbound` / `injectInboundWithMetadata`） | 直接作为 user 消息进入入站队列 → 开启新 turn |
| **agent 运行中** | `agent/engine_run.go:2417`（每轮 iteration 末尾调用 `drainAndInjectBgNotifications`），实现 `agent/engine_run.go:2502-2545` | 在「工具执行完 + 增量持久化成功之后、下一次 LLM 调用之前」注入 synthetic tool pair |
| **interactive SubAgent 运行中** | `agent/interactive.go:1843-1917`（`pendingMessages` 入队 + `subAgentSendAckWait`），清理点 `:2282-2291`；drain 入口 `agent/interactive.go:64,85-89,882-885` | 同样在 iteration 边界取队列 |
| **会话级主循环取消息** | `agent/session_queue.go:1-80`（`queuedEntry` 影子队列）、`agent/agent.go` 的 `chatProcessLoop` | 新增 priority lane，影子队列同步记录 `Priority` |

**为什么是 iteration 边界**：`engine_run.go:2417` 位于工具执行与持久化之后、下一轮 LLM 请求之前；在此注入既能让模型立刻看到回答，又不会破坏当前工具调用的完整性（不会产生「工具只执行了一半就换上下文」）。

### 9.4 与 `pendingMessages` 的关系

- `pendingMessages`（`agent/interactive.go:64`）是 interactive SubAgent 的运行中队列；`ask_user` 回复按 `Priority=100` 插入，drain 时**先取高优先级**（改 `interactive.go:85-89` 的取队逻辑为按 priority 排序，保持同级 FIFO）。
- 主 Agent 的 `bgRunPending` 队列（`agent/engine_run.go:2502` 的 `DrainBgNotifications`）同理：在 `drainAndInjectBgNotifications` 内先做一次 **stable sort by (Priority desc, seq asc)** 再注入。
- 两者都**不改变**「消息在出队时才持久化/注入」的既有约束（`agent/askuser_after_tool_test.go:16-21` 记录的教训）。

### 9.5 幂等/去重

| 事件 | 机制 |
|---|---|
| 用户重复点「提交」 | `ux_sm_answer(correlation_id)`：第二次 `INSERT OR IGNORE` 返回 0 行 → 返回 `receipt(duplicate=true)`，不注入 |
| 前端重连导致事件重放 | 前端以 `correlation_id` 去重；后端以 answer 行存在性判定 |
| 同一问题被两个 worker 同时结算超时 | 先写 receipt 的事务获胜（`INSERT OR IGNORE` + 唯一键），另一方 no-op |
| 回答到达时问题已被取消 | 只写 `receipt(cancelled)`，**不落 answer 行**；agent 收到取消提示而非答案 |

### 9.6 前端问题卡片（重新设计，替换旧 AskUserPanel）

数据源：`kind=ask_user` 的 inbound 行（`structured_json`）。

| 元素 | 行为 |
|---|---|
| 单选 | `multi_select=false && options>0`：radio |
| 多选 | `multi_select=true`：checkbox，提交时校验 required |
| 自定义 | `allow_other=true`：额外输入框（与选项互斥或并存由 `required` 决定） |
| 取消 | `allow_cancel=true`：按钮 → 提交 `answer{cancelled:true}` |
| 超时 | 显示倒计时；到期后卡片变灰，后端写 `receipt(expired)` |
| 多问题 | 一次卡片内分页/纵向排列；一次性提交全部 `answers[]` |

新 WS 事件（旧 `ask_user` / `ask_user_resolved` / `ask_user_response` **删除**）：

```text
question_card            # 服务端 → 前端：渲染问题卡片（payload = ask_user envelope）
question_card_answer     # 前端 → 服务端：提交回答（payload = answer envelope）
question_card_resolved   # 服务端 → 前端：已答/取消/超时，卡片关闭
```

事件只携带信封 JSON，不再有 `request_id`/`WaitingUser` 等旧字段。

### 9.7 超时结算

- `expires_at` 由 `ask_user.timeout_ms` 计算。
- 结算时机：会话加载时惰性 + 活跃会话定时 tick（§7.5）。**没有全局扫描器**。
- 结算动作：写 `receipt(expired)`（发送方会话库）+ 发 `question_card_resolved(reason=timeout)` + 向 agent 注入一条 `event(severity=info)`。
- 会话长期不活跃：不结算；下次加载时补结算（幂等，时间戳用 `expires_at` 而非 `now`，保证审计一致）。

---

## 10. 权限模型

### 10.1 身份来源

| 发送方 | 身份解析来源 | 可信度 |
|---|---|---|
| 用户 | `ToolContext.OriginUserID` / inbound `SenderID` | 服务端鉴权后可信 |
| 主 agent | `AgentID` + `channel:chatID` | 服务端上下文 |
| 子 agent | `role:instance` + 父会话 tenant | 服务端上下文 |
| system | 定时器/hook 注入 | 内部调用 |

**禁止**从 `payload.from` 取值；该字段仅用于输出展示。

### 10.2 权限矩阵

| 发送方 \ 目标 | 自己的 user | 同租户 agent | 同群成员 | 非同群 agent | 群（作为发送者） | 其他租户 |
|---|---|---|---|---|---|---|
| user | ✅ | ✅（自己的会话） | ✅ | — | ✅（须为成员） | ❌ |
| agent（主） | ✅（自己的 owner） | ✅ | ✅ | ✅（1:1 允许） | ✅（须为成员） | ❌ |
| agent（子） | ✅（父会话 owner） | ✅（同群/同父） | ✅ | ❌ | ✅（须为成员） | ❌ |

> 群发豁免：`groups.owner`（群宿主会话）即使不在 `agent:` 成员列表里，也允许群发；其他发送者必须是 `status='alive'` 的成员。
| system | ✅ | ✅ | ✅ | ✅（内部） | ✅ | ❌ |

附加规则：

1. SubAgent 必须 `capabilities.send_message=true`（`tools/subagent_roles.go:23-27`）。
2. `ask_user` 的 `to` 必须是 `user` 或 `agent`；`group:` 目标不允许 `ask_user`（群发提问语义不明确）。
3. `decision` 的发送者必须是 `user`，且 `target_report_id` 属于同一 owner。
4. `command` 的 `spawn/kill` 仅 user/system 可发；agent 只能发 `pause/resume/reload` 给自己的子 agent。
5. **群发要求 `from` 是 `group_members` 中 `status='alive'` 的成员**（`MEMBERSHIP_REQUIRED`）。
6. 知道 `grp_*` ID 不等于有权发：先查 `groups.owner` + 成员表。

### 10.3 租户/归属轴

- 消息的隔离轴是**会话库本身**（`tenant_id` + 物理文件）；不存在中心表的租户列。
- 群组归属轴是 `groups.owner`（创建者的稳定 user id）。
- 跨租户 ID 猜测：`group:` 返回 `NOT_FOUND`；`agent:`/`session:` 由地址解析层拒绝，不泄露存在性。

---

## 11. 群发 / 单发语义（替代 v1 §11「群组场景」）

| 场景 | 地址 | 行为 |
|---|---|---|
| 单发 | `agent:` / `session:` | 1 份投递：1 条 outbound + 1 条 inbound |
| 群发 | `group:<grp_id>` | N 份单发：1 条 outbound + N 条 inbound（各库，共享 `message_id`） |
| 群内 @ 个人 | `agent:`（提示词层把 `@` 解析成地址） | 单发；不产生群标记（或保留 `group_id` 由调用方决定） |
| 群内回复发起者 | `agent:<发起者>` | 单发；`reply_to` 指向原消息 |
| `ask_user` | `user:` / `agent:` | 单发，强制 directed |
| `decision` | `agent:<report 作者>` | 单发 |

约束：

1. **没有 `to.type=group`**：群 ID 只出现在 `group_id` 字段（标记）与 `group_members` 查询（路由）。
2. **没有群级消息表**：群历史 = 跨会话库聚合（④ §6）。
3. **成员回复默认单发给发起者**（prompt 约定），避免 N×N；显式群发必须受 fanout/hop/令牌桶限制。
4. **非成员不能群发**（D6）。
5. **`left` 成员不再投递**：群发时 `WHERE status='alive'`。

---

## 12. 删除清单（AskUser + 旧群组，无兼容）

### 12.1 AskUser 直接删除

> 用户决策 C：**不保留兼容、不做只读回放、不设兼容窗口**。以下全部在同一个 PR 删除。

| 层 | 文件:行 | 处理 |
|---|---|---|
| 工具实现 | `tools/ask_user.go`（1-132） | 删除文件 |
| 工具注册 | `tools/registry_helpers.go:258` | 删除注册 |
| 引擎等待 | `agent/engine_run.go:2264-2286,2405,2427,2488` | 删除 `WaitingUser` 分支与 metadata 拼装 |
| 引擎工具 | `agent/engine_run_tools.go:692` | 删除 |
| 等待态 | `agent/agent.go:535,674,1242-1480` | 删除 `pendingAskUser*` 全部字段/方法 |
| 回答处理 | `agent/agent.go:3920-3985` | 删除旧 `ask_user_answered` 路径（改走 §9） |
| 出站构造 | `agent/agent_process.go:550-620` | 删除 AskUser 分支 |
| 存储写 | `storage/sqlite/history.go:602-676,728-790`（`AppendAskQuestion*` / `AppendAskAnswer*`） | 删除写方法；读路径随回放一起删除 |
| 控制记录类型 | `storage/sqlite/history.go:16-30`（`ask_question`/`ask_answer`） | 删除枚举；同时删除 `PendingAskUser` 及其在 `ContextSnapshot`/`ReplayResult` 的字段（`:88-92,1090-1105`） |
| 协议结构 | `protocol/events.go:105-110,462-493` | 删除 `AskUserQuestion`/`AskUserEvent`/`AskUserResolvedEvent` |
| WS 类型 | `protocol/ws.go:15-22,64,83-88,120-128` | 删除旧类型，新增 §9.6 三事件 |
| Web | `channel/web/web.go:902,1104-1135,1524-1570`、`channel/web/web_inbound.go:444-480` | 删除旧面板/旧分发，接新卡片 |
| CLI | `channel/cli/cli_panel_askuser.go`（整文件）、`cli_helpers.go:270`、`cli.go:873-919`、`cli_agent_msg.go:16-19,370` | 删除，接新卡片 |
| Feishu | `channel/feishu/feishu.go:1920,1992,2010,2144,2243,2264,2401,2460,2490` | 删除旧卡片，接新卡片 |
| Prompt | `prompt/channels/cli.md:4`、`prompt/channels/feishu.md:29-30` | 改为「用 SendMessage(kind=ask_user)」 |
| 前端 | `web/src/components/agent/AskUserPanel.tsx`、`web/src/hooks/useAskUser.ts` | 删除，新建 `QuestionCard.tsx` / `useQuestionCard.ts` |
| 测试 | `tools/ask_user_test.go`、`agent/*askuser*_test.go`、`channel/web/web_askuser_resolved_test.go`、`cmd/xbot-cli/askuser_resolved_test.go`、`web/src/components/agent/AskUserPanel.test.tsx`、`web/src/hooks/useAskUser.test.tsx` | 删除，新测试覆盖 §15 |

**旧历史行的展示**：库里可能残留 `record_type='ask_question'/'ask_answer'` 的行。处理方式 = **按普通历史行展示/跳过**（`role` 决定气泡），不做折叠块、不做只读面板、不做语义解析。回放时这两个 record_type 视为未知控制记录直接跳过（`isControlRecordType` 已覆盖非 `message` 类型）。

**数据库列**：旧 `record_data`/`target_history_id` 保留（通用列），不因删除 AskUser 而 DROP。

### 12.2 旧群组删除（详见 ④）

- `$XBOT_HOME/peer_groups.json`：删除文件（不迁移、不导入）。
- `tools/group_state.go` 的 `GroupMembership` / `groupStore` / `peerGroupStore` / `FlushPeerGroups`：删除。
- `SendMessage` 的 `peer:` 分支（`tools/send_message.go:380-428`）：删除。
- `tools/group_state.go` 的 `PeerGroup` 文件读写（`:141-290`）：删除。
- 内存 `g<N>` 计数器（`tools/create_chat.go:13-14,170`）：删除。

---

## 13. 迁移策略

### 13.1 阶段

| 阶段 | 改动 | 兼容性 | 回滚 |
|---|---|---|---|
| M1（本设计） | 文档 only | 无 | — |
| M2 | 会话库 v4：12 列 + 索引；`appendMessageWith` 写新列；`SendMessage` 双写（旧前缀字符串 + 新信封） | 旧客户端无感 | 列保留，旧代码忽略 |
| M3 | `SendMessage` 默认结构化；`group:` 走群发新路径；新问题卡片上线 | 旧 agent 仍可发字符串 | feature flag 切回 |
| M4 | 删除 AskUser / 旧群组（§12） | 无兼容（按决策 C/D） | 回滚到上一版本二进制（旧数据已删，接受） |

### 13.2 Feature flag

```json
{
  "features": {
    "structured_messages": "off|shadow|on",
    "group_broadcast_v2": "off|on"
  }
}
```

- `shadow`：新旧都写，读旧；用于生产验证。
- `on`：新路径权威。
- 默认 `off`，灰度后开。

### 13.3 数据回填

- 新列：旧行默认 `''`；不需要回填消息内容。
- `sender_type` 按 `role` 回填（见 ③ §4）。
- **主库遗留 `session_messages`**：惰性迁移仍是显式列清单（`storage/sqlite/sessiondb.go:296-320`），新列自动取默认值，无需改迁移 SQL。
- 收尾（可选）：当 `SELECT COUNT(*) FROM tenants WHERE migrated=0 AND db_path<>''` = 0 时，v74 迁移可 `DROP TABLE session_messages`（主库）与 `DROP TABLE iteration_history`（主库，若仍存在）。**这是破坏性动作，需用户单独确认**（开放问题 Q1）。

### 13.4 迁移执行注意

- 会话库是惰性迁移：打开时重放 `sessionSchema`（`CREATE ... IF NOT EXISTS`），`ALTER TABLE` 前用 `pragma table_info` 判断列是否存在。
- 迁移期间不重写数据；逐个小文件执行。
- 主库迁移沿用 `migrateV73ToV74` 模式（`storage/sqlite/migrations.go:497-560` 的同构写法）：只建 `groups`/`group_members` 两张小表（④）。

---

## 14. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 异步化改变 agent 行为（原来同步拿回复） | agent 重复提问 | prompt/docs 明确「回答异步到达」；ask_user 有 correlation，重复提问会被 `ux_sm_idem` 挡住 | 关 feature flag |
| 跨会话库聚合查询慢 | 群历史卡顿 | 每库 `ix_sm_group`；fanout ≤128；默认 7 天窗口；超窗口走归档文件 | 降级为「只查发送者副本」 |
| 群发部分失败 | 成员收不到 | 逐个记 receipt；返回 `recipients[]` 明细；重试幂等 | 重发同 `idempotency_key` |
| 用户回复插队导致普通消息饿死 | 普通消息延迟 | 8 条强制让位规则（§9.2） | 调低优先级/关闭插队 |
| 旧客户端收到新 WS 事件 | UI 报错 | 旧事件已删除，前后端同版本发布；未知事件忽略 | 回滚前端 |
| 旧历史含 `ask_question` | 展示异常 | 视为未知控制记录跳过（§12.1） | — |
| 幂等边界只在发送方库 | 跨会话重复发送无法去重 | 文档明确；业务键带 agent 前缀 | — |
| outbound 行被误渲染/误回放 | 上下文重复 | `record_type='outbound'` 在 replay/UI 显式跳过 + 单测守护 | 删除 outbound 行不影响消息真相 |

---

## 15. 验收判据

### 15.1 功能

1. `SendMessage(to="agent:x/y", message="hi")` 在 ≤100ms 返回 `{ok:true, status:"delivered", message_id:"msg_..."}`。
2. `SendMessage(kind="ask_user", ...)` 返回后发送方 run 继续；用户回答后，发送方**下一 iteration 首条**上下文出现 `kind=answer`，`reply_to` = 原问题 `message_id`。
3. 同一 `idempotency_key` 重复发送 10 次：发送方只有 1 条 outbound，收件方只有 1 条 inbound，返回同一 `message_id`。
4. TTL 到期后，会话加载/定时 tick 内产生 `receipt(expired)`，且只产生一次（并发结算幂等）。
5. 取消未回答的 ask_user 后，再次回答返回 `CANCELLED`，不注入 agent。
6. 非法答案（`multi_select=false` 选 2 项）返回 `INVALID_ANSWER`，原问题仍 pending。
7. 跨租户/非成员发送返回 `PERMISSION_DENIED` / `MEMBERSHIP_REQUIRED` / `NOT_FOUND`，不泄露目标存在性。
8. `hop` 达到 `max_hops` 返回 `HOP_LIMIT`，消息不投递。
9. 群发 N 成员：每个成员会话库各 1 条 inbound（`group_id`/`message_id`/`sender_*` 齐全），发送方 1 条 outbound；`recipients[]` 长度 = N。
10. 运行中收到用户回答：当前工具调用完整执行（工具结果落库）→ 回答在下一次 LLM 调用前注入；用一条「工具 sleep 2s + 用户中途回答」的集成测试断言注入顺序。
11. 连续 9 条高优先级消息 + 1 条普通消息：第 9 条处理后普通消息被处理（防饿死）。

### 15.2 存储

12. `sqlite3 xbot.db ".tables"` 不含任何消息表（中心消息表必须不存在）。
13. 任意消息只在 `SessionDBRelPath(channel, chatID)` 指向的库里；`session_messages` 的 `message_id` 全局可定位。
14. `SELECT * FROM session_messages WHERE kind='answer' AND correlation_id=?` 最多 1 行。
15. `record_type='outbound'` 的行不出现在 LLM 回放、不出现在历史渲染。

### 15.3 兼容/删除

16. `GetBuiltinToolNames()` 不含 `AskUser`。
17. 全仓 grep：中心消息表名、`ask_question`、`ask_answer`、`AskUserEvent`、`WaitingUser` 无生产代码引用（测试与迁移注释除外）。
18. 旧 `SendMessage(to, message)` 调用 100% 成功（kind 缺省 = text）。

### 15.4 审计

19. `SELECT ... FROM session_messages WHERE correlation_id=?`（发送方库）能还原完整问答链。
20. 群历史聚合能还原「谁说的 + 发给了谁」（`message_id` 去重 + `recipients` 来自成员表）。

### 15.5 测试清单

- 单元：envelope 校验、幂等、优先级排序、权限矩阵、hop、列迁移。
- 集成：`SendMessage` → 写库 → 注入 → 回答 → 插队注入（CLI/Web/Feishu 三渠道）。
- 回归：`go test ./storage/... ./agent/... ./tools/... ./channel/...`。
- 前端：QuestionCard 单选/多选/自定义/取消/超时；旧组件已删除。

---

## 16. 受影响代码清单（file:line）

### 16.1 新增

| 文件 | 作用 |
|---|---|
| `tools/message_types.go` | 信封/kind/payload 类型与校验 |
| `tools/message_service.go` | Send/Resolve/Expire、群发展开 |
| `tools/message_store.go` | 会话库读写（outbound/inbound/receipt）、幂等 |
| `tools/message_routes.go` | 地址解析、权限、hop/fanout |
| `web/src/components/agent/QuestionCard.tsx` | 新问题卡片 |
| `web/src/hooks/useQuestionCard.ts` | 新卡片数据源 |

### 16.2 修改

| 文件:行 | 改动 |
|---|---|
| `storage/sqlite/sessiondb.go:27,45,51-90` | `sessionSchemaVersion 3→4`，加 12 列 + 7 索引 |
| `storage/sqlite/history.go:16-30` | 新增 `HistoryRecordOutbound` 枚举 |
| `storage/sqlite/history.go:291-337` | `appendMessageWith` INSERT 新列 |
| `storage/sqlite/history.go:857-899` | SELECT/Scan 新列 |
| `storage/sqlite/history.go:100`（`isControlRecordType`） | 包含 `outbound` |
| `llm/types.go:21-64` | `ChatMessage` 增加 sender/message/kind/correlation 字段 |
| `tools/send_message.go:64-121` | 参数扩展、分派到 MessageService |
| `tools/send_message.go:123-190` | 去掉 2s 等待语义 |
| `tools/send_message.go:240-353` | group 改为群发展开 |
| `tools/send_message.go:380-428` | 删除 peer 分支 |
| `tools/send_message.go:431-447` | `parseMentions` 支持 `role`/`role/instance`，歧义报错 |
| `tools/task_manager.go:964-1005` | 增加 MessageID/Envelope/Priority |
| `agent/engine_run.go:2417` | iteration 边界注入（保持调用点，改为优先级 drain） |
| `agent/engine_run.go:2502-2545` | 按 priority 排序注入 |
| `agent/interactive.go:64,85-89,882-885,1907` | `pendingUserMsg` 增加 priority；取队按优先级 |
| `agent/session_queue.go:1-80` | `queuedEntry` 增加 Priority；影子队列排序 |
| `agent/agent.go:4943-4960` | idle 注入携带信封/优先级 |
| `protocol/ws.go` | 删除旧 AskUser 事件；新增 `question_card*` |
| `protocol/events.go:258-330` | `HistoryMessage` 增加 sender/message/kind/correlation |
| `channel/subscription.go:1408-1490` | 转换赋值 |
| `channel/web/web.go`、`web_inbound.go` | 新卡片发布/回答分发 |
| `channel/cli/*`、`channel/feishu/*` | 新卡片渲染/回调 |
| `prompt/channels/*.md` | 改用 `SendMessage(kind=ask_user)` |
| `docs/agent/tools.md:90-115,193-204` | 更新文档 |

### 16.3 删除

| 文件/片段 | 依据 |
|---|---|
| `tools/ask_user.go` | 决策 C |
| `agent/agent.go:535,674,1242-1480` 的 pending 逻辑 | 决策 C |
| `channel/cli/cli_panel_askuser.go` | 决策 C |
| `web/src/components/agent/AskUserPanel.tsx`、`useAskUser.ts` | 决策 C |
| `tools/group_state.go` 的内存/文件存储 | 决策 D |
| `SendMessage` 的 `peer:` 分支 | 决策 D |
| 旧 AskUser 测试 | 决策 C |

---

## 17. 开放问题（需用户拍板，全局共 3 条）

1. **主库遗留 `session_messages`/`iteration_history` 的 DROP 时机**：是否在 v74 收尾迁移中直接 DROP（守卫：无 `migrated=0` 的会话）？还是永久保留空表？
2. **群历史的保留窗口**：聚合查询默认限 7 天 / 2000 条；更早的群历史是否需要归档成 `court/meetings/*.md` 后删除原始行？
3. **长期不活跃会话的 ask_user 超时**：只在会话加载时惰性结算（本设计），还是允许一个全局 cron 巡检「未加载会话」并代结算（需要枚举会话库，成本更高）？
