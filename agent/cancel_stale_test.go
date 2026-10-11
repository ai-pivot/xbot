package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// TestChatProcessLoop_SelfHealsStaleCancelState —— 2026-10-01 cancel 无效事故的守护。
//
// 事故形态（chat_D3D036023DB7 复盘）：turn 3 在 10:40:47 的 LLM 请求发出后 Run 无声消失
// （goroutine dump 无其栈、无 panic 日志、无错误日志），keepRunning 的收尾 defer
// （finishActiveCancelState）未执行 ⇒ chatCancelCh 注册残留 ⇒ IsProcessingByChannel 永真
// ⇒ busy 卡死 + 新消息永 queue；/cancel 信号发进无人拥有的 channel——cancelListener 消费
// 一次后按 reqCtx.Done() 退出，后续 buffer full——用户四连 cancel 全部无效。
//
// 修复：chatProcessLoop 顶层 defer 检测残留并自愈（删注册 + close 通知 listener + busy
// 复位 + emit idle）。
//
// mutation 判别力：删掉 chatProcessLoop 顶层的 cleanupStaleCancelState defer ⇒ 本条必红
// （注册残留 + busy 未复位 + listener 未收到 close——正是事故形态）。
func TestChatProcessLoop_SelfHealsStaleCancelState(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	events := &askUserCancelIdleChannel{}
	a.channelFinder = func(name string) (channel.Channel, bool) {
		if name == "web" {
			return events, true
		}
		return nil, false
	}

	chatKey := "web:chat-stale-selfheal"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)

	// 模拟「Run 无声消失后残留」：直接注册 cancel state + busy 置位（跳过 keepRunning 的
	// 收尾——那正是事故中发生的事）。
	cancelCh := make(chan struct{}, 1)
	a.chatCancelCh.Store(chatKey, cancelCh)
	a.pendingCancel.Store(chatKey, true)
	ss.busy.Store(true)

	// 模拟残留的 cancelListener（chatProcessLoop 内的监听 goroutine）——兜底必须通过
	// close(ch) 通知它（reqCancel 链）。
	gotClose := make(chan struct{})
	go func() {
		<-cancelCh
		close(gotClose)
	}()

	// 起最小 chatProcessLoop 并立即关闭消息通道 ⇒ 循环退出 ⇒ 顶层 defer 兜底。
	msgCh := make(chan bus.InboundMessage, 1)
	done := make(chan struct{})
	go func() {
		a.chatProcessLoop(context.Background(), chatKey, msgCh, ss)
		close(done)
	}()
	close(msgCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("chatProcessLoop 未在 msgCh close 后退出")
	}

	select {
	case <-gotClose:
	case <-time.After(2 * time.Second):
		t.Fatal("残留 cancelListener 未收到 close 通知——reqCancel 链断（listener 将永等）")
	}
	if _, found := a.chatCancelCh.Load(chatKey); found {
		t.Fatal("cancel state 注册残留未自愈——IsProcessing 永真 ⇒ busy 卡死 + queue 永挂（事故形态）")
	}
	if _, pending := a.pendingCancel.Load(chatKey); pending {
		t.Fatal("pendingCancel 残留未清理——下一轮 Run 会被误取消")
	}
	if ss.busy.Load() {
		t.Fatal("busy 未复位——会话将永卡 queue")
	}
	var idles []protocol.SessionEvent
	for _, ev := range events.events {
		if ev.Action == "idle" && ev.Channel == "web" && ev.ChatID == "chat-stale-selfheal" {
			idles = append(idles, ev)
		}
	}
	if len(idles) != 1 {
		t.Fatalf("自愈应恰好 emit 一次 session(idle)（got %d）——前端 busy 解除依赖它", len(idles))
	}
}

// TestChatProcessLoop_NormalExitNoFalseIdle —— 正常轮次的收尾（finishActiveCancelState）
// 已删注册 ⇒ 顶层 defer 检测不到残留 ⇒ 必须零副作用（不多发 idle、不动 busy）。
// 这是「检测到残留才兜底」的对称面：兜底不得干扰正常生命周期。
func TestChatProcessLoop_NormalExitNoFalseIdle(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	events := &askUserCancelIdleChannel{}
	a.channelFinder = func(name string) (channel.Channel, bool) {
		if name == "web" {
			return events, true
		}
		return nil, false
	}
	chatKey := "web:chat-normal-exit"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ss.busy.Store(false) // 正常收尾后 busy 已是 false

	msgCh := make(chan bus.InboundMessage, 1)
	done := make(chan struct{})
	go func() {
		a.chatProcessLoop(context.Background(), chatKey, msgCh, ss)
		close(done)
	}()
	close(msgCh)
	<-done

	if len(events.events) != 0 {
		t.Fatalf("正常退出（无残留）不得 emit 任何事件（got %d）——兜底必须是零副作用", len(events.events))
	}
	if _, found := a.chatCancelCh.Load(chatKey); found {
		t.Fatal("正常退出不得残留注册")
	}
}
