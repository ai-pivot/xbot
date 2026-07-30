# Plan: Archival Memory Hybrid Retrieval + Temporal Weighting

## Summary

当前 archival memory 检索是纯向量 cosine similarity（chromem-go），对精确关键词查询（项目名、文件路径、错误码）召回率低。本设计增加 **BM25 关键词检索** 与向量检索融合（Reciprocal Rank Fusion），并叠加**时序权重**（recency boost），让近期记忆在排序中获得优势。零外部依赖，纯 Go 实现。

## Background

### 问题

```
用户问: "ssh-xbot 的 frpc 配置在哪？"
向量检索: embedding("ssh-xbot frpc 配置") → 可能召回不到含 "ssh-xbot" 字面的条目
BM25:    "ssh-xbot" + "frpc" 精确匹配 → 必命中含这些关键词的条目
```

编码 agent 的查询模式以精确标识符为主（`dcp/comm.py`、`GLM-5.2-FP8`、`ssh-xbot`），纯向量检索在这类场景下召回率不足。

### 约束

- chromem-go v0.7.0 的 `Collection.documents` 字段是私有的，**无法通过 API 遍历文档**
- 但文档以 `chromem.Document`（公开类型）的 **gob 编码**持久化到磁盘，无压缩无加密，可直接读取
- archival memory 当前与 SQLite 解耦（chromem-go gob 文件持久化），不宜引入 SQLite 依赖
- 规模小（单 tenant 几十~几百条），brute-force BM25 完全可行

### chromem-go 磁盘存储格式（已验证）

```
~/.xbot/archival/
└── cbba3f0b/                    ← hash2hex("archival_{tenantID}") = SHA256[:4] hex
    ├── 00000000.gob             ← collection 元数据（名称 + metadata）
    ├── 026dff05.gob             ← 文档: hash2hex(docID) + ".gob"
    ├── 1e3ece89.gob
    └── ...
```

每个 `.gob` 文件是 `chromem.Document` 的 gob 编码（无压缩、无加密）：

```go
// 公开类型，可直接 import github.com/philippgille/chromem-go
type Document struct {
    ID        string            // UUID
    Metadata  map[string]string // {"created_at": "2026-07-29T20:06:22Z"}
    Embedding []float32         // 1024 维向量
    Content   string            // 原文
}
```

**验证结论**：`encoding/gob` 直接解码成功，ID/Content/Metadata 全部可读。BM25 索引可直接从 gob 文件构建，无需维护并行存储。

## Design

### 架构

```
archival_memory_search(query="ssh-xbot frpc", keyword="ssh-xbot")
        │
        ▼
SearchWithOptions(query, opts={Keyword:"ssh-xbot", ...})
        │
        ▼
HybridSearcher.Search(req=SearchRequest{Query, Keyword, Limit})
        │
        ├──────────────────┬──────────────────────┐
        ▼                  ▼                      ▼
  VectorRetriever     BM25Retriever          (未来: GraphRetriever)
  (chromem-go)        (BM25Index)              接口相同，插即用
  cosine sim          keyword scoring
        │                  │
        └──────┬───────────┘
               ▼
          Fuser (RRF, k=60)
               │
               ▼
       PostRanker (Temporal)
               │
               ▼
       返回 top-N (按融合+时序分数排序)
```

### 搜索抽象层

三个可插拔接口 + 一个编排器，短期实现 Vector + BM25 两个 Retriever，后续加新检索方式只需实现接口：

```go
// --- storage/vectordb/search.go ---

// SearchRequest carries query parameters to all retrievers.
type SearchRequest struct {
    Query   string // 自然语言查询（向量检索用）
    Keyword string // 精确关键词（BM25 用；空=跳过 BM25）
    Limit   int
}

// Retriever is a single retrieval strategy producing ranked results.
// 实现：VectorRetriever, BM25Retriever, (未来: GraphRetriever, ...)
type Retriever interface {
    Name() string
    Search(ctx context.Context, tenantID int64, req SearchRequest) ([]ArchivalEntry, error)
}

// Fuser combines multiple ranked result lists into one.
// 实现：RRFFuser, (未来: WeightedFuser, LearnedFuser)
type Fuser interface {
    Fuse(lists [][]ArchivalEntry, limit int) []ArchivalEntry
}

// PostRanker adjusts the fused results (e.g., temporal recency boost).
// 实现：TemporalRanker, (未来: DiversityRanker, CrossEncoderReranker)
type PostRanker interface {
    Rank(entries []ArchivalEntry) []ArchivalEntry
}

// HybridSearcher orchestrates: retrievers (parallel) → fuse → post-rank.
type HybridSearcher struct {
    retrievers []Retriever
    fuser      Fuser
    postRanker PostRanker // nil = skip post-ranking
}

func NewHybridSearcher(retrievers []Retriever, fuser Fuser, postRanker PostRanker) *HybridSearcher

func (h *HybridSearcher) Search(ctx context.Context, tenantID int64, req SearchRequest) ([]ArchivalEntry, error)
```

**设计要点**：

- **Retriever 自决参与**：每个 Retriever 收到完整 `SearchRequest`，自行决定是否运行。`VectorRetriever` 总是用 `req.Query`；`BM25Retriever` 在 `req.Keyword == ""` 时返回空。HybridSearcher 跳过空结果列表，单列表时 Fuser 退化为直通。
- **Fuser 无参数扩展**：`Fuser` 接口只接收 `[][]ArchivalEntry`（每个子切片是一个 Retriever 的排名结果），输出融合后的 `[]ArchivalEntry`。新增 Fuser 只需实现 `Fuse` 方法。
- **PostRanker 可选**：`nil` 时跳过；非 nil 时在 Fuser 之后执行。未来加 cross-encoder reranker 只需实现 `Rank` 方法。
- **`ArchivalEntry.Similarity` 复用**：Retriever 阶段填充原始分数（cosine / BM25），Fuser 阶段覆盖为 RRF 分数，PostRanker 阶段乘以时序权重得到最终分数。调用方始终看到最终分数。

**扩展示例**（未来加图遍历检索）：

```go
type GraphRetriever struct { /* ... */ }
func (r *GraphRetriever) Name() string { return "graph" }
func (r *GraphRetriever) Search(ctx context.Context, tenantID int64, req SearchRequest) ([]ArchivalEntry, error) {
    // 图遍历逻辑，返回按相关性排序的 []ArchivalEntry
}

// 注册时只需加入 retrievers 列表
searcher := NewHybridSearcher(
    []Retriever{&VectorRetriever{...}, &BM25Retriever{...}, &GraphRetriever{...}},
    &RRFFuser{K: 60},
    &TemporalRanker{Weight: 0.3, HalfLife: 30 * 24 * time.Hour},
)
```

### 检索融合：Reciprocal Rank Fusion (RRF)

RRF 是无参数的排名融合方法，不需要校准权重，在学术界和工业界（Elasticsearch、Vespa）广泛使用：

```
RRF_score(d) = Σ  1 / (k + rank_i(d))
               i∈{vector, bm25}
```

- `k = 60`（标准值，来自原始论文 Cormack et al. 2009）
- `rank_i(d)` = 文档 d 在第 i 个结果列表中的排名（1-based）
- 只出现在一个列表中的文档，只累加该列表的分数
- 两列表都命中的文档，分数叠加 → 排名更高

**为什么选 RRF 而非加权融合**：加权融合（如 `0.7*vector + 0.3*bm25`）需要归一化不同尺度的分数，且权重需要调参。RRF 只用排名，天然无参数，对不同分数分布鲁棒。

### 时序权重：Exponential Recency Boost

在 RRF 融合后，对最终分数叠加时序衰减：

```
final_score = RRF_score × (1 + α × exp(-age_days / half_life))
```

| 参数 | 默认值 | 含义 |
|------|--------|------|
| `α` (RecencyWeight) | 0.3 | 时序影响强度（0=禁用，1=与 RRF 等权） |
| `half_life` | 30 天 | 半衰期：30天前的记忆权重减半 |

**效果示例**（α=0.3, half_life=30）：

| 记忆年龄 | recency_factor | 最终乘数 |
|----------|----------------|----------|
| 今天 | exp(0) = 1.0 | 1.30× |
| 7天前 | 0.79 | 1.24× |
| 30天前 | 0.50 | 1.15× |
| 90天前 | 0.05 | 1.015× |
| 180天前 | 0.002 | 1.001× |

设计要点：
- **乘法而非加法** — 保留 RRF 的相对排序，时序只做"轻推"
- **不会完全淘汰旧记忆** — 即使 180 天前的记忆乘数仍 >1.0，只是优势微乎其微
- **半衰期 30 天**适合编码场景 — 部署配置/调试进度变化较快，但技术架构知识相对稳定

### BM25 实现

#### 分词 (Tokenization)

```
输入: "LoRA部署 dcp/comm.py GLM-5.2-FP8"
  → ASCII: split on [^a-zA-Z0-9] → ["lora", "dcp", "comm", "py", "glm", "5", "2", "fp8"]
  → CJK:   每个中文字符作为独立 token → ["部", "署"]
  → 合并:  ["lora", "部", "署", "dcp", "comm", "py", "glm", "5", "2", "fp8"]
  → 过滤:  去掉 len<2 的 token + 停用词 → ["lora", "部", "署", "dcp", "comm", "py", "glm", "fp8"]
```

- CJK 字符级分词：简单有效，无需 jieba 依赖。中文语义由向量检索覆盖，BM25 只做精确字符匹配
- 代码标识符：`snake_case`、`kebab-case`、`camelCase` 自然被 split 拆分
- 停用词表：内置 ~30 个英文停用词（the, a, is, of, ...）

#### BM25 公式

```
score(d, q) = Σ  IDF(t) × f(t,d)×(k1+1) / (f(t,d) + k1×(1 - b + b×|d|/avgdl))
              t∈q

IDF(t) = ln(1 + (N - n(t) + 0.5) / (n(t) + 0.5))
```

| 参数 | 值 | 说明 |
|------|----|------|
| k1 | 1.2 | 词频饱和度 |
| b | 0.75 | 文档长度归一化强度 |
| N | 动态 | 集合中文档总数 |
| n(t) | 动态 | 包含 term t 的文档数 |
| \|d\| | 动态 | 文档 token 数 |
| avgdl | 动态 | 平均文档长度 |

#### 持久化：从 chromem-go gob 文件构建（单数据源）

chromem-go 的 `Document` 是公开类型，gob 文件无压缩无加密，可直接 `encoding/gob` 解码。BM25 索引以 chromem-go 的 gob 文件为**唯一数据源**，无需维护并行存储：

- **首次搜索**时 lazy-load：遍历 collection 目录 `*.gob`，解码提取 `Content`+`ID`+`Metadata`，构建内存 BM25 索引
- **`Insert`** 时调 chromem-go 的 `AddDocument`（gob 文件已持久化），BM25 索引只需追加内存条目
- **`Delete`** 时调 chromem-go 的 `Delete`，BM25 索引删内存条目
- **重启后**重新 lazy-load，数据源始终是 chromem-go 的 gob 文件 — 零同步风险

collection 目录路径推导：`{archivalDir}/{hash2hex("archival_{tenantID}")}/`，其中 `hash2hex` = SHA256 前 4 字节的 hex 编码（与 chromem-go `collection.go:549` 一致）。

## Changes

### `storage/vectordb/search.go` (新建)

搜索抽象层 — 接口定义 + 编排器 + 具体实现：

**接口**：
- `SearchRequest` — 查询参数容器
- `Retriever` interface — 单一检索策略（`Name()` + `Search()`）
- `Fuser` interface — 排名列表融合（`Fuse()`）
- `PostRanker` interface — 融合后调整（`Rank()`）

**编排器**：
- `HybridSearcher` struct — 持有 `[]Retriever` + `Fuser` + `PostRanker`
- `NewHybridSearcher(retrievers, fuser, postRanker)` — 构造函数
- `Search(ctx, tenantID, req)` — 并行运行所有 Retriever → 收集非空结果 → Fuse → PostRank → 返回 top-N

**具体实现**：
- `RRFFuser{K float64}` — Reciprocal Rank Fusion，K=60
- `TemporalRanker{Weight, HalfLife float64}` — 指数衰减时序权重

**API 契约**（供 `ArchivalService.SearchWithOptions` 使用）：
```go
type SearchOptions struct {
    Keyword string // BM25 关键词（空=纯向量检索）
}
var DefaultSearchOptions = SearchOptions{} // 纯向量 + 默认时序权重
```

### `storage/vectordb/bm25.go` (新建)

BM25 索引实现（纯内存，从 chromem-go gob 文件加载）：

```go
type bm25Doc struct {
    ID        string
    Content   string
    CreatedAt time.Time
    Terms     []string  // 运行时计算，不持久化
}

type BM25Index struct {
    mu   sync.RWMutex
    docs map[string]*bm25Doc  // id → doc
    k1   float64              // 1.2
    b    float64              // 0.75
}
```

方法：
- `NewBM25Index() *BM25Index`
- `Add(id, content string, ts time.Time)` — 添加/更新文档
- `Remove(id string)` — 删除文档
- `Search(query string, limit int) []bm25Result` — 返回 `{ID, Score}` top-N
- `LoadFromDir(dir string) error` — 遍历 `*.gob` 文件，gob 解码 `chromem.Document`，构建索引
- `Count() int`

### `storage/vectordb/archival.go` (修改)

1. **`ArchivalService` struct** — 增加 BM25 索引和 searcher：
   ```go
   type ArchivalService struct {
       db            *chromem.DB
       embeddingFunc chromem.EmbeddingFunc
       embeddingLimitConfig
       persistDir  string          // 新增：archival 根目录路径
       bm25Indexes sync.Map         // 新增：tenantID → *BM25Index（lazy-load）
       searcher    *HybridSearcher  // 新增：检索编排器
   }
   ```

2. **`NewArchivalService`** — 保存 `persistDir`；构造 `HybridSearcher`：
   ```go
   s.searcher = NewHybridSearcher(
       []Retriever{
           &VectorRetriever{svc: s},     // 总是运行
           &BM25Retriever{svc: s},        // keyword 非空时运行
       },
       &RRFFuser{K: 60},
       &TemporalRanker{Weight: 0.3, HalfLife: 30 * 24 * time.Hour},
   )
   ```

3. **`Insert`** — chromem-go `AddDocument` 后，同步追加 BM25 内存索引（`bm25Index.Add`）

4. **`Delete`** — chromem-go `Delete` 后，同步删除 BM25 内存条目（`bm25Index.Remove`）

5. **`ClearAll`** — chromem-go `DeleteCollection` 后，清空 BM25 内存索引并从 `bm25Indexes` 删除

6. **新增 `VectorRetriever`** — 实现 `Retriever` 接口，包装现有向量检索逻辑

7. **新增 `SearchWithOptions`** — 委托 `HybridSearcher.Search`：
   ```go
   func (s *ArchivalService) SearchWithOptions(ctx context.Context, tenantID int64,
       query string, limit int, opts SearchOptions) ([]ArchivalEntry, error) {
       return s.searcher.Search(ctx, tenantID, SearchRequest{
           Query: query, Keyword: opts.Keyword, Limit: limit,
       })
   }
   ```

8. **`Search`** — 改为调用 `SearchWithOptions(DefaultSearchOptions)`，保持向后兼容

9. **新增 `getOrCreateBM25Index(tenantID)`** — lazy-load：首次搜索时从 chromem-go gob 文件构建 BM25 索引，缓存在 `bm25Indexes`

### `tools/memory_tools.go` (修改)

1. **`ArchivalMemorySearchTool.Parameters`** — 增加可选 `keyword` 参数：
   ```go
   {Name: "keyword", Type: "string", Description: "Optional keyword for BM25 search. When provided, combines vector + keyword search for better recall on exact terms (file paths, project names, error codes).", Required: false},
   ```

2. **`archivalSearchArgs`** — 增加 `Keyword string`

3. **`Execute`** — 有关键词时调用 `SearchWithOptions`，无关键词时走原 `Search`

### `storage/vectordb/bm25_test.go` (新建)

- `TestTokenize` — 英文/CJK/代码标识符/混合
- `TestBM25AddRemove` — 增删后 Count 和 Search 正确
- `TestBM25Search` — 关键词精确匹配 + 排序
- `TestBM25LoadFromDir` — 写入 chromem-go gob 文件 → `LoadFromDir` → 数据一致
- `TestBM25EmptyQuery` — 空查询返回空结果

### `storage/vectordb/search_test.go` (新建)

- `TestRRFFuser` — 两列表融合，双命中条目排名更高
- `TestRRFFuserSingleList` — 单列表退化（直接透传）
- `TestRRFFuserEmpty` — 空列表输入返回空结果
- `TestTemporalRanker` — 近期条目权重 > 旧条目
- `TestTemporalRankerDisabled` — Weight=0 时不影响排序
- `TestHybridSearcherVectorOnly` — keyword="" 时只有 VectorRetriever 返回结果
- `TestHybridSearcherHybrid` — keyword 非空时两路结果 RRF 融合

## Risks

| 风险 | 影响 | 缓解 |
|------|------|------|
| chromem-go gob 格式耦合 | 升级 chromem-go 版本若改 Document 结构或文件布局，`LoadFromDir` 解码失败 | `LoadFromDir` 对 decode error 容错（跳过无法解码的文件 + warn 日志）；不依赖私有字段，仅用公开的 `Document` 类型 |
| BM25 索引首次加载延迟 | 首次搜索需遍历 gob 文件 | 文件数少（几十个），每个 gob <10KB，总加载 <10ms；lazy-load 避免启动开销 |
| CJK 字符级分词噪声 | 中文查询 BM25 噪声大 | 向量检索覆盖中文语义；BM25 主要服务英文/代码关键词；双命中条目 RRF 融合自然降权单源噪声 |
| 时序权重影响排序 | 可能过度偏好近期记忆 | α=0.3 是保守值；乘法而非加法保留 RRF 排序；α=0 可完全禁用 |
| `ensureContentFits` 压缩 query 改变语义 | 长查询被 LLM 摘要后再 embedding | 已有行为，本次不改动；BM25 用原始 query 不受影响 |

## Definition of Done

- [ ] `bm25.go` + 测试通过：分词、增删、搜索、`LoadFromDir` 从 gob 文件加载
- [ ] `hybrid.go` + 测试通过：RRF 融合、时序加权
- [ ] `SearchWithOptions` 实现：纯向量/混合两条路径
- [ ] `Search` 向后兼容（现有调用方无需改动）
- [ ] `archival_memory_search` 工具支持 `keyword` 参数
- [ ] BM25 索引以 chromem-go gob 为单数据源：Insert/Delete/ClearAll 只操作内存索引
- [ ] BM25 索引 lazy-load：重启后从 gob 文件恢复
- [ ] `go build ./...` + `go test ./storage/vectordb/...` 通过
- [ ] 手动验证：用 `archival_memory_search` 搜索 "ssh-xbot" 等关键词，确认 BM25 命中

## Open Questions

- 时序权重参数（α=0.3, half_life=30d）是否合理？可根据实际使用反馈调整，当前值是保守估计。
- 是否需要让 `archival_memory_search` 工具的 LLM 自动判断是否传入 keyword？当前设计由 LLM 决定，系统提示词可引导"精确标识符查询时传入 keyword"。
