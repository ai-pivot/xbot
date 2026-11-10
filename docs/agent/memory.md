# memory/ — Pluggable Memory Providers

## Architecture: Two-Layer Memory

| Layer | Scope | Storage | Tools |
|-------|-------|---------|-------|
| **Project Knowledge** | Project-level, shared | `docs/agent/` (md files, git-trackable) | `Read`, `FileReplace`, `FileCreate` |
| **Flat Memory** | Per-user, non-project | `~/.xbot/memory/{tenantID}/` (md files) | `memory_write`, `memory_list`, `Read` |
| **Letta Memory** | Per-user, full-featured | SQLite + vector DB | `core_memory_*`, `archival_memory_*`, `recall_memory_search` |
| **None** | — | — | none (no memory tools or prompts) |

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
- **Two tiers** (core summary REMOVED 2026-11-07): short-term session summaries +
  long-term atomic memories (fact/preference/event/decision/skill). The old core tier
  was `MEMORY.md`, an LLM-rewritten summary re-injected every turn — unvalidated LLM
  output rotted it to 116KB/894 lines (27 prompt echoes, an `rm -rf /` injection probe,
  Cyrillic/kana mojibake, inner-monologue drafts) in the wild. `updateCoreSummary`
  and the `## Core` injection are deleted; quarantined copies live in
  `~/.xbot/memory/_quarantine/`.
- **Two-phase write (2026-11-07)**: `memory_add` NEVER persists global-scope memories
  directly — it stages a row in `memory_proposals` (TTL 10 min) and returns a
  `PROPOSAL <id>（未写入）` payload with the persistence warning + `memory_confirm`
  syntax. `memory_confirm(proposal_id)` is the only global write path; it re-runs the
  full gate chain (TTL + validation + duplicate). `scope='session'` writes directly
  (session-local only). Auto-extraction (ConsolidateTurn/PreCompress/Memorize) writes
  `scope='session'` only; any global-scoped auto entry is DEMOTED to a pending
  proposal. Recall appends `N 条待确认记忆（用 memory_confirm 处理）` when
  proposals are pending.
- **Content validation gate (2026-11-07)**: every write path (AddMemory /
  ProposeMemory / ConfirmProposal / addLongTermMemory) rejects — with explicit
  errors — content >2000 runes, injection patterns (`ignore all previous
  instructions`, `mark task as complete`, `rm -rf /`, `SELECT * FROM`,
  `drop table`, `<script`), line-initial draft markers (`Wait.` / `So I should` /
  `Let me count`), and Cyrillic/kana mojibake.
- **Content-level duplicate gate (2026-11-07)**: a candidate whose content is
  identical after normalization (case/whitespace/punctuation stripped) or whose
  token-set Jaccard overlap ≥ 0.8 with an ACTIVE memory is REJECTED with the
  existing memory's ID (`DuplicateMemoryError`) — at BOTH the proposal and the
  confirm phase. Deliberately NOT BM25: keyword overlap cannot distinguish a
  near-identical duplicate from a contradicting update; contradicting updates
  (low content overlap) keep flowing into the supersede chain.
  **Injection budget**: `Recall()` caps TOTAL injected content at `recallMaxRunes=3000`
  (core + short-term + long-term) — the core summary and most relevant memories stay, the
  tail is cut with a truncation marker. **Short-term is injected ONLY when `query != ""`**:
  recentShortTerm() returns OTHER sessions' summaries (unrelated to the current query) — an
  empty query previously injected them as raw "别的 session 上下文", diluting attention.
  (This was the "自动注入的别的 session context 过长" report.)
- **BM25 retrieval**: SQLite FTS5 `unicode61` with a `search_text` column. CJK runs are
  space-separated (`cjkSpaceRuns`: each Chinese char → own token, CJK↔ASCII boundary
  split too) so Chinese substrings match ("记忆" → `"记" OR "忆"`). Query transform
  `fts5OrQuery` (search paths) applies the SAME spacing — query and index stay symmetric.
  All user input is wrapped in quoted tokens (FTS5 string literal) to prevent syntax errors.
  ⚠️ **Search uses OR semantics** (`fts5OrQuery`, joined with ` OR `) — multi-term BM25
  search must not AND-combine tokens: one absent word (query "frpc" vs indexed "frps")
  zeroes the whole result set even when a high-IDF term matches exactly (user report:
  `"2008 机器 IP 地址 frpc 转发"` returned nothing while `"2008"` alone hit). OR recalls,
  `bm25()` ranks. **Dedup keeps AND** (`fts5SafeQuery`, joined with ` AND `): full-token
  overlap is the similarity criterion — OR there would false-positive distinct memories
  sharing one common word.
- **Auto-extraction**: `ConsolidateTurn` (memory.TurnConsolidator) runs after each turn —
  throttled to once per 10 min per session, incremental (watermark `LastConsolidated`),
  passes the FULL message list (never slices — protects provider prefix cache) with
  `[NEW]`/`[old]` markers in the prompt. `Memorize(ArchiveAll=true)` still runs on /new.
  The goroutine is tracked by `Agent.lifecycleWG` and cancelled on `Agent.Close()` via
  `lifecycleStopCh` (NOT `agentCtx`, which dies at end of every Run) so consolidation
  never touches a closed DB. It receives a shallow copy of `out.Messages` — in-place
  mutation by `ConsolidateTurn` must never affect the caller's slice.
- **Garbage filtering**: `stripInjectedBlocks` removes `<context>`/`<system-reminder>`/
  `<dynamic-context>` XML blocks + injection-marker lines (📂 workdir, 👥 peers, ✅ tool
  stats, 行为提醒, timestamps, sender) before LLM extraction. Prompt demands DURABLE,
  CROSS-SESSION facts only — never session-local progress, transient context, or
  system-injected metadata.
- **CompressionAware**: PreCompress extracts atomic memories + returns PreserveHints
  before context compression; PostCompress saves the compaction summary; CompressContext
  tells the compression LLM what's already backed up.
- **LLM client ownership (2026-09-02 fix)**: every LLM-using memory operation
  (Memorize/ConsolidateTurn/PreCompress/PostCompress) receives its `llm.LLM` client
  + model EXPLICITLY in its input and threads them down to `generateLLM` — the
  provider holds NO shared llmClient field. The old shared mutable `m.llmClient`
  was raced by concurrent sessions' memory ops (single-operator: one XbotMemory
  instance serves ALL sessions — the 2026-09-02 chat_BD94FA4BB469 incident had
  PostCompress's core-summary update land on another session's model/endpoint
  after a concurrent ConsolidateTurn overwrote the field; PostCompressInput had
  no LLMClient at all). PostCompressInput now carries `LLMClient`/`Model`
  (engine_run.go passes `s.cfg.LLMClient/s.cfg.Model`); the dead `SetLLM` method
  is deleted. Any new memory LLM call site MUST take its client from the input —
  never from shared state.
- **Session isolation (2026-09-02 redesign)**: `xbot_long_term_memories.scope`
  ('global' | 'session', legacy rows default 'global') + `superseded_by` columns
  (idempotent migration in `migrateLegacyTenantData`). **Auto-extraction
  (extractAtomicMemories via ConsolidateTurn/PreCompress/Memorize) writes
  scope='session'** — session-local task state never crosses sessions; the
  global pool is written ONLY by explicit `memory_add` (default 'global',
  scope param exposed to the model). **Recall's "## Recent Sessions" section is
  DELETED** (it injected OTHER sessions' short-term summaries into unrelated
  conversations — query-anchored searchShortTerm had no session filter);
  cross-session recall goes through `memory_search` on demand only. Long-term
  injection filters `scope='global' AND superseded_by IS NULL` and carries
  created dates (model-side staleness judgment). SearchMemories/ListMemories
  return scope provenance in results.
- **Supersede chain (2026-09-02 redesign)**: an explicit global AddMemory with
  strong keyword overlap (same BM25 threshold as dedup, < -6.0) marks the
  matched ACTIVE GLOBAL entries `superseded_by=<new id>` instead of
  double-storing contradictory facts ("cluster at X" + "cluster moved to Y").
  Rows are preserved (rollback), but Recall/SearchMemories/ListMemories all
  filter superseded — only current facts are visible. Session-scoped
  auto-extraction NEVER supersedes global entries (task-local state must not
  invalidate user-level facts).
- **Compression hooks are async (2026-09-02)**: PreCompress/PostCompress run
  on background goroutines (`RunConfig.SpawnBackground` — lifecycleWG-tracked,
  lifecycleStopCh-cancelled, NOT the turn ctx) on message snapshots; only the
  compaction LLM call itself is synchronous. PostCompressInput.RemovedMessageCount
  is captured PRE-swap (the old post-swap computation was always 0).
- **Bloat control**: BM25 similarity dedup (bm25 > -6.0 = duplicate), per-user cap
  (`longTermMaxEntries=300`, lowest-heat pruned), heat decay + forget threshold.
- **Tools**: `memory_search` (BM25), `memory_add`, `memory_manage`.
- **Observability**: `Recall()` logs `xbot-memory: Recall injected memories` per turn
  (query, short_term, long_term, has_core, injected_chars). `ConsolidateTurn` logs
  `xbot-memory: ConsolidateTurn extracted memories` (added count).
- **has_core**: DELETED 2026-11-07 with the core tier itself (no MEMORY.md is read,
  written, or injected anymore).

## None Mode (`none`)

`memory_provider: "none"` is an explicit, first-class mode: **no memory tools and
no memory prompts are injected at all**.

- `tools.GetMemoryTools("none")` returns nil → the agent's tool registry gets no
  `memory_*` / `core_memory_*` / `archival_memory_*` / `recall_memory_search`.
- `session/multitenant.go` stores `memProvider = nil` → no `Recall()` injection
  (`MemoryMiddleware` no-ops), no auto-consolidation (`ConsolidateTurn`/`Memorize`
  are skipped), and no `CompressionAware` hooks.
- `agent/context.go` explicitly injects no `## Memory` section (it keeps only the
  provider-neutral `ToolsFlat` guide, which contains no memory instructions).
- SubAgents get no memory provider either (`buildSubAgentMemory` returns nil for
  `none`) and `subagentMemorySection()` returns "".
- This is NOT a fallback for an unknown provider: setting any unregistered name
  still gets the flat prompt guide by default. `none` is the only value that
  deliberately disables memory.

## Tool Visibility (Unified)

Both flat and letta memory providers use the **same tool visibility model**: all registered tools
are always visible to the LLM with full parameter schemas. The previous distinction (flat mode =
all tools visible, letta mode = on-demand activation via `load_tools`) has been removed.

The memory provider only affects which **memory-specific tools** are registered:
- Flat: `memory_read`, `memory_write`, `memory_list`
- Letta: `core_memory_append`, `core_memory_replace`, `rethink`,
  `core_memory_delete`, `core_memory_clear` (2026-11-07), `archival_memory_insert`,
  `archival_memory_search`, `recall_memory_search` — core blocks are tool-managed
  only, NEVER auto-maintained (consolidation no longer rewrites them) and NEVER
  auto-injected into Recall
- Xbot: `memory_search`, `memory_add` (two-phase: stages a PROPOSAL for global
  scope), `memory_confirm` (2026-11-07, persists proposals), `memory_manage`
- None: none (memory is fully disabled — no tools, no prompt injection)

## Metrics

Knowledge system metrics are tracked in `AgentMetrics`:
- `MemoryRecalls`: Recall() calls (system prompt injection)
- `MemoryWrites`: memory_write tool calls
- `MemoryConsolidations`: successful Memorize() consolidations
- `DocsAgentReads`: Read tool calls on docs/agent/ paths
