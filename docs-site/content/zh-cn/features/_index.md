---
title: "功能"
weight: 30
---

# 功能

xbot 提供丰富的工具和能力，让 Agent 在对话中完成实际工作。

{{< columns >}}

### 内置工具
Shell、文件读写、网页搜索、定时任务、子 Agent 委派。
→ [工具列表](/zh-cn/features/tools/)

### Skills & SubAgents
Markdown 能力包、基于角色的子 Agent、群聊讨论模式。
→ [技能与子 Agent](/zh-cn/features/skills-agents/)

### MCP 协议
全局和会话级 MCP Server，stdio 和 HTTP 传输。
→ [MCP 配置](/zh-cn/features/mcp/)

<--->

### 记忆系统
Flat（默认）vs Letta（MemGPT 向量搜索）。
→ [记忆系统](/zh-cn/features/memory/)

### 插件系统
工具、hooks、widget、渠道插件。
→ [插件系统](/zh-cn/features/plugins/)

### Hooks
生命周期事件钩子，扩展 Agent 行为。
→ [Hooks](/zh-cn/features/hooks/)

{{< /columns >}}

## 功能概览

| 功能 | 作用 | 指南 |
|---------|-------------|-------|
| **工具** | 50+ 内置工具：文件操作、执行、网页、调度、上下文 | [tools.md](tools/) |
| **技能** | Markdown 能力包，指导 Agent 完成特定任务 | [skills-agents.md](skills-agents/) |
| **子 Agent** | 基于角色的子 Agent，用于委派和并行工作 | [skills-agents.md](skills-agents/) |
| **群聊** | 主持多 Agent 会议，通过 @mention 触发 | [skills-agents.md](skills-agents/) |
| **MCP** | 模型上下文协议，集成外部工具 | [mcp.md](mcp/) |
| **记忆** | Flat（默认）或 Letta（MemGPT）记忆提供者 | [memory.md](memory/) |
| **插件** | 脚本插件：信息栏 widget、工具提示、自定义工具 | [plugins.md](plugins/) |
| **Hooks** | 生命周期事件钩子，支持命令/HTTP/MCP 处理器 | [hooks.md](hooks/) |

## 快速决策指南

**我想...** → **用这个：**

- 运行 Shell 命令、编辑文件、搜索代码 → [内置工具](tools/)
- 教 Agent 一个工作流（清单、约定）→ [技能](skills-agents/)
- 委派工作给专门的 Agent → [子 Agent](skills-agents/)
- 连接外部 API 或服务 → [MCP 集成](mcp/)
- 在 TUI 中显示状态栏 widget → [插件](plugins/)
- 在工具执行前/后运行脚本 → [Hooks](hooks/)
- 跨对话持久化信息 → [记忆](memory/)
