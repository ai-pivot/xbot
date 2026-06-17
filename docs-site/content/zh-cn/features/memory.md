---
title: "Memory"
weight: 20
---

# 记忆系统

xbot 支持可插拔的记忆提供者。Agent 使用记忆在对话间持久化信息、回忆过往交互、维护用户专属上下文。

## 提供者对比

| | Flat（默认） | Letta（MemGPT 风格） |
|--|------------|---------------------|
| 核心记忆 | 内存块 | SQLite（始终在提示词中） |
| 归档记忆 | Grep 可搜索的文本块 | 向量搜索（chromem-go） |
| 回顾记忆 | 事件历史 | FTS5 全文搜索 |
| 依赖 | 无 | 需要嵌入模型 |

## 配置

```bash
MEMORY_PROVIDER=flat    # 或: letta
```

Letta 模式需配置嵌入模型：

```bash
LLM_EMBEDDING_PROVIDER=openai
LLM_EMBEDDING_BASE_URL=https://api.openai.com/v1
LLM_EMBEDDING_API_KEY=sk-xxx
LLM_EMBEDDING_MODEL=text-embedding-3-small
```

## Flat Provider（默认）

所有长期记忆存储为单个文本块，每次请求时注入系统提示词。当你通过 `/new` 开启新会话时，LLM 会合并和压缩记忆块。

**工具:** `memory_write`、`memory_list`

| 特性 | 详情 |
|------|------|
| 存储 | 文件文本块 |
| 持久化 | 与会话状态一起保存/恢复 |
| 合并 | `/new` 时由 LLM 驱动 |
| 搜索 | 基础文本匹配（grep 风格） |

{{< hint type=tip >}}
**大多数用户应该选择 Flat。** 它极其简单，无需额外配置，记住偏好和关键信息效果很好。
{{< /hint >}}

## Letta Provider

受 [MemGPT](https://memgpt.ai/) 启发的三层记忆架构。基于 SQLite 构建，支持 chromem-go 向量嵌入和 FTS5 全文搜索。

### 核心记忆

以下三个记忆块始终存在于系统提示词中：

| 记忆块 | 用途 | 隔离策略 |
|--------|------|----------|
| `persona` | Agent 的身份和性格 | **全局**——所有用户共享 |
| `human` | 每用户观察和偏好 | **按用户**——通过发送者 ID 隔离 |
| `working_context` | 当前任务状态和活跃上下文 | **按会话**——`/new` 时重置 |

**工具:** `core_memory_append`、`core_memory_replace`、`rethink`

### 归档记忆

面向详细事实、事件和上下文的长期向量存储。基于 SQLite + chromem-go 嵌入，支持语义搜索。

**工具:** `archival_memory_insert`、`archival_memory_search`

### 回顾记忆

完整的对话历史，可按日期范围搜索。由 SQLite FTS5（全文搜索）提供支持，关键词匹配速度极快。

**工具:** `recall_memory_search`

## 记忆合并

当开启新会话（`/new` 命令）时：

| Provider | 行为 |
|----------|------|
| **Flat** | LLM 合并和压缩记忆块——旧记忆被总结，冗余内容被移除 |
| **Letta** | 核心记忆跨会话保留；归档记忆保留；`working_context` 被清除 |

## 核心记忆隔离

在多租户部署中（Server 模式，多个渠道如飞书 + Web）：

- `persona` 是**全局的**——Agent 的核心身份在所有用户之间共享
- `human` 是**按用户的**——Agent 了解到的关于 Alice 的信息对 Bob 不可见
- `working_context` 是**按会话的**——每个对话维护自己的任务上下文

{{< hint type=important >}}
通过 `core_memory_append`/`core_memory_replace` 设置的核心记忆块在**会话之间是持久化的**。使用 `rethink` 触发 Agent 重新审视和演进其记忆，类似于 MemGPT 的自我编辑机制。
{{< /hint >}}

详见[核心记忆隔离设计](/design/core-memory-isolation/)。

## 参见
- [内置工具](/zh-cn/features/tools/) — 记忆工具
- [MCP](/zh-cn/features/mcp/) — 外部工具集成
- [配置参考](/zh-cn/configuration/) — memory_provider 设置
