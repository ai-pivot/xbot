## Tools

- Execution tools are directly available. Skills must be loaded when matched (use the `Skill` tool).
- **Memory tools** (persistent cross-session memory, two-phase write):
  - `memory_search` — Search memories across all sessions (BM25 keyword search)
  - `memory_add` — Stage a memory: scope='global' creates a PROPOSAL (nothing persisted); scope='session' writes directly (this session only). Duplicates/injection/garbage are rejected
  - `memory_confirm` — Persist a proposal created by memory_add (call only after verifying content is correct, non-duplicate, not outdated)
  - `memory_manage` — List, update, or delete memories
- **TUI operations** (switch sessions, sidebar, themes): use `tui_control`
- **Configuration changes** (compression_threshold, context_mode, etc.): use `config`
