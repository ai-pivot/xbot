package agent

import (
	"strconv"
	"sync"

	"xbot/channel"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Turn-level frames: `done` / `final` (STATE-MODEL §3.3, contracts C-6 / C-7)
// ---------------------------------------------------------------------------
//
// The protocol obligation is exact:
//
//   - every STARTED iteration gets exactly ONE `done`, carrying its complete
//     block (body text + reasoning + ALL tools; interrupted `running` tools are
//     downgraded to `failed`, so nothing vanishes at commit time);
//   - every TURN gets exactly ONE `final`, including empty and cancelled turns.
//
// Both are bookkeeping problems, not content problems, so the bookkeeping lives
// in one small tracker (pure, unit-testable) instead of being re-derived at
// every exit path. The engine and the cancel path both report through it; a
// duplicate report is dropped, a missing one is filled in at turn end.
//
// These frames are ADDITIVE (dual-write): the legacy progress stream still
// carries everything it carried before, so existing clients are unaffected.

// turnFrameTracker enforces "exactly one done per iteration, exactly one final
// per turn" per chat. It is safe for concurrent use (the cancel path and the
// engine goroutine may race on the same turn).
type turnFrameTracker struct {
	mu    sync.Mutex
	turns map[string]*turnFrameState
}

type turnFrameState struct {
	turnID    uint64
	lastDone  int
	finalSent bool
}

func newTurnFrameTracker() *turnFrameTracker {
	return &turnFrameTracker{turns: make(map[string]*turnFrameState)}
}

// state returns the (possibly reset) state for a chat. Caller holds t.mu.
func (t *turnFrameTracker) stateLocked(chatKey string, turnID uint64) *turnFrameState {
	st, ok := t.turns[chatKey]
	if !ok || st.turnID != turnID {
		st = &turnFrameState{turnID: turnID}
		t.turns[chatKey] = st
	}
	return st
}

// iterationDone accepts a `done` for (turn, iteration). It returns false when
// the report must be dropped: the iteration was already committed, or it does
// not advance the turn (contract C-4: strictly increasing; C-10: never send
// already-committed content back as progress).
func (t *turnFrameTracker) iterationDone(chatKey string, turnID uint64, iteration int) bool {
	if iteration <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(chatKey, turnID)
	if iteration <= st.lastDone {
		return false
	}
	st.lastDone = iteration
	return true
}

// lastDoneIteration reports the highest iteration already published for a turn.
func (t *turnFrameTracker) lastDoneIteration(chatKey string, turnID uint64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st, ok := t.turns[chatKey]; ok && st.turnID == turnID {
		return st.lastDone
	}
	return 0
}

// final accepts a `final` for a turn. It returns false when one was already
// sent (C-7: exactly one per turn — never a second one, never zero).
func (t *turnFrameTracker) final(chatKey string, turnID uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(chatKey, turnID)
	if st.finalSent {
		return false
	}
	st.finalSent = true
	return true
}

// finalSent reports whether a turn's `final` was already published.
func (t *turnFrameTracker) finalSent(chatKey string, turnID uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.turns[chatKey]
	return ok && st.turnID == turnID && st.finalSent
}

// forget drops all bookkeeping for a chat (session reset / turn fully closed).
func (t *turnFrameTracker) forget(chatKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.turns, chatKey)
}

// ---------------------------------------------------------------------------
// Agent-side emitters
// ---------------------------------------------------------------------------

// toolCallID returns the stable identity of a tool call (STATE-MODEL P2/P3′).
// Deterministic in (chat, turn, iteration, index) and memoized on the agent, so
// the id assigned during argument generation is the SAME id used when the tool
// executes and when it is committed.
func (a *Agent) toolCallID(chatKey string, turnID uint64, iteration, index int) string {
	if a.toolIDs == nil {
		return ""
	}
	return a.toolIDs.ID(chatKey, turnID, iteration, index)
}

// turnFrameSenders returns the channels of this agent that can transport
// turn-level frames. Channels without the capability (CLI, IM bridges) are
// skipped — they keep consuming the legacy progress stream unchanged.
func (a *Agent) turnFrameSenders() []channel.TurnFrameSender {
	var out []channel.TurnFrameSender
	if a.channelRange == nil {
		return out
	}
	a.channelRange(func(_ string, ch channel.Channel) bool {
		if s, ok := ch.(channel.TurnFrameSender); ok {
			out = append(out, s)
		}
		return true
	})
	return out
}

// emitIterationDone publishes exactly one `done` for (turn, iteration).
// Duplicate reports are dropped by the tracker, so every caller may report
// freely (snapshot path, cancel path, recovery) without double-sending.
func (a *Agent) emitIterationDone(channelName, chatID string, block protocol.IterationBlock) {
	if block.Iteration <= 0 || a.turnFrames == nil {
		// Bare &Agent{} construction (tests / internal one-shots) does not
		// publish turn-level frames at all.
		return
	}
	progressKey := qualifyChatID(channelName, chatID)
	if !a.turnFrames.iterationDone(progressKey, block.TurnID, block.Iteration) {
		return
	}
	if block.Tools == nil {
		block.Tools = []protocol.ToolProgress{}
	}
	for _, s := range a.turnFrameSenders() {
		s.SendIterationDone(progressKey, &block)
	}
}

// emitTurnFinal publishes exactly one `final` for a turn.
func (a *Agent) emitTurnFinal(channelName, chatID string, final protocol.TurnFinalFrame) {
	if final.TurnID == 0 || a.turnFrames == nil {
		return
	}
	progressKey := qualifyChatID(channelName, chatID)
	if !a.turnFrames.final(progressKey, final.TurnID) {
		return
	}
	for _, s := range a.turnFrameSenders() {
		s.SendTurnFinal(progressKey, &final)
	}
}

// cancelledIterationBlock reconstructs the block of the iteration that was
// IN FLIGHT when a run was cancelled. Cancellation can happen mid-tool, before
// any snapshot is written, so the live progress snapshot is the only place
// holding that iteration's content and tool set. Non-terminal tools are
// downgraded to `failed` (C-6): an interrupted tool must never disappear.
func (a *Agent) cancelledIterationBlock(progressKey string, turnID uint64) *protocol.IterationBlock {
	raw, ok := a.lastProgressSnapshot.Load(progressKey)
	if !ok {
		return nil
	}
	pe, ok := raw.(*protocol.ProgressEvent)
	if !ok || pe == nil || pe.Iteration <= 0 {
		return nil
	}
	tid := pe.TurnID
	if tid == 0 {
		tid = turnID
	}
	tools := make([]protocol.ToolProgress, 0, len(pe.ActiveTools)+len(pe.CompletedTools)+len(pe.StreamingTools))
	seen := make(map[string]bool)
	add := func(list []protocol.ToolProgress) {
		for _, t := range list {
			key := t.CallID
			if key == "" {
				key = t.Name + "#" + t.Label
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			switch t.Status {
			case "pending", "running", "generating", "":
				t.Status = "failed"
			case "error":
				t.Status = "failed"
			}
			if t.Iteration == 0 {
				t.Iteration = pe.Iteration
			}
			tools = append(tools, t)
		}
	}
	add(pe.CompletedTools)
	add(pe.ActiveTools)
	add(pe.StreamingTools)
	content := pe.Content
	if pe.StreamContent != "" {
		content = pe.StreamContent // live accumulated text is fresher than the snapshot
	}
	reasoning := pe.Reasoning
	if pe.ReasoningStreamContent != "" {
		reasoning = pe.ReasoningStreamContent
	}
	return &protocol.IterationBlock{
		TurnID:    tid,
		Iteration: pe.Iteration,
		Content:   content,
		Reasoning: reasoning,
		Tools:     tools,
		Cancelled: true,
	}
}

// turnIDFromMetadata parses the turn id carried on an inbound message.
func turnIDFromMetadata(meta map[string]string) uint64 {
	if meta == nil {
		return 0
	}
	raw := meta["turn_id"]
	if raw == "" {
		return 0
	}
	tid, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return tid
}
