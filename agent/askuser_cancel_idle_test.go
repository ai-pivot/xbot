package agent

import (
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// askUserCancelIdleChannel captures session-state events emitted by the agent
// so the cancel path's idle signal can be asserted.
type askUserCancelIdleChannel struct {
	events []protocol.SessionEvent
}

func (c *askUserCancelIdleChannel) Name() string                             { return "web" }
func (c *askUserCancelIdleChannel) Start() error                             { return nil }
func (c *askUserCancelIdleChannel) Stop()                                    {}
func (c *askUserCancelIdleChannel) Send(channel.OutboundMsg) (string, error) { return "", nil }
func (c *askUserCancelIdleChannel) SendSessionState(ev protocol.SessionEvent) {
	c.events = append(c.events, ev)
}

// 用户报告（2026-09-28）：「askuser 在被用户取消后，前端还是渲染为 busy」。
//
// 真实路径：Agent 调 AskUser → Run 以 WaitingUser 暂停返回（chatProcessLoop
// **已把 ss.busy 置 false** —— busy ⇔ iterating，WaitingUser 是暂停不是执行），
// pending prompt 保留、chatCancelCh 已注销。用户点面板 Cancel ⇒ web.go 路由
// `/cancel` + `ask_user_cancel=true` ⇒ interceptCancel 走「无活跃 Run + 有 pending」
// 分支。
//
// 该分支**必须**发 session(idle)：WaitingUser 暂停结束时（取消 = 交互结束、turn
// 终止），前端的唯一收尾依据就是这条会话级 idle —— 它让状态机清掉 activeTurn
// （busyFallback = activeTurn !== null）并把 live turn 定稿。否则前端在
// prompt 本地清掉后（`!askUser.prompt` 闸门消失）仍看到 busyFallback/streaming
// 为真 ⇒ 输入框永久渲染为 busy/stop。
//
// 旧代码把 idle 发射门控在 `ss.busy.Load()` 上（当时暂停期保持 busy=true），
// 但暂停期改成 busy=false 后该闸门恒假 ⇒ 真实取消路径永不发 idle ⇒ 本 bug。
func TestAskUserCancelDuringWaitingUserPauseEmitsSessionIdle(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	events := &askUserCancelIdleChannel{}
	a.channelFinder = func(name string) (channel.Channel, bool) {
		if name == "web" {
			return events, true
		}
		return nil, false
	}

	key := "web:chat-1"
	// 真实 WaitingUser 暂停态：chatProcessLoop 已把 busy 置 false（busy ⇔ iterating）。
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ss.busy.Store(false)
	a.bgSessionStates.Store(key, ss)
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})

	a.interceptCancel(bus.InboundMessage{
		Channel:  "web",
		ChatID:   "chat-1",
		Content:  "/cancel",
		Metadata: map[string]string{"ask_user_cancel": "true"},
	})

	idle := false
	for _, ev := range events.events {
		if ev.Action == "idle" && ev.Channel == "web" && ev.ChatID == "chat-1" {
			idle = true
		}
	}
	if !idle {
		t.Fatalf("AskUser cancel during the WaitingUser pause emitted no session(idle): %#v — 前端状态机 activeTurn 永不清 ⇒ 输入框卡 busy（用户报告）", events.events)
	}
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("AskUser prompt remained after cancel: %#v", pending)
	}
}
