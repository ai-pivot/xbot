package agent

import (
	"testing"

	channelpkg "xbot/channel"
)

// TestWireSubAgentProgress_BroadcastsToWebChannel verifies that SubAgent
// structured progress (via wireSubAgentProgress's ProgressEventHandler)
// reaches the Web channel's SendProgress with the correct qualified ChatID
// ("agent:" + fullKey). This is the backend push link of the SubAgent SSE live
// update path — if it's broken, the Web SubAgent panel never receives live
// progress (user report: "subagent session 打开之后没有实时 stream 更新，
// 必须重新打开才能刷新进度").
func TestWireSubAgentProgress_BroadcastsToWebChannel(t *testing.T) {
	a := NewTestAgent()
	webChannel := &recordingProgressChannel{name: "web"}
	a.channelRange = func(fn func(name string, ch channelpkg.Channel) bool) {
		fn("web", webChannel)
	}

	fullKey := "cli:/workspace/review:1"
	cfg := &RunConfig{TurnID: 7}
	a.wireSubAgentProgress(fullKey, "cli:/workspace", cfg)
	if cfg.ProgressEventHandler == nil {
		t.Fatal("wireSubAgentProgress did not set ProgressEventHandler")
	}

	// Trigger a structured progress event the way engine.Run does.
	cfg.ProgressEventHandler(&ProgressEvent{Structured: &StructuredProgress{
		Seq: 1, Phase: PhaseThinking, Iteration: 1, TurnID: 7,
	}})
	cfg.ProgressEventHandler(&ProgressEvent{Structured: &StructuredProgress{
		Seq: 2, Phase: PhaseToolExec, Iteration: 2, TurnID: 7,
	}})

	if len(webChannel.events) != 2 {
		t.Fatalf("web channel received %d progress events, want 2", len(webChannel.events))
	}
	for i, ev := range webChannel.events {
		if ev.ChatID != "agent:"+fullKey {
			t.Fatalf("event %d ChatID = %q, want %q", i, ev.ChatID, "agent:"+fullKey)
		}
		// CRITICAL: the frontend ChatStore derives activeTurn from progress
		// event turn_id. Without it (turn_id=0), every live stream/iteration
		// event is dropped by reduce (user report: subagent SSE no live update).
		if ev.TurnID != 7 {
			t.Fatalf("event %d TurnID = %d, want 7", i, ev.TurnID)
		}
	}
}
