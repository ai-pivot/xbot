package agent

import (
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Normalized tool array (STATE-MODEL P3 / C-8 / G-5 / G-6)
// ---------------------------------------------------------------------------
//
// The legacy wire shape sends three parallel arrays — ActiveTools (the current
// batch, pre-marked pending), CompletedTools (accumulated) and StreamingTools
// (arguments still generating) — and entries of older iterations linger in them
// until the next beginIteration. That is why every client had to filter by
// iteration and dedupe by a fallback key.
//
// The normalized `tools[]` field carries exactly the current iteration's tool
// set, each entry with a stable call_id + iteration + status. The legacy arrays
// are still sent unchanged (dual-write) so existing clients are unaffected.
// ---------------------------------------------------------------------------

// toolStatusRank orders tool statuses for monotonic merges. A tool's status may
// only move forward (generating → pending → running → done|failed); a later
// frame carrying an earlier status must not regress the entry (that is the
// "running → queued again" flicker users reported).
func toolStatusRank(status string) int {
	switch status {
	case "generating":
		return 1
	case "pending":
		return 2
	case "running":
		return 3
	case "done", "failed", "error":
		return 4
	}
	return 0
}

// canonicalToolStatus maps the engine's legacy "error" onto the wire protocol's
// terminal "failed" so clients have a single terminal-name family.
func canonicalToolStatus(status string) string {
	if status == string(ToolError) {
		return "failed"
	}
	return status
}

// toolProgressToWire converts one engine tool progress entry to the wire shape.
func toolProgressToWire(t ToolProgress) protocol.ToolProgress {
	return protocol.ToolProgress{
		Name:      t.Name,
		Label:     t.Label,
		Status:    canonicalToolStatus(string(t.Status)),
		Elapsed:   t.Elapsed.Milliseconds(),
		Iteration: t.Iteration,
		Summary:   t.Summary,
		Detail:    t.Detail,
		Args:      t.Args,
		ToolHints: t.ToolHints,
		UIMode:    t.UIMode,
		UILibs:    t.UILibs,
		UISurface: t.UISurface,
		CallID:    t.CallID,
	}
}

// toolMergeKey is the identity used to dedupe a tool inside one iteration.
// call_id is the only identity (P2) — the name/label fallback exists solely for
// synthetic entries the engine creates without an id (e.g. `reqerr`), which are
// never two distinct tools of the same iteration.
func toolMergeKey(t protocol.ToolProgress) string {
	if t.CallID != "" {
		return "id:" + t.CallID
	}
	if t.Label != "" {
		return "label:" + t.Name + "#" + t.Label
	}
	return "name:" + t.Name
}

// normalizeIterationTools merges ActiveTools + CompletedTools into the single
// normalized array of the CURRENT iteration:
//
//   - only entries belonging to `iteration` are kept (an entry without an
//     iteration stamp is treated as belonging to it — legacy synthetic tools);
//   - duplicates (the same call appears as "active pending" and "completed
//     done") are merged, keeping the HIGHER status and the richer payload, so
//     the status sequence a client observes is monotone;
//   - `interruptedAsFailed` downgrades non-terminal statuses to "failed" — used
//     on cancellation so an interrupted `running` tool never silently vanishes
//     from the committed block (C-6).
//
// The returned slice is nil when the iteration has no tools (the caller's
// json tag omits it; `done` blocks always materialize an empty slice instead).
func normalizeIterationTools(active, completed []ToolProgress, iteration int, interruptedAsFailed bool) []protocol.ToolProgress {
	if len(active) == 0 && len(completed) == 0 {
		return nil
	}
	merged := make(map[string]protocol.ToolProgress, len(active)+len(completed))
	order := make([]string, 0, len(active)+len(completed))
	add := func(t ToolProgress) {
		if t.Iteration != 0 && iteration > 0 && t.Iteration != iteration {
			return // belongs to another iteration — never carried in this frame
		}
		w := toolProgressToWire(t)
		if w.Iteration == 0 && iteration > 0 {
			w.Iteration = iteration
		}
		key := toolMergeKey(w)
		prev, ok := merged[key]
		if !ok {
			merged[key] = w
			order = append(order, key)
			return
		}
		merged[key] = mergeToolProgress(prev, w)
	}
	for _, t := range completed {
		add(t)
	}
	for _, t := range active {
		add(t)
	}
	if interruptedAsFailed {
		for k, w := range merged {
			switch w.Status {
			case "pending", "running", "generating", "":
				w.Status = "failed"
				merged[k] = w
			}
		}
	}
	out := make([]protocol.ToolProgress, 0, len(order))
	for _, k := range order {
		out = append(out, merged[k])
	}
	return out
}

// mergeToolProgress merges a newer entry into an older one without ever
// regressing the status (C-8) and without dropping fields the newer entry does
// not carry (a `state`-only update must not erase the `meta` payload).
func mergeToolProgress(prev, next protocol.ToolProgress) protocol.ToolProgress {
	if toolStatusRank(next.Status) < toolStatusRank(prev.Status) {
		next.Status = prev.Status
	}
	if next.Name == "" {
		next.Name = prev.Name
	}
	if next.Label == "" {
		next.Label = prev.Label
	}
	if next.Summary == "" {
		next.Summary = prev.Summary
	}
	if next.Detail == "" {
		next.Detail = prev.Detail
	}
	if next.Args == "" {
		next.Args = prev.Args
	}
	if next.ToolHints == "" {
		next.ToolHints = prev.ToolHints
	}
	if next.UIMode == "" {
		next.UIMode = prev.UIMode
	}
	if len(next.UILibs) == 0 {
		next.UILibs = prev.UILibs
	}
	if next.UISurface == nil {
		next.UISurface = prev.UISurface
	}
	if next.CallID == "" {
		next.CallID = prev.CallID
	}
	if next.ProviderCallID == "" {
		next.ProviderCallID = prev.ProviderCallID
	}
	if next.Elapsed == 0 {
		next.Elapsed = prev.Elapsed
	}
	if next.GenChars == 0 {
		next.GenChars = prev.GenChars
	}
	if next.Iteration == 0 {
		next.Iteration = prev.Iteration
	}
	return next
}

// blockTools returns the tool list of a `done` block: always a non-nil slice
// (P5: an empty set is `[]`, never a missing field).
func blockTools(active, completed []ToolProgress, iteration int, cancelled bool) []protocol.ToolProgress {
	tools := normalizeIterationTools(active, completed, iteration, cancelled)
	if tools == nil {
		return []protocol.ToolProgress{}
	}
	return tools
}

// iterationBlockFromSnapshot builds the authoritative `done` block of one
// iteration from its engine snapshot. A cancelled iteration downgrades every
// non-terminal tool to `failed` — an interrupted tool must be visible in the
// committed block, never silently dropped (contract C-6).
func iterationBlockFromSnapshot(turnID uint64, snap IterationSnapshot, cancelled bool) protocol.IterationBlock {
	tools := make([]protocol.ToolProgress, 0, len(snap.Tools))
	for _, t := range snap.Tools {
		status := canonicalToolStatus(t.Status)
		if cancelled {
			switch status {
			case "pending", "running", "generating", "":
				status = "failed"
			}
		}
		tools = append(tools, protocol.ToolProgress{
			Name:      t.Name,
			Label:     t.Label,
			Status:    status,
			Elapsed:   t.ElapsedMS,
			Iteration: snap.Iteration,
			Summary:   t.Summary,
			Detail:    t.Detail,
			Args:      t.Args,
			ToolHints: t.ToolHints,
			UIMode:    t.UIMode,
			UILibs:    t.UILibs,
			UISurface: t.UISurface,
			CallID:    t.CallID,
		})
	}
	return protocol.IterationBlock{
		TurnID:    turnID,
		Iteration: snap.Iteration,
		Content:   snap.Content,
		Reasoning: snap.Reasoning,
		Tools:     tools,
		Cancelled: cancelled,
	}
}
