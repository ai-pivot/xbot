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
- `CountInbox`（未读徽标）。
- `AppendDecision`：批红闭环，与 `AnswerAskUser` **共用同一套 CAS**：
  1. 先查 `(chat_id, idempotency_key)`，命中即返回原 decision 行（`duplicate=true`）；
  2. 目标行（`reply_to` 指向的 message_id）不存在 → `ErrStructuredNotFound`（404）；
     已 `answered` → `ErrAskUserAnswered`（409）；已 `cancelled` → `ErrAskUserCancelled`（409）；
  3. `UPDATE ... SET answered=1 WHERE answered=0 AND cancelled=0` CAS（并发批红只成功一次）；
  4. 写 `kind=decision` 行（`reply_to` = 目标行 id）。
  > 旧实现用无条件 `MarkStructuredAnswered` 且只读 `payload.target_message_id`，
  > 导致重复批红返回 200、目标 `answered` 永远为 0 —— 该函数已删除，杜绝无 CAS 的置位路径。
- `AnswerAskUser` 同时服务 `POST /api/answer` 与 `POST /api/message{kind:"answer"}`：
  后者按 `reply_to` 定位 ask_user、派生 correlation，再走同一事务（见 1.3）。
- `kind=event` / `kind=command` 只做展示与控制，**不进 LLM 回放**（见 1.5）。
- `row_to_llm` 路径：`getHistoryFromWith` 同时读取新列，`llm.ChatMessage` /
  `protocol.HistoryMessage` 携带 `kind/correlation_id/reply_to/structured_json/
  direction/answered/cancelled`，历史/回放零额外查询。

### 1.3 API（`channel/web/web_structured.go`）

| 端点 | 行为 |
|---|---|
| `POST /api/message` | 兼容旧 `{content}` 文本；出现 `kind`/`payload` 时走结构化分支。kind 白名单校验、payload ≤256KB、幂等写入、成功后向该会话广播 `structured_message`。`kind=decision` 走批红 CAS（`reply_to` 必需，目标不存在 404 / 已答已取消 409）；`kind=answer` 走回答 CAS（`reply_to` 必需，等价 `POST /api/answer`）。可选 `created_at`（RFC3339）只用于导入/回填/演示 |
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
  服务端 CAS 把被批报告置 `answered=1`（刷新后仍显示已批红）。

### 2.1 行渲染（视觉规范）

- 每行统一带 `HH:mm:ss`（`toLocaleTimeString('zh-CN', {hour12:false})`）+ kind 徽标
  （ask_user 琥珀 / report 紫 / decision 绿实心 / answer 绿描边 / event 灰 / text 蓝）
  + 发送者 + 单行省略的内容摘要。
- event/text/command 为紧凑行（32–36px，行间 2px）；ask_user/report 卡保留内边距。
- 消息列 `max-w-[880px]` 居中，卡片不再横跨全宽。
- 次要文字（时间戳、证据路径）≥13px 且用显式颜色（`text-slate-500 dark:text-slate-400`），
  不再依赖 `opacity-50` 在深色主题下压暗。
- 收件箱条目显示相对时间（刚刚 / N 分钟前 / N 小时前 / M月D日 HH:mm）。

## 3. 测试与性能

| 类别 | 位置 | 结果 |
|---|---|---|
| 幂等（同 key 两写） | `storage/sqlite/structured_messages_test.go` | PASS |
| 双击回答 / 重复回答拒绝 | 同上 | PASS |
| 取消后回答拒绝 | 同上 | PASS |
| 批红 CAS（同键幂等 / 不同键 409 / 目标 404 / 已取消 409） | 同上（`TestAppendDecision*`） | PASS |
| event/command 不进 LLM 回放、UI 仍可见 | 同上（`TestReplayExcludesEventAndCommand*`） | PASS |
| 收件箱 + 游标分页 | 同上 | PASS |
| reply_to 悬空 | 同上 | PASS |
| 非法 kind / 256KB 超限 | `channel/web/web_structured_test.go` | PASS（400） |
| 批红/回答 409/404/400 映射 | 同上（`TestDecision*` / `TestMessageKindAnswer*`） | PASS |
| 行渲染格式（HH:mm:ss / kind 徽标 / 相对时间） | `web/src/structured/structuredRowFormat.test.ts` | PASS（8 例） |
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
5. `POST /api/message` 的 `created_at` 允许客户端回填（导入/演示）。服务端不做
   单调性校验；调用方要自己保证递增，否则时间线顺序按 `created_at + message_id` 排序。

## 5. 决策记录

### 2026-10-20 · 闭环语义统一到存储列 `answered`（取代「派生存在性」）

**问题**：设计文档曾写「`answered` 由同库 `EXISTS(kind='answer' AND reply_to=msg_X)` 派生」，
实现用的是存储列。两套语义并存导致：
- `POST /api/message{kind:"decision"}` 重复批红返回 200、目标 `answered` 永远 0；
- `POST /api/message{kind:"answer"}` 写行但不闭环，收件箱不收敛。

**决策**：统一到存储列 `answered`，由 `AnswerAskUser` / `AppendDecision` 的 CAS 原子置位。
派生查询无法原子拒绝并发重复提交，无法保证收件箱与卡片刷新后一致。文档（§7.5 / §8.2 /
§12 字段表）同步改写。`kind=answer` 走 `/api/message` 时复用同一闭环（`answerViaMessage`），
不另开第二条写入路径。

### 2026-10-20 · `event` / `command` 不进 LLM 回放

**问题**：`display_only` 写死 0、`structuredRole` 把 event 当 user，`getHistoryFromWith`
全表读，导致批量事件（e2e 1000 条）作为 user 消息进入下次 prompt，直接爆 context。

**决策**：不改 `display_only`（主聊天视图 `useChatMessages.ts` 会丢弃这些行，改了事件就消失），
而是在 `history.go replayWith()` 的回放点按 kind 过滤：`structuredKindHiddenFromLLM()`
跳过 `event` + `command`。UI 展示路径（`replayForDisplayRecords` / `/api/messages` /
主聊天视图）完全不过滤 —— 「可见但不进上下文」是单一明确语义。

**测试**：`TestReplayExcludesEventAndCommandButKeepsDialogue`：写 N 条 event + 1 条 command
+ ask_user/report/answer/text，`Replay()` 只含后四类；`ReplayForDisplay()` 仍含 3 条 event。
