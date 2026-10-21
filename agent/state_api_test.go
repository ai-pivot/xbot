package agent

import (
	"testing"

	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// G-9 / C-12: the session-state snapshot must be SELF-CONSISTENT —
// live.iteration > the last committed iteration of the same turn.
// ---------------------------------------------------------------------------

func TestConsistentLive_DropsAlreadyCommittedIteration(t *testing.T) {
	turns := []protocol.SessionStateTurn{{
		TurnID: 4,
		Blocks: []protocol.IterationBlock{
			{TurnID: 4, Iteration: 1},
			{TurnID: 4, Iteration: 2},
		},
	}}
	live := &protocol.SessionStateLive{TurnID: 4, Iteration: 2}
	if got := consistentLive(turns, live); got != nil {
		t.Fatalf("live iteration 2 was kept although block 2 is already committed: %+v", got)
	}
	if got := consistentLive(turns, &protocol.SessionStateLive{TurnID: 4, Iteration: 3}); got == nil {
		t.Fatal("live iteration 3 must be kept (strictly newer than the last committed block)")
	}
}

func TestConsistentLive_KeepsLiveOfNewerTurn(t *testing.T) {
	turns := []protocol.SessionStateTurn{{TurnID: 4, Blocks: []protocol.IterationBlock{{TurnID: 4, Iteration: 5}}}}
	live := &protocol.SessionStateLive{TurnID: 5, Iteration: 1}
	if got := consistentLive(turns, live); got == nil {
		t.Fatal("a new turn's iteration 1 must be kept even though the previous turn reached 5")
	}
}

func TestConsistentLive_NilAndZero(t *testing.T) {
	if consistentLive(nil, nil) != nil {
		t.Fatal("nil live must stay nil")
	}
	if consistentLive(nil, &protocol.SessionStateLive{TurnID: 1, Iteration: 0}) != nil {
		t.Fatal("iteration 0 is not a live iteration")
	}
}
