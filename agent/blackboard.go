package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/channel"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// blackboardNotifyWindow is the coalescing window per (session, board): a burst
// of changes on one board reaches a watcher as ONE notification instead of one
// per keystroke of the plan. Coalescing is leading + trailing, so a change that
// lands inside the window is still delivered (deferred), never dropped.
const blackboardNotifyWindow = 3 * time.Second

// BlackboardService exposes the shared blackboard storage (RPC layer / tests).
func (a *Agent) BlackboardService() *sqlite.BlackboardService { return a.blackboardSvc }

// BlackboardChanged publishes a board change made outside the tool path (the web
// UI writing on the board). Same single publish point as the tool, so the live
// UI and every watcher see human edits exactly like agent edits.
func (a *Agent) BlackboardChanged(ev tools.BlackboardEvent) {
	if a.blackboardHub != nil {
		a.blackboardHub.PublishBlackboardChange(ev)
	}
}

// blackboardHub is the runtime side of the shared blackboard: it fans accepted
// changes out to web clients (live UI) and delivers a coalesced digest to the
// sessions that explicitly watch a board — through the SAME background
// notification pipeline as cron/webhook/peer messages, so a busy agent gets the
// digest injected into its current iteration and an idle one wakes for a turn.
//
// Watching is opt-in per session and lives in memory: it is a subscription, not
// durable state, and losing it on restart is correct (an agent that wants the
// feed re-subscribes; the board itself is durable).
type blackboardHub struct {
	agent *Agent

	// window is the coalescing window per (session, board). A var (not a const)
	// so the coalescing rules are testable without sleeping for seconds.
	window time.Duration

	mu      sync.Mutex
	watches map[string]map[string]string // sessionKey -> board -> prefix ("" = all)
	// lastSent / pending / timers implement the per-(session, board) window.
	lastSent map[blackboardWatchKey]int64
	pending  map[blackboardWatchKey][]string
	timers   map[blackboardWatchKey]*time.Timer
}

type blackboardWatchKey struct {
	session string
	board   string
}

func newBlackboardHub(a *Agent) *blackboardHub {
	return &blackboardHub{
		agent:    a,
		window:   blackboardNotifyWindow,
		watches:  map[string]map[string]string{},
		lastSent: map[blackboardWatchKey]int64{},
		pending:  map[blackboardWatchKey][]string{},
		timers:   map[blackboardWatchKey]*time.Timer{},
	}
}

// WatchBoard subscribes a session to a board. Idempotent: re-watching updates
// the prefix filter (the latest call wins) rather than stacking subscriptions.
func (h *blackboardHub) WatchBoard(sessionKey, board, prefix string) {
	if sessionKey == "" || board == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watches[sessionKey] == nil {
		h.watches[sessionKey] = map[string]string{}
	}
	h.watches[sessionKey][board] = prefix
}

// UnwatchBoard drops a subscription, reporting whether one existed.
func (h *blackboardHub) UnwatchBoard(sessionKey, board string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	boards, ok := h.watches[sessionKey]
	if !ok {
		return false
	}
	if _, ok := boards[board]; !ok {
		return false
	}
	delete(boards, board)
	if len(boards) == 0 {
		delete(h.watches, sessionKey)
	}
	key := blackboardWatchKey{session: sessionKey, board: board}
	if t := h.timers[key]; t != nil {
		t.Stop()
		delete(h.timers, key)
	}
	delete(h.pending, key)
	delete(h.lastSent, key)
	return true
}

// ListWatches returns a session's subscriptions.
func (h *blackboardHub) ListWatches(sessionKey string) []tools.BlackboardWatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	boards := h.watches[sessionKey]
	out := make([]tools.BlackboardWatch, 0, len(boards))
	for board, prefix := range boards {
		out = append(out, tools.BlackboardWatch{Board: board, Prefix: prefix})
	}
	return out
}

// PublishBlackboardChange is the tools-side entry point: it pushes the change to
// the live UI and, for every watching session, queues a coalesced digest. It
// never blocks the writer: the only work here is map updates plus a channel send
// into the notification pipeline.
func (h *blackboardHub) PublishBlackboardChange(ev tools.BlackboardEvent) {
	h.agent.broadcastBlackboardUpdate(&protocol.BlackboardUpdatePayload{
		Board: ev.Board, Key: ev.Key, Op: ev.Op, Revision: ev.Revision, Actor: ev.Actor,
		Kind: ev.Kind, Title: ev.Title, Closed: ev.Closed, ClaimedBy: ev.ClaimedBy, At: ev.At,
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UnixMilli()
	line := blackboardEventLine(ev)
	for session, boards := range h.watches {
		prefix, ok := boards[ev.Board]
		if !ok || (prefix != "" && !strings.HasPrefix(ev.Key, prefix)) {
			continue
		}
		// A session must never be woken by its own write.
		if session == ev.Actor {
			continue
		}
		key := blackboardWatchKey{session: session, board: ev.Board}
		if now-h.lastSent[key] >= h.window.Milliseconds() {
			h.lastSent[key] = now
			queued := append(h.pending[key], line)
			delete(h.pending, key)
			h.deliver(session, ev.Board, queued)
			continue
		}
		h.pending[key] = append(h.pending[key], line)
		if h.timers[key] == nil {
			remaining := h.window - time.Duration(now-h.lastSent[key])*time.Millisecond
			h.timers[key] = time.AfterFunc(remaining, func() { h.flush(key) })
		}
	}
}

// flush delivers whatever was coalesced during the window and clears the state.
func (h *blackboardHub) flush(key blackboardWatchKey) {
	h.mu.Lock()
	lines := h.pending[key]
	delete(h.pending, key)
	delete(h.timers, key)
	if len(lines) > 0 {
		h.lastSent[key] = time.Now().UnixMilli()
	}
	h.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	h.deliver(key.session, key.board, lines)
}

// deliver routes a digest to the watching session through the shared background
// notification pipeline (busy ⇒ injected into the current iteration; idle ⇒ a
// new turn). Sessions are addressed by "channel:chatID", which is exactly what
// the pipeline routes on.
func (h *blackboardHub) deliver(session, board string, lines []string) {
	ch, chatID, ok := splitSessionKey(session)
	if !ok || h.agent == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📋 黑板 %s 有 %d 处变更（%s）：\n", board, len(lines), session)
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	b.WriteString("需要时用 Blackboard(action=\"list\") 查看整块板；不想被打扰就 Blackboard(action=\"unwatch\")。")
	h.agent.injectAsyncMessage(ch, chatID, "", b.String(), tools.AsyncSourceBlackboard)
}

// blackboardEventLine renders one change for a digest.
func blackboardEventLine(ev tools.BlackboardEvent) string {
	title := ev.Title
	if title == "" {
		title = ev.Key
	}
	return fmt.Sprintf("%s：%s（%s） · rev %d", ev.Key, labelForOp(ev), title, ev.Revision)
}

func labelForOp(ev tools.BlackboardEvent) string {
	switch ev.Op {
	case "post":
		return "新增条目"
	case "update":
		return "内容已更新"
	case "claim":
		if ev.ClaimedBy != "" {
			return "已被 " + ev.ClaimedBy + " 认领"
		}
		return "已被认领"
	case "release":
		return "认领已释放"
	case "close":
		return "已完成/关闭"
	case "reopen":
		return "重新打开"
	case "delete":
		return "条目已删除"
	default:
		return ev.Op
	}
}

// splitSessionKey splits "channel:chatID" (chatID may contain ':').
func splitSessionKey(sessionKey string) (string, string, bool) {
	i := strings.Index(sessionKey, ":")
	if i <= 0 || i == len(sessionKey)-1 {
		return "", "", false
	}
	return sessionKey[:i], sessionKey[i+1:], true
}

// broadcastBlackboardUpdate fans a board change out to every channel that can
// show it. Interface existence check, never a hardcoded channel name — the Web
// channel implements it as a seq=0 broadcast to ALL web clients (a board is
// shared across sessions, so route-scoped delivery would miss the clients that
// most need the update).
func (a *Agent) broadcastBlackboardUpdate(p *protocol.BlackboardUpdatePayload) {
	if a.channelRange == nil || p == nil {
		return
	}
	a.channelRange(func(_ string, c channel.Channel) bool {
		if sender, ok := c.(channel.BlackboardUpdateSender); ok {
			sender.SendBlackboardUpdate(p)
		}
		return true
	})
}
