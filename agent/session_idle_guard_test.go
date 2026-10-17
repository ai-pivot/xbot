package agent

// session_idle_guard_test —— 用户 2026-10-17 P0「后端乱发 session_idle」的服务端判据。
//
// 病根：回合收尾的 defer 在 processMessage 返回后**无条件**发 session(idle)。若用户在前一轮
// 收尾前就发来下一条消息（新一轮已 active），这条**迟到的旧轮 idle** 会落在新轮 busy 之后 ⇒
// 前端（web 与原生一致）把正在跑的新轮误标 idle ⇒ 卡 busy / 界面冻结 / 历史闪烁。
//
// 修法（在服务端，不在客户端打补丁）：每会话维护单调递增的"轮次代号"，
// **只有仍是当前轮**的收尾才允许发 idle（`emitSessionIdleIfCurrent`）。

import "testing"

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

// 门控出口本身：**旧轮代号必须被拦下**（在 emit 之前返回，不触碰 bus）。
// 这里用"零值 Agent（bus 为 nil）"做探针：若没被拦下，emitSessionState 会解引用 nil 而 panic。
func TestEmitSessionIdleIfCurrentSuppressesStaleGen(t *testing.T) {
	a := &Agent{}
	g1 := a.bumpRunGen("web:chat_Z")
	_ = a.bumpRunGen("web:chat_Z") // 新一轮接管
	stale := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = r
				stale = false
				return
			}
		}()
		// 迟到旧轮：必须**早退**（不打 bus） —— 若实现了"无条件发"，这里会 panic 在 nil bus 上
		a.emitSessionIdleIfCurrent("web:chat_Z", "web", "chat_Z", "u1", g1)
		stale = true
	}()
	if !stale {
		t.Fatal("⛔ 旧轮 idle 未被拦下（会误标正在跑的新轮）")
	}
}
