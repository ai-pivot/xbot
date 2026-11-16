package agent

import (
	"context"
	"testing"
	"time"
)

// TestDeriveRunBaseContext_IgnoresCallerCancel is the regression guard for
// "子代理 idle 了但最后一条消息不通知父会话".
//
// 机制：interactive session 的 Run ctx 若从调用方的 ctx 派生，调用方那一轮
// 结束时 ctx 会被 cancel，子代理 Run 跟着 cancel → `cancelled == true` →
// interactive.go 里包住 SendSubAgentNotify 的
// `if notifyMgr != nil && !cancelled` 整段被跳过 → 完成通知永远发不出去。
//
// 因此派生出来的 base 必须与调用方 ctx 的 cancel 完全解耦。
func TestDeriveRunBaseContext_IgnoresCallerCancel(t *testing.T) {
	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()

	a := &Agent{agentCtx: agentCtx}

	callerCtx, callerCancel := context.WithCancel(context.Background())
	base := a.deriveRunBaseContext(callerCtx)

	// 模拟「父 agent 的 turn 结束」。
	callerCancel()

	if err := base.Err(); err != nil {
		t.Fatalf("Run base ctx was cancelled by the caller (%v) — "+
			"the sub-agent Run would die with the parent's turn and its "+
			"completion notification would never be sent", err)
	}

	// 反过来：Agent 生命周期结束时必须真的能取消，否则 unload/shutdown 失效。
	agentCancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if base.Err() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Run base ctx did not follow the Agent lifecycle — unload/shutdown would leak the Run")
}

// TestDeriveRunBaseContext_BareAgentDetachesCallerCancel covers bare &Agent{}
// (tests / early construction) where agentCtx is nil. The caller's cancel must
// still not propagate, but its values must be preserved.
func TestDeriveRunBaseContext_BareAgentDetachesCallerCancel(t *testing.T) {
	a := &Agent{}

	type ctxKey struct{}
	callerCtx, callerCancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "kept"))

	base := a.deriveRunBaseContext(callerCtx)
	callerCancel()

	if err := base.Err(); err != nil {
		t.Fatalf("bare Agent: caller cancel leaked into Run base ctx (%v)", err)
	}
	if got := base.Value(ctxKey{}); got != "kept" {
		t.Fatalf("bare Agent: expected caller values to survive, got %v", got)
	}
}
