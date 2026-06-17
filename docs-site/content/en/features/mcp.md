---
title: "MCP Integration"
weight: 45
---

# MCP Integration

MCP (Model Context Protocol) is a standard protocol for connecting xbot to
external tools. In plain terms: you can "bolt on" capabilities — filesystem
access, databases, search engines, custom APIs — and the agent can call them
directly.

{{< hint type=tip >}}
**Let the agent configure it for you.** Say "connect a filesystem MCP server"
or "I want to access my company's API via MCP" — the agent writes the config
and reloads it automatically.
{{< /hint >}}

## Two Connection Modes

### Global MCP (recommended)

Available in all sessions. Configured in `~/.xbot/mcp.json`:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/documents"],
      "description": "Filesystem access"
    },
    "web-search": {
      "url": "http://localhost:3001/sse",
      "description": "Web search service"
    }
  }
}
```

### Session MCP (temporary)

Dynamically added during a conversation, scoped to the current session only:

> "Temporarily add an MCP server at localhost:8080"

## What MCP Enables

Once connected, the agent can use all tools exposed by the MCP server:

| MCP Server | What the agent can do |
|-----------|----------------------|
| Filesystem | Read and write files in designated directories |
| Database | Query and modify data |
| Search engine | Search web content |
| Custom API | Call your company's internal APIs |
| GitHub | Operate repos, issues, PRs |

Tools are named as `mcp_<server>_<tool>`. For example, the `read_file` tool
from a `filesystem` server becomes `mcp_filesystem_read_file`.

## Transport Methods

| Method | Use case |
|--------|----------|
| **stdio** (command) | Locally launched server process. Configured via `command` + `args`. |
| **HTTP** (SSE) | An already-running service. Configured via `url`. |

{{< hint type=note >}}
**stdio transport gotcha:** When xbot runs as a system service with a minimal
`PATH`, tools like `npx`/`nvm` may not be visible. xbot auto-resolves commands
using the login shell's PATH to avoid this.
{{< /hint >}}

## Feishu MCP Tools

xbot includes 20+ built-in Feishu MCP tools (Docs, Bitable, Drive, Wiki, files)
that are automatically available in Feishu channels. These require user OAuth
authorization before use.

## Runtime Management

The agent can dynamically manage MCP servers during conversation:

| Operation | Description |
|-----------|-------------|
| Add server | Dynamically connect a new MCP service |
| Remove server | Disconnect a service no longer needed |
| List servers | View all active MCP servers |
| Reload config | Reload `mcp.json` to apply changes |

## Reference

### Config Location

`~/.xbot/mcp.json`

### Server Config Fields

```json
{
  "command": "npx",              // stdio: launch command
  "args": ["-y", "some-server"], // stdio: command arguments
  "url": "http://...",           // HTTP: server address (SSE)
  "description": "What it does"  // Optional: helps the agent understand this server
}
```

### Security Considerations

- MCP servers run as independent processes (stdio) or connect to external services (HTTP)
- Tool execution is constrained by the sandbox configuration
- File paths are limited to workspace scope
- HTTP services should use authentication tokens where possible
