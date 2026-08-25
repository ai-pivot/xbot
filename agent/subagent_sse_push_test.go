package agent

import (
	"testing"

	channelpkg "xbot/channel"
	"xbot/protocol"
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

	// ── stream 回调必须始终推送全量（StreamContent/ReasoningStreamContent），
	// 绝不用 delta push（StreamDelta）—— 否则前端 normalize 把 StreamDelta 误判为
	// iteration 事件，迭代边界时清空 content/reasoning → 流式内容倒流
	// （用户报告："思考了 1300 字符突然变成思考了 3 字符"）。 ──
	webChannel.events = nil
	cfg.StreamContentFunc("hello")
	cfg.StreamContentFunc("hello world") // 前缀扩展 —— 若走 delta 会发 StreamDelta
	cfg.StreamReasoningFunc("thinking")
	cfg.StreamReasoningFunc("thinking hard") // 前缀扩展 —— 走 delta 会发 ReasoningStreamDelta
	if len(webChannel.events) != 4 {
		t.Fatalf("stream callbacks produced %d events, want 4", len(webChannel.events))
	}
	for i, ev := range webChannel.events {
		if ev.StreamDelta != "" || ev.ReasoningStreamDelta != "" {
			t.Fatalf("event %d used delta push (StreamDelta=%q ReasoningStreamDelta=%q) — must always push full", i, ev.StreamDelta, ev.ReasoningStreamDelta)
		}
	}
	if webChannel.events[0].StreamContent != "hello" || webChannel.events[1].StreamContent != "hello world" {
		t.Fatalf("content full-push = %q / %q, want 'hello' / 'hello world'", webChannel.events[0].StreamContent, webChannel.events[1].StreamContent)
	}
	if webChannel.events[2].ReasoningStreamContent != "thinking" || webChannel.events[3].ReasoningStreamContent != "thinking hard" {
		t.Fatalf("reasoning full-push = %q / %q, want 'thinking' / 'thinking hard'", webChannel.events[2].ReasoningStreamContent, webChannel.events[3].ReasoningStreamContent)
	}

	// ── PhaseDone（turn 结束）处理必须与主 agent buildProgressEventHandler 一致：
	// ① snapshot phase=done（让前端 historyToReplaced 排除 active，不重复渲染 live）
	// ② recordFinalIteration 补记最后迭代到 iteration_history（否则 active_progress/
	//    committed 缺最后一个 iter —— 用户报告"有时候不渲染最后一个iter"）。 ──
	webChannel.events = nil
	cfg.ProgressEventHandler(&ProgressEvent{Structured: &StructuredProgress{
		Seq: 3, Phase: PhaseDone, Iteration: 2, TurnID: 7,
	}})
	if v, ok := a.lastProgressSnapshot.Load("agent:" + fullKey); ok {
		if pe := v.(*protocol.ProgressEvent); pe.Phase != "done" {
			t.Fatalf("PhaseDone snapshot phase = %q, want done", pe.Phase)
		}
	} else {
		t.Fatal("PhaseDone did not store lastProgressSnapshot")
	}
	// 最后迭代应补记进 iteration_history（wireSubAgentProgress handler 必须做，
	// 与 main buildProgressEventHandler 的 recordFinalIteration 一致）。
	if hist, ok := a.iterationHistories.Load("agent:" + fullKey); ok {
		h := *hist.(*[]protocol.ProgressEvent)
		if len(h) == 0 || h[len(h)-1].Iteration != 2 {
			t.Fatalf("PhaseDone did not record final iteration: %#v", h)
		}
	} else {
		t.Fatal("PhaseDone iterationHistories empty — final iteration not recorded (subagent loses last iter)")
	}
}
