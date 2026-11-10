package agent

import (
	"sort"
	"strings"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/channel/web"
	"xbot/protocol"
	"xbot/tools"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Session queue shadow state — the single visibility source for the pending
// queue (Web Staging Tray, queue list/cancel REST).
//
// msgCh is a plain Go channel: it cannot be inspected, selectively removed
// from, or position-queried. queuedEntry mirrors every message admitted to
// msgCh until chatProcessLoop dequeues it, giving the queue a queryable
// projection with FIFO order identical to the channel itself.
//
// Lifecycle (single-writer discipline, no races):
//   - append : admitToMsgCh, immediately after the msg lands in msgCh
//   - dequeue: chatProcessLoop, immediately after taking the msg out of msgCh
//   - cancel : CancelQueuedMessage (REST) marks the entry Cancelled; the
//     dequeue then skips processing (the msg is already in msgCh and cannot
//     be removed from a Go channel — skipping at dequeue is the only way).
//
// Invariant: entry order mirrors msgCh's FIFO exactly (append and dequeue
// happen on opposite ends of the same channel), so
//   queue seq = turn_id allocation = dequeue order = processing order.
// ---------------------------------------------------------------------------

// queuedEntry mirrors one message sitting in msgCh (admitted, not yet dequeued).
type queuedEntry struct {
	MsgID      string // msg.RequestID — the cancel handle
	TurnID     uint64 // pre-allocated at admission (0 for answer/resume)
	Content    string // FULL content — the interject path re-sends it on cancel+resend
	Preview    string // first ~80 chars of content (tray rendering)
	Source     string // user | notification | answer | resume | command
	EnqueuedAt int64  // unix millis
	Cancelled  bool   // set by CancelQueuedMessage; dequeue skips processing
}

const queuePreviewRunes = 80

// newQueuedEntry builds the shadow entry for a message entering msgCh.
func newQueuedEntry(msg bus.InboundMessage, turnID uint64) queuedEntry {
	preview := msg.Content
	if r := []rune(preview); len(r) > queuePreviewRunes {
		preview = string(r[:queuePreviewRunes]) + "…"
	}
	return queuedEntry{
		MsgID:      msg.RequestID,
		TurnID:     turnID,
		Content:    msg.Content,
		Preview:    preview,
		Source:     queuedEntrySource(msg),
		EnqueuedAt: time.Now().UnixMilli(),
	}
}

// queuedEntrySource classifies the queue item for tray rendering (👤/🔔 icons).
func queuedEntrySource(msg bus.InboundMessage) string {
	if msg.Metadata != nil {
		if msg.Metadata[bgNotificationMetadataKey] == "true" {
			return "notification"
		}
		if msg.Metadata["ask_user_answered"] == "true" {
			return "answer"
		}
		if msg.Metadata["resume_turn"] == "true" {
			return "resume"
		}
	}
	if strings.HasPrefix(strings.TrimSpace(msg.Content), "/") {
		return "command"
	}
	return "user"
}

// queueAppend records an admitted message. Call AFTER the msg landed in msgCh.
func (ss *bgSessionState) queueAppend(e queuedEntry) {
	ss.queueMu.Lock()
	ss.queue = append(ss.queue, e)
	ss.queueMu.Unlock()
}

// queueDequeue pops the entry matching the message chatProcessLoop just took
// out of msgCh. Returns (cancelled, true) when the entry existed and was
// marked Cancelled while queued — the caller must skip processing it (the
// user cancelled it via the REST queue API). Returns (_, false) on shadow
// miss (unknown message, e.g. admitted before an upgrade) — process normally.
func (ss *bgSessionState) queueDequeue(msgID string) (cancelled bool) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	if len(ss.queue) == 0 {
		return false
	}
	if head := ss.queue[0]; head.MsgID == msgID {
		ss.queue = ss.queue[1:]
		return head.Cancelled
	}
	// Shadow drift: the dequeued message is not the head — a bookkeeping bug.
	// Find by ID so the shadow stays consistent with msgCh.
	for i, e := range ss.queue {
		if e.MsgID == msgID {
			ss.queue = append(ss.queue[:i], ss.queue[i+1:]...)
			if e.Cancelled {
				return true
			}
			log.WithFields(log.Fields{
				"msg_id":   msgID,
				"head_id":  ss.queue[0].MsgID,
				"position": i,
			}).Warn("session queue shadow drift: dequeued message was not the head")
			return false
		}
	}
	return false
}

// queueMarkCancelled marks a queued entry Cancelled. The message stays in
// msgCh (Go channels cannot be selectively drained) — chatProcessLoop skips
// it at dequeue time. Returns false when the message is not queued (already
// processing or unknown).
func (ss *bgSessionState) queueMarkCancelled(msgID string) bool {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	for i := range ss.queue {
		if ss.queue[i].MsgID == msgID && !ss.queue[i].Cancelled {
			ss.queue[i].Cancelled = true
			return true
		}
	}
	return false
}

// queueSnapshot returns a copy of the pending (non-cancelled) entries.
func (ss *bgSessionState) queueSnapshot() []queuedEntry {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	if len(ss.queue) == 0 {
		return nil
	}
	out := make([]queuedEntry, 0, len(ss.queue))
	for _, e := range ss.queue {
		if !e.Cancelled {
			out = append(out, e)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Agent-facing queue API (wired to Web REST via WebCallbacks).
// ---------------------------------------------------------------------------

// QueueSnapshotFor returns the pending queue entries for a session.
// channelName/chatID use the canonical (channel, chatID) pair; the session's
// chatWorker must exist (it is created on the first message of a session).
func (a *Agent) QueueSnapshotFor(channelName, chatID string) []protocol.QueueItemPayload {
	state, ok := a.bgSessionStates.Load(qualifyChatID(channelName, chatID))
	if !ok {
		return nil
	}
	entries := state.(*bgSessionState).queueSnapshot()
	if len(entries) == 0 {
		return nil
	}
	items := make([]protocol.QueueItemPayload, 0, len(entries))
	for _, e := range entries {
		items = append(items, protocol.QueueItemPayload{
			MsgID:      e.MsgID,
			TurnID:     e.TurnID,
			Content:    e.Content,
			Preview:    e.Preview,
			Source:     e.Source,
			EnqueuedAt: e.EnqueuedAt,
		})
	}
	return items
}

// ---------------------------------------------------------------------------
// Reorder (Staging Tray drag-and-drop)
// ---------------------------------------------------------------------------

// queueReorder reorders the shadow queue to match msgIDs — the order the user
// sees in the Staging Tray. IDs that are not queued are ignored, and queued
// entries missing from msgIDs keep their relative order after the listed ones:
// a reorder must never drop or duplicate an entry. Empty ids (notifications
// without a request id) are never ranked, so those entries stay in their
// arrival order relative to each other.
//
// Returns the resulting id order (every pending entry, listed ones first) so
// the caller can project it onto the delivery channel.
func (ss *bgSessionState) queueReorder(msgIDs []string) []string {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()

	rank := make(map[string]int, len(msgIDs))
	for i, id := range msgIDs {
		if id == "" {
			continue // unaddressable entry — keep arrival order
		}
		if _, dup := rank[id]; !dup {
			rank[id] = i
		}
	}

	listed := make([]queuedEntry, 0, len(ss.queue))
	rest := make([]queuedEntry, 0, len(ss.queue))
	for _, e := range ss.queue {
		if _, ok := rank[e.MsgID]; ok {
			listed = append(listed, e)
			continue
		}
		rest = append(rest, e)
	}
	sort.SliceStable(listed, func(i, j int) bool { return rank[listed[i].MsgID] < rank[listed[j].MsgID] })

	ss.queue = append(listed, rest...)

	order := make([]string, 0, len(ss.queue))
	for _, e := range ss.queue {
		order = append(order, e.MsgID)
	}
	return order
}

// reorderChannel re-emits the messages buffered in ch so their delivery order
// matches order (the shadow queue order, id per entry).
//
// Go channels are FIFO and cannot be reordered in place, so the buffered
// messages are drained, stably sorted by their position in order, and pushed
// back. This is safe because the consumer is either blocked on a receive (it
// then takes the NEW head) or busy processing (it takes the new head when it
// returns); nothing can be stolen mid-drain. Messages whose id is not ranked
// (already dequeued and being processed, or an id the client did not send) keep
// their relative channel order. Pushing back cannot overflow: the pushed count
// equals the drained count, and the channel never held more than its capacity.
func reorderChannel(ch chan bus.InboundMessage, order []string) int {
	rank := make(map[string]int, len(order))
	for i, id := range order {
		if id == "" {
			continue
		}
		if _, dup := rank[id]; !dup {
			rank[id] = i
		}
	}

	var drained []bus.InboundMessage
drain:
	for {
		select {
		case m := <-ch:
			drained = append(drained, m)
		default:
			break drain
		}
	}
	sort.SliceStable(drained, func(i, j int) bool {
		ri, oki := rank[drained[i].RequestID]
		rj, okj := rank[drained[j].RequestID]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true // ranked entries first
		case okj:
			return false
		default:
			return false // stable: unranked entries keep drained order
		}
	})
	for _, m := range drained {
		ch <- m
	}
	return len(drained)
}

// queueReorderAdmitWait bounds how long a reorder waits for in-flight admits
// (a producer between queueAppend and the completion of its channel send) to
// land before giving up. With room in the channel the send completes in
// microseconds; a saturated channel only clears when the consumer dequeues.
const queueReorderAdmitWait = 20 * time.Millisecond

// waitForInflightAdmits waits (bounded) until no admit sits between queueAppend
// and its completed channel send. Returns false when one is still in flight
// after the wait: its message is in the shadow queue (already broadcast to
// clients) but not yet in msgCh, so a drain-based projection would miss it and
// silently deliver a different order than the one displayed.
func (ss *bgSessionState) waitForInflightAdmits() bool {
	deadline := time.Now().Add(queueReorderAdmitWait)
	for {
		if ss.inflightAdmits.Load() == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// ReorderQueue reorders a session's pending (queued, not yet started) messages.
// msgIDs is the desired order as rendered by the client (Staging Tray
// drag-and-drop); unknown ids and unlisted entries are handled by queueReorder.
// Returns false when the session has no worker, nothing is queued, the requested
// order is already in effect, or a message was still being admitted (see below)
// — no snapshot broadcast then (a no-op drag must not spam every subscriber).
//
// ORDER CONSISTENCY: the projection (reorderChannel) drains msgCh, sorts the
// buffered messages by the shadow queue order and pushes them back. That is
// exact only while every queued entry is actually IN the channel. A producer
// that has already appended its shadow entry but has not completed its channel
// send (inflightAdmits > 0) would be missed by the drain — reachable when the
// channel is saturated, because then the producer blocks until the consumer
// frees a slot. We wait briefly for such admits to land, and if one is still in
// flight we refuse the reorder (no projection, no broadcast) instead of
// reporting an order we cannot deliver.
func (a *Agent) ReorderQueue(channelName, chatID string, msgIDs []string) bool {
	key := qualifyChatID(channelName, chatID)
	state, ok := a.bgSessionStates.Load(key)
	if !ok {
		return false
	}
	ss := state.(*bgSessionState)

	if ss.msgCh != nil && !ss.waitForInflightAdmits() {
		log.WithField("session_key", key).Warn("Queued messages reorder skipped: an admit is still in flight (saturated msgCh)")
		return false
	}

	before := ss.queueSnapshot()
	order := ss.queueReorder(msgIDs)
	if len(order) == 0 {
		return false
	}
	after := ss.queueSnapshot()
	if queueSameOrder(before, after) {
		return false // already in this order
	}
	if ss.msgCh != nil {
		reorderChannel(ss.msgCh, order)
	}

	log.WithFields(log.Fields{
		"session_key": key,
		"count":       len(order),
		"order":       strings.Join(order, ","),
	}).Info("Queued messages reordered via REST queue API")

	a.emitQueueState(channelName, chatID, ss)
	return true
}

// queueSameOrder reports whether two snapshots list the same ids in the same
// order (both are the non-cancelled projection of the same queue).
func queueSameOrder(a, b []queuedEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].MsgID != b[i].MsgID {
			return false
		}
	}
	return true
}

// CancelQueuedMessage cancels a queued-but-unstarted message. The message is
// skipped at dequeue time (no turn runs for it — its pre-allocated turn_id is
// simply never processed). Returns false when the message is not queued
// (already processing, already cancelled, or unknown).
func (a *Agent) CancelQueuedMessage(channelName, chatID, msgID string) bool {
	key := qualifyChatID(channelName, chatID)
	state, ok := a.bgSessionStates.Load(key)
	if !ok {
		return false
	}
	ss := state.(*bgSessionState)
	if !ss.queueMarkCancelled(msgID) {
		return false
	}
	log.WithFields(log.Fields{
		"session_key": key,
		"msg_id":      msgID,
	}).Info("Queued message cancelled via REST queue API")
	a.emitQueueState(channelName, chatID, ss)
	return true
}

// emitQueueState broadcasts the current queue snapshot to QueueStateSender
// channels (Web renders it as the Staging Tray). Full-snapshot semantics —
// frontends replace, never merge. Same fan-out contract as emitSessionState.
func (a *Agent) emitQueueState(channelName, chatID string, ss *bgSessionState) {
	entries := ss.queueSnapshot()
	items := make([]protocol.QueueItemPayload, 0, len(entries))
	for _, e := range entries {
		items = append(items, protocol.QueueItemPayload{
			MsgID:      e.MsgID,
			TurnID:     e.TurnID,
			Content:    e.Content,
			Preview:    e.Preview,
			Source:     e.Source,
			EnqueuedAt: e.EnqueuedAt,
		})
	}
	payload := &protocol.QueueStatePayload{
		Channel: channelName,
		ChatID:  chatID,
		Items:   items,
	}

	// Shared-server-hub dedup, identical to emitSessionState: when the CLI
	// channel is a RemoteCLIChannel backed by the web hub, skip the mirror
	// channel so subscribers don't receive the snapshot twice.
	sharedServerHub := false
	if a.channelFinder != nil {
		if cliChannel, ok := a.channelFinder("cli"); ok {
			_, sharedServerHub = cliChannel.(*web.RemoteCLIChannel)
		}
	}

	publish := func(name string, ch channel.Channel) {
		if sharedServerHub &&
			((channelName == "cli" && name == "web") ||
				(channelName == "web" && name == "cli")) {
			return
		}
		if sender, ok := ch.(channel.QueueStateSender); ok {
			sender.SendQueueState(channelName, chatID, payload)
		}
	}

	if a.channelRange != nil {
		a.channelRange(func(name string, ch channel.Channel) bool {
			publish(name, ch)
			return true
		})
		return
	}
	if a.channelFinder == nil {
		return
	}
	for _, name := range []string{"cli", "web"} {
		if ch, ok := a.channelFinder(name); ok {
			publish(name, ch)
		}
	}
}

// ---------------------------------------------------------------------------
// ⚡ User interrupt (interject) — deliver a message into the ACTIVE turn as a
// synthetic tool result (user_interrupt), without queueing and without
// starting a new turn. Web's ⚡ mode and "convert queued → interject" both
// route here.
//
// busy (incl. WaitingUser pause): the interrupt flows through the bgRunPending
//   pipeline and is injected by the Run loop's drain at the next tool
//   boundary (injectSyntheticToolPair — same battle-tested channel bg task
//   notifications use). No new turn, no user message row.
// idle: degrades to a normal user message (fresh turn) — there is nothing to
//   interject into. Returns false so the transport can report the degradation.
// ---------------------------------------------------------------------------

// InjectUserInterrupt delivers a ⚡ user interject into the active turn.
// Returns true when the interrupt was routed for injection; false when the
// session is idle — the caller (web_inbound) falls through to the normal
// send path. ⚠️ This function MUST NOT send the message itself on idle:
// the web caller's false-branch already dispatches it as a regular user
// message (doing both = the message is processed twice — two turns, two
// replies; the frontend busy state lags the server, so the race is real).
func (a *Agent) InjectUserInterrupt(channelName, chatID, senderID, content string) bool {
	key := qualifyChatID(channelName, chatID)

	if state, ok := a.bgSessionStates.Load(key); ok && state.(*bgSessionState).busy.Load() {
		if mgr := a.bgTaskMgr.Load(); mgr != nil {
			mgr.SendAsyncMessage(&tools.AsyncMessageNotification{
				Key:     key,
				Sid:     senderID,
				Content: content,
				Source:  tools.AsyncSourceUserInterrupt,
			})
			log.WithFields(log.Fields{
				"session_key": key,
				"content_len": len(content),
			}).Info("User interject routed into active turn (synthetic tool)")
			return true
		}
	}
	// Idle (or no bg manager): return false — the caller degrades to a normal
	// user message through its own dispatch path (web_inbound fall-through).
	return false
}
