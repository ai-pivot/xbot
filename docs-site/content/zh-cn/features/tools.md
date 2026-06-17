---
title: "Tools"
weight: 35
---

# Built-in Tools

xbot 内置 50+ 工具，Agent 在对话中可以随时调用。所有工具默认可用，无需额外配置。

{{< hint type=note >}}
**AI 原生设计**：像 `config` 和 `tui_control` 这样的工具让 Agent 可以自行调整设置和 UI。你可以直接说「切换到暗色主题」或「调整侧边栏宽度」，Agent 会帮你完成。
{{< /hint >}}

## File Operations

| Tool | Description |
|------|-------------|
| `Read` | Read file content with line numbers, optional offset/limit |
| `FileCreate` | Create new files (errors if file already exists) |
| `FileReplace` | Search-and-replace in files (exact or RE2 regex, line range, replace_all) |
| `Glob` | Find files by glob pattern (`**` recursive matching) |
| `Grep` | Search file contents with RE2 regex, include filter, ignore_case, context_lines |
| `Cd` | Change working directory (persists across tool calls) |
| `DownloadFile` | Download files from Feishu messages or web/OSS URLs |

## Execution

| Tool | Description |
|------|-------------|
| `Shell` | 执行 Shell 命令。可配置 `timeout`（默认 120s，最大 600s），`background` 模式用于长时间任务。启用权限控制时支持 `run_as`。 |

{{< hint type=note >}}
**后台模式**：设置 `background: true` 用于开发服务器或构建进程。Agent 会收到任务 ID 并继续工作。使用 `task_status` 查看进度——但不要反复轮询。

![后台任务](/img/cli/bg-tasks.gif)
{{< /hint >}}

## Web & Search

| Tool | Description |
|------|-------------|
| `Fetch` | 获取网页内容，通过 readability 转 Markdown + tiktoken 截断 |
| `WebSearch` | 通过 Tavily API 搜索网页，可配置深度和最大结果数 |
| `search_tools` | 通过嵌入向量相似度进行工具语义搜索 |

## Context & Session

| Tool | Description |
|------|-------------|
| `context_edit` | Edit conversation context: list turns, delete turn/message, truncate, regex replace |
| `ChatHistory` | Retrieve recent messages from group chats |
| `recall` | Retrieve offloaded or masked observation content with pagination |
| `recall_masked` | Retrieve masked observations only |
| `offload_recall` | 通过 offload ID 检索 offload 工具结果，支持 `offset` 和 `limit` 分页 |

{{< hint type=info >}}
**为什么需要 offload？** 大型工具输出会自动存入磁盘，而不是塞满上下文窗口。Agent 看到的是摘要 + offload ID。需要完整内容时使用 `offload_recall`。
{{< /hint >}}

## Scheduling & Events

| Tool | Description |
|------|-------------|
| `Cron` | Manage scheduled tasks: add (interval/delay/cron_expr/at), list, remove |
| `EventTrigger` | Manage webhook event triggers with Go template support and HMAC-SHA256 verification |

## Interactive Cards (Feishu)

| Tool | Description |
|------|-------------|
| `card_create` | 创建交互式卡片会话 |
| `card_add_content` | 添加内容（markdown、div、image、table、chart） |
| `card_add_interactive` | 添加交互元素（button、input、select、date_picker） |
| `card_add_container` | 添加容器（column_set、form、collapsible_panel） |
| `card_preview` | 预览卡片 JSON |
| `card_send` | 发送卡片到聊天 |

## Feishu Docs & Drive

| Tool | Description |
|------|-------------|
| `feishu_docx_create` | 创建飞书文档 |
| `feishu_docx_get_content` | 读取文档内容 |
| `feishu_docx_get_block` | 按 ID 获取特定块 |
| `feishu_docx_list_blocks` | 列出文档中的所有块 |
| `feishu_docx_find_block` | 查找符合条件的块 |
| `feishu_docx_insert_block` | 插入新块 |
| `feishu_docx_delete_blocks` | 按 ID 删除块 |
| `feishu_bitable_fields` | 列出/管理多维表格字段 |
| `feishu_bitable_record` | 读/写多维表格记录 |
| `feishu_bitable_list` | 列出多维表格数据表 |
| `feishu_bitable_batch_create` | 批量创建记录 |
| `feishu_list_all_bitables` | 列出机器人可访问的所有多维表格 |
| `feishu_wiki_list_spaces` | 列出知识库空间 |
| `feishu_wiki_list_nodes` | 列出空间中的知识库节点 |
| `feishu_wiki_get_node` | 获取知识库节点内容 |
| `feishu_wiki_move_node` | 移动知识库节点 |
| `feishu_wiki_create_node` | 创建知识库节点 |
| `feishu_search_wiki` | 跨知识库空间搜索 |
| `feishu_download_file` | 从飞书下载文件 |
| `feishu_upload_file` | 上传文件到飞书 |
| `feishu_list_files` | 列出飞书云盘文件 |
| `feishu_add_permission` | 为文件/文件夹添加权限 |
| `feishu_send_file` | 发送文件到聊天 |

## Memory Tools

可用工具取决于 memory provider。详见 [Memory System](../memory/)。

### Flat Provider（默认）

| Tool | Description |
|------|-------------|
| `memory_write` | 写入基于文件的内存存储 |
| `memory_list` | 列出当前内存内容 |

### Letta Provider

| Tool | Description |
|------|-------------|
| `core_memory_append` | 追加到核心内存块（persona/human/working_context） |
| `core_memory_replace` | 替换核心内存块内容 |
| `rethink` | 重新审视和演变核心内存（A-Mem 风格自反思） |
| `archival_memory_insert` | 插入到档案（向量支持）长期内存 |
| `archival_memory_search` | 通过嵌入向量进行档案内存语义搜索 |
| `recall_memory_search` | 按日期范围搜索对话历史（FTS5 全文搜索） |

## SubAgents & Skills

| Tool | Description |
|------|-------------|
| `SubAgent` | 委派任务给子 Agent：支持单次或交互式多轮会话，foreground/background 模式，model tier 选择，控制操作（send、unload、inspect、interrupt） |
| `CreateChat` | 创建 Agent 私聊或主持群聊（Meeting Mode，通过 @提及控制发言） |
| `SendMessage` | 向 Agent、群组、Peer Group、会话或 IM 渠道（飞书）发送消息 |
| `Skill` | 发现并加载来自 `~/.xbot/skills/` 或项目 `.xbot/skills/` 的技能 |

## AI-Native Configuration

| Tool | Description |
|------|-------------|
| `config` | AI 读取/修改 xbot 配置（`config.json` 和运行时设置）。敏感键（API key、token）在读取时**被遮盖**——Agent 看到的是 `sk-a***` 而非真实密钥。写入不被阻止。 |
| `tui_control` | AI 操作 TUI：切换/关闭会话、调整侧边栏、更换主题、发送斜杠命令、管理布局 |

## Multi-Agent Collaboration

| Tool | Description |
|------|-------------|
| `Worktree` | Git worktree 多 Agent 工作区隔离。支持 `init`、`cleanup`、`status`。 |
| `JoinGroup` | 加入 Peer Group 进行异步广播通信 |
| `LeaveGroup` | 离开 Peer Group |
| `ListGroupMembers` | 列出 Peer Group 成员 |

## Background Tasks

![后台任务](/img/cli/bg-tasks.gif)

| Tool | Description |
|------|-------------|
| `task_status` | Check background task status |
| `task_kill` | Terminate a running background task |
| `task_read` | Read background task output |

## MCP Integration

| Tool | Description |
|------|-------------|
| `ManageTools` | 添加/移除/列出/重载 MCP 服务器 |

## Other

| Tool | Description |
|------|-------------|
| `AskUser` | Ask user a multiple-choice question |
| `TodoWrite` / `TodoList` | Structured TODO management with cross-session persistence |
| `Logs` | List/read xbot log files with filtering |
| `oauth_authorize` | Send OAuth authorization card to user |

![AskUser 交互式对话框](/img/cli/askuser.png)

## Permission Control

启用权限控制时，`Shell`、`FileCreate` 和 `FileReplace` 获得以下额外参数：

| 参数 | 说明 |
|------|------|
| `run_as` | 以指定 OS 用户身份执行（如 `root`），需要审批 |
| `reason` | 使用 `run_as` 时必填的理由，显示在审批提示中 |

## Tool Execution Model

所有工具在配置的沙箱内运行：

| 沙箱 | 行为 |
|------|------|
| `none`（默认） | 工具直接在本机执行——完全访问文件系统 |
| `docker` | 工具在隔离的 Docker 容器中执行 |
| `remote` | 工具在远程 Runner（沙箱服务器）上执行 |

文件工具遵循工作区范围：默认情况下，操作仅限于项目目录及其子目录。`Read`、`FileCreate`、`FileReplace`、`Glob` 和 `Grep` 都执行此边界约束。

## Hooks System

xbot has a full lifecycle hooks system that fires on 17 events (PreToolUse, PostToolUse, UserPromptSubmit, etc.). Supports command/http/mcp_tool/callback handlers with JSON configuration. See [Hooks System Design](/design/hooks-system/) for details.

## 参见
- [技能与子 Agent](/zh-cn/features/skills-agents/) — 扩展 Agent
- [MCP](/zh-cn/features/mcp/) — 连接外部工具
- [插件](/zh-cn/features/plugins/) — hooks、widget、自定义工具
