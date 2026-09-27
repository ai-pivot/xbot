package serverapp

import (
	"context"
	"fmt"
	"time"

	"xbot/storage/sqlite"
	"xbot/tools"
)

// blackboardHumanActor labels board writes that came from the UI rather than
// from an agent, so the audit trail (and the watchers' digests) can tell them
// apart. Agents write through the Blackboard tool and carry their session key.
const blackboardHumanActor = "human"

// registerBlackboardHandlers exposes the shared blackboard to the web UI: the
// panel reads the board of the session it is showing, and the operator can
// write on it (post / close / reopen / release / delete) with the same
// guarantees agents get — revision CAS and lease semantics live in the storage
// layer, not in this file.
func registerBlackboardHandlers(t RPCTable, h *RPCContext) {
	// blackboard_boards lists every board with its derived counts (the panel's
	// picker): totals come from SQL, blocked/claimed from the same derivation the
	// tool uses.
	t["blackboard_boards"] = rpc0err(func(ctx context.Context) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		boards, err := svc.Boards()
		if err != nil {
			return nil, err
		}
		return map[string]any{"boards": boards}, nil
	})

	// blackboard_list returns the entries of one board. An empty board means "the
	// board of this session" (channel:chatID) — the same default the tool uses, so
	// the panel shows exactly what the agent sees. Bodies are omitted unless asked
	// for: a panel renders digests, and a board can hold a lot of text.
	t["blackboard_list"] = rpc1(func(ctx context.Context, p struct {
		Board         string `json:"board"`
		Channel       string `json:"channel"`
		ChatID        string `json:"chat_id"`
		Prefix        string `json:"prefix"`
		IncludeClosed bool   `json:"include_closed"`
		IncludeBody   bool   `json:"include_body"`
		Limit         int    `json:"limit"`
	}) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		board := p.Board
		if board == "" {
			board = channelName + ":" + chatID
		}
		entries, err := svc.List(sqlite.BlackboardListOptions{
			Board: board, Prefix: p.Prefix, IncludeClosed: p.IncludeClosed,
			IncludeBody: p.IncludeBody, Limit: p.Limit,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"board": board, "entries": entries}, nil
	})

	// blackboard_get returns one entry WITH its body (the panel expands a row on
	// demand instead of shipping every body in the list).
	t["blackboard_get"] = rpc1(func(ctx context.Context, p struct {
		Board string `json:"board"`
		Key   string `json:"key"`
	}) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		entry, err := svc.Get(p.Board, p.Key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"entry": entry}, nil
	})

	// blackboard_post writes an entry from the UI.
	t["blackboard_post"] = rpc1(func(ctx context.Context, p struct {
		Board     string   `json:"board"`
		Channel   string   `json:"channel"`
		ChatID    string   `json:"chat_id"`
		Key       string   `json:"key"`
		Kind      string   `json:"kind"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Status    string   `json:"status"`
		BlockedBy []string `json:"blocked_by"`
	}) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		board := p.Board
		if board == "" {
			board = channelName + ":" + chatID
		}
		entry, err := svc.Post(&sqlite.BlackboardEntry{
			Board: board, Key: p.Key, Kind: p.Kind, Title: p.Title,
			Body: p.Body, Status: p.Status, BlockedBy: p.BlockedBy,
			CreatedBy: blackboardHumanActor,
		})
		if err != nil {
			return nil, err
		}
		publishBlackboardChange(h, "post", entry)
		return map[string]any{"entry": entry}, nil
	})

	// blackboard_close marks an entry resolved (closed=true) or reopens it.
	// expected_revision is optional here: a human acting on the board should not
	// have to have read a revision first. The CAS is still enforced against the
	// revision read in this call, so a concurrent agent write surfaces as a
	// conflict instead of being silently overwritten.
	t["blackboard_close"] = rpc1(func(ctx context.Context, p struct {
		Board            string `json:"board"`
		Key              string `json:"key"`
		Closed           bool   `json:"closed"`
		ExpectedRevision int64  `json:"expected_revision"`
	}) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		revision, err := blackboardRevisionOr(p.ExpectedRevision, func() (int64, error) {
			cur, err := svc.Get(p.Board, p.Key)
			if err != nil {
				return 0, err
			}
			return cur.Revision, nil
		})
		if err != nil {
			return nil, err
		}
		entry, err := svc.SetClosed(p.Board, p.Key, p.Closed, revision)
		if err != nil {
			return nil, err
		}
		op := "close"
		if !p.Closed {
			op = "reopen"
		}
		publishBlackboardChange(h, op, entry)
		return map[string]any{"entry": entry}, nil
	})

	// blackboard_release frees a lease the operator judges stuck. Force-release
	// (no token) is the documented escape hatch — a wedged holder must never be
	// able to freeze a board.
	t["blackboard_release"] = rpc1(func(ctx context.Context, p struct {
		Board string `json:"board"`
		Key   string `json:"key"`
	}) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		entry, changed, err := svc.Release(p.Board, p.Key, "")
		if err != nil {
			return nil, err
		}
		if changed {
			publishBlackboardChange(h, "release", entry)
		}
		return map[string]any{"entry": entry, "changed": changed}, nil
	})

	// blackboard_delete removes an entry (same optional-CAS rule as close).
	t["blackboard_delete"] = rpc1(func(ctx context.Context, p struct {
		Board            string `json:"board"`
		Key              string `json:"key"`
		ExpectedRevision int64  `json:"expected_revision"`
	}) (any, error) {
		svc := h.Ag.BlackboardService()
		if svc == nil {
			return nil, fmt.Errorf("blackboard not available")
		}
		entry, err := svc.Delete(p.Board, p.Key, p.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		publishBlackboardChange(h, "delete", entry)
		return map[string]any{"ok": true}, nil
	})
}

// blackboardRevisionOr returns the caller's revision when given, else reads the
// live one (the explicit read-modify-write that keeps the CAS meaningful).
func blackboardRevisionOr(expected int64, readCurrent func() (int64, error)) (int64, error) {
	if expected > 0 {
		return expected, nil
	}
	return readCurrent()
}

// publishBlackboardChange reports a UI-originated change through the same single
// publish point agents use, so live panels and watchers see human edits too.
func publishBlackboardChange(h *RPCContext, op string, e *sqlite.BlackboardEntry) {
	if h.Ag == nil || e == nil {
		return
	}
	h.Ag.BlackboardChanged(tools.BlackboardEvent{
		Board: e.Board, Key: e.Key, Op: op, Revision: e.Revision, Actor: blackboardHumanActor,
		Kind: e.Kind, Title: e.Title, Closed: e.Closed, ClaimedBy: e.ClaimedBy, At: time.Now().UnixMilli(),
	})
}
