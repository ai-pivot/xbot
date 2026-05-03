# Plan: Deterministic Message Rendering Refactor

## Summary

Refactor the CLI TUI message rendering to guarantee deterministic positioning. Each message is identified by `agentTurnID` + `role`, so duplicate or late-arriving events update existing entries instead of creating duplicates. The queue flush mechanism is changed from heuristic tick-based to event-driven: flush only after the outbound assistant message arrives, not on any tick where `!typing`.

## Problem Analysis

### Bug 1: Cancel + Late Tool Completion → Duplicate Iteration Render

**Root cause**: After Ctrl+C, `turnCancelled=true` blocks progress events but allows `PhaseDone` through. `handleProgressDone` runs, creates a `tool_summary` message and appends to `m.messages`. Then the cancel reply arrives as `cliOutboundMsg`, `handleAgentMessage` runs again and calls `endAgentTurn` + tries to re-build tool summary from the same iteration history (which was already cleared by the first `endAgentTurn`). The PhaseDone path already appended a `tool_summary`, and `handleAgentMessage` creates another one (or fails to find the first one to relocate).

**Fix**: Track whether a turn's completion has already been processed via a `turnDoneProcessed` flag keyed by `agentTurnID`. When `handleProgressDone` runs and sets this flag, `handleAgentMessage` knows the tool summary was already handled and doesn't create a duplicate.

### Bug 2: Local Mode Queue Flush Before Reply

**Root cause**: `handleProgressDone` sets `needFlushQueue=true` + calls `endAgentTurn`. The next tick sees `!typing && needFlushQueue` and flushes the queue. But the assistant reply (`cliOutboundMsg`) may not have arrived yet — it's still in `asyncCh`. So the queued user message gets appended before the assistant reply.

**Fix**: Instead of flushing on tick when `!typing`, flush only when the assistant reply has been received. Track with `turnReplyReceived[agentTurnID]`. The tick handler checks both `needFlushQueue` and the reply-received flag for the *previous* turn.

### Core Design: Turn-Keyed Message Slots

Each message in `m.messages` gets a `turnID` field. When adding assistant/tool_summary/system messages, we tag them with the current `agentTurnID`. This enables:

1. **Dedup by turn+role**: Before appending an assistant or tool_summary message, check if one already exists for this turn. If so, update in-place instead of appending.
2. **Deterministic ordering**: tool_summary always goes before assistant for the same turn, guaranteed by the slot system.
3. **Safe late arrivals**: Duplicate events (late tool completions after cancel) find the existing slot and update it, rather than creating new entries.

## Changes

### `channel/cli_types.go`
- Add `turnID uint64` field to `cliMessage` struct
- Add `turnDoneFlags map[uint64]turnDoneFlag` to `cliModel` (or use a bitfield approach)
  - `turnDoneFlag`: bit flags for `doneProcessed`, `replyReceived`

### `channel/cli_helpers.go`
- `startAgentTurn()`: initialize entry in `turnDoneFlags` for new turnID
- `endAgentTurn()`: leave `turnDoneFlags` entry intact (don't delete — needed for late arrivals). Clean up entries older than current turnID minus 2.
- Add helper `findMessageByTurn(turnID, role)` → returns index or -1
- Add helper `updateOrAppendMessage(turnID, role, msg)` → finds existing and updates, or appends
- Modify `flushMessageQueue()`: check `turnReplyReceived` for the previous turn instead of just `!typing`

### `channel/cli_update_handlers.go` — `handleProgressMsg()`
- No fundamental changes to the guard logic — `turnCancelled` + PhaseDone bypass stays.

### `channel/cli_update_handlers.go` — `handleProgressDone()`
- Set `turnDoneFlags[turnID].doneProcessed = true`
- Use `updateOrAppendMessage(turnID, "tool_summary", ...)` instead of raw `m.messages = append(...)` for the pending tool summary

### `channel/cli_message.go` — `handleAgentMessage()`
- Set `turnDoneFlags[turnID].replyReceived = true`
- Check `turnDoneFlags[turnID].doneProcessed` — if true, skip tool summary creation (already done by PhaseDone)
- Use `updateOrAppendMessage(turnID, "assistant", ...)` for the assistant message
- Use `updateOrAppendMessage(turnID, "tool_summary", ...)` for the tool summary relocation

### `channel/cli_update_handlers.go` — `handleTickMsg()`
- Change flush condition from `needFlushQueue && !typing` to `needFlushQueue && isPreviousTurnReplyReceived()`
- This ensures the queued message is only sent after the previous turn's assistant reply is visible

### `channel/cli_update_handlers.go` — `handleCtrlC()`
- No changes needed — the `turnDoneFlags` system handles the aftermath

## Risks
- **Regression in normal flow**: Mitigated by tagging all messages with turnID and using updateOrAppend for all insertion paths. Existing single-event-per-turn flow is unchanged.
- **Memory leak in turnDoneFlags**: Mitigated by cleaning up entries older than `currentTurnID - 2` in `startAgentTurn()`.
- **Remote mode**: Same code paths — remote mode uses the same `handleProgressMsg`/`handleAgentMessage` pipeline. The `turnDoneFlags` approach works for both local and remote.
- **SubAgent session view**: The `channelName == "agent"` path in `handleProgressDone` also needs turnID tagging.

## Definition of Done
- [ ] `cliMessage` has `turnID` field, all message insertion paths tag it
- [ ] `turnDoneFlags` tracks per-turn completion state (`doneProcessed`, `replyReceived`)
- [ ] `handleProgressDone` uses update-or-append for tool_summary (no duplicates)
- [ ] `handleAgentMessage` checks `doneProcessed` to avoid re-creating tool summary
- [ ] `handleAgentMessage` uses update-or-append for assistant message
- [ ] Queue flush waits for `replyReceived` of previous turn (not just `!typing`)
- [ ] Cancel scenario: Ctrl+C → PhaseDone → late outbound reply produces clean output (one tool_summary, one assistant, no duplicates)
- [ ] Queue scenario: msg1 → reply1 → msg2 ordering guaranteed regardless of PhaseDone/reply arrival order
- [ ] `go build ./...` passes
- [ ] `go test ./channel/...` passes
- [ ] Manual test: cancel during tool execution produces correct display
