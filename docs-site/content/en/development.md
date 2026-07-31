---
title: "Development"
weight: 70
---

# Development Guide

This guide is for developers who want to contribute to xbot or understand its
internals.

## Prerequisites

- **Go 1.26+**
- **Node.js 22+** (for Web UI development)
- **Hugo Extended** (for docs site development)
- **golangci-lint v2.10+**

## Project structure

```
xbot/
├── agent/          # Core agent loop, middleware, engine, SubAgents
├── bus/            # Message bus (pub/sub)
├── channel/        # Channel adapters (CLI, Feishu, QQ, NapCat, Web)
├── cli/            # CLI channel (BubbleTea TUI)
├── cmd/            # Entry points (xbot-cli, runner, server)
├── config/         # Config loading and types
├── cron/           # Scheduled task system
├── docs-site/      # Hugo documentation site
├── internal/       # Internal utilities (textarea, runner client, etc.)
├── llm/            # LLM client (OpenAI, Anthropic, streaming)
├── memory/         # Memory providers (flat, letta)
├── plugin/         # Plugin system (tools, hooks, widgets)
├── protocol/       # Shared protocol types
├── serverapp/      # Server core (RPC table, dispatcher)
├── session/        # Multi-tenant session management
├── storage/        # SQLite storage layer
├── tools/          # Built-in tools (Shell, Read, Edit, Grep, etc.)
├── web/            # Web frontend (React + TypeScript)
└── prompt/         # System prompt templates
```

## Build & run

```bash
# Build server + runner
make build

# Build and run server
make run

# Build CLI only
go build -o xbot-cli ./cmd/xbot-cli

# Run tests
make test                    # Go tests
cd web && npm run lint && npm run build  # Frontend checks

# Full CI locally
make ci                      # lint + build + test + web-lint + web-build
```

## Pre-commit hooks

The project uses pre-commit hooks (`scripts/pre-commit`) that run:
1. `gofmt` check
2. `golangci-lint run ./...`
3. `go build ./...`
4. `go test ./...`
5. `go test` for `plugin/protocol` submodule

Install with: `cp scripts/pre-commit .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit`

## Architecture overview

Read the [Architecture](/architecture/) page for the full system design. Key
concepts:

- **Backend** is a pure RPC client (zero business logic). Every method is a
  1-3 line typed call via Transport.
- **Transport** is the execution layer: `localTransport` calls the Agent
  directly, `remoteTransport` forwards over WebSocket.
- **Pipeline** assembles the system prompt via ordered middleware
  (prompt → global context → channel prompt → skills → memory → user message).
- **Concurrency**: global LLM semaphore, per-tenant semaphores, parallel Read
  execution, SubAgent goroutines.

## Adding a new tool

1. Create a file in `tools/` implementing the `Tool` interface:
   ```go
   type MyTool struct{}
   func (t *MyTool) Name() string { return "MyTool" }
   func (t *MyTool) Description() string { return "What this tool does" }
   func (t *MyTool) Parameters() json.RawMessage { /* JSON schema */ }
   func (t *MyTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
       // implementation
   }
   ```

2. Register it in `tools/registry.go`.

3. Add tests in `tools/my_tool_test.go`.

## Adding a new channel

1. Create a package under `channel/yourchannel/`.
2. Implement the channel interface (message handling, reconnection).
3. Add a config struct in `config/config.go`.
4. Register in `channel/` dispatcher.
5. Add documentation under `docs-site/content/en/channels/`.

## Documentation

The docs site uses [Hugo](https://gohugo.io/) with the
[GeekDoc](https://github.com/thegeeklab/hugo-geekdoc) theme.

```bash
cd docs-site
hugo server -D    # Local dev server with drafts
hugo --minify     # Production build
```

Docs are bilingual: English (default, `content/en/`) and Chinese
(`content/zh-cn/`). Both use the same menu structure defined in `hugo.toml`.

## Contributing

1. Fork the repo and create a feature branch
2. Write code following the existing conventions (see `AGENTS.md`)
3. Run `make ci` to verify all checks pass
4. Add tests for new functionality
5. Update documentation if behavior changes
6. Submit a PR with a clear description

{{< hint type=note >}}
Read `AGENTS.md` at the project root for detailed conventions, gotchas, and
architecture notes before making code changes.
{{< /hint >}}
