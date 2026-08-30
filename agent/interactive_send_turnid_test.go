package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	"xbot/protocol"
)

// TestSendToInteractiveSession_StreamCallbacksCarryNewTurnID reproduces audit
// finding A1: wireSubAgentProgress's stream callbacks (StreamContentFunc /
// StreamReasoningFunc / StreamUsageFunc) read cfg.TurnID through LAZY closure
// evaluation over the ia.cfg pointer. SendToInteractiveSession assigned the
// new turn id only to its local copy (cfg := *ia.cfg; assignSubAgentTurnID
// (&cfg, sess)) — ia.cfg.TurnID kept the SPAWN turn's value. Consequences:
// every stream event of the SECOND+ send turn carried the OLD turn_id while
// structured events (runState initialized from the copy) carried the NEW one —
// the frontend wrote live stream content into the committed OLD turn's slot
// ("渲染重复历史" / turn 关联断裂), and GetActiveProgress's running-correction
// branch (runTurnID = ia.cfg.TurnID) rewrote snapshots to the old turn too.
//
// The fix: SendToInteractiveSession writes the assigned turn id back to
// ia.cfg.TurnID (single write covers both lazy consumers).
func TestSendToInteractiveSession_StreamCallbacksCarryNewTurnID(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:tid1"

	// Seed the session with a turn-1 user message row so the send Run's
	// assignSubAgentTurnID allocates turn 2 (GetMaxTurnID=1 → TurnID=2).
	if sess, err := mt.GetOrCreateSession("agent", key); err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	} else {
		if _, err := sess.AppendMessage(llm.ChatMessage{Role: "user", Content: "turn-1", TurnID: 1}); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	mockCh := &mockProgressChannel2{}
	a.channelRange = func(fn func(string, channel.Channel) bool) {
		fn("web", mockCh)
	}

	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "turn-2 reply"}}}
	ia := &interactiveAgent{
		roleName:     "reviewer",
		instance:     "tid1",
		background:   false,
		mu:           sync.Mutex{},
		systemPrompt: llm.NewSystemMessage("You are a reviewer."),
		messages:     []llm.ChatMessage{llm.NewUserMessage("turn-1 question")},
		cfg: &RunConfig{
			LLMClient: mock,
			Model:     "test-model",
			Tools:     newTestRegistry(),
			AgentID:   "main/reviewer",
			Channel:   "agent",
			ChatID:    key,
			TurnID:    1, // spawn turn's id (assigned by SpawnInteractiveSession)
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	// Wire the spawn-turn progress callbacks exactly like SpawnInteractiveSession.
	a.wireSubAgentProgress(key, "/w", ia.cfg)
	if ia.cfg.StreamContentFunc == nil {
		t.Fatalf("wireSubAgentProgress did not set StreamContentFunc (channelRange had no ProgressSender?)")
	}

	out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel:  "cli",
		ChatID:   "/w",
		SenderID: "u1",
		ChatType: "p2p",
		Content:  "turn-2 question",
		Metadata: map[string]string{"instance_id": "tid1"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("SendToInteractiveSession returned error: %v", out.Error)
	}
	if !waitFor(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return ia.lastReply == "turn-2 reply" && !ia.running
	}) {
		ia.mu.Lock()
		lastErr, running := ia.lastError, ia.running
		ia.mu.Unlock()
		t.Fatalf("send Run did not complete in time (running=%v lastError=%q)", running, lastErr)
	}

	// A1: ia.cfg.TurnID must be written back to the NEW turn id (2). The send
	// allocated it on its local copy; the lazy stream callbacks and
	// GetActiveProgress's correction branch read ia.cfg.TurnID.
	ia.mu.Lock()
	cfgTurnID := ia.cfg.TurnID
	streamFn := ia.cfg.StreamContentFunc
	ia.mu.Unlock()
	if cfgTurnID != 2 {
		t.Errorf("BUG REPRODUCED (A1): ia.cfg.TurnID = %d after send, want 2 — the new turn id stayed on the send's local copy; stream callbacks still read the spawn turn's id (%d)", cfgTurnID, cfgTurnID)
	}

	// A1 behavioral: the LAZY stream closure broadcasts with the NEW turn id.
	n0 := len(mockCh.getEvents())
	streamFn("turn-2 live content")
	events := mockCh.getEvents()
	if len(events) != n0+1 {
		t.Fatalf("stream callback did not broadcast (events %d → %d)", n0, len(events))
	}
	got := events[len(events)-1]
	if got.TurnID != 2 {
		t.Errorf("BUG REPRODUCED (A1): stream event TurnID = %d, want 2 — live stream lands in the committed OLD turn's slot (duplicate-render bug class)", got.TurnID)
	}
}

// TestSendToInteractiveSession_ClearsStreamStateAtTurnBoundary reproduces
// audit finding A2: the Pre-Run per-turn reset deleted lastProgressSnapshot +
// iterationHistories but NOT a.streamState — the previous turn's final
// StreamContent survived the turn boundary, and mergeStreamState (which only
// fills EMPTY snapshot fields) leaked it into the NEW turn's GetActiveProgress
// snapshot: switching sessions / SSE reconnecting to a mid-run send turn
// rendered turn N's full streamed text as turn N+1's live content (duplicate
// rendering). The main agent clears streamState at every turn boundary AND
// after every structured event (emitTurnStarted / buildProgressEventHandler →
// clearStreamState) — the subagent send path did neither.
func TestSendToInteractiveSession_ClearsStreamStateAtTurnBoundary(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:tid2"
	agentProgressKey := "agent:" + key

	mockCh := &mockProgressChannel2{}
	a.channelRange = func(fn func(string, channel.Channel) bool) {
		fn("web", mockCh)
	}

	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "turn-2 reply"}}}
	ia := &interactiveAgent{
		roleName:     "reviewer",
		instance:     "tid2",
		background:   false,
		mu:           sync.Mutex{},
		systemPrompt: llm.NewSystemMessage("You are a reviewer."),
		messages:     []llm.ChatMessage{llm.NewUserMessage("turn-1 question")},
		cfg: &RunConfig{
			LLMClient: mock,
			Model:     "test-model",
			Tools:     newTestRegistry(),
			AgentID:   "main/reviewer",
			Channel:   "agent",
			ChatID:    key,
			TurnID:    1,
		},
	}
	a.interactiveSubAgents.Store(key, ia)
	a.wireSubAgentProgress(key, "/w", ia.cfg)

	// Turn-1 residue in streamState — exactly what the spawn Run's stream
	// callbacks leave behind (the final full StreamContent of the last turn).
	a.updateStreamState(agentProgressKey, func(s *protocol.ProgressEvent) {
		s.StreamContent = "turn-1 final stream content"
		s.TurnID = 1
	})

	out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel:  "cli",
		ChatID:   "/w",
		SenderID: "u1",
		ChatType: "p2p",
		Content:  "turn-2 question",
		Metadata: map[string]string{"instance_id": "tid2"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("SendToInteractiveSession returned error: %v", out.Error)
	}
	if !waitFor(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return ia.lastReply == "turn-2 reply" && !ia.running
	}) {
		ia.mu.Lock()
		lastErr, running := ia.lastError, ia.running
		ia.mu.Unlock()
		t.Fatalf("send Run did not complete in time (running=%v lastError=%q)", running, lastErr)
	}

	// A2: the turn boundary must delete streamState — the new turn's
	// GetActiveProgress snapshot must not inherit turn-1's stream content.
	if _, ok := a.streamState.Load(agentProgressKey); ok {
		t.Errorf("BUG REPRODUCED (A2): streamState survived the send turn boundary — mergeStreamState would leak turn-1's StreamContent into turn-2's GetActiveProgress snapshot (duplicate live rendering on session switch / SSE reconnect)")
	}
}

// TestSendToInteractiveSession_InterruptDrainsPendingMessages reproduces
// audit finding A4: the send Run's cancelled+interrupted branch reset only
// running/cancelCurrent/interrupted — pendingMessages queued by concurrent
// SendMessage callers were left in place, so the NEXT Run's
// wirePendingMessageDrain injected the CANCELLED turn's stale instructions
// into the new turn (semantic confusion, not a crash). The fix mirrors
// syncInteractiveSessionAfterRewind's pending cleanup: drain every pending
// message with an error reply at the interrupt boundary.
// hangLLM blocks inside Generate until ctx is cancelled — keeps the send
// Run in-flight (LLM call executing) so the interrupt hits a live Run without
// depending on the tool-execution path (whose ToolContext needs fields the
// bare test Agent does not wire).
type hangLLM struct{}

func (m *hangLLM) Generate(ctx context.Context, model string, messages []llm.ChatMessage, toolDefs []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (m *hangLLM) ListModels() []string { return []string{"test-model"} }

func TestSendToInteractiveSession_InterruptDrainsPendingMessages(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:tid4"

	ia := &interactiveAgent{
		roleName:     "reviewer",
		instance:     "tid4",
		background:   false,
		mu:           sync.Mutex{},
		systemPrompt: llm.NewSystemMessage("You are a reviewer."),
		messages:     []llm.ChatMessage{llm.NewUserMessage("turn-1 question")},
		cfg: &RunConfig{
			LLMClient: &hangLLM{},
			Model:     "test-model",
			Tools:     newTestRegistry(),
			AgentID:   "main/reviewer",
			Channel:   "agent",
			ChatID:    key,
			TurnID:    1,
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	// Send #1: async Run starts, LLM returns a tool call, the tool blocks.
	if _, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel:  "cli",
		ChatID:   "/w",
		SenderID: "u1",
		ChatType: "p2p",
		Content:  "turn-1 question (long-running)",
		Metadata: map[string]string{"instance_id": "tid4"},
	}); err != nil {
		t.Fatalf("send #1: %v", err)
	}
	if !waitFor(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return ia.running
	}) {
		t.Fatalf("send #1 Run did not start in time")
	}

	// Send #2 while running: queues into pendingMessages and blocks on replyCh.
	pendingDone := make(chan *channel.OutboundMsg, 1)
	go func() {
		out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
			Channel:  "cli",
			ChatID:   "/w",
			SenderID: "u1",
			ChatType: "p2p",
			Content:  "second message queued while running",
			Metadata: map[string]string{"instance_id": "tid4"},
		})
		if err != nil {
			pendingDone <- &channel.OutboundMsg{Content: err.Error(), Error: err}
			return
		}
		pendingDone <- out
	}()
	if !waitFor(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return len(ia.pendingMessages) == 1
	}) {
		t.Fatalf("send #2 did not queue into pendingMessages in time")
	}

	// Interrupt the running turn: cancelCurrent → hangTool returns ctx.Err()
	// → Run returns cancelled → the interrupted branch must drain the pending
	// queue with an error reply (fix) instead of leaving it for the next Run.
	if err := a.InterruptInteractiveSession(context.Background(), "reviewer", "cli", "/w", "tid4"); err != nil {
		t.Fatalf("InterruptInteractiveSession: %v", err)
	}

	select {
	case out := <-pendingDone:
		if out == nil || out.Error == nil {
			t.Fatalf("queued send #2 should fail with a delivery error after the interrupt, got %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("BUG REPRODUCED (A4): queued send #2 still blocked 5s after the interrupt — pendingMessages were not drained at the interrupt boundary")
	}

	ia.mu.Lock()
	left := len(ia.pendingMessages)
	ia.mu.Unlock()
	if left != 0 {
		t.Errorf("BUG REPRODUCED (A4): %d pending message(s) survived the interrupt — the next Run would inject the cancelled turn's stale instructions", left)
	}
}

// TestSubAgentProgressEventHandler_ClearsStreamState guards the A2 alignment:
// the subagent's ProgressEventHandler clears streamState after every
// structured event, exactly like the main agent's buildProgressEventHandler
// (engine_wire.go: a.clearStreamState(progressKey) after lastProgressSnapshot
// .Store). Without it, only the Pre-Run reset cleans streamState — a mid-turn
// structured event leaves the stream residue in place for GetActiveProgress.
func TestSubAgentProgressEventHandler_ClearsStreamState(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/w/reviewer:tid3"
	agentProgressKey := "agent:" + key
	mockCh := &mockProgressChannel2{}
	a.channelRange = func(fn func(string, channel.Channel) bool) {
		fn("web", mockCh)
	}

	ia := &interactiveAgent{
		roleName:   "reviewer",
		instance:   "tid3",
		background: false,
		mu:         sync.Mutex{},
		cfg:        &RunConfig{Channel: "agent", ChatID: key},
	}
	a.wireSubAgentProgress(key, "/w", ia.cfg)
	if ia.cfg.ProgressEventHandler == nil {
		t.Fatalf("wireSubAgentProgress did not set ProgressEventHandler")
	}

	// Live stream residue (mid-iteration: stream callbacks wrote StreamContent).
	a.updateStreamState(agentProgressKey, func(s *protocol.ProgressEvent) {
		s.StreamContent = "mid-iteration stream content"
		s.Iteration = 1
	})

	// A structured event arrives (the snapshot supersedes the live stream).
	ia.cfg.ProgressEventHandler(&ProgressEvent{Structured: &StructuredProgress{
		Phase: "tool_exec", Iteration: 1, TurnID: 7,
	}})

	if _, ok := a.streamState.Load(agentProgressKey); ok {
		t.Errorf("BUG REPRODUCED (A2-alignment): subagent ProgressEventHandler did not clear streamState after a structured event — main agent's handler does (engine_wire buildProgressEventHandler); GetActiveProgress merges the stale StreamContent into the structured snapshot")
	}
}
