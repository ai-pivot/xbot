---
title: "Memory System"
weight: 50
---

# Memory System

xbot supports pluggable memory providers. The agent uses memory to persist
information across conversations, recall past interactions, and maintain
user-specific context.

{{< columns >}}

## Flat
*(default)*

- In-memory text blob
- Zero dependencies
- Simple: append, replace, search

<--->

## Letta
*(MemGPT-inspired)*

- Three-tier: core + archival + recall
- Vector search via chromem-go
- FTS5 full-text search
- Requires embedding model

{{< /columns >}}

## Configuration

```bash
# Set via environment variable
MEMORY_PROVIDER=flat    # or: letta
```

For Letta mode, configure an embedding model:

```bash
LLM_EMBEDDING_PROVIDER=openai
LLM_EMBEDDING_BASE_URL=https://api.openai.com/v1
LLM_EMBEDDING_API_KEY=sk-xxx
LLM_EMBEDDING_MODEL=text-embedding-3-small
```

## Flat Provider (default)

All long-term memories are stored as a single text blob and injected into the
system prompt on every request. When you start a new conversation (`/new`), the
LLM consolidates and summarizes the memory blob.

**Tools:** `memory_write`, `memory_list`

| Characteristic | Detail |
|---------------|--------|
| Storage | In-memory text blob |
| Persistence | Saved/restored with session state |
| Consolidation | LLM-driven on `/new` |
| Search | Basic text matching (grep-style) |

{{< hint type=tip >}}
**Flat is the right choice** for most users. It's dead simple, requires zero
additional setup, and works great for remembering preferences and key facts.
{{< /hint >}}

## Letta Provider

Three-tier memory architecture inspired by [MemGPT](https://memgpt.ai/).
Powered by SQLite with vector embeddings and full-text search.

### Core Memory

Three blocks always present in the system prompt:

| Block | Purpose | Isolation |
|-------|---------|-----------|
| `persona` | Agent's identity and personality | **Global** — shared across all users |
| `human` | Per-user observations and preferences | **Per-user** — isolated by sender ID |
| `working_context` | Current task state and active context | **Per-session** — reset on `/new` |

**Tools:** `core_memory_append`, `core_memory_replace`, `rethink`

### Archival Memory

Long-term vector-backed storage for detailed facts, events, and context. Stored
in SQLite with chromem-go embeddings for semantic search.

**Tools:** `archival_memory_insert`, `archival_memory_search`

### Recall Memory

Full conversation history searchable by date range. Powered by SQLite FTS5
(full-text search) for fast keyword matching.

**Tools:** `recall_memory_search`

## Memory Consolidation

When starting a new conversation (`/new` command):

| Provider | Behavior |
|----------|----------|
| **Flat** | LLM merges and consolidates the memory blob — old memories are summarized, redundancies removed |
| **Letta** | Core memory persists across sessions; archival memory is retained; `working_context` is cleared |

## Core Memory Isolation

In multi-tenant deployments (Server mode with multiple channels like Feishu + Web):

- `persona` is **global** — the agent's core identity is shared across all users
- `human` is **per-user** — what the agent learns about Alice is invisible to Bob
- `working_context` is **per-session** — each conversation maintains its own task context

{{< hint type=important >}}
Core memory blocks set via `core_memory_append`/`core_memory_replace` are
**persistent across sessions**. Use `rethink` to trigger the agent to
re-examine and evolve its memory, similar to MemGPT's self-editing mechanism.
{{< /hint >}}
