package agent

import (
	"encoding/json"
	"fmt"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

// ---------------------------------------------------------------------------
// Authoritative state reads for the pull endpoints (STATE-MODEL §2.4 / §3.5)
// ---------------------------------------------------------------------------
//
// These three accessors back GET /api/iteration, GET /api/turn and
// GET /api/session-state. They are read-only, never materialize synthetic
// content, and answer "absent" explicitly (a missing iteration is a legal,
// authoritative answer — clients use it to confirm a legal iteration jump).
//
// Authority order:
//   - committed iterations → iteration_history table (the single write path,
//     engine_run_tools.go:writeIterationHistory);
//   - the in-flight iteration → lastProgressSnapshot (the same source
//     GetActiveProgress uses; nothing else knows the live state);
//   - session fields → live agent state (running / todos / goal / queue).

// stateTurnWindow bounds how many of the most recent turns / iterations a
// session-state snapshot carries. A 1,600-iteration turn must never produce a
// multi-megabyte snapshot (STATE-MODEL §12 W5).
const (
	stateTurnWindow    = 8
	stateIterWindow    = 200
)

// stateSession returns the TenantSession for a chat, or nil when unavailable.
func (a *Agent) stateSession(channelName, chatID string) *sqliteIterationSource {
	if a.multiSession == nil {
		return nil
	}
	sess, err := a.multiSession.GetOrCreateSession(channelName, chatID)
	if err != nil || sess == nil {
		return nil
	}
	return &sqliteIterationSource{session: sess}
}

// sqliteIterationSource wraps a TenantSession for iteration reads (kept as a
// tiny struct so the accessors below stay testable without the agent).
type sqliteIterationSource struct {
	session interface {
		SessionService() *sqlite.SessionService
		TenantID() int64
	}
}

// iterationRecords returns the committed iteration records of a turn.
func (s *sqliteIterationSource) iterationRecords(turnID uint64) ([]sqlite.IterationRecord, error) {
	if s == nil || s.session == nil {
		return nil, nil
	}
	svc := s.session.SessionService()
	if svc == nil {
		return nil, nil
	}
	return svc.GetIterationHistoryByTurn(s.session.TenantID(), turnID)
}

// iterationRecord returns one committed iteration record.
func (s *sqliteIterationSource) iterationRecord(turnID uint64, iteration int) (sqlite.IterationRecord, bool, error) {
	if s == nil || s.session == nil {
		return sqlite.IterationRecord{}, false, nil
	}
	svc := s.session.SessionService()
	if svc == nil {
		return sqlite.IterationRecord{}, false, nil
	}
	return svc.GetIterationHistoryByNumber(s.session.TenantID(), turnID, iteration)
}

// recordToBlock converts one DB iteration row to its wire block.
func recordToBlock(rec sqlite.IterationRecord) protocol.IterationBlock {
	tools := decodeRecordTools(rec.Tools)
	if tools == nil {
		tools = []protocol.ToolProgress{}
	}
	for i := range tools {
		if tools[i].Iteration == 0 {
			tools[i].Iteration = rec.Iteration
		}
	}
	return protocol.IterationBlock{
		TurnID:    rec.TurnID,
		Iteration: rec.Iteration,
		Content:   rec.Content,
		Reasoning: rec.Reasoning,
		Tools:     tools,
	}
}

// decodeRecordTools decodes the persisted tool snapshot JSON of an iteration.
func decodeRecordTools(raw string) []protocol.ToolProgress {
	if raw == "" {
		return nil
	}
	var snaps []IterationToolSnapshot
	if err := json.Unmarshal([]byte(raw), &snaps); err != nil {
		return nil
	}
	out := make([]protocol.ToolProgress, 0, len(snaps))
	for _, t := range snaps {
		out = append(out, protocol.ToolProgress{
			Name:           t.Name,
			Label:          t.Label,
			Status:         canonicalToolStatus(t.Status),
			Elapsed:        t.ElapsedMS,
			Summary:        t.Summary,
			Args:           t.Args,
			Detail:         t.Detail,
			ToolHints:      t.ToolHints,
			UIMode:         t.UIMode,
			UILibs:         t.UILibs,
			UISurface:      t.UISurface,
			CallID:         t.CallID,
			ProviderCallID: t.ProviderCallID,
		})
	}
	return out
}

// IterationBlock returns the authoritative block of one iteration:
// the committed record when it exists, otherwise the LIVE in-flight iteration
// (which is not persisted yet). (nil, nil) means "absent" — the only legal
// answer for an iteration number that was skipped or never existed.
func (a *Agent) IterationBlock(channelName, chatID string, turnID uint64, iteration int) (*protocol.IterationBlock, error) {
	if turnID == 0 || iteration <= 0 {
		return nil, nil
	}
	progressKey := qualifyChatID(channelName, chatID)
	if live := a.liveIterationBlock(progressKey, turnID, iteration); live != nil {
		return live, nil
	}
	src := a.stateSession(channelName, chatID)
	if src == nil {
		return nil, nil
	}
	rec, ok, err := src.iterationRecord(turnID, iteration)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	block := recordToBlock(rec)
	return &block, nil
}

// liveIterationBlock returns the in-flight iteration's block when it matches
// (turn, iteration) exactly. Matching is strict: the live snapshot is the ONLY
// source for an iteration that has not been committed yet.
func (a *Agent) liveIterationBlock(progressKey string, turnID uint64, iteration int) *protocol.IterationBlock {
	raw, ok := a.lastProgressSnapshot.Load(progressKey)
	if !ok {
		return nil
	}
	pe, ok := raw.(*protocol.ProgressEvent)
	if !ok || pe == nil {
		return nil
	}
	if pe.TurnID != 0 && pe.TurnID != turnID {
		return nil
	}
	if pe.Iteration != iteration {
		return nil
	}
	content := pe.Content
	if pe.StreamContent != "" {
		content = pe.StreamContent
	}
	reasoning := pe.Reasoning
	if pe.ReasoningStreamContent != "" {
		reasoning = pe.ReasoningStreamContent
	}
	tools := make([]protocol.ToolProgress, 0, len(pe.ActiveTools)+len(pe.CompletedTools))
	tools = append(tools, pe.CompletedTools...)
	tools = append(tools, pe.ActiveTools...)
	for i := range tools {
		if tools[i].Iteration == 0 {
			tools[i].Iteration = iteration
		}
	}
	return &protocol.IterationBlock{
		TurnID:    turnID,
		Iteration: iteration,
		Content:   content,
		Reasoning: reasoning,
		Tools:     tools,
	}
}

// TurnSnapshot returns one turn's committed blocks plus its closing record.
// Final is nil while the turn is still running (or when no closing record was
// ever written) — an honest "not closed yet", never a fabricated one.
func (a *Agent) TurnSnapshot(channelName, chatID string, turnID uint64) (*protocol.TurnSnapshot, error) {
	if turnID == 0 {
		return nil, nil
	}
	src := a.stateSession(channelName, chatID)
	if src == nil {
		return nil, nil
	}
	recs, err := src.iterationRecords(turnID)
	if err != nil {
		return nil, err
	}
	progressKey := qualifyChatID(channelName, chatID)
	blocks := make([]protocol.IterationBlock, 0, len(recs))
	for _, rec := range recs {
		blocks = append(blocks, recordToBlock(rec))
	}
	// The in-flight iteration of this turn is part of the turn's content.
	if !a.cursorTurnRunning(progressKey, turnID) {
		// Turn finished: DB records are complete.
	} else if live := a.liveIterationBlock(progressKey, turnID, a.liveIterationOf(progressKey, turnID)); live != nil {
		blocks = append(blocks, *live)
	}
	if len(blocks) == 0 && !a.cursorTurnRunning(progressKey, turnID) {
		return nil, nil
	}
	snap := &protocol.TurnSnapshot{TurnID: turnID, Blocks: blocks}
	if !a.cursorTurnRunning(progressKey, turnID) {
		content := ""
		if len(blocks) > 0 {
			content = blocks[len(blocks)-1].Content
		}
		snap.Final = &protocol.TurnFinalFrame{TurnID: turnID, Content: content}
	}
	return snap, nil
}

// liveIterationOf returns the live snapshot's iteration when it belongs to the
// given turn (0 otherwise).
func (a *Agent) liveIterationOf(progressKey string, turnID uint64) int {
	raw, ok := a.lastProgressSnapshot.Load(progressKey)
	if !ok {
		return 0
	}
	pe, ok := raw.(*protocol.ProgressEvent)
	if !ok || pe == nil {
		return 0
	}
	if pe.TurnID != 0 && pe.TurnID != turnID {
		return 0
	}
	return pe.Iteration
}

// cursorTurnRunning reports whether the given turn is still producing.
func (a *Agent) cursorTurnRunning(progressKey string, turnID uint64) bool {
	if turnID == 0 {
		return false
	}
	return a.getActiveTurnID(progressKey) == turnID
}

// SessionState assembles the whole-session snapshot used by
// GET /api/session-state: the last N turns' blocks, the (at most one) in-flight
// iteration and the turn-independent session fields.
//
// Self-consistency (contract C-12) is enforced HERE, not in the transport:
// the live iteration is dropped unless it is strictly newer than the newest
// committed block of its turn, so a snapshot can never contain the same
// iteration twice.
func (a *Agent) SessionState(channelName, chatID string) (*protocol.SessionStateSnapshot, error) {
	progressKey := qualifyChatID(channelName, chatID)
	snapshot := &protocol.SessionStateSnapshot{}

	// ── committed turns ────────────────────────────────────────────────
	src := a.stateSession(channelName, chatID)
	var turnIDs []uint64
	if src != nil && src.session != nil {
		var maxTurn uint64
		if sess, ok := src.session.(interface{ GetMaxTurnID() (uint64, error) }); ok {
			if v, err := sess.GetMaxTurnID(); err == nil {
				maxTurn = v
			}
		}
		start := uint64(1)
		if maxTurn > stateTurnWindow {
			start = maxTurn - stateTurnWindow + 1
		}
		for t := maxTurn; t >= start && t >= 1; t-- {
			turnIDs = append(turnIDs, t)
			if t == 0 {
				break
			}
		}
		for i, j := 0, len(turnIDs)-1; i < j; i, j = i+1, j-1 {
			turnIDs[i], turnIDs[j] = turnIDs[j], turnIDs[i]
		}
	}

	turns := make([]protocol.SessionStateTurn, 0, len(turnIDs))
	for _, turnID := range turnIDs {
		recs, err := src.iterationRecords(turnID)
		if err != nil {
			return nil, fmt.Errorf("read iteration history for turn %d: %w", turnID, err)
		}
		if len(recs) > stateIterWindow {
			recs = recs[len(recs)-stateIterWindow:]
		}
		blocks := make([]protocol.IterationBlock, 0, len(recs))
		for _, rec := range recs {
			blocks = append(blocks, recordToBlock(rec))
		}
		st := protocol.SessionStateTurn{TurnID: turnID, Blocks: blocks}
		if !a.cursorTurnRunning(progressKey, turnID) && len(blocks) > 0 {
			st.Final = &protocol.TurnFinalFrame{TurnID: turnID, Content: blocks[len(blocks)-1].Content}
		}
		turns = append(turns, st)
	}
	snapshot.Turns = turns

	// ── the single in-flight iteration ─────────────────────────────────
	snapshot.Live = consistentLive(turns, a.liveSnapshotBlock(progressKey))

	// ── session fields ─────────────────────────────────────────────────
	snapshot.Sess = protocol.SessionStateSession{
		Running: a.IsProcessingByChannel(channelName, chatID),
		Todos:   a.GetTodos(channelName, chatID),
		Queue:   a.QueueSnapshotFor(channelName, chatID),
	}
	if snapshot.Sess.Todos == nil {
		snapshot.Sess.Todos = []protocol.TodoItem{}
	}
	if snapshot.Sess.Queue == nil {
		snapshot.Sess.Queue = []protocol.QueueItemPayload{}
	}
	if a.goalManager != nil {
		snapshot.Sess.Goal = a.goalManager.GoalInfo(progressKey)
	}
	return snapshot, nil
}

// consistentLive enforces the snapshot self-consistency invariant (C-12):
// the in-flight iteration is kept ONLY when it is strictly newer than the
// newest committed block of the SAME turn. Anything else would render the same
// iteration twice (once as a committed block, once as live) — the classic
// "same number block painted two times" flicker.
//
// A live iteration belonging to a NEWER turn is always kept: its turn simply
// has no committed blocks in the window yet.
func consistentLive(turns []protocol.SessionStateTurn, live *protocol.SessionStateLive) *protocol.SessionStateLive {
	if live == nil || live.Iteration <= 0 {
		return nil
	}
	lastCommitted := 0
	for _, t := range turns {
		if t.TurnID != live.TurnID {
			continue
		}
		for _, b := range t.Blocks {
			if b.Iteration > lastCommitted {
				lastCommitted = b.Iteration
			}
		}
	}
	if live.Iteration <= lastCommitted {
		return nil
	}
	return live
}

// liveSnapshotBlock converts the live progress snapshot into the wire form.
func (a *Agent) liveSnapshotBlock(progressKey string) *protocol.SessionStateLive {
	raw, ok := a.lastProgressSnapshot.Load(progressKey)
	if !ok {
		return nil
	}
	pe, ok := raw.(*protocol.ProgressEvent)
	if !ok || pe == nil || pe.Iteration <= 0 {
		return nil
	}
	content := pe.Content
	if pe.StreamContent != "" {
		content = pe.StreamContent
	}
	reasoning := pe.Reasoning
	if pe.ReasoningStreamContent != "" {
		reasoning = pe.ReasoningStreamContent
	}
	tools := make([]protocol.ToolProgress, 0, len(pe.ActiveTools)+len(pe.CompletedTools))
	tools = append(tools, pe.CompletedTools...)
	tools = append(tools, pe.ActiveTools...)
	for i := range tools {
		if tools[i].Iteration == 0 {
			tools[i].Iteration = pe.Iteration
		}
	}
	return &protocol.SessionStateLive{
		TurnID:    pe.TurnID,
		Iteration: pe.Iteration,
		Content:   content,
		Reasoning: reasoning,
		Tools:     tools,
	}
}
