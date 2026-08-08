## Tools

- Execution tools are directly available. Skills must be loaded when matched (use the `Skill` tool).
- **Memory tools** (persistent cross-session memory):
  - `memory_search` — Search memories across all sessions (BM25 keyword search)
  - `memory_add` — Save a memory for future conversations (fact/preference/event/decision/skill)
  - `memory_manage` — List, update, or delete memories
- **TUI operations** (switch sessions, sidebar, themes): use `tui_control`
- **Configuration changes** (max_iterations, context_mode, etc.): use `config`
