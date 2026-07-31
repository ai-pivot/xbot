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

## Tool Visibility (Unified)

Both flat and letta memory providers use the **same tool visibility model**: all registered tools
are always visible to the LLM with full parameter schemas. The previous distinction (flat mode =
all tools visible, letta mode = on-demand activation via `load_tools`) has been removed.

The memory provider only affects which **memory-specific tools** are registered:
- Flat: `memory_read`, `memory_write`, `memory_list`
- Letta: `core_memory_*`, `archival_memory_*`, `recall_memory_search`

## Metrics

Knowledge system metrics are tracked in `AgentMetrics`:
- `MemoryRecalls`: Recall() calls (system prompt injection)
- `MemoryWrites`: memory_write tool calls
- `MemoryConsolidations`: successful Memorize() consolidations
- `DocsAgentReads`: Read tool calls on docs/agent/ paths
