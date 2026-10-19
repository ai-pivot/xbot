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
- **Native `BOOLEAN`, not `INTEGER 0/1` — in BOTH directions.** `pg_schema.go`
  declares real `BOOLEAN`, so: scans must target `*bool` (scanning into `*int` fails
  with `cannot scan bool (OID 16) into *int`), predicates must be `= true` / `= false`
  (or `COALESCE(col, false)`), and **writes must use `true`/`false` literals**
  (`SET migrated = 1` fails with `column "migrated" is of type boolean but expression
  is of type integer`, SQLSTATE 42804). `COALESCE` on a boolean column is still
  boolean — it does not make it int-scanable.
- **One statement per prepared Exec.** pgx rejects multi-statement strings
  ("cannot insert multiple commands into a prepared statement"). Split them.
- **`search_path` does not propagate across `pgxpool` connections.** Per-test isolation
  must use a *dedicated database* per test (see `pg_contract_test.go`), not `SET search_path`.
- `json.Marshal(nil)` yields `"null"`; the message-tool JSON column must be `[]` so the
  `HasAssistantReplyAfterLastUser` / tool predicates match.
- **PG `Clear` drops `session_messages` only**, SQLite's also drops `iteration_history`.
  Tests that assert usage aggregates must therefore use baseline deltas or membership
  checks rather than absolute counts.

## One replay implementation, both backends

The append-only history fold (compression snapshots → masks → context edits → AskUser,
keyed off each record's history id) lives in **`sqlite.ApplyHistoryRecords`** and is
exported precisely so the PG backend reuses it (`pgSessionStore.Replay` reads its rows
into `[]sqlite.HistoryRecord` and calls the fold). Checkpoint selection likewise shares
`sqlite.ValidCheckpointSnapshot`. Do **not** re-implement replay semantics in a backend:
a second copy is how masks/context edits silently diverge (that is exactly what the
contract suite caught). `pg_session.go` adds only the record reader (`pgHistoryRecords`),
the checkpoint lookup (`pgLatestCheckpoint`) and a trivial occurrence lookup.

## Memory providers are backend-agnostic (DI invariant)

> A memory provider depends **only** on `storage.*` interfaces plus pure logic. Adding or
> switching a storage backend requires **zero** changes under `memory/`.

Enforced by `memory/provider_di_test.go`: providers may not import `database/sql`,
`xbot/storage/sqlite` or pgx, and `ProviderDeps` may not declare a driver/DSN field.

Why it matters: a provider holding a driver handle eventually grows dialect-specific SQL
(placeholders, FTS syntax, boolean literals) and then cannot run on some backend. DB-backed
memory is therefore implemented in the storage layer and reaches providers only as a
*semantic* interface.

- `storage.AgentMemoryStore` (embedded in `MemoryStore`) is the xbot provider's seam:
  search takes **pre-tokenized terms** and returns **already-ordered** results, so neither
  the tokenizer (provider-owned `cjkSpaceRuns`) nor the ranking direction (`bm25` is
  negative/lower-is-better; `ts_rank` is positive/higher-is-better) leaks across.
- `memory/letta` takes `storage.MemoryStore` (not `*sqlite.CoreMemoryService`).
- The chromem archival/tool-index stores are created in **one shared path**
  (`MultiTenantSession.initMemoryServices`) used by both constructors, with directories
  from an explicit `WithDataDir` (default `config.XbotHome()`) — never derived from
  `dbPath`, which on PG holds a DSN. The files stay local on purpose: bodies live in the
  database, vectors in files, keyed by `tenantID`, so preserving `tenants.id` keeps them valid.

## Cross-backend FTS parity (measured, not assumed)

The provider's `cjkSpaceRuns` splits CJK into single-character tokens for the **index and
the query**, which is what makes PG's `to_tsvector('simple', ...)` usable with **no
extension**: PG's `simple` config also treats a CJK run as one token, so both backends see
the same token stream. `zhparser`/`pg_jieba` are not needed (and were unavailable on the
test PG); `pg_trgm` is unnecessary.

Two consequences worth remembering:

- **Index the same field set on both backends.** SQLite's FTS5 table indexes
  `content, keywords, tags, search_text`; the PG generated `search_tsv` must concatenate
  the same four, otherwise a keyword-only query hits on SQLite and misses on PG.
- **`bm25` is corpus-scaled; `ts_rank` is not.** Measured with a perfect match: bm25 ≈ 0
  with 2 rows, −5.99 with 20, −7.53 with 40 — so the provider's dedup threshold (−6.0)
  only becomes reachable on a realistically sized corpus. Contract tests for dedup must
  seed a corpus; asserting on a 2-row fixture tests corpus size, not the contract.


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

## Migration: SQLite → PostgreSQL

`cmd/xbot-migrate` copies an existing SQLite installation into PG. Use it with the
server **stopped** (or against a `VACUUM INTO` snapshot):

```bash
xbot-migrate -sqlite ~/.xbot/xbot.db -pg "postgres://…/target?sslmode=disable" -dry-run
xbot-migrate ... -wipe        # full re-run: TRUNCATE the target first
xbot-migrate ... -verify-only # compare only, no writes
```

### Why it is not a storage-interface copy

The layouts differ structurally: SQLite is a main DB plus **one DB file per session**
(v71), PG is one schema. Two consequences force an explicit per-column copy:

1. **Primary keys must be preserved.** `session_messages.id` is referenced by
   `iteration_history.message_id`, by control records (`target_history_id`), by
   compression snapshots (`HistoryIDs`) and by mask/context-edit targets. Renumbering
   silently corrupts replay. The `storage.*` interfaces do not expose PK-preserving
   inserts, so the tool issues `INSERT … OVERRIDING SYSTEM VALUE` and re-aligns every
   IDENTITY sequence with `setval(pg_get_serial_sequence(...), MAX(id))` — otherwise
   the first runtime insert collides.
2. **Each session DB owns an independent id sequence starting at 1.** Merging would
   collide, so every DB is assigned a disjoint **id window** and each DB's ids *and*
   every internal reference (including the ids embedded in control-record JSON) are
   shifted by the same offset. Referential integrity is exact; only absolute numbers
   change.

### Conversion rules

- **Follow the PG column TYPE, never the column name.** The same name has different
  types across tables: `user_settings.updated_at` is an epoch INTEGER while other
  tables store a timestamp string; `oauth_tokens.expires_at` is an INTEGER in SQLite
  and TIMESTAMPTZ in PG.
- IDENTITY tables are discovered from PG metadata at run time; a hard-coded list rots.
- Timestamps arrive in several shapes, including Go's `time.Time.String()`
  (`… +0000 UTC m=+267.07`) — the monotonic suffix and the zone name must be stripped.
- **Do not add `ORDER BY` when scanning a source table.** Ordering that matters is
  *between* tables (`tenants` first, for the FKs), not within one; and on a DB with a
  damaged index page `ORDER BY id` either fails with `database disk image is
  malformed` or returns the same row twice, while the plain table scan reads cleanly.

### Data conditions the tool reports rather than hides

Real installations contain states SQLite tolerates and PG rejects. Each is handled
explicitly and reported, never silently dropped:

| Condition | Handling |
|---|---|
| Child rows whose tenant was deleted (SQLite has no FK) | skipped + counted |
| Duplicate rows from a corrupt source btree | deduped on the real (composite) PK + counted |
| Invalid UTF-8 (mid-character truncation) | damaged bytes → U+FFFD, rest preserved |
| A session DB referenced by `tenants.db_path` but missing on disk | warned, its rows absent |

### Not migrated

- `oauth_tokens` — its SQLite shape (INTEGER `expires_at` + `scopes`/`raw`) is
  incompatible with the PG schema, and the PG backend keeps OAuth in a local SQLite
  file.
- FTS shadow tables (`event_history_fts*`) — derived indexes, rebuilt not copied.
- chromem vector stores (`~/.xbot/archival`, `~/.xbot/tool_index`) — file-based and
  keyed by `tenantID`, so preserving `tenants.id` keeps them valid in place.

### Verification

Row counts (session tables summed across shards), `MAX(id)` for main-DB identity
tables, and orphaned `target_history_id` references; any mismatch fails the run.
Session `MAX(id)` is deliberately *not* compared across source and target — windows
make it incomparable; row counts plus reference integrity are the real proof.

## Known scope limits

- Per-session DB (v71 one-session-one-DB) is a SQLite mechanism. Under PG the same
  `tenantID` partitions rows in a shared schema; `session_messages` / `iteration_history`
  do not become separate databases.
- `docs/plans/storage-interfaces.md` holds the original design notes.
