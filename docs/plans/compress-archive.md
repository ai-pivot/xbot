# Plan: Compression Archive — Soft-Delete Original Messages

## Summary

将压缩时的 `session_messages` 硬删除改为软删除归档。压缩前将当前消息标记为 `is_archived=1` 并记录 `compact_generation`，然后插入压缩后的 SessionView。运行时查询只读未归档消息。新增 `compact_retention` 配置参数控制保留代数，超过的旧归档自动清理。

## Changes

### `storage/sqlite/schema.go`
- 新增 `session_messages` 的两个字段：`is_archived INTEGER DEFAULT 0`, `compact_generation INTEGER DEFAULT 0`
- 新增索引 `idx_session_messages_tenant_archived` on `(tenant_id, is_archived, id)`

### `storage/sqlite/db.go`
- `schemaVersion` 33 → 34

### `storage/sqlite/migrations.go`
- 新增 `migrateV33ToV34`：ALTER TABLE session_messages ADD COLUMN is_archived/compact_generation + CREATE INDEX

### `storage/sqlite/session.go`
- `GetAllMessages` / `GetHistory` / `GetMessagesCount` / `GetUserMessageCount`：查询条件加 `AND COALESCE(is_archived, 0) = 0`
- `Clear`：保持不变（硬删，用于 /reset、session 初始化）
- 新增 `ArchiveForCompress(tenantID int64) (generation int, error)`：将当前未归档消息标记为 archived + 递增 generation
- 新增 `PurgeArchivedGenerations(tenantID int64, keepGenerations int) (int64, error)`：删除超出保留代数的归档消息
- `AddMessage`：写入时 `compact_generation` 默认 0（未归档）

### `session/tenant.go`
- 新增 `ArchiveForCompress() (int, error)` 代理到 SessionService
- 新增 `PurgeArchivedGenerations(keepGenerations int) (int64, error)` 代理到 SessionService

### `agent/persist_bridge.go`
- `RewriteAfterCompress`：不再调用 `session.Clear()`，改为：
  1. `session.ArchiveForCompress()` — 软删除当前消息
  2. 循环 `session.AddMessage(sessionView)` — 插入压缩后消息
  3. `session.PurgeArchivedGenerations(retention)` — 清理超代归档

### `config/config.go`
- `AgentConfig` 新增 `CompactRetention int json:"compact_retention"` — 保留代数，默认 3（0=无限保留，-1=禁用归档即原始行为）

### `agent/compress_pipeline.go`
- `CompressPipelineParams` 新增 `CompactRetention int`
- `ApplyCompress` 传 retention 给 PersistenceBridge

## Risks
- **查询性能**：`is_archived` 过滤在所有查询路径上，需索引覆盖。已有 `idx_session_messages_tenant_created`，新增条件过滤走新索引。
- **存储增长**：归档消息累积。默认保留 3 代，预计增长 ~1.7x，完全可接受。
- **迁移安全**：ALTER TABLE ADD COLUMN 是 SQLite 安全操作，不影响现有数据。

## Definition of Done
- [ ] DB migration v33→v34 成功执行
- [ ] 压缩后 session_messages 中原始消息 is_archived=1 保留
- [ ] 压缩后 SessionView 正确插入（is_archived=0）
- [ ] GetAllMessages/GetHistory 只返回未归档消息
- [ ] 超代归档被自动清理
- [ ] /reset 和 session 初始化仍为硬删
- [ ] `go build ./...` 和 `go test ./...` 通过
- [ ] `compact_retention` 配置项生效

## Open Questions
- `compact_retention = 0` 表示无限保留 vs `compact_retention = -1` 禁用归档？我倾向 0=无限保留，负值=禁用（原始行为），这样配置更直观。
