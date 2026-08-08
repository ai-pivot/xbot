# xbot 记忆系统重设计方案

> 基于 2025-2026 最新记忆系统论文研究 + xbot 现有架构分析
>
> **设计原则**：新增 `xbot` provider，与现有 `flat`/`letta` 并存，不替换老系统。
> 所有记忆工具和 prompt 片段**仅在 `memory_provider: "xbot"` 时注入**。

## 一、现有系统痛点分析

### 1.1 Flat Memory 的问题

| 问题 | 影响 |
|------|------|
| **无跨会话检索** | `Recall()` 直接读 `MEMORY.md` 全文注入 system prompt，无法按相关性检索 |
| **1000 字符硬限制** | 核心记忆被截断，超出部分需要 agent 手动用 Read 工具查看 |
| **Memorize 仅 `/new` 触发** | 增量合并未实现，只有 `/new` 命令才执行完整归档 |
| **无语义搜索** | `SearchTools` 用简单子串匹配，无法处理同义词/改述 |
| **HISTORY.md 无结构** | 纯文本追加，无法按时间/主题/重要性检索 |
| **LLM 合并质量不可控** | 每次 `/new` 调用 LLM 重写整个 MEMORY.md，容易丢失信息 |

### 1.2 Letta Memory 的问题

| 问题 | 影响 |
|------|------|
| **依赖 chromem-go 向量数据库** | 需要 embedding 模型，增加外部依赖和资源消耗 |
| **三层结构过于僵化** | persona/human/working_context 固定三块，无法适应不同场景 |
| **Memorize 仅 `/new` 触发** | 同 flat，增量合并未实现 |
| **去重逻辑复杂且不可靠** | 向量相似度 >0.5 判断冲突，误判率高 |
| **工具过多且分散** | 6 个工具（core_memory_append/replace/rethink, archival_memory_insert/search, recall_memory_search），agent 难以正确使用 |
| **SubAgent 记忆不持久化** | flat 模式下 SubAgent 始终创建 LettaMemory，归档可能不生效 |

### 1.3 共性问题

| 问题 | 影响 |
|------|------|
| **无自动记忆触发** | 记忆合并只在 `/new` 时发生，用户不主动归档就永远丢失 |
| **无跨会话检索** | 两个 provider 都不支持"搜索过去对话中的某个话题" |
| **Recall 无相关性过滤** | flat 全量注入，letta 注入三个 block 全文，都不根据当前对话内容筛选 |
| **无遗忘机制** | 记忆只增不减，长期使用后 system prompt 膨胀 |
| **无重要性分级** | 所有记忆同等对待，关键信息和琐碎细节混在一起 |
| **无时间感知** | 无法按时间范围检索记忆，无法感知记忆的新旧 |

### 1.4 现有注入路径的隔离问题

通过代码分析，现有记忆系统的注入点散布在 **8 处**，全部使用字符串比较 `memoryProvider == "letta"` 做分支：

| # | 文件 | 函数/位置 | 分支逻辑 | 隔离情况 |
|---|------|---------|---------|---------|
| 1 | `agent/agent.go:1624-1638` | `initServices()` | `if "letta"` → 注册 6 个 letta 工具; `if "flat"` → 注册 2 个 flat 工具 | ✅ 隔离正确 |
| 2 | `agent/context.go:150-157` | `enrichPromptData()` | `switch "letta"` → ToolsLetta + MemoryLetta; `default` → ToolsFlat + 空 Memory | ⚠️ `none` 走 default，注入了 flat 的 Tools prompt 但无工具 |
| 3 | `agent/middleware_builtin.go:21-42` | `SystemPromptMiddleware` | 传入 `memoryProvider` 给模板渲染 | ✅ 依赖 #2 |
| 4 | `agent/middleware_builtin.go:399-428` | `MemoryMiddleware` | 检查 `mem == nil`，不检查字符串 | ✅ nil 检查正确 |
| 5 | `agent/middleware_builtin.go:503-508` | `buildSystemGuideText()` | `if "letta"` → letta guide; `else` → flat guide | ⚠️ `none` 走 flat guide |
| 6 | `session/multitenant.go:329-342` | `GetOrCreateSession()` | `switch` 创建对应 provider 实例 | ✅ 隔离正确 |
| 7 | `agent/engine_wire.go:1176-1245` | `buildSubAgentMemory()` | **硬编码 letta**，不检查主 agent 的 provider | ⚠️ flat 模式下 SubAgent 仍创建 letta |
| 8 | `agent/engine_wire.go:811` | `subagentMemorySection` | SubAgent 有 Memory 能力时**无条件注入**记忆指南 | ⚠️ 不区分 provider |

**关键发现**：
- `none` 模式存在不一致：prompt 注入了 flat 的工具说明，但实际没有注册任何记忆工具
- SubAgent 记忆硬编码为 letta，不尊重主 agent 的 provider 选择
- 所有分支用字符串比较，没有 enum/常量，容易遗漏

**新方案必须确保**：`xbot` provider 的所有工具和 prompt **仅在 `memory_provider: "xbot"` 时注入**，不影响 flat/letta/none 的行为。

## 二、论文调研总结

### 2.1 关键论文与方案

| 论文/系统 | 核心思想 | 对我们的启发 |
|-----------|---------|-------------|
| **A-MEM** (NeurIPS 2025) | Zettelkasten 方法，原子笔记 + 动态链接 + 自演化 | 记忆应为原子化笔记，通过链接形成知识网络 |
| **Mem0** (ECAI 2025) | LLM 提取原子事实 → ADD/UPDATE/DELETE 决策 → 紧凑检索 | 写入路径用 LLM，读取路径轻量（无 LLM） |
| **MemoryOS** (EMNLP 2025) | STM/MTM/LTM 三层 + 热度衰减 + 分层检索 | 分层 + 热度机制，近期记忆优先 |
| **MIRIX** (2025) | Core/Episodic/Semantic/Procedural 多类型记忆 | 不同类型记忆需要不同管理策略 |
| **MemoryBank** (AAAI 2024) | Ebbinghaus 遗忘曲线，记忆随时间衰减 | 遗忘机制是长期记忆的关键 |
| **Memweave** (2025) | Markdown 文件 + SQLite FTS5，零基础设施 | 文件为源、SQLite FTS5 为索引层 |
| **OpenClaw** (2026) | SQLite FTS5 + BM25 本地搜索，无向量依赖 | BM25 足以支撑 agent 记忆检索 |
| **Hermes Agent** (2025) | FTS5 + LLM 摘要 = 跨会话持久记忆 | FTS5 + 摘要是可行方案 |

### 2.2 核心设计原则（从论文中提炼）

1. **写入路径用 LLM，读取路径轻量**（Mem0 原则）
   - 写入时用 LLM 提取/合并/去重
   - 读取时用 BM25/FTS5 关键词检索，无需 LLM 调用

2. **原子化记忆条目**（A-MEM 原则）
   - 每条记忆是一个独立的原子事实/事件，不是大段文本
   - 通过标签/链接关联，形成知识网络

3. **分层 + 热度衰减**（MemoryOS 原则）
   - 工作记忆（当前会话）→ 短期记忆（近期会话）→ 长期记忆（归档）
   - 热度衰减：访问频率 + 时间新近度 → 优先级

4. **遗忘机制**（MemoryBank 原则）
   - 记忆随时间衰减，低重要性记忆被遗忘
   - 防止记忆库无限膨胀

5. **文件为源，索引为派生**（Memweave 原则）
   - 记忆以 Markdown 文件存储（可读、可编辑、可 git diff）
   - SQLite FTS5 作为派生索引，删除后可从文件重建

6. **无外部 embedding 依赖**（我们的约束）
   - 用 SQLite FTS5 BM25 替代向量搜索
   - 用 LLM 关键词提取替代 embedding 语义匹配
   - 可选：未来支持本地 embedding 模型（如 BGE-small）作为增强

## 三、新记忆系统设计：`xbot` Provider

### 3.1 设计原则

1. **新增不替换**：`xbot` 是第四种 provider，与 `flat`/`letta`/`none` 并存
2. **严格隔离**：所有工具和 prompt 片段**仅在 `memory_provider: "xbot"` 时注入**
3. **零外部依赖**：不引入 chromem-go 或任何 embedding 模型
4. **复用现有 SQLite**：使用 xbot 已有的 SQLite 数据库连接，不新增数据库进程

### 3.2 架构总览

```
┌─────────────────────────────────────────────────────────────────┐
│                     xbot Provider 架构                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────────┐    ┌──────────────┐    ┌───────────────────┐  │
│  │ Working Mem │───▶│ Short-term   │───▶│  Long-term Mem    │  │
│  │ (会话内)     │    │ Mem (近期)    │    │  (归档)           │  │
│  │             │    │              │    │                   │  │
│  │ 当前对话上下文│    │ 最近 N 个会话 │    │ 原子化记忆条目    │  │
│  │ 不持久化     │    │ SQLite 表     │    │ SQLite FTS5 索引  │  │
│  │             │    │ 热度排序      │    │ Markdown 文件     │  │
│  └─────────────┘    └──────────────┘    └───────────────────┘  │
│         │                    │                      │            │
│         ▼                    ▼                      ▼            │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │              Recall (读取路径 — 轻量, 零 LLM)             │    │
│  │                                                          │    │
│  │  1. 核心摘要 (MEMORY.md, ≤2000 chars)                   │    │
│  │  2. 短期记忆 Top-K (BM25 + 热度)                         │    │
│  │  3. 长期记忆 Top-K (BM25 关键词检索)                     │    │
│  │  4. 合并 + 去重 + 注入 system prompt                     │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │             Memorize (写入路径 — LLM 驱动)                │    │
│  │                                                          │    │
│  │  1. 对话结束时自动触发（不再仅 /new）                     │    │
│  │  2. LLM 提取原子事实 + 事件摘要                           │    │
│  │  3. 与现有记忆去重/合并/更新（LLM 决策 ADD/UPDATE/DELETE）│    │
│  │  4. 写入 SQLite + Markdown 文件                           │    │
│  │  5. 更新 FTS5 索引                                        │    │
│  │  6. 热度衰减计算                                          │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  存储: ~/.xbot/memory/{tenantID}/                                │
│    ├── memory.db        (SQLite: 记忆表 + FTS5 索引)             │
│    ├── notes/            (Markdown 文件，每条记忆一个 .md)        │
│    │   ├── fact_001.md                                           │
│  │   ├── fact_002.md                                           │
│  │   └── event_001.md                                          │
│  └── MEMORY.md          (核心摘要，≤2000 chars，注入 prompt)     │
└─────────────────────────────────────────────────────────────────┘
```

### 3.3 三层记忆模型

#### Layer 1: Working Memory（工作记忆）

- **范围**: 当前会话的对话上下文
- **存储**: 内存中，不持久化（已有 session_messages 表）
- **作用**: agent loop 中的即时上下文
- **无需改动**: 现有的 session_messages + context management 已覆盖

#### Layer 2: Short-term Memory（短期记忆）

- **范围**: 最近 N 个会话（默认 N=5）
- **存储**: SQLite 表 `short_term_memories`
- **内容**: 每个会话结束时自动生成的摘要（LLM 驱动）
- **热度**: 基于时间新近度 + 访问频率
- **生命周期**: 超过 N 个会话后自动降级为长期记忆
- **检索**: BM25 关键词检索 + 热度排序

```sql
CREATE TABLE short_term_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    session_id TEXT NOT NULL,          -- 会话 ID
    summary TEXT NOT NULL,             -- LLM 生成的会话摘要
    key_topics TEXT,                   -- 逗号分隔的关键主题
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    access_count INTEGER DEFAULT 0,
    heat_score REAL DEFAULT 1.0        -- 热度分数
);

CREATE INDEX idx_stm_tenant ON short_term_memories(tenant_id);
CREATE INDEX idx_stm_session ON short_term_memories(session_id);
```

#### Layer 3: Long-term Memory（长期记忆）

- **范围**: 所有归档的原子化记忆
- **存储**: SQLite 表 `long_term_memories` + FTS5 虚拟表 + Markdown 文件
- **内容**: 原子化的事实、偏好、事件、决策
- **类型**: fact / preference / event / decision / skill
- **检索**: SQLite FTS5 BM25 关键词检索
- **生命周期**: 热度衰减 + 遗忘机制

```sql
-- 长期记忆主表
CREATE TABLE long_term_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    type TEXT NOT NULL,                -- fact/preference/event/decision/skill
    content TEXT NOT NULL,             -- 原子化记忆内容
    keywords TEXT,                     -- 逗号分隔的关键词（LLM 提取）
    tags TEXT,                         -- 逗号分隔的标签
    source_session TEXT,              -- 来源会话 ID
    importance REAL DEFAULT 0.5,       -- 重要性 0.0-1.0
    heat_score REAL DEFAULT 1.0,       -- 热度分数
    access_count INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    file_path TEXT                     -- 对应 Markdown 文件路径
);

-- FTS5 全文索引（BM25 检索）
CREATE VIRTUAL TABLE long_term_memories_fts USING fts5(
    content,
    keywords,
    tags,
    content='long_term_memories',
    content_rowid='id',
    tokenize='unicode61'
);

-- FTS5 同步触发器
CREATE TRIGGER ltm_ai AFTER INSERT ON long_term_memories BEGIN
    INSERT INTO long_term_memories_fts(rowid, content, keywords, tags)
    VALUES (new.id, new.content, new.keywords, new.tags);
END;
CREATE TRIGGER ltm_ad AFTER DELETE ON long_term_memories BEGIN
    INSERT INTO long_term_memories_fts(long_term_memories_fts, rowid, content, keywords, tags)
    VALUES('delete', old.id, old.content, old.keywords, old.tags);
END;
CREATE TRIGGER ltm_au AFTER UPDATE ON long_term_memories BEGIN
    INSERT INTO long_term_memories_fts(long_term_memories_fts, rowid, content, keywords, tags)
    VALUES('delete', old.id, old.content, old.keywords, old.tags);
    INSERT INTO long_term_memories_fts(rowid, content, keywords, tags)
    VALUES (new.id, new.content, new.keywords, new.tags);
END;
```

### 3.4 核心接口设计

```go
// memory/xbot/xbot.go

package xbot

// XbotMemory 是新的记忆系统实现（第四种 provider）
type XbotMemory struct {
    tenantID  int64
    baseDir   string                    // ~/.xbot/memory/{tenantID}/
    db        *sql.DB                   // SQLite 连接（复用现有 xbot DB）
    llmClient llm.LLM                   // 用于 Memorize 时的 LLM 调用
    model     string                    // LLM 模型名
    mu        sync.RWMutex
}

// === MemoryProvider 接口实现（与 flat/letta 相同接口） ===

// Recall 读取路径：BM25 检索 + 热度排序，零 LLM 调用
func (m *XbotMemory) Recall(ctx context.Context, query string) (string, error)

// Memorize 写入路径：LLM 提取原子记忆 + 去重/合并
func (m *XbotMemory) Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)

// Close 清理资源
func (m *XbotMemory) Close() error

// === 新增接口（xbot provider 独有） ===

// SearchMemories 跨会话搜索记忆（供 memory_search 工具调用）
func (m *XbotMemory) SearchMemories(ctx context.Context, query string, opts SearchOptions) ([]MemoryEntry, error)

// AddMemory 手动添加记忆（供 memory_add 工具调用）
func (m *XbotMemory) AddMemory(ctx context.Context, entry MemoryEntry) (int64, error)

// UpdateMemory 更新记忆（供 memory_manage 工具调用）
func (m *XbotMemory) UpdateMemory(ctx context.Context, id int64, entry MemoryEntry) error

// DeleteMemory 删除记忆（供 memory_manage 工具调用）
func (m *XbotMemory) DeleteMemory(ctx context.Context, id int64) error

// ListMemories 列出记忆（供 memory_manage 工具调用）
func (m *XbotMemory) ListMemories(ctx context.Context, opts ListOptions) ([]MemoryEntry, error)

// DecayMemories 执行热度衰减（定期调用）
func (m *XbotMemory) DecayMemories(ctx context.Context) error

// ConsolidateSession 合并会话记忆（会话结束时自动调用）
func (m *XbotMemory) ConsolidateSession(ctx context.Context, sessionID string, messages []llm.ChatMessage) error
```

### 3.5 Recall 读取路径（轻量，无 LLM 调用）

```go
func (m *XbotMemory) Recall(ctx context.Context, query string) (string, error) {
    var sb strings.Builder
    sb.WriteString("# Memory\n\n")
    
    // 1. 核心摘要（MEMORY.md，≤2000 chars）
    coreSummary := m.readCoreSummary()
    if coreSummary != "" {
        sb.WriteString("## Core\n")
        sb.WriteString(coreSummary)
        sb.WriteString("\n\n")
    }
    
    // 2. 短期记忆 Top-K（BM25 + 热度）
    shortTermMems := m.searchShortTerm(query, 3)
    if len(shortTermMems) > 0 {
        sb.WriteString("## Recent Sessions\n")
        for _, mem := range shortTermMems {
            sb.WriteString(fmt.Sprintf("- %s\n", mem.Summary))
        }
        sb.WriteString("\n")
    }
    
    // 3. 长期记忆 Top-K（BM25 关键词检索）
    longTermMems := m.searchLongTerm(query, 5)
    if len(longTermMems) > 0 {
        sb.WriteString("## Long-term Memories\n")
        for _, mem := range longTermMems {
            sb.WriteString(fmt.Sprintf("- [%s] %s\n", mem.Type, mem.Content))
        }
        sb.WriteString("\n")
    }
    
    // 4. 提示 agent 可用工具
    sb.WriteString("Use `memory_search` to find more memories, `memory_add` to save new ones.\n")
    
    return sb.String(), nil
}

// BM25 检索（SQLite FTS5）
func (m *XbotMemory) searchLongTerm(query string, topK int) []LongTermMemory {
    // FTS5 BM25 检索
    rows, err := m.db.Query(`
        SELECT ltm.id, ltm.type, ltm.content, ltm.keywords, ltm.tags,
               ltm.importance, ltm.heat_score, ltm.access_count,
               bm25(long_term_memories_fts) as score
        FROM long_term_memories_fts fts
        JOIN long_term_memories ltm ON ltm.id = fts.rowid
        WHERE ltm.tenant_id = ? AND long_term_memories_fts MATCH ?
        ORDER BY score ASC  -- BM25 score 越小越相关（SQLite FTS5 特性）
        LIMIT ?
    `, m.tenantID, query, topK)
    
    // 更新访问计数和热度
    for _, mem := range results {
        m.touchMemory(mem.ID)
    }
    
    return results
}
```

**关键设计点**：
- **零 LLM 调用**：读取路径完全用 SQLite FTS5 BM25，无外部依赖
- **相关性过滤**：根据当前用户消息（query）检索相关记忆，而非全量注入
- **热度排序**：BM25 分数 × 热度分数 = 最终排序权重
- **访问追踪**：每次被检索到的记忆，access_count +1，last_accessed_at 更新

### 3.6 Memorize 写入路径（LLM 驱动）

#### 3.6.1 自动触发（不再仅 `/new`）

```go
// 会话结束时自动调用（在 handleNewSession 或会话超时时）
func (m *XbotMemory) Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error) {
    // 1. 生成会话摘要 → 短期记忆
    sessionSummary := m.generateSessionSummary(ctx, input.Messages)
    m.addShortTermMemory(sessionSummary)
    
    // 2. 提取原子记忆 → 长期记忆
    atomicMemories := m.extractAtomicMemories(ctx, input.Messages)
    for _, mem := range atomicMemories {
        m.addLongTermMemory(mem)
    }
    
    // 3. 更新核心摘要（MEMORY.md）
    m.updateCoreSummary(ctx)
    
    // 4. 热度衰减
    m.decayMemories()
    
    // 5. 短期记忆降级（超过 N 个会话的降为长期）
    m.evictShortTerm()
    
    return MemorizeResult{OK: true}, nil
}
```

#### 3.6.2 LLM 提取原子记忆

```go
func (m *XbotMemory) extractAtomicMemories(ctx context.Context, messages []llm.ChatMessage) []MemoryEntry {
    // 构造 prompt，让 LLM 从对话中提取原子化记忆
    prompt := `Analyze the following conversation and extract atomic memories.
    
For each memory, provide:
- type: one of "fact", "preference", "event", "decision", "skill"
- content: a single, self-contained fact or event (1-2 sentences)
- keywords: 3-5 comma-separated keywords for search
- tags: 1-3 comma-separated category tags
- importance: 0.0-1.0 (how important is this for future interactions?)

Only extract memories that are:
1. Likely to be useful in future conversations
2. Not already obvious from the system prompt or project context
3. Specific enough to be actionable (not vague observations)

Use the extract_memories tool to return results.`
    
    resp, err := m.llmClient.Generate(ctx, m.model, 
        []llm.ChatMessage{
            llm.NewSystemMessage(prompt),
            llm.NewUserMessage(formatMessages(messages)),
        }, extractMemoriesTool, "")
    
    // 解析 LLM 返回的记忆条目
    return parseExtractedMemories(resp)
}

// extract_memories 工具定义（内部工具，仅 Memorize 时使用，不注册到 agent 工具集）
type extractMemoriesToolDef struct{}
func (t *extractMemoriesToolDef) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "memories", Type: "array", Required: true, Description: "Extracted atomic memories",
            Items: &llm.ToolParam{
                Properties: map[string]llm.ToolParam{
                    "type":       {Type: "string", Required: true, Enum: []string{"fact", "preference", "event", "decision", "skill"}},
                    "content":    {Type: "string", Required: true, Description: "Single self-contained fact or event"},
                    "keywords":   {Type: "string", Required: true, Description: "3-5 comma-separated keywords"},
                    "tags":       {Type: "string", Description: "1-3 comma-separated category tags"},
                    "importance": {Type: "number", Description: "0.0-1.0 importance score"},
                },
            }},
    }
}
```

#### 3.6.3 去重/合并/更新（Mem0 风格的 ADD/UPDATE/DELETE）

```go
func (m *XbotMemory) addLongTermMemory(entry MemoryEntry) error {
    // 1. 用 FTS5 检索相似记忆
    similar := m.searchLongTerm(entry.Keywords, 5)
    
    // 2. 如果有高相似度记忆，用 LLM 决策 ADD/UPDATE/DELETE
    if len(similar) > 0 {
        decision := m.llmDeduplicate(entry, similar)
        switch decision.Action {
        case "ADD":
            m.insertLongTermMemory(entry)
        case "UPDATE":
            m.updateLongTermMemory(decision.TargetID, entry)
        case "DELETE":
            m.deleteLongTermMemory(decision.TargetID)
            m.insertLongTermMemory(entry)
        case "NOOP":
            // 跳过
        }
    } else {
        // 3. 无相似记忆，直接插入
        m.insertLongTermMemory(entry)
    }
    return nil
}
```

### 3.7 热度衰减与遗忘机制

```go
// 热度计算公式（受 MemoryBank Ebbinghaus 曲线启发）
// heat_score = importance × recency_factor × frequency_factor
//
// recency_factor = exp(-Δt / half_life)    // Δt = 距上次访问的时间
// frequency_factor = log(1 + access_count)  // 对数增长，防止高频记忆无限膨胀
// half_life = 30 days (可配置)

func (m *XbotMemory) decayMemories() error {
    _, err := m.db.Exec(`
        UPDATE long_term_memories
        SET heat_score = importance * 
            exp(-julianday('now') - julianday(last_accessed_at)) / 30.0 *
            log(1 + access_count)
        WHERE tenant_id = ?
    `, m.tenantID)
    return err
}

// 遗忘机制：热度低于阈值的记忆标记为"遗忘"
// 但不立即删除，而是降低检索优先级
func (m *XbotMemory) forgetMemories() error {
    _, err := m.db.Exec(`
        UPDATE long_term_memories
        SET importance = importance * 0.95  -- 缓慢衰减
        WHERE heat_score < 0.1
        AND tenant_id = ?
    `, m.tenantID)
    return err
}
```

### 3.8 工具设计（精简为 3 个）

替换现有的 8 个记忆工具（flat 2 个 + letta 6 个），精简为 3 个统一工具：

#### `memory_search` — 搜索记忆

```go
type MemorySearchTool struct{}
// 参数:
// - query: 搜索关键词（必填）
// - type: 记忆类型过滤（可选: fact/preference/event/decision/skill/all）
// - limit: 返回数量（默认 10，最大 50）
// 返回: 匹配的记忆列表，含相关性分数、类型、时间戳
```

#### `memory_add` — 添加记忆

```go
type MemoryAddTool struct{}
// 参数:
// - content: 记忆内容（必填，1-2 句话）
// - type: 记忆类型（必填: fact/preference/event/decision/skill）
// - keywords: 搜索关键词（可选，LLM 自动提取如果留空）
// - tags: 分类标签（可选）
// - importance: 重要性 0.0-1.0（可选，默认 0.5）
// 返回: 记忆 ID
```

#### `memory_manage` — 管理记忆

```go
type MemoryManageTool struct{}
// 参数:
// - action: list/delete/update/pin/unpin
// - id: 记忆 ID（delete/update/pin/unpin 时必填）
// - query: 搜索关键词（list 时可选，用于过滤）
// - content: 新内容（update 时必填）
// 返回: 操作结果
```

### 3.9 自动记忆触发机制

**核心改进**：不再仅依赖 `/new` 命令触发记忆合并。

```go
// 在 agent loop 中添加自动触发点

// 1. 会话结束自动触发（handleNewSession 或会话超时）
func (a *Agent) handleNewSession(...) {
    // ... 现有逻辑 ...
    mem := tenantSession.Memory()
    if mem != nil {
        // 自动合并，不需要用户手动 /new
        mem.Memorize(ctx, memory.MemorizeInput{
            Messages:         messages,
            LastConsolidated: lastConsolidated,
            LLMClient:        userCtx.LLMClient,
            Model:            userCtx.Model,
            ArchiveAll:       true,
            SessionID:        chatID,  // 新增：会话 ID
        })
    }
}

// 2. 对话轮次阈值触发（每 N 轮自动提取记忆）
const autoMemorizeThreshold = 20  // 每 20 轮对话自动提取一次
if turnCount % autoMemorizeThreshold == 0 {
    go mem.ConsolidateSession(ctx, chatID, recentMessages)
}

// 3. 重要性触发（检测到关键信息时自动保存）
// 通过 hook 在 PostToolUse 检测关键信息
```

### 3.10 核心摘要（MEMORY.md）自动维护

```go
// MEMORY.md 是注入 system prompt 的核心摘要
// 不再由 LLM 每次全量重写，而是增量维护

func (m *XbotMemory) updateCoreSummary(ctx context.Context) error {
    // 1. 读取当前 MEMORY.md
    currentSummary := m.readCoreSummary()
    
    // 2. 读取最近的高重要性记忆（Top 10）
    topMems := m.queryLongTerm("SELECT * FROM long_term_memories WHERE importance > 0.7 ORDER BY heat_score DESC LIMIT 10")
    
    // 3. LLM 生成增量更新（不是全量重写）
    prompt := fmt.Sprintf(`Current core summary:
%s

Recent high-importance memories:
%s

Update the core summary to incorporate any new critical information.
Keep it under 2000 characters. Preserve existing important information.
Use the update_summary tool to return the updated summary.`, 
        currentSummary, formatMemories(topMems))
    
    resp, err := m.llmClient.Generate(ctx, m.model, ...)
    
    // 4. 原子写入 MEMORY.md
    m.writeCoreSummary(newSummary)
    
    return nil
}
```

### 3.11 跨会话检索

```go
// SearchMemories 跨所有会话搜索记忆
func (m *XbotMemory) SearchMemories(ctx context.Context, query string, opts SearchOptions) ([]MemoryEntry, error) {
    // 1. 搜索长期记忆（FTS5 BM25）
    longTermResults := m.searchLongTerm(query, opts.Limit)
    
    // 2. 搜索短期记忆（FTS5 BM25）
    shortTermResults := m.searchShortTerm(query, opts.Limit)
    
    // 3. 合并 + 去重 + 排序
    allResults := mergeAndDedup(longTermResults, shortTermResults)
    
    // 4. 按相关性 × 热度排序
    sort.Slice(allResults, func(i, j int) bool {
        return allResults[i].Score * allResults[i].HeatScore > 
               allResults[j].Score * allResults[j].HeatScore
    })
    
    // 5. 截断到 Limit
    if len(allResults) > opts.Limit {
        allResults = allResults[:opts.Limit]
    }
    
    return allResults, nil
}
```

### 3.12 配置

```json
{
  "agent": {
    "memory_provider": "xbot",
    "memory": {
      "auto_memorize": true,
      "auto_memorize_threshold": 20,
      "short_term_capacity": 5,
      "long_term_max_entries": 10000,
      "heat_half_life_days": 30,
      "forget_threshold": 0.1,
      "core_summary_max_chars": 2000,
      "recall_top_k": 5,
      "llm_consolidate_model": ""
    }
  }
}
```

## 四、与现有架构的集成（严格隔离）

### 4.1 注入点修改清单

以下是需要修改的所有注入点，**每个都必须新增 `case "xbot"` 分支**，确保 xbot provider 的工具和 prompt 仅在 `memory_provider: "xbot"` 时注入：

#### 注入点 1: `agent/agent.go` — `initServices()` 工具注册

```go
// agent/agent.go:1624-1638 — 修改后

memoryProvider := resolveMemoryProvider(cfg.MemoryProvider)

if memoryProvider == "letta" {
    for _, tool := range tools.LettaMemoryTools() {
        registry.RegisterCore(tool)
    }
    registry.RegisterCore(&tools.SearchToolsTool{})
}
if memoryProvider == "flat" {
    for _, tool := range tools.FlatMemoryTools() {
        registry.RegisterCore(tool)
    }
}
// ★ 新增：xbot provider 的工具注册
if memoryProvider == "xbot" {
    for _, tool := range tools.XbotMemoryTools() {
        registry.RegisterCore(tool)
    }
}
// none 模式：不注册任何记忆工具（现有行为不变）
```

**隔离保证**：`xbot` 模式只注册 `XbotMemoryTools()` 返回的 3 个工具（`memory_search`、`memory_add`、`memory_manage`），不注册 flat/letta 的任何工具。

#### 注入点 2: `agent/context.go` — `enrichPromptData()` prompt 片段

```go
// agent/context.go:150-159 — 修改后

func enrichPromptData(data PromptData) PromptData {
    data.Identity = prompt.Identity
    data.Behavior = prompt.Behavior
    data.Environment = prompt.Environment
    data.CodeRules = prompt.CodeRules

    switch data.MemoryProvider {
    case "letta":
        data.Tools = prompt.ToolsLetta
        data.Memory = prompt.MemoryLetta
    case "xbot":
        // ★ 新增：xbot provider 的 prompt 片段
        data.Tools = prompt.ToolsXbot       // modes/tools_xbot.md
        data.Memory = prompt.MemoryXbot     // modes/memory_xbot.md
    default:  // flat, none, 未设置
        data.Tools = prompt.ToolsFlat
        data.Memory = ""
    }
    return data
}
```

**隔离保证**：`xbot` 模式注入专属的 `ToolsXbot` 和 `MemoryXbot` prompt，不混入 flat/letta 的内容。

#### 注入点 3: `agent/middleware_builtin.go` — `buildSystemGuideText()` 用户引导

```go
// agent/middleware_builtin.go:503-508 — 修改后

func buildSystemGuideText(memoryProvider string) string {
    switch memoryProvider {
    case "letta":
        return prompt.UserMessageGuideLetta
    case "xbot":
        // ★ 新增：xbot provider 的用户引导
        return prompt.UserMessageGuideXbot   // guides/user_message_xbot.md
    default:
        return prompt.UserMessageGuideFlat
    }
}
```

**隔离保证**：`xbot` 模式注入专属的用户引导文本，不使用 flat/letta 的引导。

#### 注入点 4: `session/multitenant.go` — `GetOrCreateSession()` provider 创建

```go
// session/multitenant.go:329-342 — 修改后

var memProvider memory.MemoryProvider
switch m.memoryProvider {
case "letta":
    memProvider = letta.New(tenantID, m.coreSvc, m.archivalSvc, m.memorySvc, m.toolIndexSvc)
    m.migrateProfileToCoreMemory(tenantID)
case "xbot":
    // ★ 新增：xbot provider 创建
    memDB := m.getDB()  // 复用现有 SQLite 连接
    memDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", tenantID))
    memProvider = xbotmemory.New(tenantID, memDir, memDB)
case "none":
    memProvider = nil
default:
    flatMemDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", tenantID))
    memProvider = flat.New(tenantID, flatMemDir)
}
```

**隔离保证**：`xbot` 模式创建独立的 `XbotMemory` 实例，不影响 flat/letta 的创建逻辑。

#### 注入点 5: `agent/middleware_builtin.go` — `MemoryMiddleware` (无需修改)

```go
// agent/middleware_builtin.go:399-428 — 无需修改
// MemoryMiddleware 通过 mc.Extra[ExtraKeyMemoryProvider] 获取接口实例
// XbotMemory 实现了 MemoryProvider 接口，自动兼容
// none 模式: mem == nil → 短路跳过（现有行为不变）
```

**隔离保证**：`MemoryMiddleware` 不检查字符串，只检查接口实例是否为 nil。`xbot` 模式下 `XbotMemory.Recall()` 返回的内容自动注入 `SystemParts["20_memory"]`，flat/letta/none 不受影响。

#### 注入点 6: `prompt/embed.go` — 新增嵌入文件

```go
// prompt/embed.go — 新增

//go:embed modes/tools_xbot.md
var ToolsXbot string

//go:embed modes/memory_xbot.md
var MemoryXbot string

//go:embed guides/user_message_xbot.md
var UserMessageGuideXbot string
```

**需要创建的 prompt 文件**：

- `prompt/modes/tools_xbot.md` — xbot 模式工具说明（描述 memory_search/memory_add/memory_manage）
- `prompt/modes/memory_xbot.md` — xbot 模式记忆系统使用指南
- `prompt/guides/user_message_xbot.md` — xbot 模式用户消息引导

#### 注入点 7: `agent/engine_wire.go` — SubAgent 记忆（需修改）

```go
// agent/engine_wire.go:1176-1245 — 修改后

func (a *Agent) buildSubAgentMemory(ctx, ...) memory.MemoryProvider {
    // ★ 修改：根据主 agent 的 memoryProvider 决定 SubAgent 的记忆系统
    switch a.memoryProvider {
    case "xbot":
        // xbot 模式：SubAgent 使用 xbot 记忆
        subTenantID := deriveSubAgentTenantID(...)
        memDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", subTenantID))
        return xbotmemory.New(subTenantID, memDir, a.db)
    case "letta":
        // 现有行为：SubAgent 使用 letta 记忆
        subTenantID := deriveSubAgentTenantID(...)
        return letta.New(subTenantID, ...)
    case "none":
        return nil
    default:
        // flat 模式：SubAgent 不创建独立记忆（现有行为）
        return nil
    }
}
```

**隔离保证**：SubAgent 的记忆系统跟随主 agent 的 provider 选择，不再硬编码 letta。

#### 注入点 8: `agent/engine_wire.go` — `subagentMemorySection` (需修改)

```go
// agent/engine_wire.go:811 — 修改后

// SubAgent 记忆指南注入需要根据 provider 类型选择不同内容
func subagentMemorySection(provider string) string {
    switch provider {
    case "xbot":
        return prompt.SubagentMemoryXbot   // 新增：xbot 模式的 SubAgent 记忆指南
    case "letta":
        return subagentMemorySection  // 现有 letta 指南
    default:
        return ""  // flat/none 模式不注入
    }
}
```

**隔离保证**：SubAgent 记忆指南不再无条件注入，而是根据 provider 类型选择。

### 4.2 新增 Provider 注册

```go
// memory/xbot/xbot.go

package xbot

func New(tenantID int64, baseDir string, db *sql.DB) *XbotMemory {
    m := &XbotMemory{
        tenantID: tenantID,
        baseDir:  baseDir,
        db:       db,
    }
    m.initSchema()
    return m
}

func (m *XbotMemory) initSchema() {
    // 执行建表语句（IF NOT EXISTS，安全幂等）
    m.db.Exec(schemaSQL)
}
```

### 4.3 工具注册

```go
// tools/xbot_memory_tools.go

func XbotMemoryTools() []Tool {
    return []Tool{
        &MemorySearchTool{},
        &MemoryAddTool{},
        &MemoryManageTool{},
    }
}
```

### 4.4 System Prompt 注入

```go
// agent/middleware_builtin.go — MemoryMiddleware 无需改动
// Recall() 返回的格式已兼容现有注入路径
// SystemParts["20_memory"] 注入点不变

// prompt/modes/memory_xbot.md — 新增 prompt 片段
// 描述 xbot 记忆系统的使用说明
```

### 4.5 自动记忆触发

```go
// agent/prompt_handler.go — handleNewSession

func (a *Agent) handleNewSession(...) {
    // ... 现有逻辑 ...
    mem := tenantSession.Memory()
    if mem != nil && len(snapshot) > 0 {
        // 自动合并（不再需要 ArchiveAll=true 才执行）
        result, _ := mem.Memorize(ctx, memory.MemorizeInput{
            Messages:         snapshot,
            LastConsolidated: 0,
            LLMClient:        userCtx.LLMClient,
            Model:            userCtx.Model,
            ArchiveAll:       true,
            SessionID:        chatID,
        })
    }
}
```

### 4.6 数据库迁移

```sql
-- 新增表（在现有 SQLite 数据库中创建）
-- 不影响现有表，与 flat/letta 并存
-- 所有表名使用 xbot_ 前缀避免冲突

CREATE TABLE IF NOT EXISTS xbot_short_term_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    session_id TEXT NOT NULL,
    summary TEXT NOT NULL,
    key_topics TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    access_count INTEGER DEFAULT 0,
    heat_score REAL DEFAULT 1.0
);

CREATE INDEX IF NOT EXISTS idx_xbot_stm_tenant ON xbot_short_term_memories(tenant_id);
CREATE INDEX IF NOT EXISTS idx_xbot_stm_session ON xbot_short_term_memories(session_id);

CREATE VIRTUAL TABLE IF NOT EXISTS xbot_short_term_memories_fts USING fts5(
    summary, key_topics,
    content='xbot_short_term_memories', content_rowid='id',
    tokenize='unicode61'
);

-- FTS5 同步触发器（short_term_memories）
CREATE TRIGGER IF NOT EXISTS xbot_stm_ai AFTER INSERT ON xbot_short_term_memories BEGIN
    INSERT INTO xbot_short_term_memories_fts(rowid, summary, key_topics)
    VALUES (new.id, new.summary, new.key_topics);
END;
CREATE TRIGGER IF NOT EXISTS xbot_stm_ad AFTER DELETE ON xbot_short_term_memories BEGIN
    INSERT INTO xbot_short_term_memories_fts(xbot_short_term_memories_fts, rowid, summary, key_topics)
    VALUES('delete', old.id, old.summary, old.key_topics);
END;
CREATE TRIGGER IF NOT EXISTS xbot_stm_au AFTER UPDATE ON xbot_short_term_memories BEGIN
    INSERT INTO xbot_short_term_memories_fts(xbot_short_term_memories_fts, rowid, summary, key_topics)
    VALUES('delete', old.id, old.summary, old.key_topics);
    INSERT INTO xbot_short_term_memories_fts(rowid, summary, key_topics)
    VALUES (new.id, new.summary, new.key_topics);
END;

CREATE TABLE IF NOT EXISTS xbot_long_term_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    type TEXT NOT NULL,
    content TEXT NOT NULL,
    keywords TEXT,
    tags TEXT,
    source_session TEXT,
    importance REAL DEFAULT 0.5,
    heat_score REAL DEFAULT 1.0,
    access_count INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    file_path TEXT
);

CREATE INDEX IF NOT EXISTS idx_xbot_ltm_tenant ON xbot_long_term_memories(tenant_id);
CREATE INDEX IF NOT EXISTS idx_xbot_ltm_type ON xbot_long_term_memories(type);
CREATE INDEX IF NOT EXISTS idx_xbot_ltm_heat ON xbot_long_term_memories(heat_score);

CREATE VIRTUAL TABLE IF NOT EXISTS xbot_long_term_memories_fts USING fts5(
    content, keywords, tags,
    content='xbot_long_term_memories', content_rowid='id',
    tokenize='unicode61'
);

-- FTS5 同步触发器（long_term_memories）
CREATE TRIGGER IF NOT EXISTS xbot_ltm_ai AFTER INSERT ON xbot_long_term_memories BEGIN
    INSERT INTO xbot_long_term_memories_fts(rowid, content, keywords, tags)
    VALUES (new.id, new.content, new.keywords, new.tags);
END;
CREATE TRIGGER IF NOT EXISTS xbot_ltm_ad AFTER DELETE ON xbot_long_term_memories BEGIN
    INSERT INTO xbot_long_term_memories_fts(xbot_long_term_memories_fts, rowid, content, keywords, tags)
    VALUES('delete', old.id, old.content, old.keywords, old.tags);
END;
CREATE TRIGGER IF NOT EXISTS xbot_ltm_au AFTER UPDATE ON xbot_long_term_memories BEGIN
    INSERT INTO xbot_long_term_memories_fts(xbot_long_term_memories_fts, rowid, content, keywords, tags)
    VALUES('delete', old.id, old.content, old.keywords, old.tags);
    INSERT INTO xbot_long_term_memories_fts(rowid, content, keywords, tags)
    VALUES (new.id, new.content, new.keywords, new.tags);
END;
```

**注意**：所有表名使用 `xbot_` 前缀，避免与 letta 模式的 `core_memory_blocks`、`event_history` 等表冲突。两套表可以安全共存于同一数据库。

### 4.7 隔离验证矩阵

| 注入点 | flat | letta | xbot | none | 隔离方式 |
|--------|------|-------|------|------|---------|
| 工具注册 | memory_write, memory_list | 6 个 letta 工具 + search_tools | **memory_search, memory_add, memory_manage** | 无 | `if memoryProvider == "xbot"` |
| System prompt Tools | ToolsFlat | ToolsLetta | **ToolsXbot** | ToolsFlat | `switch data.MemoryProvider` |
| System prompt Memory | 空 | MemoryLetta | **MemoryXbot** | 空 | `switch data.MemoryProvider` |
| 用户引导 | UserMessageGuideFlat | UserMessageGuideLetta | **UserMessageGuideXbot** | UserMessageGuideFlat | `switch memoryProvider` |
| MemoryMiddleware | flat.Recall() | letta.Recall() | **xbot.Recall()** | 跳过 (nil) | 接口实例 nil 检查 |
| Provider 创建 | flat.New() | letta.New() | **xbot.New()** | nil | `switch m.memoryProvider` |
| SubAgent 记忆 | nil (不创建) | letta.New() | **xbot.New()** | nil | `switch a.memoryProvider` |
| SubAgent prompt | 不注入 | subagentMemorySection | **SubagentMemoryXbot** | 不注入 | `switch provider` |
| DB 表 | MEMORY.md + HISTORY.md | core_memory_blocks + event_history + archival | **xbot_short_term_memories + xbot_long_term_memories** | 无 | 表名前缀隔离 |

### 4.8 迁移策略

- `memory_provider: "xbot"` 为新可选值，默认仍为 `"flat"`
- 保留 `flat` 和 `letta` 作为兼容选项，**不修改其任何代码**
- 首次使用 `xbot` provider 时自动建表（`IF NOT EXISTS`）
- 可选：提供从 flat/letta 迁移数据到 xbot 的迁移脚本
- 用户通过 `config.json` 或 `/set memory_provider xbot` 切换

## 五、与现有方案的对比

| 维度 | Flat | Letta | **xbot (新)** |
|------|------|-------|---------------|
| **跨会话检索** | ❌ | ✅ (向量) | ✅ (BM25) |
| **外部依赖** | 无 | chromem-go | **无** |
| **自动记忆** | ❌ (仅 /new) | ❌ (仅 /new) | **✅ (会话结束 + 阈值)** |
| **相关性过滤** | ❌ (全量注入) | ❌ (全量注入) | **✅ (BM25 检索)** |
| **遗忘机制** | ❌ | ❌ | **✅ (热度衰减)** |
| **重要性分级** | ❌ | ❌ | **✅ (0.0-1.0)** |
| **记忆类型** | 无 | 3 块固定 | **5 类型 (fact/preference/event/decision/skill)** |
| **工具数量** | 2 | 6 | **3 (精简)** |
| **LLM 调用** | 写入时 | 写入时 | **写入时 (读取零 LLM)** |
| **可读性** | ✅ (Markdown) | ❌ (SQLite) | **✅ (Markdown + SQLite)** |
| **可编辑性** | ✅ | ❌ | **✅ (Markdown 文件)** |
| **存储效率** | 高 (文本) | 低 (向量) | **中 (SQLite + 文件)** |
| **与现有系统隔离** | — | — | **✅ (独立表 + 独立工具 + 独立 prompt)** |

## 六、实现路线图

### Phase 1: 核心实现（1-2 周）
1. `memory/xbot/xbot.go` — XbotMemory 结构体 + 接口实现
2. SQLite schema + FTS5 索引（`xbot_` 前缀表名）
3. Recall 读取路径（BM25 检索 + 热度排序）
4. Memorize 写入路径（LLM 提取 + 去重）
5. 3 个工具实现（memory_search, memory_add, memory_manage）
6. Provider 注册 + 工具注册（8 个注入点修改）
7. 3 个 prompt 文件创建（tools_xbot.md, memory_xbot.md, user_message_xbot.md）

### Phase 2: 自动化 + 优化（1 周）
1. 自动记忆触发（会话结束 + 轮次阈值）
2. 热度衰减 + 遗忘机制
3. 核心摘要自动维护
4. 短期记忆降级机制
5. SubAgent 记忆支持

### Phase 3: 增强（可选）
1. 从 flat/letta 迁移脚本
2. 记忆可视化（CLI 面板）
3. 可选本地 embedding 增强（BGE-small）
4. 记忆导入/导出

## 七、压缩联动设计：解决压缩失忆问题

### 7.1 现有压缩系统的失忆根因

通过代码分析，压缩系统与记忆系统之间存在**根本性断裂**：

```
压缩流程:                                记忆流程:
maybeCompress                            Memorize
  └→ runCompression                        └→ 仅 /new 命令触发
       └→ ApplyCompress                         └→ ArchiveAll=true 才执行
            └→ compactMessages
                 └→ LLM 摘要 (Tools=空!)
                      └→ 旧消息被摘要替代
                           └→ ❌ 不调用 Memorize
                           └→ ❌ 不保存到记忆系统
                           └→ ❌ 压缩 LLM 无工具可用
```

**失忆的 5 个根因**：

| # | 根因 | 代码位置 | 影响 |
|---|------|---------|------|
| 1 | **压缩流程不调用 Memorize** | `engine_run.go:1073-1270` | 被压缩的消息直接被 LLM 摘要替代，不进入长期记忆 |
| 2 | **压缩 LLM 无工具可用** | `compress.go:555` `Tools: tools.NewRegistry()` | `SetMemoryTools` 注入的工具定义被忽略，压缩 LLM 无法写入记忆 |
| 3 | **Memorize 仅 `/new` 触发** | `prompt_handler.go:114` | 用户不主动 `/new`，记忆永远不合并 |
| 4 | **SubAgent Memorize 是 no-op** | `engine_wire.go:1272` `ArchiveAll=false` | SubAgent 退出时记忆整合从未实际执行 |
| 5 | **Offload/Mask 引用依赖 LLM 保留** | `compress_pipeline.go:119-125` | LLM 摘要中未保留的 ID 被清理，对应数据永久丢失 |

### 7.2 新增 `CompressionAware` 接口

在 `memory/memory.go` 中新增可选接口，允许记忆系统干涉压缩流程：

```go
// memory/memory.go — 新增接口

// CompressionAware 允许记忆系统干涉上下文压缩流程。
// 实现此接口的 MemoryProvider 可以在压缩前保存即将丢失的消息，
// 在压缩后注入记忆上下文，以及影响压缩策略。
type CompressionAware interface {
    // PreCompress 在压缩执行前调用。
    // 接收即将被压缩的消息列表，将其中的关键信息保存到长期记忆。
    // 返回的 PreCompressResult 可以影响压缩行为。
    PreCompress(ctx context.Context, input PreCompressInput) (*PreCompressResult, error)

    // PostCompress 在压缩完成后调用。
    // 接收压缩后的消息列表和压缩摘要，执行记忆后处理。
    PostCompress(ctx context.Context, input PostCompressInput) error

    // CompressContext 注入到压缩 LLM 的 system prompt 中。
    // 允许记忆系统向压缩 LLM 提供额外上下文（如"这些信息很重要，务必保留"）。
    CompressContext(ctx context.Context) (string, error)
}

// PreCompressInput 压缩前输入
type PreCompressInput struct {
    // MessagesToCompress 即将被压缩的消息（不含 system 和 tail）
    MessagesToCompress []llm.ChatMessage
    // TailMessages 压缩后保留的尾部消息
    TailMessages []llm.ChatMessage
    // SessionID 当前会话 ID
    SessionID string
    // LLMClient 用于记忆提取的 LLM
    LLMClient llm.LLM
    // Model LLM 模型名
    Model string
}

// PreCompressResult 压缩前处理结果
type PreCompressResult struct {
    // SavedCount 保存到长期记忆的条目数
    SavedCount int
    // PreserveHints 需要压缩 LLM 务必保留的关键信息提示
    // 这些提示会被注入到压缩 prompt 中
    PreserveHints []string
    // SkipCompress 如果为 true，表示记忆系统已处理所有信息，
    // 可以跳过压缩（极端情况：记忆系统已保存全部信息，直接清空上下文）
    SkipCompress bool
}

// PostCompressInput 压缩后输入
type PostCompressInput struct {
    // CompressedMessages 压缩后的完整消息列表（含摘要 + tail）
    CompressedMessages []llm.ChatMessage
    // CompactionSummary LLM 生成的压缩摘要文本
    CompactionSummary string
    // RemovedMessageCount 被压缩移除的消息数
    RemovedMessageCount int
    // SessionID 当前会话 ID
    SessionID string
}
```

### 7.3 压缩流程联动设计

修改 `runCompression` 和 `ApplyCompress`，在压缩前后调用 `CompressionAware` 接口：

```
修改后的压缩流程:

maybeCompress
  └→ runCompression
       1. Emit PreCompactEvent (hooks)
       2. ★ PreCompress: 如果 MemoryProvider 实现了 CompressionAware
          │   → 提取即将被压缩消息中的关键信息 → 保存到长期记忆
          │   → 返回 PreserveHints（"务必保留"的关键信息）
       3. ApplyCompress
          │   ├→ ★ 注入 CompressContext 到压缩 prompt
          │   │   → 记忆系统提供额外上下文给压缩 LLM
          │   ├→ CM.Compress → compactMessages
          │   │   → 压缩 prompt 包含 PreserveHints + CompressContext
          │   └→ 持久化 + 清理
       4. ★ PostCompress: 如果 MemoryProvider 实现了 CompressionAware
          │   → 将压缩摘要保存为短期记忆
          │   → 更新核心摘要
       5. Emit PostCompactEvent (hooks)
```

#### 7.3.1 修改 `runCompression`（`engine_run.go`）

```go
// engine_run.go — runCompression 修改后

func (s *runState) runCompression(ctx context.Context) error {
    // ... 现有逻辑 ...

    // ★ 新增：PreCompress 阶段
    var preserveHints []string
    if mem, ok := s.cfg.Memory.(memory.CompressionAware); ok && mem != nil {
        // 获取即将被压缩的消息（通过 ContextManager 的分割逻辑）
        toCompress, tail := s.splitMessagesForCompress(messages)

        preResult, err := mem.PreCompress(ctx, memory.PreCompressInput{
            MessagesToCompress: toCompress,
            TailMessages:       tail,
            SessionID:          s.cfg.SessionID,
            LLMClient:          s.cfg.LLMClient,
            Model:              s.cfg.Model,
        })
        if err != nil {
            log.Warnf("PreCompress failed: %v", err)
            // 不阻断压缩流程，继续执行
        } else {
            preserveHints = preResult.PreserveHints
            if preResult.SkipCompress {
                // 记忆系统已处理，跳过压缩
                return nil
            }
        }
    }

    // ★ 将 preserveHints 传递给 ApplyCompress
    pipelineResult, err := ApplyCompress(ctx, CompressPipelineParams{
        // ... 现有参数 ...
        PreserveHints: preserveHints,           // ★ 新增
        Memory:        s.cfg.Memory,            // ★ 新增：传递 MemoryProvider
        SessionID:     s.cfg.SessionID,        // ★ 新增
    })

    // ... 现有后处理 ...

    // ★ 新增：PostCompress 阶段
    if mem, ok := s.cfg.Memory.(memory.CompressionAware); ok && mem != nil {
        err := mem.PostCompress(ctx, memory.PostCompressInput{
            CompressedMessages:  pipelineResult.NewMessages,
            CompactionSummary:   extractCompactionSummary(pipelineResult.NewMessages),
            RemovedMessageCount:  len(messages) - len(pipelineResult.NewMessages),
            SessionID:            s.cfg.SessionID,
        })
        if err != nil {
            log.Warnf("PostCompress failed: %v", err)
        }
    }

    return nil
}
```

#### 7.3.2 修改 `ApplyCompress`（`compress_pipeline.go`）

```go
// compress_pipeline.go — CompressPipelineParams 新增字段

type CompressPipelineParams struct {
    // ... 现有字段 ...
    CM              ContextManager
    Messages        []llm.ChatMessage
    LLMClient       llm.LLM
    Model           string
    // ... 其他现有字段 ...

    // ★ 新增：记忆系统联动
    PreserveHints []string              // PreCompress 返回的关键信息提示
    Memory        memory.MemoryProvider  // 记忆系统实例（可能实现 CompressionAware）
    SessionID     string                 // 当前会话 ID
}
```

#### 7.3.3 修改 `compactMessages`（`compress.go`）

```go
// compress.go — compactMessages 修改后

func compactMessages(ctx context.Context, params CompactParams) (*CompressResult, error) {
    // ... 现有消息分割逻辑 ...

    // ★ 新增：将 PreserveHints 注入压缩 prompt
    compactionPrompt := buildCompactionPrompt(params.PreserveHints)

    // ★ 新增：将 CompressContext 注入压缩 prompt
    if mem, ok := params.Memory.(memory.CompressionAware); ok && mem != nil {
        extraCtx, err := mem.CompressContext(ctx)
        if err == nil && extraCtx != "" {
            compactionPrompt += "\n\n## Memory Context\n" + extraCtx
        }
    }

    // ... 现有 LLM 调用逻辑 ...
}

// buildCompactionPrompt 在现有 prompt 基础上追加 PreserveHints
func buildCompactionPrompt(preserveHints []string) string {
    prompt := compactionPrompt  // 现有的固定 prompt
    if len(preserveHints) > 0 {
        prompt += "\n\n## CRITICAL — Must Preserve\n"
        prompt += "The following information has been identified as critical by the memory system.\n"
        prompt += "You MUST include these in your summary:\n\n"
        for _, hint := range preserveHints {
            prompt += fmt.Sprintf("- %s\n", hint)
        }
    }
    return prompt
}
```

### 7.4 XbotMemory 实现 `CompressionAware` 接口

```go
// memory/xbot/xbot.go

// PreCompress 在压缩前提取即将丢失的关键信息
func (m *XbotMemory) PreCompress(ctx context.Context, input memory.PreCompressInput) (*memory.PreCompressResult, error) {
    // 1. 从即将被压缩的消息中提取原子记忆
    atomicMemories := m.extractAtomicMemories(ctx, input.MessagesToCompress)

    // 2. 保存到长期记忆
    savedCount := 0
    for _, entry := range atomicMemories {
        entry.SourceSession = input.SessionID
        if err := m.addLongTermMemory(entry); err == nil {
            savedCount++
        }
    }

    // 3. 生成 PreserveHints — 提取高重要性记忆的关键信息
    //    这些提示会注入到压缩 prompt 中，确保压缩 LLM 不会遗漏
    var hints []string
    for _, entry := range atomicMemories {
        if entry.Importance >= 0.7 {
            hints = append(hints, fmt.Sprintf("[%s] %s", entry.Type, entry.Content))
        }
    }

    // 4. 生成会话摘要 → 短期记忆
    summary := m.generateSessionSummary(ctx, input.MessagesToCompress)
    m.addShortTermMemory(ShortTermMemory{
        SessionID:  input.SessionID,
        Summary:    summary,
        KeyTopics:  extractKeyTopics(atomicMemories),
    })

    return &memory.PreCompressResult{
        SavedCount:    savedCount,
        PreserveHints: hints,
        SkipCompress:  false,  // 不跳过压缩，压缩仍有价值（精简上下文）
    }, nil
}

// PostCompress 在压缩后执行记忆后处理
func (m *XbotMemory) PostCompress(ctx context.Context, input memory.PostCompressInput) error {
    // 1. 将压缩摘要保存为短期记忆条目
    m.addShortTermMemory(ShortTermMemory{
        SessionID:  input.SessionID,
        Summary:    input.CompactionSummary,
        KeyTopics:  "compaction_summary",
    })

    // 2. 更新核心摘要
    m.updateCoreSummary(ctx)

    // 3. 热度衰减
    m.decayMemories()

    return nil
}

// CompressContext 提供记忆上下文给压缩 LLM
func (m *XbotMemory) CompressContext(ctx context.Context) (string, error) {
    // 返回当前核心摘要 + 高重要性记忆
    // 这些信息注入到压缩 prompt 中，帮助 LLM 理解哪些信息已经保存在记忆中
    // （可以放心压缩）以及哪些信息需要保留在摘要中
    var sb strings.Builder

    coreSummary := m.readCoreSummary()
    if coreSummary != "" {
        sb.WriteString("Already saved in long-term memory (safe to compress away):\n")
        sb.WriteString(coreSummary)
    }

    // 列出高重要性记忆，让压缩 LLM 知道这些信息已有备份
    topMems := m.queryLongTerm(`
        SELECT content, type FROM xbot_long_term_memories
        WHERE tenant_id = ? AND importance >= 0.7
        ORDER BY heat_score DESC LIMIT 10
    `, m.tenantID)

    if len(topMems) > 0 {
        sb.WriteString("\nHigh-importance memories already saved:\n")
        for _, mem := range topMems {
            sb.WriteString(fmt.Sprintf("- [%s] %s\n", mem.Type, mem.Content))
        }
    }

    return sb.String(), nil
}
```

### 7.5 压缩失忆问题的完整解决方案

通过 `CompressionAware` 接口，xbot 记忆系统从三个层面解决压缩失忆：

```
压缩前 (PreCompress):
  ├── 从即将被压缩的消息中提取原子记忆 → 保存到长期记忆
  ├── 生成会话摘要 → 保存到短期记忆
  └── 返回 PreserveHints → 注入到压缩 prompt

压缩中 (CompressContext + PreserveHints):
  ├── 记忆上下文注入压缩 prompt → LLM 知道哪些信息已有备份
  └── PreserveHints 强制保留关键信息 → 压缩摘要不遗漏

压缩后 (PostCompress):
  ├── 压缩摘要保存为短期记忆 → 可跨会话检索
  ├── 更新核心摘要 → 下次 Recall 注入最新信息
  └── 热度衰减 → 旧记忆自然降级
```

**信息丢失路径的修复对照**：

| 失忆根因 | 修复方式 |
|----------|---------|
| 压缩不调用 Memorize | `PreCompress` 在压缩前提取并保存关键信息 |
| 压缩 LLM 无工具 | `CompressContext` 注入记忆上下文，`PreserveHints` 指导保留 |
| Memorize 仅 `/new` 触发 | `PreCompress` + `PostCompress` 在每次压缩时自动触发 |
| SubAgent Memorize 是 no-op | xbot provider 的 SubAgent 也实现 `CompressionAware` |
| Offload/Mask 引用依赖 LLM | `PreCompress` 将关键工具结果提取为原子记忆，不依赖 LLM 保留 ID |

### 7.6 对现有代码的修改范围

**修改原则**：不修改 flat/letta provider 的任何代码，只新增 `CompressionAware` 接口和在压缩流程中添加可选调用。

| 文件 | 修改内容 | 影响范围 |
|------|---------|---------|
| `memory/memory.go` | 新增 `CompressionAware` 接口 + 相关类型 | 仅新增，不修改现有接口 |
| `agent/compress_pipeline.go` | `CompressPipelineParams` 新增 3 个字段 | 向后兼容（零值时不影响现有行为） |
| `agent/engine_run.go` | `runCompression` 新增 PreCompress/PostCompress 调用 | 类型断言失败时跳过（向后兼容） |
| `agent/compress.go` | `compactMessages` 接收 `PreserveHints` + `Memory` 参数 | 向后兼容（空值时行为不变） |
| `memory/xbot/xbot.go` | 实现 `CompressionAware` 接口 | 仅 xbot provider |

**向后兼容性保证**：
- `CompressionAware` 是**可选接口**，flat/letta 不实现它，压缩流程通过类型断言检查：
  ```go
  if mem, ok := s.cfg.Memory.(memory.CompressionAware); ok && mem != nil {
      // 只有实现了 CompressionAware 的 provider 才执行
  }
  ```
- flat/letta/none provider 的压缩行为**完全不变**
- `CompressPipelineParams` 的新字段为零值时，`ApplyCompress` 行为不变

### 7.7 压缩 prompt 增强

现有的 `compactionPrompt`（`compress.go:40-91`）新增两个动态段落：

```go
// compress.go — 增强后的压缩 prompt 构建

func buildCompactionPrompt(preserveHints []string, memoryContext string) string {
    prompt := compactionPrompt  // 现有固定 prompt

    // ★ 新增：记忆上下文（告诉 LLM 哪些信息已安全保存）
    if memoryContext != "" {
        prompt += "\n\n## Memory Context\n"
        prompt += "The following information has already been saved to long-term memory.\n"
        prompt += "You can safely compress these without losing them:\n\n"
        prompt += memoryContext
    }

    // ★ 新增：必须保留的关键信息
    if len(preserveHints) > 0 {
        prompt += "\n\n## CRITICAL — Must Preserve\n"
        prompt += "The memory system has identified the following as critical.\n"
        prompt += "You MUST include these in your summary:\n\n"
        for _, hint := range preserveHints {
            prompt += fmt.Sprintf("- %s\n", hint)
        }
    }

    return prompt
}
```

### 7.8 压缩记忆提取的 LLM Prompt

`PreCompress` 中提取原子记忆的 prompt 与 `Memorize` 中的提取 prompt 类似，但增加压缩上下文：

```go
func (m *XbotMemory) extractAtomicMemoriesForCompress(
    ctx context.Context,
    messages []llm.ChatMessage,
    preserveHints []string,
) []MemoryEntry {
    prompt := `Analyze the following conversation messages that are about to be compressed
(removed from the active context window). Extract atomic memories to preserve
critical information before compression.

Focus on:
1. Key decisions and their rationale
2. Important file paths, function names, and code patterns
3. Error messages and their solutions
4. User preferences and requirements
5. Task progress and next steps

For each memory, provide:
- type: fact/preference/event/decision/skill
- content: a single, self-contained fact or event (1-2 sentences)
- keywords: 3-5 comma-separated keywords for search
- tags: 1-3 comma-separated category tags
- importance: 0.0-1.0 (how important is this for future interactions?)

Use the extract_memories tool to return results.`

    // ... LLM 调用 ...
}
```

### 7.9 压缩联动的数据流

```
用户消息 → agent loop → maybeCompress (token 超阈值)
                           │
                           ▼
                     runCompression
                           │
    ┌──────────────────────┼──────────────────────┐
    │                      │                      │
    ▼                      ▼                      ▼
PreCompress           ApplyCompress          PostCompress
(xbot memory)         (压缩执行)            (xbot memory)
    │                      │                      │
    │ 1. 提取原子记忆       │ 1. 压缩 prompt        │ 1. 保存压缩摘要
    │    → long_term_mem   │    + PreserveHints    │    → short_term_mem
    │ 2. 生成会话摘要       │    + MemoryContext    │ 2. 更新核心摘要
    │    → short_term_mem  │ 2. LLM 生成摘要        │ 3. 热度衰减
    │ 3. 返回              │ 3. 持久化              │
    │    PreserveHints     │ 4. 清理 offload/mask  │
    │    (高重要性信息)     │ 5. TokenTracker 重置   │
    │                      │                      │
    └──────────────────────┴──────────────────────┘
                           │
                           ▼
                    压缩后的上下文
                    (摘要 + tail + 记忆注入)
```

### 7.10 与现有压缩系统的兼容性

| 组件 | 现有行为 | xbot provider | flat/letta/none |
|------|---------|---------------|-----------------|
| `runCompression` | 直接调用 `ApplyCompress` | 先 `PreCompress` → `ApplyCompress` → `PostCompress` | **不变**（类型断言跳过） |
| `ApplyCompress` | 6 步 pipeline | 新增 `PreserveHints` + `Memory` 参数 | **不变**（零值时行为相同） |
| `compactMessages` | 固定 `compactionPrompt` | 动态追加 `PreserveHints` + `MemoryContext` | **不变**（空值时 prompt 不变） |
| `CompressPipelineParams` | 11 个字段 | 新增 3 个字段 | **不变**（零值兼容） |
| `MemoryProvider` 接口 | `Recall` + `Memorize` + `Close` | 新增 `CompressionAware` 可选接口 | **不变**（不实现即可） |

## 八、关键设计决策总结

1. **新增不替换** — `xbot` 是第四种 provider，与 flat/letta/none 并存，不修改现有代码
2. **严格隔离** — 8 个注入点全部新增 `case "xbot"` 分支，工具/prompt/DB 表完全隔离
3. **SQLite FTS5 BM25 替代向量搜索** — 零外部依赖，SQLite 内置，<20ms 查询
4. **写入用 LLM，读取用 BM25** — Mem0 原则：写入路径重（LLM 提取/去重），读取路径轻（BM25 检索）
5. **原子化记忆** — A-MEM 启发：每条记忆是独立原子事实，不是大段文本
6. **三层记忆 + 热度衰减** — MemoryOS 启发：工作/短期/长期 + 热度排序
7. **自动记忆触发** — 不再依赖用户手动 `/new`，会话结束自动提取
8. **精简工具** — 3 个工具替代 8 个，降低 agent 使用门槛
9. **文件为源 + 索引为派生** — Memweave 启发：Markdown 可读可编辑，SQLite 是派生索引
10. **遗忘机制** — MemoryBank 启发：热度衰减 + 低重要性遗忘
11. **表名前缀隔离** — `xbot_` 前缀避免与 letta 的表冲突，两套表安全共存
12. **压缩联动** — `CompressionAware` 可选接口，PreCompress 保存关键信息 + PostCompress 记录摘要 + CompressContext 注入压缩 prompt
13. **压缩失忆修复** — 压缩前提取原子记忆 → PreserveHints 指导压缩 LLM → 压缩后保存摘要，三重保障信息不丢失
14. **向后兼容** — `CompressionAware` 是可选接口，flat/letta 不实现，压缩行为完全不变
