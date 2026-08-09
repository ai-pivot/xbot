# memory/ — Pluggable Memory Providers

## Architecture: Two-Layer Memory

| Layer | Scope | Storage | Tools |
|-------|-------|---------|-------|
| **Project Knowledge** | Project-level, shared | `docs/agent/` (md files, git-trackable) | `Read`, `FileReplace`, `FileCreate` |
| **Flat Memory** | Per-user, non-project | `~/.xbot/memory/{tenantID}/` (md files) | `memory_write`, `memory_list`, `Read` |
| **Letta Memory** | Per-user, full-featured | SQLite + vector DB | `core_memory_*`, `archival_memory_*`, `recall_memory_search` |

**Project knowledge** uses the same tools as regular code editing (Read/FileReplace/FileCreate).
The `knowledge_write`/`knowledge_list` tools have been removed — AGENTS.md references `docs/agent/` files directly.

## Key Interface

```go
// memory/memory.go
type MemoryProvider interface {
    Recall(ctx context.Context, query string) (string, error)
    Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)
    Close() error
}
```

## Flat Memory (`flat/`)

- **File-based**: `MEMORY.md` (≤1000 chars, injected into system prompt) + `HISTORY.md` (event timeline)
- Directory: `~/.xbot/memory/{tenantID}/`
- `Recall()`: reads MEMORY.md for system prompt injection
- `Memorize()`: LLM consolidation with `save_memory` tool; updates MEMORY.md and appends to HISTORY.md
- Tool search: simple substring match (no vector DB)
- No SQLite dependency
- **No knowledge/ subdirectory** — project knowledge is managed via AGENTS.md + docs/agent/

## Letta Memory (`letta/`)

- Core memory: persona, human, working_context blocks (stored in SQLite)
- Archival memory: vector DB with semantic search
- Each tenant has isolated memory
- `consolidate_memory` tool: moves working_context items to archival

## Xbot Memory (`xbot/`)

Zero-dependency memory provider (no embedding API) built on SQLite FTS5 BM25.

- **Cross-session by design**: memories are scoped by `user_id` (canonical owner),
  NOT `tenant_id`. The same user sees the same memories across ALL sessions/channels.
  `New()` resolves owner from `tenants.owner_user_id` when not passed; `SetOwnerUserID`
  fixes up providers created via the tool-extras path. `scopeArg()` NEVER falls back
  to tenant_id (that was the "different sessions see different memories" bug).
- **Three tiers**: core summary (`MEMORY.md`, ≤2000 chars, always injected) + short-term
  session summaries + long-term atomic memories (fact/preference/event/decision/skill).
- **BM25 retrieval**: SQLite FTS5 `unicode61` with a `search_text` column. CJK runs are
  space-separated (`cjkSpaceRuns`: each Chinese char → own token, CJK↔ASCII boundary
  split too) so Chinese substrings match ("记忆" → `"记" AND "忆"`). Query transform
  `fts5SafeQuery` applies the SAME spacing — query and index stay symmetric. All
  user input is wrapped in quoted tokens (FTS5 string literal) to prevent syntax errors.
- **Auto-extraction**: `ConsolidateTurn` (memory.TurnConsolidator) runs after each turn —
  throttled to once per 10 min per session, incremental (watermark `LastConsolidated`),
  passes the FULL message list (never slices — protects provider prefix cache) with
  `[NEW]`/`[old]` markers in the prompt. `Memorize(ArchiveAll=true)` still runs on /new.
- **Garbage filtering**: `stripInjectedBlocks` removes `<context>`/`<system-reminder>`/
  `<dynamic-context>` XML blocks + injection-marker lines (📂 workdir, 👥 peers, ✅ tool
  stats, 行为提醒, timestamps, sender) before LLM extraction. Prompt demands DURABLE,
  CROSS-SESSION facts only — never session-local progress, transient context, or
  system-injected metadata.
- **CompressionAware**: PreCompress extracts atomic memories + returns PreserveHints
  before context compression; PostCompress saves the compaction summary; CompressContext
  tells the compression LLM what's already backed up.
- **Bloat control**: BM25 similarity dedup (bm25 > -6.0 = duplicate), per-user cap
  (`longTermMaxEntries=300`, lowest-heat pruned), heat decay + forget threshold.
- **Tools**: `memory_search` (BM25), `memory_add`, `memory_manage`.
- **Observability**: `Recall()` logs `xbot-memory: Recall injected memories` per turn
  (query, short_term, long_term, has_core, injected_chars). `ConsolidateTurn` logs
  `xbot-memory: ConsolidateTurn extracted memories` (added count).
- **has_core**: true when `~/.xbot/memory/{tenant}/MEMORY.md` exists AND is non-empty
  (produced by `updateCoreSummary`). Independent of memory count.

## Tool Visibility (Unified)

Both flat and letta memory providers use the **same tool visibility model**: all registered tools
are always visible to the LLM with full parameter schemas. The previous distinction (flat mode =
all tools visible, letta mode = on-demand activation via `load_tools`) has been removed.

The memory provider only affects which **memory-specific tools** are registered:
- Flat: `memory_read`, `memory_write`, `memory_list`
- Letta: `core_memory_*`, `archival_memory_*`, `recall_memory_search`
- Xbot: `memory_search`, `memory_add`, `memory_manage`

## Metrics

Knowledge system metrics are tracked in `AgentMetrics`:
- `MemoryRecalls`: Recall() calls (system prompt injection)
- `MemoryWrites`: memory_write tool calls
- `MemoryConsolidations`: successful Memorize() consolidations
- `DocsAgentReads`: Read tool calls on docs/agent/ paths
