package protocol

// ---------------------------------------------------------------------------
// Turn-level frames: `done` (one per started iteration) and `final` (one per
// turn). See docs in xbot-harmony/docs/STATE-MODEL.md §3.3 (frame language),
// §14.1 (contracts C-3..C-10) and §14.3 (CI criteria G-3..G-8).
//
// These frames are ADDITIVE: the legacy `progress_structured` payload keeps
// carrying active_tools/completed_tools/streaming_tools/iteration_history
// unchanged (dual-write), so existing web / mobile clients keep working.
// ---------------------------------------------------------------------------

// IterationBlock is the COMPLETE, authoritative content of exactly one
// iteration of one turn. It is the payload of the `done` frame and the element
// type of `final.blocks`.
//
// Contract C-6: every started iteration has exactly one `done`, and the block
// carries ALL of that iteration's visible content — body text, reasoning and
// the full tool set (interrupted `running` tools are downgraded to `failed`, so
// nothing silently disappears at commit time).
type IterationBlock struct {
	// TurnID is the server-assigned turn this iteration belongs to (P2).
	TurnID uint64 `json:"turn_id"`
	// Iteration is 1-based and strictly increasing within a turn (C-4).
	Iteration int `json:"iteration"`
	// Content is the accumulated assistant text of this iteration.
	Content string `json:"content"`
	// Reasoning is the accumulated reasoning/thinking text of this iteration.
	Reasoning string `json:"reasoning"`
	// Tools is the iteration's complete tool set. It is ALWAYS present (an
	// empty set is `[]`, never a missing field — P5: missing != empty).
	Tools []ToolProgress `json:"tools"`
	// Cancelled marks an iteration that was interrupted by the user or by
	// context cancellation.
	Cancelled bool `json:"cancelled,omitempty"`
}

// TurnFinalFrame is the payload of the `final` frame: exactly one per turn
// (including empty and cancelled turns — contract C-7).
type TurnFinalFrame struct {
	// TurnID identifies the turn being closed.
	TurnID uint64 `json:"turn_id"`
	// Content is the turn's final assistant text ("" for an empty turn).
	Content string `json:"content"`
	// Blocks, when carried, REPLACES the turn's block list wholesale
	// (server authority). Omitted = "not carried" (clients fall back to the
	// blocks they already committed via `done` frames).
	Blocks []IterationBlock `json:"blocks,omitempty"`
	// DroppedBefore = how many leading blocks were truncated from Blocks so the
	// payload stays bounded (rendered as "更早的 N 个").
	DroppedBefore int `json:"dropped_before,omitempty"`
	// Cancelled marks a turn that ended by user cancel / context cancel.
	Cancelled bool `json:"cancelled,omitempty"`
	// Recovered marks a turn closed by server restart recovery (C-13).
	Recovered bool `json:"recovered,omitempty"`
}

// ---------------------------------------------------------------------------
// /api/session-state snapshot (P6: REST provides an authoritative snapshot
// stamped with (epoch, seq); it never participates in live reconciliation)
// ---------------------------------------------------------------------------

// SessionStateLive is the (at most one) in-flight iteration. It is always
// self-consistent with the turn's committed blocks: Live.Iteration is strictly
// greater than the last committed iteration of that turn (C-12).
type SessionStateLive struct {
	TurnID    uint64         `json:"turn_id"`
	Iteration int            `json:"iteration"`
	Content   string         `json:"content"`
	Reasoning string         `json:"reasoning"`
	Tools     []ToolProgress `json:"tools"`
}

// SessionStateTurn is one committed turn in the snapshot window.
type SessionStateTurn struct {
	TurnID uint64           `json:"turn"`
	Blocks []IterationBlock `json:"blocks"`
	// Final is nil while the turn has no authoritative closing record yet
	// (still running, or closed by a path that did not record one).
	Final *TurnFinalFrame `json:"final,omitempty"`
}

// TurnSnapshot is the /api/turn response body (one turn's authoritative
// content): every committed iteration block plus the closing record, if any.
// Final == nil means "the turn has not been closed" (still running / no final
// recorded) — that is a legitimate, non-error answer (contract C-7 detection).
type TurnSnapshot struct {
	TurnID uint64           `json:"turn"`
	Blocks []IterationBlock `json:"blocks"`
	Final  *TurnFinalFrame  `json:"final,omitempty"`
}

// SessionStateSession carries the turn-independent session fields.
type SessionStateSession struct {
	Running bool               `json:"running"`
	Todos   []TodoItem         `json:"todos"`
	Goal    *GoalInfo          `json:"goal"`
	Queue   []QueueItemPayload `json:"queue"`
}

// SessionStateSnapshot is the /api/session-state response body's `state` part.
type SessionStateSnapshot struct {
	// Epoch identifies this server process instance (restart detection, C-13).
	Epoch string `json:"epoch"`
	// Seq is the per-chat frame watermark this snapshot was taken at. Frames
	// with seq <= this value are already reflected here (P6 / C-12).
	Seq   uint64              `json:"seq"`
	Turns []SessionStateTurn  `json:"turns"`
	Live  *SessionStateLive   `json:"live"`
	Sess  SessionStateSession `json:"session"`
}
