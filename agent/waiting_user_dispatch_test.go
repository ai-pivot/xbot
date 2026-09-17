package agent

// waiting_user_dispatch_test.go — AskUser 面板的送达契约。
//
// 2026-09-17 用户 P0：「askuser 严重 bug，前端完全不弹窗」。根因不是前端：引擎在
// WaitingUser 出站派发时拿 **per-request 的 reqCtx** 当"仅 shutdown 才放弃"的判据，
// 而该派发发生在请求 teardown **之后**，teardown 的 finishActiveCancelState 会
// **无条件** reqCancel() ⇒ reqCtx 必然已 Done ⇒ select 的「发送到 bus」与
// 「ctx.Done」两个分支同时就绪 ⇒ Go 随机选 ⇒ **面板随机不弹**（日志实证
// "Context cancelled, dropping WaitingUser response"；丢的那次连 ask_question 都
// 没落库 ⇒ 刷新也恢复不了，用户只看到一个 ✓ 的工具 pill 且 turn 直接结束）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

// WaitingUser 出站消息在**请求已被 teardown 取消**之后仍必须送达 bus。
func TestDispatchWaitingUser_DeliversAfterRequestTeardown(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}

	// 真实现场：请求体跑完，teardown（finishActiveCancelState）已 reqCancel() 掉
	// per-request ctx。
	reqCtx, reqCancel := context.WithCancel(context.Background())
	reqCancel()
	if reqCtx.Err() == nil {
		t.Fatal("precondition: reqCtx 必须已取消")
	}

	// 会话/进程级 ctx 仍然活着（没有 shutdown）⇒ 必须送达。
	loopCtx := context.Background()
	msg := bus.OutboundMessage{Channel: "web", ChatID: "chat-ask", WaitingUser: true}
	if !a.dispatchWaitingUser(loopCtx, msg) {
		t.Fatal("WaitingUser 必须送达（AskUser 面板靠这条消息弹出）—— 不得拿 per-request ctx 当判据")
	}
	select {
	case got := <-a.bus.Outbound:
		if !got.WaitingUser || got.ChatID != "chat-ask" {
			t.Fatalf("bus 收到 %+v，want WaitingUser for chat-ask", got)
		}
	default:
		t.Fatal("WaitingUser 消息没有进 bus")
	}
}

// 真正的 shutdown 必须放弃（且绝不阻塞）。
func TestDispatchWaitingUser_DropsOnShutdown(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan bool, 1)
	go func() {
		done <- a.dispatchWaitingUser(ctx, bus.OutboundMessage{Channel: "web", ChatID: "c", WaitingUser: true})
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("shutdown 时必须返回 false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown 时不得阻塞")
	}
}

// ⛔ 源码级回归守卫：WaitingUser 派发的实现【绝不能】出现 per-request 的 reqCtx
// （teardown 已取消它 ⇒ 掷硬币丢消息 ⇒ AskUser 面板随机不弹）。
// dispatchWaitingUser 必须存在且是唯一派发入口。
func TestWaitingUserDispatchMustNotUseRequestCtx(t *testing.T) {
	raw, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read agent.go: %v", err)
	}
	src := string(raw)
	const sig = "func (a *Agent) dispatchWaitingUser("
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatal("dispatchWaitingUser 不存在 —— WaitingUser 派发必须走这个唯一实现")
	}
	rest := src[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatal("无法定位 dispatchWaitingUser 函数体")
	}
	body := rest[:end]
	if strings.Contains(body, "reqCtx") {
		t.Fatalf("dispatchWaitingUser 不得引用 reqCtx（teardown 会先取消它 ⇒ 面板随机不弹）:\n%s", body)
	}
	// 调用点同样不得把它绑回 reqCtx。
	if strings.Contains(src, "dispatchWaitingUser(reqCtx") {
		t.Fatal("WaitingUser 派发不得用 reqCtx（必须是会话/进程级 ctx）")
	}
}
