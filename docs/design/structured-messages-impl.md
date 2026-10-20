# 结构化消息 + 卡片交互 —— 实现记录（v1）

> 分支：`feat/court-orchestration`（worktree `/tmp/xbot-court`）
> 对应设计：`docs/design/sendmessage-structured.md`（v2.1）
> 本次目标：后端真实现（落库/投递/回调/插队）+ Web 真渲染 + 删除旧 AskUser。

## 1. 后端

### 1.1 会话库 schema v5

`storage/sqlite/sessiondb.go`

新增列（旧库逐列 `ALTER TABLE` 幂等补齐）：

| 列 | 类型 | 说明 |
|---|---|---|
| `chat_id` | TEXT NOT NULL DEFAULT '' | 会话 ID（幂等复合索引用） |
| `kind` | TEXT NOT NULL DEFAULT '' | text / ask_user / answer / report / decision / command / event |
| `correlation_id` | TEXT NOT NULL DEFAULT '' | 请求—响应关联键 |
| `reply_to` | TEXT NOT NULL DEFAULT '' | 直接回复的行 id（允许悬空） |
| `idempotency_key` | TEXT NOT NULL DEFAULT '' | 发送者作用域幂等键 |
| `structured_json` | TEXT NOT NULL DEFAULT '' | 完整信封 JSON |
| `direction` | TEXT NOT NULL DEFAULT '' | in / out |
| `answered` | INTEGER NOT NULL DEFAULT 0 | ask_user 终态 |
| `cancelled` | INTEGER NOT NULL DEFAULT 0 | ask_user 终态 |

索引：

- `ux_sm_chat_idem UNIQUE(chat_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`
- `ix_sm_kind_answered (kind, answered)`
- `ix_sm_correlation (correlation_id)`

**主库 schema 同步到 75**：v72 之后主库通常没有 `session_messages`，但迁移期
（`tenants.migrated=0`）仍会读主库旧表。`migrateV74ToV75` 对旧表逐列幂等补列；
表不存在时静默跳过。`schema.go` 的 CREATE TABLE 同步加列，保证新库列清单一致。

### 1.2 存储层

`storage/sqlite/structured_messages.go`

- `AppendStructuredMessage`：`(chat_id, idempotency_key)` 命中已有行 → 返回原 id，
  `created=false`；并发下 UNIQUE 冲突也会回读，保持幂等。
- `AnswerAskUser`：单事务内
  1. 按 `correlation_id` 定位最新 ask_user；
  2. 已有 answer 行：同 key → 幂等返回；不同 key → `ErrAskUserAnswered`；已取消 →
     `ErrAskUserCancelled`；
  3. `UPDATE ... WHERE answered=0 AND cancelled=0` CAS（并发双击只有一次成功）；
  4. 写 `kind=answer` 行（`reply_to` = 问题行 id）。
- `ListInbox`：`kind='ask_user' AND answered=0 AND cancelled=0`，id DESC keyset。
- `ListStructuredMessages`：按 kind 过滤 + keyset。
- `CountInbox` / `MarkStructuredAnswered`（批红后持久化报告态）。
- `row_to_llm` 路径：`getHistoryFromWith` 同时读取新列，`llm.ChatMessage` /
  `protocol.HistoryMessage` 携带 `kind/correlation_id/reply_to/structured_json/
  direction/answered/cancelled`，历史/回放零额外查询。

### 1.3 API（`channel/web/web_structured.go`）

| 端点 | 行为 |
|---|---|
| `POST /api/message` | 兼容旧 `{content}` 文本；出现 `kind`/`payload` 时走结构化分支。kind 白名单校验、payload ≤256KB、幂等写入、成功后向该会话广播 `structured_message` |
| `POST /api/answer` | `{correlation_id, answers[], cancelled?, idempotency_key?}`；服务端按 ask_user 信封校验单选/多选/自定义；幂等；409 映射 |
| `GET /api/inbox?cursor=&limit=` | 未回答 ask_user + `unanswered` 计数 |
| `GET /api/messages?kind=&cursor=&limit=` | 按 kind 过滤 |

权限全部复用 `resolveAPISession`：未知会话 404、命令行/WS 同一套 canonical chat id。

### 1.4 插队优先队列（真接线）

`agent/structured_queue.go` + `agent/structured_queue_test.go`

- 两个 heap lane：high（answer/decision=100）与 low（text/command=10、event/后台=0），
  入队/出队 O(log n)（benchmark：Push ≈183ns，Pop ≈286ns）。
- 同级 FIFO（seq）；连续 8 条 high 后从 low 让位一条（防饿死，仍 O(log n)）。
- **消费点**：`Agent.wireBgNotificationDrain` 在 `runState.drainAndInjectBgNotifications`
  （engine iteration 边界）先 drain 结构化队列，转成 `tools.StructuredNotice`；
  `agent/engine_run.go` 的新 case 以 synthetic tool pair 注入，不打断当前工具调用。
- **空闲投递**：`DeliverStructuredMessage` 检测会话 busy；空闲时用
  `structured_delivery=true` + 空 content 注入 inbound 开启新 turn，`processMessage`
  跳过重复 user 行（结构化行已落库）。
- 投递失败不静默丢：入队失败返回 false；inject 失败按现有 `persistenceErr` 记录。

### 1.5 删除旧 AskUser（不兼容）

- 删除 `tools/ask_user.go` + 测试，移除 `RegisterCore(&AskUserTool{})`。
- 删除旧 Web 问题弹层：`AskUserPanel.tsx`、`useAskUser.ts` 及其测试；
  `AgentPanel` 不再渲染/水合旧 prompt。
- 删除 `POST /api/ask_user/respond` 路由与 handler；旧交互入口不再存在。
- 旧 `ask_question`/`ask_answer` 控制记录**不再产生新写入**（唯一写入方是已删除的
  工具路径）；旧行仍在库中，按普通历史行只读展示。
- 依赖旧工具语义的测试（agent / cli / feishu / web askuser 专项）同步删除。

> 残留说明：CLI/Feishu 的旧面板代码与 agent 的 WaitingUser 暂停分支仍保留（不在
> 本次 Web 范围；工具已不可调用，因此不可达）。后续清理单独提交。

## 2. 前端

- `src/structured/StructuredCards.tsx`：ask_user 琥珀卡 / report 紫色奏折卡 /
  decision 绿色结果卡 / answer 绿色记录 / text 文本；等待脉冲、已回答禁用、
  取消；长文本 >2000 字折叠；全部 React 文本节点（无 dangerouslySetInnerHTML）。
- `src/structured/StructuredMessageStream.tsx`：>500 条启用
  `@tanstack/react-virtual`；卡片用 IntersectionObserver 懒挂载；
  按 `created_at` + `message_id` 稳定排序。
- `src/structured/Inbox.tsx`：未读徽标、谁问的/问题/时间、点击展开直接回答、
  空/加载/错误态。
- `src/structured/useStructuredMessages.ts`：分页首屏（最多 1000 条）、SSE
  `structured_message` 按 message_id 去重、重连后重新拉页补齐、answer/decision
  防抖 + 乐观更新 + 失败回滚。
- `src/pages/StructuredPage.tsx` + `/structured` 路由（AuthGuard + WSProvider +
  SessionStoreProvider）；`core.structured` 面板注册（侧边栏入口）。
- 决策卡：准/驳/改（改带输入框）→ `POST /api/message (kind=decision)`；成功后
  服务端把被批报告标记 `answered=1`（刷新后仍显示已批红）。

## 3. 测试与性能

| 类别 | 位置 | 结果 |
|---|---|---|
| 幂等（同 key 两写） | `storage/sqlite/structured_messages_test.go` | PASS |
| 双击回答 / 重复回答拒绝 | 同上 | PASS |
| 取消后回答拒绝 | 同上 | PASS |
| 收件箱 + 游标分页 | 同上 | PASS |
| reply_to 悬空 | 同上 | PASS |
| 非法 kind / 256KB 超限 | `channel/web/web_structured_test.go` | PASS（400） |
| 未知会话 404 | 同上 | PASS |
| 插队优先级 / 8 条让位 / FIFO | `agent/structured_queue_test.go` | PASS |
| iteration 边界 drain 真接线 | 同上 | PASS |
| benchmark | 同上 | Push 183ns / Pop 286ns |
| `go test ./...` | 全仓 | PASS |
| `npm run build` / `npm run lint` | web | 0 error |
| Playwright（卡片/回答/批红/收件箱/1000 条） | `web/e2e/structured-cards.spec.ts` | 截图见 `web/e2e/artifacts/cards-*.png` |

## 4. 已知限制

1. 结构化消息流是独立的 `/structured` 视图 + `core.structured` 面板，而不是与
   普通聊天气泡逐条交错（历史行已携带 kind 字段，后续可无损接入主消息流）。
2. SSE 增量使用独立的 EventSource（与主聊天流并行），重连补齐为「重新拉最新一页
   + message_id 去重」；服务端不存事件回放缓冲。
3. 主库 schema 75 仅用于迁移期旧表；v72 之后主库无消息表时迁移自动跳过。
4. CLI/Feishu 旧 AskUser 面板与 agent 暂停分支残留（工具已删除，不可达）。
