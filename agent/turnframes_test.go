package agent

import (
	"sync"
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Fake channel that records turn-level frames (STATE-MODEL C-6 / C-7 / G-3/G-4)
// ---------------------------------------------------------------------------

type frameRecordingChannel struct {
	mu     sync.Mutex
	frames []protocol.WSMessage // Type = done|final, payload in Done/Final
}

func (f *frameRecordingChannel) SendIterationDone(chatID string, block *protocol.IterationBlock) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, protocol.WSMessage{
		Type: protocol.MsgTypeDone, ChatID: chatID, TurnID: block.TurnID, Done: block,
	})
}

func (f *frameRecordingChannel) SendTurnFinal(chatID string, final *protocol.TurnFinalFrame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, protocol.WSMessage{
		Type: protocol.MsgTypeFinal, ChatID: chatID, TurnID: final.TurnID, Final: final,
	})
}

// channel.Channel — the minimum needed to be registered in channelRange.
func (f *frameRecordingChannel) Send(channel.OutboundMsg) (string, error) { return "", nil }
func (f *frameRecordingChannel) Name() string                             { return "web" }
func (f *frameRecordingChannel) Start() error                             { return nil }
func (f *frameRecordingChannel) Stop()                                    {}

func (f *frameRecordingChannel) frameCounts() (done int, final int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fr := range f.frames {
		switch fr.Type {
		case protocol.MsgTypeDone:
			done++
		case protocol.MsgTypeFinal:
			final++
		}
	}
	return done, final
}

func (f *frameRecordingChannel) doneFrames() []*protocol.IterationBlock {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*protocol.IterationBlock
	for _, fr := range f.frames {
		if fr.Type == protocol.MsgTypeDone && fr.Done != nil {
			out = append(out, fr.Done)
		}
	}
	return out
}

func (f *frameRecordingChannel) finalFrames() []*protocol.TurnFinalFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*protocol.TurnFinalFrame
	for _, fr := range f.frames {
		if fr.Type == protocol.MsgTypeFinal && fr.Final != nil {
			out = append(out, fr.Final)
		}
	}
	return out
}

func newFrameTestAgent() (*Agent, *frameRecordingChannel) {
	ch := &frameRecordingChannel{}
	a := &Agent{
		toolIDs:    newToolIdentity(),
		turnFrames: newTurnFrameTracker(),
	}
	a.channelRange = func(fn func(string, channel.Channel) bool) { fn("web", ch) }
	return a, ch
}

// ---------------------------------------------------------------------------
// G-3: every started iteration gets EXACTLY ONE `done`
// ---------------------------------------------------------------------------

// TestDoneExactlyOnce_NormalCancelAndCtxCancel drives the three exit paths that
// historically lost iteration content:
//
//	normal  → snapshotCompletedIteration (engine_run_tools.go)
//	cancel  → handleCancelledRun (agent_process.go) synthesizes the in-flight block
//	ctx     → the llm-error early return, same synthesized block
//
// All three report the SAME iteration; the tracker must collapse them into one
// frame. Without the tracker a cancelled turn produced either ZERO `done`
// (content silently lost) or TWO (double commit / flicker).
func TestDoneExactlyOnce_NormalCancelAndCtxCancel(t *testing.T) {
	a, ch := newFrameTestAgent()
	const turn, iteration = 7, 3
	block := protocol.IterationBlock{
		TurnID: turn, Iteration: iteration, Content: "hello",
		Tools: []protocol.ToolProgress{{CallID: "call-7-3-0", Name: "Shell", Status: "done", Iteration: iteration}},
	}

	a.emitIterationDone("web", "chat-1", block)      // normal path
	a.emitIterationDone("web", "chat-1", block)      // cancel path (duplicate)
	a.emitIterationDone("web", "chat-1", block)      // ctx-cancel path (duplicate)

	done, final := ch.frameCounts()
	if done != 1 {
		t.Fatalf("done frames = %d, want exactly 1 across all exit paths", done)
	}
	if final != 0 {
		t.Fatalf("final frames = %d, want 0 (no turn closure here)", final)
	}
	got := ch.doneFrames()[0]
	if got.Iteration != iteration || got.Content != "hello" || len(got.Tools) != 1 {
		t.Fatalf("unexpected block: %+v", got)
	}
	if got.Tools[0].CallID != "call-7-3-0" {
		t.Fatalf("block tools lost their identity: %+v", got.Tools)
	}
}

// TestDoneNeverRegressesOrRepeats — contract C-4 / C-10: an iteration number
// that was already committed must never be published again (a late frame must
// not re-open a committed iteration), and the sequence must strictly increase.
func TestDoneNeverRegressesOrRepeats(t *testing.T) {
	a, ch := newFrameTestAgent()
	mk := func(iter int) protocol.IterationBlock {
		return protocol.IterationBlock{TurnID: 1, Iteration: iter, Tools: []protocol.ToolProgress{}}
	}
	a.emitIterationDone("web", "chat-1", mk(1))
	a.emitIterationDone("web", "chat-1", mk(2))
	a.emitIterationDone("web", "chat-1", mk(1)) // late/replayed → must be dropped
	a.emitIterationDone("web", "chat-1", mk(3))

	blocks := ch.doneFrames()
	if len(blocks) != 3 {
		t.Fatalf("done frames = %d, want 3 (1,2,3 — the replayed 1 dropped)", len(blocks))
	}
	prev := 0
	for _, b := range blocks {
		if b.Iteration <= prev {
			t.Fatalf("iteration went backwards: %d after %d", b.Iteration, prev)
		}
		prev = b.Iteration
	}
}

// TestDoneOfNewTurnIsAccepted — turn ids are per-chat monotonic; a new turn's
// iteration 1 must not be rejected just because the previous turn reached 5.
func TestDoneOfNewTurnIsAccepted(t *testing.T) {
	a, ch := newFrameTestAgent()
	a.emitIterationDone("web", "chat-1", protocol.IterationBlock{TurnID: 1, Iteration: 5, Tools: []protocol.ToolProgress{}})
	a.emitIterationDone("web", "chat-1", protocol.IterationBlock{TurnID: 2, Iteration: 1, Tools: []protocol.ToolProgress{}})
	if done, _ := ch.frameCounts(); done != 2 {
		t.Fatalf("done frames = %d, want 2 (new turn restarts iteration numbering)", done)
	}
}

// ---------------------------------------------------------------------------
// G-4: every turn gets EXACTLY ONE `final`, including EMPTY turns
// ---------------------------------------------------------------------------

// TestFinalExactlyOnceIncludingEmptyTurn — an empty turn (no content at all)
// must still be closed, and a duplicate report (normal path + deferred path)
// must not produce a second frame. Otherwise the client either keeps spinning
// forever (no final) or closes twice (double commit / flicker).
func TestFinalExactlyOnceIncludingEmptyTurn(t *testing.T) {
	a, ch := newFrameTestAgent()
	// Empty turn: content "" — must still be closed.
	a.emitTurnFinal("web", "chat-1", protocol.TurnFinalFrame{TurnID: 3})
	a.emitTurnFinal("web", "chat-1", protocol.TurnFinalFrame{TurnID: 3}) // duplicate

	_, final := ch.frameCounts()
	if final != 1 {
		t.Fatalf("final frames = %d, want exactly 1", final)
	}
	got := ch.finalFrames()[0]
	if got.TurnID != 3 || got.Content != "" {
		t.Fatalf("unexpected final: %+v", got)
	}

	// Next turn gets its own final.
	a.emitTurnFinal("web", "chat-1", protocol.TurnFinalFrame{TurnID: 4, Content: "x"})
	if _, final := ch.frameCounts(); final != 2 {
		t.Fatalf("final frames = %d after a second turn, want 2", final)
	}
}

// TestFinalCarriesCancelled — a cancelled turn's final must say so; clients use
// it to distinguish "stopped by the user" from "completed normally".
func TestFinalCarriesCancelled(t *testing.T) {
	a, ch := newFrameTestAgent()
	a.emitTurnFinal("web", "chat-1", protocol.TurnFinalFrame{TurnID: 9, Cancelled: true})
	frames := ch.finalFrames()
	if len(frames) != 1 || !frames[0].Cancelled {
		t.Fatalf("expected one cancelled final, got %+v", frames)
	}
}

// ---------------------------------------------------------------------------
// G-5: the cancelled block is COMPLETE — interrupted tools become failed
// ---------------------------------------------------------------------------

// TestCancelledIterationBlockDowngradesRunningTools reproduces the old loss:
// snapshotCompletedIteration only carried finished tools, so a tool that was
// still `running` when the user cancelled VANISHED from the committed block.
func TestCancelledIterationBlockDowngradesRunningTools(t *testing.T) {
	a, _ := newFrameTestAgent()
	progressKey := qualifyChatID("web", "chat-1")
	a.lastProgressSnapshot.Store(progressKey, &protocol.ProgressEvent{
		ChatID:    progressKey,
		TurnID:    5,
		Iteration: 2,
		Content:   "partial",
		CompletedTools: []protocol.ToolProgress{
			{CallID: "call-5-2-0", Name: "Read", Status: "done", Iteration: 2},
		},
		ActiveTools: []protocol.ToolProgress{
			{CallID: "call-5-2-1", Name: "Shell", Status: "running", Iteration: 2},
			{CallID: "call-5-2-2", Name: "Grep", Status: "pending", Iteration: 2},
		},
		StreamingTools: []protocol.ToolProgress{
			{CallID: "call-5-2-3", Name: "Edit", Status: "generating", Iteration: 2},
		},
	})

	block := a.cancelledIterationBlock(progressKey, 5)
	if block == nil {
		t.Fatal("cancelled iteration block missing — the in-flight iteration would have no done at all")
	}
	if block.TurnID != 5 || block.Iteration != 2 || !block.Cancelled {
		t.Fatalf("unexpected block header: %+v", block)
	}
	if len(block.Tools) != 4 {
		t.Fatalf("block tools = %d, want all 4 (nothing may disappear on cancel): %+v", len(block.Tools), block.Tools)
	}
	statuses := map[string]string{}
	for _, tool := range block.Tools {
		statuses[tool.CallID] = tool.Status
	}
	if statuses["call-5-2-0"] != "done" {
		t.Fatalf("finished tool status changed: %v", statuses)
	}
	for _, id := range []string{"call-5-2-1", "call-5-2-2", "call-5-2-3"} {
		if statuses[id] != "failed" {
			t.Fatalf("interrupted tool %s = %q, want failed (interrupted running tools must be downgraded)", id, statuses[id])
		}
	}
}

// ---------------------------------------------------------------------------
// P2/P3′: tool identity is assigned by the server and never changes
// ---------------------------------------------------------------------------

// TestToolIdentityStableAcrossPhases — the id assigned while arguments are still
// generating is the SAME id used when the tool executes, so the UI never rebuilds
// a pill for one call (contract C-8).
func TestToolIdentityStableAcrossPhases(t *testing.T) {
	a, _ := newFrameTestAgent()
	const chat, turn = "web:chat-1", uint64(4)

	generatingID := a.toolCallID(chat, turn, 2, 0)
	if generatingID == "" {
		t.Fatal("generating-phase tool has no call_id — clients would have to invent name#label keys")
	}
	// Repeated frames of the same tool → identical id.
	if again := a.toolCallID(chat, turn, 2, 0); again != generatingID {
		t.Fatalf("call_id changed across frames: %q → %q", generatingID, again)
	}
	// A different index in the same iteration gets a different id.
	other := a.toolCallID(chat, turn, 2, 1)
	if other == generatingID {
		t.Fatalf("two tools of one iteration share the identity %q", other)
	}
	// The next iteration restarts the index space but must not collide.
	next := a.toolCallID(chat, turn, 3, 0)
	if next == generatingID {
		t.Fatalf("iteration 3 re-used iteration 2's id %q", next)
	}
	// Another chat never shares identity.
	if foreign := a.toolCallID("web:chat-2", turn, 2, 0); foreign == generatingID {
		t.Fatalf("different chats share tool identity %q", foreign)
	}
}
