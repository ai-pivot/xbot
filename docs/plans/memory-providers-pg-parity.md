# Plan: memory providers on PG (letta + xbot) — creation logic in the common path

## Summary

Today the PG backend **hard-disables** both non-flat memory providers
(`session/multitenant.go:305-307` — `m.memoryProvider = "flat"`, "xbot/letta 记忆
provider 暂不支持 PG"), the chromem archival/tool-index services are created **only**
in the SQLite constructor and their directories are derived from `dbPath` (which on PG
holds a **DSN**, not a path), and the `xbot` provider reaches storage through a raw
`*sql.DB`. This plan makes the memory stack backend-agnostic: one shared init path
called by both constructors, an explicit data directory (never derived from `dbPath`),
and every provider dependency satisfied through the `storage.*` interfaces — so `letta`
and `xbot` both run on PG.

## Facts established (recon, 2026-10-19)

| Item | Evidence |
|---|---|
| PG forces flat | `session/multitenant.go:305-307` (after all opts) |
| Vector service creation lives only in the SQLite ctor; dirs from `dbPath` | `multitenant.go:225-235` (`archival`), `:238-256` (`tool_index`) — vs PG `dbPath = dsn` (`:289`, doc `:275`) |
| Generator dirs today equal `~/.xbot/{archival,tool_index}` | `dbPath = ~/.xbot/xbot.db` ⇒ `filepath.Dir` = `~/.xbot`; `config.XbotHome()` = `$XBOT_HOME` or `~/.xbot` |
| letta touches only `InitBlocks` / `GetAllBlocks` / `SetBlock` | `memory/letta/letta.go:90,111,252,344` — **all already on `storage.MemoryStore`** (`storage/interfaces.go:163-168`) |
| `migrateProfileToCoreMemory` is already interface-based | `multitenant.go:631-643` uses `m.store.Memory()/Config()` |
| `recallTimeRangeFn` is SQLite-only | `multitenant.go:260-262` → `vectordb.NewSQLiteRecallTimeRangeFunc(db.Conn())` |
| `xbot` provider owns its DDL + SQL against `*sql.DB` | `memory/xbot/xbot.go` (2070 lines, ~51 SQL stmts, 4 tables incl. 2 FTS5 virtual, 12× `bm25()`); receives `deps.DB` (`memory/memory.go:191`), nil on PG (`multitenant.go:432-434`) |
| PG has no `xbot_*` tables | `grep -c "xbot_" storage/pg_schema.go` → 0 |
| PG FTS works with app-side CJK pre-split, **no extension needed** | measured on PG 16.0.15: `to_tsvector('simple','记忆系统')` → 1 token; `'记 忆 系 统'` → 4 tokens; `@@ to_tsquery('simple','记 \| 忆')` → true; `ts_rank` → 0.0608 (higher = better); `zhparser`/`pg_jieba` **not available**, `pg_trgm` available but unnecessary |
| Shared query-construction helpers already exist | `fts5SafeQuery` / `fts5OrQuery` / `fts5Tokens` / `cjkSpaceRuns` (`memory/xbot/xbot.go:636-780`) |

## Invariant (the point of this plan)

> A memory provider depends **only** on `storage.*` interfaces plus pure logic.
> Adding or switching a storage backend must require **zero** changes under
> `memory/`. Per-backend SQL/DDL belongs to the storage layer, never to a provider.

Two things this does **not** mean:
- The provider is allowed to be *tokenizer/ranking agnostic* only if the interface is
  shaped right — hence `AgentMemoryStore` takes an already-tokenized query and returns
  **already-ordered** results (otherwise FTS dialect details — `bm25` vs `ts_rank`
  direction, CJK splitting — leak back into the provider).
- Relocating the `xbot` provider's existing SQL is a **one-time** de-coupling, not
  per-backend adaptation.

### Providers never see a driver or a dialect

`ProviderDeps` must never carry a `*sql.DB`, a DSN, or any dialect-flavoured knob. A
provider holding a driver handle will inevitably grow dialect-specific SQL and then be
unrunnable on some backend — so **DB-backed memory is ours** (storage layer) and reaches
providers only as a *semantic* interface (tokenized query in, ordered results out).

This is already the de-facto situation — keep it that way: providers are registered from
in-repo packages only (`memory/{flat,letta,xbot}` `init()`), the plugin system exposes no
memory hook, and `ProviderDeps.DB` has exactly one consumer (`xbot`). The registry is
**internal**, not a third-party extension point; do not document it as one.

Guard (same style as the repo's forbidden-identifier regression tests): a unit test over
`memory/**` non-test sources asserting no `database/sql`, `xbot/storage/sqlite` or
`github.com/jackc/pgx*` import, plus a check that `ProviderDeps` declares no driver/DSN field.

### Current DI violations (exhaustive)

| Where | Violation |
|---|---|
| `memory/memory.go:191` | `ProviderDeps.DB *sql.DB` — a raw driver handle handed to a provider |
| `memory/letta/letta.go:63` | `letta.Deps.CoreSvc *sqlite.CoreMemoryService` — concrete SQLite type in provider deps |

Everything else already satisfies the invariant: `letta` reads core memory through
`storage.MemoryStore` (`MemorySvc`), `flat` touches storage not at all (files +
in-memory tool index), and the `archival`/`tool_index` services are vector stores
(backend-independent) whose only backend-flavoured part is **where their directory
comes from** (today `filepath.Dir(dbPath)`).

## Changes

### P1 — creation logic into the common path (enables letta on PG)

#### `session/multitenant.go`
- What: new `initMemoryServices()` on `MultiTenantSession`, called by **both** `NewMultiTenant`
  and `NewMultiTenantPG` after opts are applied. Move the `archival` + `tool_index`
  creation blocks (and the shared `embOpts` build) out of the SQLite ctor into it.
- What: new option `WithDataDir(dir)`; default `config.XbotHome()`. The vector dirs become
  `filepath.Join(dataDir, "archival")` / `"tool_index"` — **never** `filepath.Dir(dbPath)`.
- What: delete the `m.memoryProvider = "flat"` override; if a requested provider's deps
  cannot be satisfied, **fail explicitly** (return error / log ERROR + refuse to start)
  — no silent downgrade.
- Why: one creation path, backend-independent; the default data dir preserves the existing
  files so no re-embedding is needed.

#### `session/multitenant.go` (deps)
- What: `recallTimeRangeFn` becomes backend-agnostic — `vectordb.NewStoreRecallTimeRangeFunc(store)`
  instead of the `*sql.DB` variant; keep the SQLite one only if still needed internally.
- Why: letta's recall path must not require a SQLite handle.

#### `storage/interfaces.go` + `memory/letta/letta.go`
- What: `letta.Deps.CoreSvc` and `LettaMemory.coreSvc` change from
  `*sqlite.CoreMemoryService` to `storage.MemoryStore`; drop `CoreService()`; delete
  `MultiTenantSession.coreSvc` + `CoreMemoryService()`.
- Why: the three methods letta uses are already on that interface — this is the whole
  coupling.

#### `storage/interfaces.go` + `storage/pg_memory.go` + `storage/sqlite_adapter.go`
- What: add `GetHistoryEntriesInRange(ctx, tenantID, start, end, limit)` to `MemoryStore`
  (PG already has the table/index; SQLite has `idx_event_history_tenant_created`).
- Why: replaces the SQLite-only `RecallTimeRangeFunc`.

### P2 — `xbot` provider storage through the common interface (enables xbot on PG)

#### `storage/interfaces.go`
- What: new sub-interface `AgentMemoryStore` (short/long-term memory rows + keyword search
  + supersede/decay/eviction primitives), embedded into `MemoryStore` so it is reached via
  the existing `Storage.Memory()` — consistent with `ConfigStore` embedding its
  sub-interfaces. **Search takes a structured query** (`{Terms []string, ...}`) and
  **returns already-ordered results**, so ranking direction stays a backend detail.
- Why: the provider must not see SQL; and ordered-result return hides the
  `bm25` (negative, lower = better) vs `ts_rank` (positive, higher = better) difference.

#### `storage/sqlite/agent_memory.go` (new) + `storage/sqlite_adapter.go`
- What: move the xbot provider's DDL + SQL (4 tables, ~51 statements) here as
  `sqlite.AgentMemoryService`; the adapter delegates.
- Why: the storage layer owns schema/SQL for every other subsystem; the provider keeps
  only policy (prompt, extraction, dedup, LLM calls).

#### `storage/pg_agent_memory.go` (new) + `storage/pg_schema.go`
- What: PG implementation with native SQL; add `xbot_short_term_memories` /
  `xbot_long_term_memories` to `pg_schema.go` (mirroring SQLite columns + `search_text`),
  plus `search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', search_text)) STORED`
  with a **GIN index**. `thing` search renders the structured query as `to_tsquery('simple', …)`.
- Why: native PG FTS, **no extension dependency**.

#### `memory/xbot/xbot.go`
- What: drop its DDL/migration/backfill and `*sql.DB`; take `storage.AgentMemoryStore` via
  `ProviderDeps`. Keep `cjkSpaceRuns`/`fts5Tokens`/stopwords as the **shared tokenizer** and
  move them to one exported home used by the caller when building both the indexed
  `search_text` and the query (single source of truth for CJK policy).
- Why: identical recall semantics on both backends; no SQL in the provider.

#### `memory/memory.go`
- What: **delete** `ProviderDeps.DB *sql.DB` — do not replace it with any driver/DSN/dialect
  surface. The `xbot` provider receives `storage.AgentMemoryStore` through the same deps
  slot; the struct stays semantic-only.
- What: add the guard test described under "Providers never see a driver or a dialect".
- Why: a driver handle in deps is precisely what makes a provider dialect-bound.

### P3 — migration & docs (after P1/P2)
- `cmd/xbot-migrate`: add `xbot_short_term_memories` / `xbot_long_term_memories` to the copy
  set; **rebuild** `search_text`/`search_tsv` instead of copying FTS shadow tables.
- `docs/agent/storage.md`: record that chromem files stay local (deliberate dual source:
  bodies in PG, vectors in files) and the CJK pre-split rule.
- chromem `~/.xbot/{archival,tool_index}`: no migration; optional ops cleanup (54 MB).

## Risks

- **xbot provider refactor is the big one** (2070 lines / 51 SQL / semantic search):
  behaviour drift. Mitigation: `AgentMemoryStore` contract tests run against both backends
  **plus** a parity test that feeds one fixed corpus through both and asserts **identical
  ordered results** (the analogue of `ApplyHistoryRecords`).
- **Existing PG instances** need the new tables: `initPGSchema` is idempotent
  (`CREATE TABLE IF NOT EXISTS`) — verified pattern from PR #444.
- **Removing the force-flat changes failure modes**: a misconfigured PG instance could now
  try a provider with missing deps ⇒ must fail fast with a clear message (never silent).
- **CJK ranking parity**: SQLite `bm25` and PG `ts_rank` are different scales; the test
  asserts ordering, not scores.

## Definition of Done

- [ ] Both constructors call one shared memory-service init path; no vector dir derived from `dbPath`.
- [ ] `memory/letta` imports no `storage/sqlite`; letta works on PG (core memory + archival
      search exercised against a live PG).
- [ ] `memory/xbot` holds no `*sql.DB`; xbot works on PG; a CJK query returns the expected rows.
- [ ] `pg_schema.go` owns `xbot_*` DDL (+ tsvector/GIN); a fresh PG DB gets them.
- [ ] Contract suite extended for `AgentMemoryStore` (both backends green) + search parity test.
- [ ] `gofmt` / `golangci-lint` / `go build` / `go test` green; PR(s) open, **not merged**.

## Open Questions

1. Split into **two PRs** (P1 letta-on-PG, then P2 xbot-on-PG) or one?
2. Confirm moving the xbot provider's ~51 SQL statements into the storage layer (my
   recommendation) rather than keeping SQL in the provider behind a narrower interface.
3. Confirm PG FTS = `tsvector('simple')` + shared `cjkSpaceRuns` (zero extensions).
