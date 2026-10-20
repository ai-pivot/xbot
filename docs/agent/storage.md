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
- **Round trips, not SQL, dominate on a remote PG.** A single `INSERT` costs one
  network round trip (~50ms on a cross-host link), so any loop that writes "one row
  at a time" turns into minutes: forking a 2235-message session used to spend ~4
  minutes in `AppendMessages` + `AppendIterationHistory` loops. Batch instead —
  `pgx.Batch` inside one transaction collapses N statements into **one** round trip
  while preserving per-statement result order (that is how the returned ids stay
  aligned with the input slice). Bulk paths therefore have batch variants
  (`AppendMessages`, `AppendIterationHistories`); when adding a new "copy/migrate
  many rows" path, ask for the batch API rather than looping. Measured on the
  production server: 3046 rows 255s → 1.3s.
- **PG `Clear` drops `session_messages` only**, SQLite's also drops `iteration_history`.
  Tests that assert usage aggregates must therefore use baseline deltas or membership
  checks rather than absolute counts.
- **PG validates UTF-8; SQLite does not.** SQLite stores TEXT as raw bytes, so an
  invalid byte sequence lives there silently for years; PG rejects the *entire
  statement* with SQLSTATE 22021 (`invalid byte sequence for encoding "UTF8"`) on the
  first write, which surfaces as a lost conversation turn
  (`append session history: persist message batch: … item N`), not as one garbled
  character. The bytes come from byte-slicing CJK text — `args[:80] + "..."` cuts a
  3-byte rune and leaves a 1–2 byte orphan (observed: `0xe6 0x2e 0x2e`). Two rules:
  **never truncate a string that can reach a message or the DB with `s[:n]`** — use
  `internal/textutil` (`TruncateBytes`, `TruncateBytesWithSuffix`, `TruncateRunes`);
  and treat `pgText()` in `storage/pg_sanitize.go` as the write-boundary backstop, so
  an unforeseen producer (third-party API, PDF/OCR extractor) degrades to U+FFFD
  instead of killing the turn. `encoding/json` already repairs invalid bytes, so JSON
  columns are safe by construction.
- **Go slices must never be passed into a TEXT column.** pgx has no encode plan for
  `[]string → text`: a **nil** slice becomes SQL `NULL` (→ `23502` on a NOT NULL
  column) and a non-nil one fails with `unable to encode []string into text format`.
  Serialize explicitly with `encoding/json` first. Pair this with the read side:
  **every read path must decode what it writes.** `pgConfigStore.GetSubscription` used
  to scan `cached_models` into a throwaway `sql.NullString` and drop it, so the struct
  a caller loaded and wrote back carried a nil slice — the two bugs combined to make
  every "edit subscription" request fail on PG while SQLite was fine.
- **Not every column in a row is user-editable.** `cached_models` (API-discovered model
  cache) is written by the discovery path, never by `Update`. SQLite's
  `LLMSubscriptionService.Update` omits it, and the PG store must too — otherwise an
  innocent rename wipes the cache. When the two backends disagree about which columns
  an operation touches, contract tests catch it; "it compiles and SQLite passes" does
  not.

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

### Cutover runbook (a real production switch)

The tool is the mechanical part; the *ordering* around it is what makes a cutover
safe. This is the sequence that worked on a live installation — it is a checklist,
not a script, because every step's preconditions differ per host. **Never hard-code
the DSN**: read it from the environment (or a 0600 file) and confirm which database
it points at *before* the step that truncates.

1. **Quiesce first.** The tool reads SQLite snapshots, and a running instance keeps
   writing, so anything produced between snapshot and cutover is silently lost.
   Stop the service and confirm it is really down.
2. **Snapshot, don't read the live file.** `.backup` (or `VACUUM INTO`) the main DB
   *and* every session DB. A consistent snapshot also keeps the live file untouched,
   which is what makes rollback free.
3. **Check the source for damage before trusting it.** A DB whose `wal_checkpoint`
   failed on shutdown can have real btree page damage, and the failure is silent:
   an index scan returns *fewer rows than the table holds* (observed: 17 tenants via
   index vs 23 via table scan). Symptom to look for is a count that disagrees with
   the sidebar/UI. Only when damaged, rebuild with `.recover` and require
   `PRAGMA integrity_check` to return `ok` before migrating; a healthy DB needs
   neither step.
4. **Repair orphaned parents in the snapshot, not in PG.** A session can be visible
   while its `tenants` row was lost to corruption. Recover the original `tenant_id`
   from the session DB's own `session_messages` and re-insert the `tenants` row
   pointing at that session DB — this also brings back the `event_history` rows
   scoped to that tenant. Doing it in the snapshot keeps the source self-consistent.
5. **Migrate with `-wipe` only when you mean it.** `-wipe` is `TRUNCATE … CASCADE`
   on every target table. It is correct for a first cutover or a full re-run, and
   catastrophic against a target that already has live data. Run `-dry-run` first.
6. **Rewrite `config.json` as the service's own user.** Add
   `"storage": {"type": "pg", "dsn": …}` and back the file up first. Writing it as
   root leaves the file owned by root and the service (running as its own user) can
   no longer read it.
7. **Start, wait for HTTP, then verify against PG directly.** Readiness is the HTTP
   port, not `systemctl is-active`. Then count `tenants` / `session_messages` /
   `iteration_history` on the PG side and compare with the source.
8. **Rollback is: stop, restore the config backup, start.** Because the SQLite files
   were only ever read, no data has to be copied back. The one-way door is the other
   direction: messages written to PG after the cutover do not return to SQLite.

Two footguns worth restating because they are easy to reintroduce:

- **A default DSN pointing at a real server plus `-wipe` is a data-loss trap.** A
  script that runs unattended with both defaults will eventually be run against the
  wrong database. Require the DSN explicitly.
- **Deploying the binary is not part of migrating.** Building and installing a new
  binary belongs to the deploy step; mixing it in means a migration failure leaves
  you unable to tell which change caused it.

## Runtime services are backend-agnostic too

The same rule that applies to memory providers applies to every runtime service that
used to hold a SQLite handle: the scheduler, the webhook router and the OAuth server.
A `*sqlite.XxxService` field in those layers is what made the feature silently vanish
on PG, because the wiring was guarded by `MultiSession.DB() != nil` and that handle is
nil on PG. They are now reached through `Storage`:

| Accessor | Interface | Consumers |
|---|---|---|
| `Storage.Cron()` | `CronStore` | `cron.Scheduler`, `tools.CronTool`, `plugin.WirePluginCrons`, web cron panel |
| `Storage.Trigger()` | `event.TriggerStore` | `event.Router` (webhook dispatch) |
| `Storage.OAuth()` | `OAuthTokenStore` | `oauth.Manager` via a thin adapter in `serverapp` |

Consequences worth knowing:

- **OAuth is created after `InitServer`**, because the token store comes from the
  storage backend. Previously it opened its own SQLite file before the server started,
  and PG skipped it entirely.
- **`RecordFire` does not disable one-shot triggers** — that decision belongs to
  `event.Router.dispatchOne` (it calls `UpdateEnabled`). Keeping it in the store made
  only one backend do it, which is exactly the kind of divergence the contract suite
  catches.
- `oauth_tokens` is part of the main SQLite schema (`createSchema`) rather than being
  created lazily by the OAuth package: a backend-neutral store cannot rely on a
  package-level init step that only runs when OAuth happens to be enabled.
- **A new table needs a path for existing databases.** `createSchema` only runs on a
  brand-new file and the migration chain only runs when the version changes, so a
  database already at the current version never receives a newly-added table. Such
  tables go into `ensureAdditiveTables` (`storage/sqlite/db.go`), which runs at the end
  of both the same-version branch and `migrateSchema`. Every statement there must be
  `CREATE … IF NOT EXISTS`. Guard: `additive_tables_test.go`.
- Nullable TEXT columns in PG projections are `COALESCEd` (`cron_expr`, `at`,
  `sender_id`) — scanning NULL into a plain `string` fails at runtime, not at compile
  time.

## Known scope limits

- Per-session DB (v71 one-session-one-DB) is a SQLite mechanism. Under PG the same
  `tenantID` partitions rows in a shared schema; `session_messages` / `iteration_history`
  do not become separate databases.
- `MultiSession.DB()` remains as a SQLite-only escape hatch (WAL checkpoint on
  shutdown, legacy browse SQL). Anything reachable by a normal user path must go
  through `Storage`.
- `docs/plans/storage-interfaces.md` holds the original design notes.
