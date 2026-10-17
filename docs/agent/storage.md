# Storage Backends (SQLite + PostgreSQL)

Backend-agnostic storage layer. One set of interfaces (`storage/interfaces.go`), two
implementations (SQLite adapter + native PG), one contract test suite that both must pass.

## Why

`agent/`, `serverapp/`, `channel/web/` etc. must never touch `*sql.DB` or any
backend-specific concept. They depend only on the `storage.*` interfaces, so the same
call sites serve SQLite (default) and PostgreSQL (`storage.type = "pg"`).

## The five interfaces

`storage.Storage` is the single top-level entry point (one instance per process):

```go
type Storage interface {
    Session(tenantID int64) (SessionStore, error) // per-session data (messages + iterations)
    Tenant() TenantStore                          // tenant registry + per-session DB metadata + model binding
    Memory() MemoryStore                          // core memory + long-term + events
    Config() ConfigStore                          // user profile/settings, LLM subs, usage, cron, triggers, share, web auth
    Close() error
}
```

Sub-interfaces decouple the SQLite-specific concrete types that used to leak upward:

| Interface | Was | Purpose |
|---|---|---|
| `SubscriptionBackend` | `*sqlite.LLMSubscriptionService` | LLMFactory model resolution — **must carry the subscription id** |
| `TenantBackend` | `*sqlite.TenantService` | per-session model restoration (`Get/SetTenantSubscription`) |
| `WebAuthStore` | `*sqlite.DB` | web login (embedded in `ConfigStore`) |

`ConfigStore` embeds `SubscriptionBackend` + `WebAuthStore`, so `store.Config()` is a
drop-in for the old `LLMSubscriptionService` + `UserSettingsService` + web-auth paths.

## Files

- `storage/interfaces.go` — the interfaces (single source of truth for the method set)
- `storage/sqlite_adapter.go` — thin wrappers over the existing `storage/sqlite` services
  (`sqliteSessionStore` / `sqliteTenantStore` / `sqliteMemoryStore` / `sqliteConfigStore`);
  `NewSQLiteStorage`, `NewSQLiteConfigStore`, `NewSQLiteTenantStore`, `NewSQLiteMemoryStore`
- `storage/pg_storage.go` — `NewPGStorage(ctx, dsn)` / `NewPGStorageWithPool(pool)`
- `storage/pg_schema.go` — `initPGSchema(ctx, pool)` — idempotent DDL for all 25 tables
- `storage/pg_{session,tenant,memory,config}.go` — the PG implementations

## Wiring

- Config: `config.StorageConfig{Type, DSN}` (`config.json` → `"storage": {"type":"pg","dsn":...}`).
  `Type` empty/`"sqlite"` = default backend.
- `serverapp/server_core.go` picks the backend from `cfg.Storage.Type` and constructs
  `MultiTenantSession` via `NewMultiTenant` (SQLite) or `NewMultiTenantPG` (PG).
- `MultiTenantSession.store storage.Storage` is the handle; `session/*.go` reads/writes
  go through `store.Session(tenantID)` / `store.Tenant()` / `store.Memory()`.
- **SQLite-only escape hatch**: some browse/detail callbacks run SQLite-specific SQL
  (legacy synthetic-tool-pair repair, regions/detail endpoints). They go through
  `MultiTenantSession.SessionServiceForSQLite(tenantID)` which returns an explicit error
  on PG (**never a silent degradation**).
- `agent.MultiSession().DB()` is the raw SQLite handle (nil on PG) — used only for the
  WAL checkpoint on shutdown and legacy browse SQL; nothing new should depend on it.

## Gotchas (PG)

- **`sql.ErrNoRows` ≠ `pgx.ErrNoRows`.** All PG code must check `pgx.ErrNoRows`; a
  `sql.ErrNoRows` check never fires → the "not found" branch is dead (silent wrong result).
- **Native `BOOLEAN`, not `INTEGER 0/1`.** `pg_schema.go` declares real `BOOLEAN`; every
  predicate must be `= true` / `= false` (or `COALESCE(col, false)`), and scans must target
  `*bool`. `boolean = integer` raises SQLSTATE 42883.
- **One statement per prepared Exec.** pgx rejects multi-statement strings
  ("cannot insert multiple commands into a prepared statement"). Split them.
- **`search_path` does not propagate across `pgxpool` connections.** Per-test isolation
  must use a *dedicated database* per test (see `pg_contract_test.go`), not `SET search_path`.
- `json.Marshal(nil)` yields `"null"`; the message-tool JSON column must be `[]` so the
  `HasAssistantReplyAfterLastUser` / tool predicates match.

## Contract test suite

One suite, both backends, registered via `StorageFactory`:

- `storage/contract_test.go` — `RunStorageContractTests(t, factory)` + shared helpers
  (`getSessionStore`, `createTestTenant`, `uniqueChatID`, `dummyMsg`). Domains:
  SessionStore / TenantStore / MemoryStore / ConfigStore / CrossStore / EdgeCases.
- Per-domain files (do not merge them): `contract_{session,tenant,memory,config,cross,edge}_test.go`.
- `sqlite_contract_test.go` — `TestSQLiteStorageContract` (temp file).
- `pg_contract_test.go` — `TestPGStorageContract` (per-test database `xbot_test_<nanos>`);
  needs `XBOT_PG_TEST_DSN` (skips when unset).

```bash
go test ./storage/ -run TestSQLiteStorageContract -timeout 180s
XBOT_PG_TEST_DSN="postgres://postgres:***@host:5432/xbot?sslmode=disable" \
  go test ./storage/ -run TestPGStorageContract -timeout 300s
```

The PG backend is pure Go (`github.com/jackc/pgx/v5` + `pgxpool`) — no cgo/libpq.

## Known scope limits

- Per-session DB (v71 one-session-one-DB) is a SQLite mechanism. Under PG the same
  `tenantID` partitions rows in a shared schema; `session_messages` / `iteration_history`
  do not become separate databases.
- `docs/plans/storage-interfaces.md` holds the original design notes.
