package agent

import (
	"context"
	"testing"

	"xbot/tools"
)

// set_goal_complete must emit a progress event after completing the goal —
// the GoalBanner relies on it to switch to the completed style in real time
// (user report: "goal 被agent set goal complete不会实时更新前端样式" — the
// frontend previously had no goal event source at all; now the tool pushes
// emitGoalProgress so the banner updates mid-turn).
func TestSetGoalCompleteToolEmitsProgress(t *testing.T) {
	gm := NewGoalManager()
	gm.Set("web:chat-1", "写完报告")

	type emit struct {
		ch     string
		chatID string
	}
	var emitted []emit
	tool := &setGoalCompleteTool{
		manager: gm,
		onComplete: func(chName, chatID string) {
			emitted = append(emitted, emit{ch: chName, chatID: chatID})
		},
	}

	ctx := &tools.ToolContext{Ctx: context.Background(), Channel: "web", ChatID: "chat-1"}
	res, err := tool.Execute(ctx, `{"summary":"报告已写完"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if res == nil || res.Summary == "" {
		t.Fatalf("Execute must return a non-empty result")
	}

	// Goal marked completed with the summary.
	g := gm.Get("web:chat-1")
	if g == nil || g.Status != GoalCompleted || g.Summary != "报告已写完" {
		t.Fatalf("goal not completed: %+v", g)
	}
	// Progress emit fired with the session coordinates.
	if len(emitted) != 1 || emitted[0].ch != "web" || emitted[0].chatID != "chat-1" {
		t.Fatalf("onComplete not called with session coordinates: %+v", emitted)
	}

	// Emitting must not depend on the goal being completable — a second call on
	// an already-completed goal still notifies (idempotent progress refresh).
	if _, err := tool.Execute(ctx, `{"summary":"again"}`); err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("onComplete must fire on every call (progress refresh), got %d", len(emitted))
	}

	// nil onComplete must not panic (defensive registration paths).
	plain := &setGoalCompleteTool{manager: gm}
	if _, err := plain.Execute(ctx, `{"summary":"x"}`); err != nil {
		t.Fatalf("nil-onComplete Execute failed: %v", err)
	}
}
