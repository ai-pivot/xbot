package agent

import "testing"

// // busy 判定是「回流只给一次」的关键：busy 走 structured 队列（携带内容），
// idle 走「落库 + 空注入」。两者同时发生 = 用户实测的重复投递（2026-11-15）。
func TestSessionBusyReflectsBGState(t *testing.T) {
	a := &Agent{}
	const ch, chat = "web", "chat_x"
	if a.SessionBusy(ch, chat) {
		t.Fatal("unknown session must not report busy")
	}
	key := qualifyChatID(ch, chat)
	st := &bgSessionState{}
	st.busy.Store(true)
	a.bgSessionStates.Store(key, st)
	if !a.SessionBusy(ch, chat) {
		t.Fatal("busy flag set but SessionBusy returned false")
	}
	st.busy.Store(false)
	if a.SessionBusy(ch, chat) {
		t.Fatal("idle session reported busy")
	}
	if a.SessionBusy("", chat) || a.SessionBusy(ch, "") {
		t.Fatal("empty channel/chat must not report busy")
	}
}
