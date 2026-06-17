---
title: "MCP 工具集成"
weight: 45
---

# MCP 工具集成

MCP（Model Context Protocol）是一种让 xbot 接入外部工具的标准协议。简单说，你可以给 xbot 「外挂」各种能力——文件系统、数据库、搜索、自定义 API——AI 就能直接调用它们。

## 最简单的用法：让 AI 帮你配

> 「帮我接一个文件系统的 MCP 服务」

> 「我想连上 xxx API，帮我配 MCP」

AI 会帮你写好配置文件并自动加载。

## 两种接入方式

### 全局 MCP（推荐）

所有会话都能用。在 `~/.xbot/mcp.json` 里配置：

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/documents"],
      "description": "文件系统访问"
    },
    "web-search": {
      "url": "http://localhost:3001/sse",
      "description": "网页搜索服务"
    }
  }
}
```

### 临时 MCP（会话内）

在对话中让 AI 动态添加，只对当前会话有效：

> 「临时加一个 MCP 服务连接到 localhost:8080」

## MCP 能干什么

接上 MCP 服务后，AI 就能直接使用那个服务提供的所有工具。比如：

| MCP 服务 | AI 能做什么 |
|----------|------------|
| 文件系统 | 读写指定目录的文件 |
| 数据库 | 查询、修改数据 |
| 搜索引擎 | 搜索网页内容 |
| 自定义 API | 调用你公司的内部 API |
| GitHub | 操作仓库、Issue、PR |

工具名格式：`mcp_<服务名>_<工具名>`。比如 `filesystem` 服务的 `read_file` 工具变成 `mcp_filesystem_read_file`。

## 传输方式

MCP 支持两种连接方式：

| 方式 | 适用场景 |
|------|----------|
| **stdio**（命令行） | 本地启动的服务进程，通过 `command` + `args` 配置 |
| **HTTP**（SSE） | 已在运行的服务，通过 `url` 配置 |

## 飞书 MCP

xbot 内置了 20+ 个飞书 MCP 工具（文档、多维表格、云盘、文件等），在飞书渠道自动可用，需要用户 OAuth 授权。

## 参考手册

### 配置文件位置

`~/.xbot/mcp.json`

### 服务配置字段

```json
{
  "command": "npx",              // stdio: 启动命令
  "args": ["-y", "some-server"], // stdio: 命令参数
  "url": "http://...",           // HTTP: 服务地址
  "description": "服务描述"       // 可选：帮助 AI 理解这个服务
}
```

### 运行时管理

AI 可以在对话中动态管理 MCP 服务：

| 操作 | 说明 |
|------|------|
| 添加服务 | 动态接入新的 MCP 服务 |
| 移除服务 | 断开不再需要的服务 |
| 列出服务 | 查看当前所有 MCP 服务 |
| 重载配置 | 重新加载 mcp.json |

### 安全提示

- MCP 服务作为独立进程运行（stdio）或连接外部服务（HTTP）
- 工具执行受沙箱配置约束
- 文件路径受工作区范围限制
- HTTP 服务建议使用认证 token

## 参见
- [内置工具](/zh-cn/features/tools/) — 所有可用工具
- [技能与子 Agent](/zh-cn/features/skills-agents/) — 扩展 Agent
- [插件](/zh-cn/features/plugins/) — hooks、widget、自定义工具
