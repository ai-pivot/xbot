package cli

// This file has been split into focused modules:
//
//   cli_tab.go         — Tab completion (command + file path)
//   cli_inbound.go     — Message sending / input processing
//   cli_cache.go       — Render cache management (invalidate, rebuild, merge)
//   cli_slash.go       — Slash command handler
//   cli_agent_msg.go   — Agent message handler (streaming, turn management)
//   cli_progress.go    — Progress block rendering (iteration, tools, reasoning)
//   cli_msg_render.go  — Single message rendering (by role)
//   cli_viewport.go    — Viewport content management
//   cli_search.go      — Message search, fold, typewriter tick
//   cli_tool_render.go — Tool body rendering (Read/Shell/Grep/Glob)
//   cli_diff.go        — Diff rendering with syntax highlighting
