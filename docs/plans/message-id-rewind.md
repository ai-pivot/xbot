# Plan: 统一消息 ID 定位（移除时间戳依赖）

## Summary

当前 xbot 的消息定位（rewind/trim/dedup）依赖时间戳匹配，但前端 optimistic 时间戳（客户端、毫秒精度）与 DB 时间戳（服务器、秒精度）不一致，导致 Web rewind "target not found" bug。DB 表 `session_messages` 已有 `id INTEGER PRIMARY KEY AUTOINCREMENT`，但从未传递到上层。本次重构将 DB id 贯穿全链路，所有消息定位操作改用 id，彻底消除时间戳精度问题。

## 问题链路

```
DB session_messages (id ✓, ORDER BY id ASC ✓)
  ↓ SELECT — 查询列不含 id ✗              ← id 在这里丢失
  ↓ scanMessages → llm.ChatMessage (无 ID 字段 ✗)
  ↓ ConvertMessagesToHistory → HistoryMessage (无 ID 字段 ✗)
  ↓ JSON → 前端 ChatMessage.id = `hist-${数组索引}` ✗
  ↓ rewind → Date.parse(timestamp) → cutoff_ms ✗
  ↓ trim_history RPC → cutoff int64 (Unix 时间戳) ✗
  ↓ PurgeNewerThanOrEqual → DELETE ... WHERE created_at >= ? ✗
```

## Changes

### Phase 1: DB + Go 类型层（让 id 流通）

#### `storage/sqlite/columns.go`
- **What**: `sessionMessageSelectCols` 开头加 `id`
- **Why**: 所有消息查询都通过这个常量，加 id 后所有查询自动返回 id

#### `storage/sqlite/session.go` — `scanMessages()`
- **What**: Scan 增加 `&msg.ID`（int64），放在第一个位置
- **Why**: 对应 SELECT 列顺序变化，解析 DB id 到 ChatMessage.ID

#### `llm/types.go` — `ChatMessage` 结构体
- **What**: 增加 `ID int64` 字段，`json:"-"`（不参与 LLM 上下文）
- **Why**: Go 侧消息类型的 id 载体，贯穿 agent loop → RPC → 回调

#### `protocol/events.go` — `HistoryMessage` 结构体
- **What**: 增加 `ID int64` 字段，`json:"id"`
- **Why**: JSON 序列化后前端能拿到 DB id

#### `channel/subscription.go` — `ConvertMessagesToHistory()`
- **What**: 每个 `HistoryMessage` 构造点增加 `ID: m.ID`
- **Why**: 把 `llm.ChatMessage.ID` 透传到 `HistoryMessage`

### Phase 2: 存储层截断改用 id

#### `storage/sqlite/session.go` — `PurgeNewerThanOrEqual()`
- **What**: 新增 `PurgeFromMessageID(tenantID, messageID int64)` 方法，用 `DELETE ... WHERE id >= ?`。原方法保留但标记 deprecated（压缩路径暂不改）
- **Why**: rewind 截断用 id 精确删除，消除时间戳比较的精度问题

#### `session/multitenant.go` — `TrimHistory()`
- **What**: 新增 `TrimHistoryFromMessageID(channel, chatID string, messageID int64) error`
- **Why**: 对接 `PurgeFromMessageID`，保留原 `TrimHistory` 给压缩路径用

### Phase 3: Rewind 路径改用 id

#### `agent/agent.go` — `RewindCheckpoint()`
- **What**: 无需改动（用 ordinal，不依赖时间戳）
- **Why**: 确认 checkpoint 回滚走的是 turn ordinal，与消息 id 正交

#### `serverapp/callbacks.go` — `rewindWebHistory()`
- **What**: 签名从 `cutoff time.Time` 改为 `messageID int64`；匹配逻辑从 `msg.Timestamp.Equal(cutoff)` 改为 `msg.ID == messageID`；截断调用 `TrimHistoryFromMessageID`
- **Why**: 消除 "target not found" 根因——id 精确匹配，无精度问题

#### `serverapp/callbacks.go` — `RewindHistory` callback 注册
- **What**: 签名从 `cutoff time.Time` 改为 `messageID int64`
- **Why**: 透传 id 到 `rewindWebHistory`

#### `channel/web/web.go` — `WebCallbacks.RewindHistory` 字段
- **What**: 类型从 `func(..., cutoff time.Time) (...)` 改为 `func(..., messageID int64) (...)`
- **Why**: callback 接口变更

#### `channel/web/web_api.go` — `handleHistoryRewind()`
- **What**: 请求体从 `CutoffMS int64` 改为 `MessageID int64`；调用改为传 `messageID`
- **Why**: API 接口变更

### Phase 4: RPC 层

#### `agent/req_types.go` — `trimHistoryReq`
- **What**: `Cutoff int64` 改为 `MessageID int64`；字段名 `json:"message_id"`
- **Why**: RPC 参数从时间戳改为消息 id

#### `agent/client.go` — `TrimHistory()`
- **What**: 签名从 `cutoff time.Time` 改为 `messageID int64`；传 `messageID` 而非 `cutoff.Unix()`
- **Why**: CLI 远程模式通过 RPC 调用 trim，需传 id

#### `serverapp/rpc_table.go` — `trim_history` handler
- **What**: 从 `p.Cutoff` → `time.Unix()` 改为直接传 `p.MessageID` 给 `TrimHistoryFromMessageID`
- **Why**: RPC handler 适配新参数

### Phase 5: CLI TUI 适配

#### `channel/cli/cli_panel_rewind.go` — `rewindItem` 结构体 + `applyRewind()`
- **What**: `rewindItem` 增加 `DBID int64` 字段；`openRewindPanel()` 填充 `DBID`；`applyRewind()` 和 `executeRewind()` 调用 `TrimHistoryFn` 时传 `DBID` 而非 `Time`
- **Why**: TUI rewind 改用 DB id 截断

#### `channel/cli/cli_types.go` — `TrimHistoryFn` 类型
- **What**: 类型从 `func(channelName, chatID string, cutoff time.Time) error` 改为 `func(channelName, chatID string, messageID int64) error`
- **Why**: callback 接口变更

#### `channel/cli/cli.go` — `SetTrimHistoryFn` + `trimHistoryFn` 字段
- **What**: 类型同步改为 `func(int64) error`（本地模式快捷回调）
- **Why**: 与 `TrimHistoryFn` 保持一致

#### `cmd/xbot-cli/main.go` — `TrimHistoryFn` 装配
- **What**: callback 实现改为调用 `client.TrimHistory(channel, chatID, messageID)`
- **Why**: 本地 CLI 模式通过 RPC client 调用后端

### Phase 6: 前端适配

#### `web/src/components/agent/api.ts` — `HistMsg` 接口
- **What**: 增加 `id?: number` 字段
- **Why**: 接收后端返回的 DB id

#### `web/src/components/agent/api.ts` — `rewindHistory()`
- **What**: 参数从 `cutoffMS: number` 改为 `messageID: number`；请求体从 `cutoff_ms` 改为 `message_id`
- **Why**: API 接口变更

#### `web/src/hooks/useChatMessages.ts` — `parseHistoryMessages()`
- **What**: `id` 从 `hist-${i}` / `seq-${seq}` 改为 `db-${m.id}`（当 id 存在时）
- **Why**: 前端消息 id 用 DB id，rewind 时能取到

#### `web/src/hooks/useChatMessages.ts` — send 回执
- **What**: `.then()` 回调中用服务器返回的 `id` 更新 optimistic 消息的 `id`（当前同步 timestamp 的死代码可一并删除）
- **Why**: 发送后前端消息 id 从临时 id 切换为 DB id

#### `web/src/workspace/panels/AgentPanel.tsx` — `rewindTo()`
- **What**: 从 `Date.parse(originalMessage.timestamp)` 改为 `originalMessage.id`（解析出 DB id）
- **Why**: rewind 传 DB id 而非时间戳

### Phase 7: CLI 去重适配

#### `channel/cli/cli_update_session.go` — `msgIdentity`
- **What**: 从 `{role, timestamp}` 改为 `{role, dbID}`（dbID 即 `HistoryMessage.ID`）；`toCLIMessage` 传递 ID；`dedupAppend` 用新 key
- **Why**: 用 DB id 去重，消除合成时间戳冲突导致的误删

## Risks

- **向后兼容**: 旧 DB 数据的 id 列已存在（AUTOINCREMENT），不需要迁移。所有旧消息都有 id。
- **压缩路径**: `PurgeOldMessages`（压缩清理）和 `TrimHistory`（压缩后 trim）仍用时间戳。本次不改压缩路径——它用 `id` 排序 + LIMIT offset 找边界（正确），截断用时间戳风险低（压缩是低频操作）。保留原 `TrimHistory` 方法不删。
- **CLI `trimHistoryFn` 签名变更**: 是 breaking change，但 CLI 和 server 在同一仓库，同步修改无兼容问题。
- **前端 `HistMsg.id` 可选**: 旧服务器不返回 id 时前端 fallback 到 `hist-${i}`，保证灰度安全。
- **`RewindCheckpoint`**: 用 turn ordinal 不受影响，无需改动。

## Definition of Done

- [ ] DB 层: `sessionMessageSelectCols` 含 id，`scanMessages` 解析 id，`ChatMessage.ID` / `HistoryMessage.ID` 传递到位
- [ ] 存储层: `PurgeFromMessageID` + `TrimHistoryFromMessageID` 用 `DELETE ... WHERE id >= ?`
- [ ] Web rewind: `handleHistoryRewind` 接收 `message_id`，`rewindWebHistory` 用 id 匹配
- [ ] RPC: `trim_history` 传 `message_id`，`client.TrimHistory` 签名变更
- [ ] CLI: `rewindItem` 携带 DBID，`applyRewind` / `executeRewind` 传 id
- [ ] 前端: `HistMsg.id`、`rewindHistory` 传 id、`parseHistoryMessages` 用 `db-${id}`
- [ ] 去重: `msgIdentity` 改用 `{role, dbID}`
- [ ] `go build ./...` 通过
- [ ] `go test ./agent/ ./serverapp/ ./storage/... ./channel/cli/` 通过
- [ ] Docker serve 验证: 发消息 → cancel → rewind 不报 "target not found"
- [ ] Docker serve 验证: 正常 turn → rewind 正常

## Open Questions

- `PurgeNewerThan`（`> ` 变体）当前仅被 `PurgeNewerThanOrEqual` 注释提及，无实际调用者。是否直接删除？→ 计划保留，不改动，避免扩大范围。
