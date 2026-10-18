package agent

// session_idle_guard_test —— 用户 2026-10-17 P0「后端乱发 session_idle」的服务端判据。
//
// 病根：回合收尾的 defer 在 processMessage 返回后**无条件**发 session(idle)。若用户在前一轮
// 收尾前就发来下一条消息（新一轮已 active），这条**迟到的旧轮 idle** 会落在新轮 busy 之后 ⇒
// 前端（web 与原生一致）把正在跑的新轮误标 idle ⇒ 卡 busy / 界面冻结 / 历史闪烁。
//
// 修法（在服务端，不在客户端打补丁）：每会话维护单调递增的"轮次代号"，
// **只有仍是当前轮**的收尾才允许发 idle（`emitSessionIdleIfCurrent`）。
//
// ⛔ 旧判据的教训（本文件重写的原因）：它用"零值 Agent 若不早退就会 nil bus panic"做探针 ——
// 而 emitSessionState（agent.go）**根本不碰 a.bus**，零值 Agent 在 channelFinder==nil 时
// 直接 return ⇒ 把守卫整段删掉该测试**仍然绿**（零判别力）。现在的判据用**可观测的
// fake channel** 数发射次数，并额外钉住"收尾 defer 调的是守卫版"这个接线点。

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"xbot/channel"
)

func TestRunGenMonotonicAndScoped(t *testing.T) {
	a := &Agent{}
	g1 := a.bumpRunGen("web:chat_X")
	if g1 == 0 {
		t.Fatal("代号应从 1 起（0 保留给\"没有轮\"）")
	}
	if !a.isCurrentRunGen("web:chat_X", g1) {
		t.Fatal("首轮代号应判为当前")
	}
	g2 := a.bumpRunGen("web:chat_X")
	if g2 <= g1 {
		t.Fatalf("代号必须单调递增：%d -> %d", g1, g2)
	}
	if a.isCurrentRunGen("web:chat_X", g1) {
		t.Fatal("⛔ 旧轮代号不得仍判为当前（否则迟到 idle 会误标新一轮）")
	}
	if !a.isCurrentRunGen("web:chat_X", g2) {
		t.Fatal("最新轮代号必须判为当前")
	}
	// 会话之间互不影响
	gY := a.bumpRunGen("web:chat_Y")
	if a.isCurrentRunGen("web:chat_X", gY) {
		t.Fatal("不同会话的代号不得串用")
	}
	if !a.isCurrentRunGen("web:chat_Y", gY) {
		t.Fatal("各会话各自维护当前轮")
	}
	// 未知会话 ⇒ 一律不判当前（宁可不发 idle，也不误标在跑的会话）
	if a.isCurrentRunGen("web:never", 1) {
		t.Fatal("从未 bump 过的会话不得判为当前")
	}
}

// newIdleGuardProbe 构造一个"能看见发射次数"的 Agent：
// channelFinder 返回记录器（复用 askuser_cancel_idle_test.go 的同包类型），
// 于是 emitSessionState 的每次发布都会被 append 下来可数。
func newIdleGuardProbe() (*Agent, *askUserCancelIdleChannel) {
	rec := &askUserCancelIdleChannel{}
	a := &Agent{}
	a.channelFinder = func(name string) (channel.Channel, bool) {
		if name == "web" {
			return rec, true
		}
		return nil, false
	}
	return a, rec
}

// 旧轮代号 ⇒ **一次都不许发**（这是本 P0 的核心行为）。
func TestEmitSessionIdleIfCurrentStaleGenEmitsNothing(t *testing.T) {
	a, rec := newIdleGuardProbe()
	g1 := a.bumpRunGen("web:chat_Z")
	_ = a.bumpRunGen("web:chat_Z") // 新一轮接管

	a.emitSessionIdleIfCurrent("web:chat_Z", "web", "chat_Z", "u1", g1)

	if len(rec.events) != 0 {
		t.Fatalf("⛔ 迟到旧轮的 idle 必须被拦下（一次都不许发），实际发了 %d 条：%+v", len(rec.events), rec.events)
	}
}

// 当前轮代号 ⇒ 必须发且必须只发 1 条正确的 idle（防止"守卫把正常 idle 也吃掉"）。
func TestEmitSessionIdleIfCurrentCurrentGenEmitsOneIdle(t *testing.T) {
	a, rec := newIdleGuardProbe()
	g := a.bumpRunGen("web:chat_W")

	a.emitSessionIdleIfCurrent("web:chat_W", "web", "chat_W", "u-owner", g)

	if len(rec.events) != 1 {
		t.Fatalf("当前轮收尾必须发且只发 1 条 idle，实际 %d 条：%+v", len(rec.events), rec.events)
	}
	ev := rec.events[0]
	if ev.Action != "idle" || ev.Channel != "web" || ev.ChatID != "chat_W" || ev.SenderID != "u-owner" {
		t.Fatalf("idle 事件字段不对（会话/sender 错位会让别的会话被误标 idle）：%+v", ev)
	}
}

// ── wire：收尾 defer 必须走守卫版（否则上面两条行为判据守不住真实路径）──────────
//
// 判据 = agent.go 的回合收尾块（`isWaitingUser := ...` → `<-sem // 释放槽位`）里：
//   - 必须出现 `emitSessionIdleIfCurrent(`；
//   - 不得出现裸的 `Action: "idle"`（= 绕过守卫的无条件发布）。
//
// 判别力自证：把该窗口里的守卫调用换成裸 emit 后检查器必须报错。
func teardownWindow(t *testing.T, src string) string {
	t.Helper()
	const start = "isWaitingUser := response != nil && response.WaitingUser"
	const end = "<-sem // 释放槽位"
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("wire: agent.go 里找不到收尾块起点 %q（结构改了？请同步本判据）", start)
	}
	rest := src[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("wire: agent.go 里找不到收尾块终点 %q（结构改了？请同步本判据）", end)
	}
	return rest[:j]
}

func checkTeardownUsesGuardedIdle(window string) error {
	if !strings.Contains(window, "emitSessionIdleIfCurrent(") {
		return fmt.Errorf("回合收尾未调用 emitSessionIdleIfCurrent —— 迟到的旧轮 idle 会误标正在跑的新轮（P0）")
	}
	if strings.Contains(window, `Action: "idle"`) {
		return fmt.Errorf("回合收尾里出现裸的 `Action: \"idle\"` —— 绕过了代际守卫（必须走 emitSessionIdleIfCurrent）")
	}
	return nil
}

func TestTeardownEmitsIdleThroughGenerationGuard(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read agent.go: %v", err)
	}
	if err := checkTeardownUsesGuardedIdle(teardownWindow(t, string(src))); err != nil {
		t.Fatalf("⛔ %v", err)
	}
}

func TestTeardownGuard_RejectsUnguardedEmit(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read agent.go: %v", err)
	}
	window := teardownWindow(t, string(src))
	if err := checkTeardownUsesGuardedIdle(window); err != nil {
		t.Fatalf("前置：当前收尾块必须已合规：%v", err)
	}
	mutated := strings.Replace(window, "a.emitSessionIdleIfCurrent(cancelKey, msg.Channel, msg.ChatID, msg.SenderID, runGen)",
		"a.emitSessionState(protocol.SessionEvent{Channel: msg.Channel, ChatID: msg.ChatID, Action: \"idle\", SenderID: msg.SenderID})", 1)
	if mutated == window {
		t.Fatal("变异未生效（锚点串变了？请同步更新本测试）")
	}
	if err := checkTeardownUsesGuardedIdle(mutated); err == nil {
		t.Fatal("⛔ 换回无条件 emit 后判据仍然通过 —— 本判据没有判别力")
	}
}
