package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/protocol"
)

// TestAskUserInterruptThroughProcessLoop 是 D-03 在**交付线**上的判据（不依赖
// AskUser 工具、不依赖 /api/message 的 kind=ask_user）：
//
//	会话上有一条**新鲜**的待答提问，此时用户插一句话（新 turn 开始走
//	chatProcessLoop 的 keepRunning）——提问必须还在。
//
// 为什么必须走 loop 而不是直接调 autoResolveStalePendingAskUser：直接调只能证明
// 函数本身对，证明不了调用点还在。删掉 keepRunning 里那一行调用，本用例会红。
//
// 该文件刻意**不引用 TTL 常量、不引用 createdAt 字段**，因此可以在修复前的树
// （5198f54c 家族）上编译并跑红：那时任何新 turn 都会无条件收回提问 ⇒ 必红。
func TestAskUserInterruptThroughProcessLoop(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	// 预热会话库：loop 里的 processMessage 会打开/写会话库，Windows 上首次打开
	// 是十几秒级（见 testAsyncWait），不能让它挤占「多久内走完 keepRunning」的窗口。
	if _, err := mt.GetOrCreateSession("web", "chat-interrupt"); err != nil {
		t.Fatalf("warm session db: %v", err)
	}
	a := &Agent{bus: bus.NewMessageBus(), multiSession: mt}

	chatKey := "web:chat-interrupt"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)

	a.setPendingAskUser("web", "chat-interrupt", &protocol.ProgressEvent{RequestID: "req-pending"})
	if a.GetPendingAskUser("web", "chat-interrupt") == nil {
		t.Fatal("前置失败：pending 没登记上，用例无意义")
	}

	msgCh := make(chan bus.InboundMessage, 4)
	done := make(chan struct{})
	go func() {
		a.chatProcessLoop(context.Background(), chatKey, msgCh, ss)
		close(done)
	}()
	defer func() {
		close(msgCh)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Log("chatProcessLoop 未在 3s 内退出（不影响本断言）")
		}
	}()

	// 用户插话：一条普通新消息。
	msgCh <- bus.InboundMessage{
		Channel: "web", ChatID: "chat-interrupt", Content: "等一下，我先问个别的", RequestID: "req-interrupt",
		Metadata: map[string]string{"turn_id": "1"},
	}

	// 给 loop 时间把 turn 起起来并执行到 keepRunning 里的提问处理（不依赖 LLM：
	// 即使 processMessage 因未配置模型而报错，那一步也已经执行过）。
	// 上限用 testAsyncWait：Windows CI 上这段链路含 SQLite 打开/写入，个位数秒
	// 的 deadline 就是在赌机器速度（见 testAsyncWait 的现场日志）。
	deadline := time.Now().Add(testAsyncWait)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if ss.busy.Load() {
			// busy 已置位 ⇒ 已进入 keepRunning；autoResolveStalePendingAskUser 是
			// Store(true) 之后的紧邻语句，纯内存判定。这里给足余量只为消除
			// 「goroutine 被调度推迟」这一变量，不改变断言语义。
			time.Sleep(2 * time.Second)
			break
		}
	}

	if pending := a.GetPendingAskUser("web", "chat-interrupt"); pending == nil {
		t.Fatal("插话把待答提问丢了（D-03）：pending 应为非 nil —— 用户再也答不上这个问题")
	}
}
