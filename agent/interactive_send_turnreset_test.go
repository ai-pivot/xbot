package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	channelpkg "xbot/channel"
	"xbot/llm"
	"xbot/protocol"
)

// TestSendToInteractiveSession_ForegroundClearsTurnState verifies that a
// non-background (foreground) interactive session's action="send" clears the
// previous turn's per-turn iteration state — ia.iterationHistory,
// a.lastProgressSnapshot["agent:<key>"] and a.iterationHistories["agent:<key>"].
//
// Each send allocates a FRESH turn (assignSubAgentTurnID) and iterations
// restart at 1. The old code only did this reset inside `if ia.background`,
// so a foreground session (SubAgent spawned with explicit background=false)
// kept the previous turn's records across sends:
//
//  1. recordIterationSnapshot dedups by iteration NUMBER only — the new turn's
//     iterations 1..M were appended to the old turn's 1..N, producing
//     duplicate iteration numbers in GetActiveProgress's history.
//  2. attachIterationDelta's shouldAppend (nextIteration > prev.Iteration)
//     used the OLD turn's max iteration as baseline, dropping the new turn's
//     first N deltas — GetActiveProgress/PhaseDone lost early iterations.
//
// This mirrors what the main agent does at every turn boundary
// (emitTurnStarted → iterationHistories.Delete) and what the background send
// path already does.
func TestSendToInteractiveSession_ForegroundClearsTurnState(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/w/reviewer:fg1"
	agentProgressKey := "agent:" + key

	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "turn-2 reply"}}}
	ia := &interactiveAgent{
		roleName:     "reviewer",
		instance:     "fg1",
		background:   false, // foreground SubAgent (explicit background=false)
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
		},
		// Turn-1 residue a foreground session accumulates after its spawn Run
		// completes (out.IterationHistory appended at write-back, progress
		// events recorded via wireSubAgentProgress).
		iterationHistory: []IterationSnapshot{
			{Iteration: 1, Content: "turn-1 iter 1"},
			{Iteration: 2, Content: "turn-1 iter 2"},
			{Iteration: 3, Content: "turn-1 iter 3"},
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	// Turn-1 progress residue (what wireSubAgentProgress records during the
	// spawn Run: per-iteration history + the final PhaseDone snapshot).
	a.iterationHistories.Store(agentProgressKey, &[]protocol.ProgressEvent{
		{Iteration: 1, Phase: "thinking"},
		{Iteration: 2, Phase: "tool_exec"},
		{Iteration: 3, Phase: "done"},
	})
	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{
		Iteration: 3, Phase: "done",
	})

	out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel:  "cli",
		ChatID:   "/w",
		SenderID: "u1",
		ChatType: "p2p",
		Content:  "turn-2 question",
		Metadata: map[string]string{"instance_id": "fg1"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("SendToInteractiveSession returned error: %v", out.Error)
	}

	// The send path runs the new turn asynchronously; wait for its write-back
	// to finish (lastReply is set at the very end of the goroutine).
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

	// 1. The previous turn's per-turn progress state must be cleared.
	// The new turn's iterations restart at 1; leaving the old snapshot in
	// a.lastProgressSnapshot makes attachIterationDelta drop the new turn's
	// first N deltas (shouldAppend: nextIteration > prev.Iteration fails
	// while prev.Iteration is still the old turn's max).
	if _, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
		t.Errorf("BUG REPRODUCED: lastProgressSnapshot survived a foreground send — old turn snapshot (iteration 3) poisons the new turn's attachIterationDelta baseline")
	}
	if _, ok := a.iterationHistories.Load(agentProgressKey); ok {
		t.Errorf("BUG REPRODUCED: iterationHistories survived a foreground send — old turn iterations mix into the new turn's GetActiveProgress history (duplicate iteration numbers)")
	}

	// 2. ia.iterationHistory must contain ONLY the new turn's iterations —
	// no duplicate iteration numbers from the old turn.
	ia.mu.Lock()
	iters := append([]IterationSnapshot(nil), ia.iterationHistory...)
	ia.mu.Unlock()
	seen := map[int]bool{}
	for _, it := range iters {
		if seen[it.Iteration] {
			t.Errorf("BUG REPRODUCED: duplicate iteration number %d in ia.iterationHistory — old turn iterations were not cleared before the new turn appended its own (got %v)", it.Iteration, iters)
		}
		seen[it.Iteration] = true
	}
	for _, it := range iters {
		if it.Content == "turn-1 iter 1" || it.Content == "turn-1 iter 2" || it.Content == "turn-1 iter 3" {
			t.Errorf("BUG REPRODUCED: old turn iteration content %q leaked into the new turn's history (iters=%v)", it.Content, iters)
		}
	}
}

// waitFor polls cond until it returns true or the timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestSendSubAgentPhaseDone_ClonesPerChannel verifies that sendSubAgentPhaseDone
// delivers an INDEPENDENT deep copy of the payload to every ProgressSender
// channel — the same "payload 独立" contract as wireSubAgentProgress's broadcast
// (each channel gets its own cloneProgressEvent). The old code shallow-copied
// the snapshot once (`cp := *snap`) and sent the SAME payload pointer (sharing
// slice backing arrays) to every channel: a channel that mutates the payload
// or appends to its slices corrupts the snapshot for every other channel.
func TestSendSubAgentPhaseDone_ClonesPerChannel(t *testing.T) {
	a := NewTestAgent()
	cliChannel := &recordingProgressChannel{name: "cli"}
	webChannel := &recordingProgressChannel{name: "web"}
	channels := []channelpkg.Channel{cliChannel, webChannel}
	a.channelRange = func(fn func(name string, ch channelpkg.Channel) bool) {
		for _, ch := range channels {
			if !fn(ch.Name(), ch) {
				return
			}
		}
	}

	key := "cli:/w/reviewer:fg1"
	agentProgressKey := "agent:" + key
	snapshot := &protocol.ProgressEvent{
		ChatID:    agentProgressKey,
		Phase:     "running",
		Iteration: 2,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Status: "running", Iteration: 2},
		},
	}
	a.lastProgressSnapshot.Store(agentProgressKey, snapshot)

	a.sendSubAgentPhaseDone(key)

	if len(cliChannel.events) != 1 || len(webChannel.events) != 1 {
		t.Fatalf("each channel must receive exactly one PhaseDone event, got cli=%d web=%d", len(cliChannel.events), len(webChannel.events))
	}
	cliEv, webEv := cliChannel.events[0], webChannel.events[0]

	// Phase must be overridden to done on both.
	if cliEv.Phase != "done" || webEv.Phase != "done" {
		t.Fatalf("PhaseDone event phase: cli=%q web=%q, want done/done", cliEv.Phase, webEv.Phase)
	}
	// Payload independence: the two channels must NOT share the same event
	// instance, and neither must alias the stored snapshot.
	if cliEv == webEv {
		t.Fatal("BUG REPRODUCED: cli and web received the SAME payload instance — channels share mutable state (contract: each channel gets an independent clone)")
	}
	if cliEv == snapshot || webEv == snapshot {
		t.Fatalf("BUG REPRODUCED: payload aliases the stored lastProgressSnapshot — Phase override mutates the snapshot (Phase=%q)", snapshot.Phase)
	}
	if snapshot.Phase != "running" {
		t.Fatalf("stored snapshot was mutated by sendSubAgentPhaseDone (phase=%q, want running)", snapshot.Phase)
	}
	// Slice independence: appending via one channel must not affect the other.
	cliEv.ActiveTools[0].Name = "mutated-by-cli"
	if webEv.ActiveTools[0].Name != "Shell" {
		t.Fatalf("BUG REPRODUCED: slice backing array shared across channels — cli mutation leaked into web (got %q)", webEv.ActiveTools[0].Name)
	}
	if &cliEv.ActiveTools[0] == &snapshot.ActiveTools[0] {
		t.Fatal("BUG REPRODUCED: ActiveTools backing array aliases the stored snapshot")
	}
}
