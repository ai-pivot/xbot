package agent

// cancel_turn_end_test.go — 2026-11-04 P0
// 用户实测：「取消 goal 之后前端卡死在 busy 态」。
//
// 根因（两处同时成立，缺一不可）：
//   ① engine 的 progressFinalizer 看到 s.waitingUser==true 就不发 PhaseDone；
//   ② agent.go 的收尾看到 response.WaitingUser==true 就不发 session(idle)。
// 而 `s.waitingUser` 只在 AskUser 执行时被置 true、**never 复位**，用户点停止
// （ctx cancel）后 Run 直接返回，两处都按「暂停等回答」处理 ⇒ 前端收不到任何
// 「本轮结束」信号，永久 typing。
//
// 判据：**取消 ≠ 暂停**。取消必须收尾（发 PhaseDone / session idle）；
// 真·等用户回答时才允许吞掉 PhaseDone（否则 AskUser 之前的迭代会从进度面板消失）。

import (
	"context"
	"testing"

	"xbot/channel"
)

// newTestRunStateForFinalizer 造一个够用的 runState：只要 ProgressEventHandler。
func newTestRunStateForFinalizer(phases *[]string) *runState {
	s := &runState{
		cfg: RunConfig{
			ProgressEventHandler: func(ev *ProgressEvent) {
				if ev != nil && ev.Structured != nil {
					*phases = append(*phases, string(ev.Structured.Phase))
				}
			},
		},
		tokenTracker: NewTokenTracker(0, 0),
	}
	s.autoNotify = true
	s.initProgress()
	return s
}

// TestProgressFinalizerEmitsPhaseDoneOnCancel 锁住 ①：被取消的轮次必须发 PhaseDone。
func TestProgressFinalizerEmitsPhaseDoneOnCancel(t *testing.T) {
	var phases []string
	s := newTestRunStateForFinalizer(&phases)

	ctx, cancel := context.WithCancel(context.Background())
	s.runCtx = ctx
	s.waitingUser = true // AskUser 设过就再没复位 —— 真实状态
	cancel()             // 用户点了停止

	if s.progressFinalizer == nil {
		t.Fatal("progressFinalizer is nil — 测试前提不成立")
	}
	s.progressFinalizer()

	if len(phases) == 0 {
		t.Fatal("取消后没有发出任何 progress 事件：前端拿不到 done，永久 typing")
	}
	if last := phases[len(phases)-1]; last != string(PhaseDone) {
		t.Fatalf("取消后最后发出的 phase = %q, want %q", last, PhaseDone)
	}
}

// TestProgressFinalizerSuppressesPhaseDoneWhileWaitingUser 是对照组（防误伤）：
// 真·等用户回答时**不能**发 PhaseDone，否则 AskUser 之前的迭代会从进度面板消失。
func TestProgressFinalizerSuppressesPhaseDoneWhileWaitingUser(t *testing.T) {
	var phases []string
	s := newTestRunStateForFinalizer(&phases)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 保持 ctx 活着 = 还在等用户回答
	s.runCtx = ctx
	s.waitingUser = true

	s.progressFinalizer()

	for _, p := range phases {
		if p == string(PhaseDone) {
			t.Fatal("等待用户回答时不应发 PhaseDone（会清掉前序迭代的进度状态）")
		}
	}
}

// TestIsWaitingUserPause_CancelWins 锁住 ②：取消优先于 WaitingUser。
func TestIsWaitingUserPause_CancelWins(t *testing.T) {
	waiting := &channel.OutboundMsg{WaitingUser: true}

	if isWaitingUserPause(waiting, true) {
		t.Fatal("已取消的轮次不得再按「暂停等回答」收尾（前端会卡 busy）")
	}
	if !isWaitingUserPause(waiting, false) {
		t.Fatal("未取消 + WaitingUser 时必须按暂停处理（否则清掉 AskUser 进度）")
	}
	if isWaitingUserPause(nil, false) {
		t.Fatal("没有 response 时不应判为暂停")
	}
	if isWaitingUserPause(&channel.OutboundMsg{}, false) {
		t.Fatal("WaitingUser=false 时不应判为暂停")
	}
}
