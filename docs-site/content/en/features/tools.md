---
title: "Built-in Tools"
weight: 35
---

# Built-in Tools

xbot includes 50+ built-in tools the agent can invoke during conversations. Every
tool is available by default — no configuration needed.

{{< hint type=note >}}
**AI-native design:** Tools like `config` and `tui_control` let the agent adjust
its own settings and UI. You can say "switch to dark theme" or "resize the
sidebar" and the agent handles it.
{{< /hint >}}

## File Operations

| Tool | Description |
|------|-------------|
| `Read` | Read file content with line numbers. Supports `offset` and `max_lines` for pagination. |
| `FileCreate` | Create new files (and parent directories). Errors if file already exists unless `rewrite` is set. |
| `FileReplace` | Find-and-replace in files: exact string match or RE2 regex. Supports `replace_all`, line range restriction. |
| `Glob` | Find files by glob pattern with `**` recursive matching. |
| `Grep` | Search file contents with RE2 regex. Supports `include` filter, `ignore_case`, `context_lines`. |
| `Cd` | Change working directory. Persists across tool calls within the session. |
| `DownloadFile` | Download files from Feishu messages or web/OSS URLs. |

## Execution

| Tool | Description |
|------|-------------|
| `Shell` | Execute shell commands. Configurable `timeout` (default 120s, max 600s), `background` mode for long-running tasks. `run_as` available when permission control is enabled. |

{{< hint type=note >}}
**Background mode:** Set `background: true` for dev servers or build processes.
The agent receives a task ID and continues working. Use `task_status` to check
progress — but don't poll repeatedly.

![Background tasks](/img/cli/bg-tasks.gif)
{{< /hint >}}

## Web & Search

| Tool | Description |
|------|-------------|
| `Fetch` | Fetch web URL content, convert HTML to markdown via readability, truncate with tiktoken. |
| `WebSearch` | Web search via Tavily API with configurable depth and max results. |
| `search_tools` | Semantic search for available tools using embedding similarity. |

## Context & Session

| Tool | Description |
|------|-------------|
| `context_edit` | Edit conversation context: list turns, delete turn/message, truncate, regex replace. |
| `ChatHistory` | Retrieve recent messages from group chats. |
| `recall` | Retrieve offloaded or masked observation content with pagination. |
| `recall_masked` | Retrieve masked observations. List all masked items or retrieve by ID. |
| `offload_recall` | Retrieve offloaded tool result content by offload ID, with `offset` and `limit` pagination. |

{{< hint type=info >}}
**Why offload?** Large tool outputs are automatically stored to disk instead of
clogging the context window. The agent sees a summary + an offload ID. Use
`offload_recall` when you need the full content.
{{< /hint >}}

## Scheduling & Events

| Tool | Description |
|------|-------------|
| `Cron` | Manage scheduled tasks: add (interval/delay/cron expression/at), list, remove. |
| `EventTrigger` | Manage webhook event triggers with Go template support and HMAC-SHA256 verification. |

## Interactive Cards (Feishu)

| Tool | Description |
|------|-------------|
| `card_create` | Create interactive card sessions. |
| `card_add_content` | Add content blocks: markdown, div, image, table, chart. |
| `card_add_interactive` | Add interactive elements: button, input, select, date picker. |
| `card_add_container` | Add containers: column set, form, collapsible panel. |
| `card_preview` | Preview card JSON before sending. |
| `card_send` | Send card to a Feishu chat. |

## Feishu Docs & Drive

| Tool | Description |
|------|-------------|
| `feishu_docx_create` | Create a new Feishu document. |
| `feishu_docx_get_content` | Read document content. |
| `feishu_docx_get_block` | Get a specific block by ID. |
| `feishu_docx_list_blocks` | List all blocks in a document. |
| `feishu_docx_find_block` | Find blocks matching criteria. |
| `feishu_docx_insert_block` | Insert a new block. |
| `feishu_docx_delete_blocks` | Delete blocks by ID. |
| `feishu_bitable_fields` | List/manage Bitable fields. |
| `feishu_bitable_record` | Read/write Bitable records. |
| `feishu_bitable_list` | List Bitable tables. |
| `feishu_bitable_batch_create` | Batch create records. |
| `feishu_list_all_bitables` | List all Bitables accessible to the bot. |
| `feishu_wiki_list_spaces` | List wiki spaces. |
| `feishu_wiki_list_nodes` | List wiki nodes in a space. |
| `feishu_wiki_get_node` | Get wiki node content. |
| `feishu_wiki_move_node` | Move a wiki node. |
| `feishu_wiki_create_node` | Create a wiki node. |
| `feishu_search_wiki` | Search across wiki spaces. |
| `feishu_download_file` | Download a file from Feishu. |
| `feishu_upload_file` | Upload a file to Feishu. |
| `feishu_list_files` | List files in Feishu Drive. |
| `feishu_add_permission` | Add permission to a file/folder. |
| `feishu_send_file` | Send a file to a chat. |

## Memory Tools

Tools available depend on the memory provider. See [Memory System](../memory/) for details.

### Flat Provider (default)

| Tool | Description |
|------|-------------|
| `memory_write` | Write to the file-based memory store. |
| `memory_list` | List current memory contents. |

### Letta Provider

| Tool | Description |
|------|-------------|
| `core_memory_append` | Append to core memory blocks (`persona` / `human` / `working_context`). |
| `core_memory_replace` | Replace content in core memory blocks. |
| `rethink` | Re-examine and evolve core memory (A-Mem style self-reflection). |
| `archival_memory_insert` | Insert into archival (vector-backed) long-term memory. |
| `archival_memory_search` | Semantic search archival memory via embeddings. |
| `recall_memory_search` | Search conversation history by date range (FTS5 full-text search). |

## SubAgents & Skills

| Tool | Description |
|------|-------------|
| `SubAgent` | Delegate tasks to sub-agents: one-shot or interactive multi-turn sessions. Supports foreground/background, model tier selection, and control actions (send, unload, inspect, interrupt). |
| `CreateChat` | Create agent private chats or moderated group chats (Meeting Mode with @mentions). |
| `SendMessage` | Send messages to agents, groups, peer groups, sessions, or IM channels (Feishu). |
| `Skill` | Discover and load skills from `~/.xbot/skills/` or project `.xbot/skills/`. |

## Multi-Agent Collaboration

| Tool | Description |
|------|-------------|
| `Worktree` | Git worktree-based multi-agent workspace isolation. Supports `init`, `cleanup`, `status`. |
| `JoinGroup` | Join a peer group for async broadcast communication. |
| `LeaveGroup` | Leave a peer group. |
| `ListGroupMembers` | List members of a peer group. |

## AI-Native Configuration

| Tool | Description |
|------|-------------|
| `config` | AI reads/modifies xbot config (`config.json` & runtime settings). Sensitive keys (API keys, tokens) are **masked on read** — the agent sees `sk-a***` instead of the real key. Writes are not blocked. |
| `tui_control` | AI operates the TUI: switch/close sessions, resize sidebar, change theme, send slash commands, manage layout. |

## Background Tasks

| Tool | Description |
|------|-------------|
| `task_status` | Check background task status (shell commands run with `background: true`). |
| `task_kill` | Terminate a running background task. |
| `task_read` | Read background task output. |

## MCP Integration

| Tool | Description |
|------|-------------|
| `ManageTools` | Add/remove/list/reload MCP servers dynamically. |

## Other

| Tool | Description |
|------|-------------|
| `AskUser` | Ask the user a multiple-choice question. Blocks until the user responds. |
| `TodoWrite` | Write/update the structured TODO list. |
| `TodoList` | List current TODOs with status. |
| `Logs` | List/read xbot log files with filtering. |
| `oauth_authorize` | Send an OAuth authorization card to the user (Feishu). |

![AskUser interactive dialog](/img/cli/askuser.png)

## Permission Control

When permission control is enabled, `Shell`, `FileCreate`, and `FileReplace`
gain additional parameters:

| Parameter | Description |
|-----------|-------------|
| `run_as` | OS user to execute as (e.g., `root`). Requires approval. |
| `reason` | Required reason when using `run_as`. Displayed in the approval prompt. |

## Tool Execution Model

All tools run within the configured sandbox:

| Sandbox | Behavior |
|---------|----------|
| `none` (default) | Tools execute directly on the host — full access to filesystem. |
| `docker` | Tools execute in an isolated Docker container. |
| `remote` | Tools execute on a remote runner (sandbox server). |

File tools respect workspace scope: by default, operations are limited to the
project directory and its subdirectories. `Read`, `FileCreate`, `FileReplace`,
`Glob`, and `Grep` enforce this boundary.
