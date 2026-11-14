package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// TestSubAgentNotifyReachesRealParentSession 是 2026-11-03 P0 的端到端回归：
//
// 首辅（interactive 子代理，parentKey 被旧代码写成镜像地址 "agent:web:web-4"）
// turn 结束后投递完成通知，通知必须落到**用户可见会话** (web, web-4)，
// 而不是镜像会话 (agent, web:web-4)。
//
// 线上故障现场：36 次通知全部投进 sessions/agent/8f/web_web-4-*.db
// （13MB / 2934 条），御前 12 小时一条都没收到。
//
// 变异自证：把 drainAndProcessNotifications 里的 sessionAddress 归一化删掉，
// 本测试会在 2 秒后以 "notification never reached" 失败。
func TestSubAgentNotifyReachesRealParentSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(tools.NewBackgroundTaskManager())

	// 旧代码（spawn 路径修复前）写进 parentKey 的就是这个镜像地址。
	const mirrorKey = "agent:web:web-4"
	a.enqueueBgNotification(&tools.SubAgentBgNotify{
		Key:       mirrorKey,
		ParentKey: mirrorKey,
		Type:      tools.SubAgentBgNotifyCompleted,
		Role:      "court.shoufu",
		Instance:  "main",
		Content:   "PONG-15:30",
	})

	a.drainAndProcessNotifications(mirrorKey)

	select {
	case msg := <-a.bus.Inbound:
		if msg.Channel != "web" || msg.ChatID != "web-4" {
			t.Fatalf("notification delivered to %s:%s, want web:web-4 "+
				"(mirror address must collapse to the real session)", msg.Channel, msg.ChatID)
		}
		if msg.Content == "" {
			t.Fatal("delivered notification has empty content")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never reached the parent session — " +
			"sub-agent completion notices are being swallowed")
	}
}

// TestDrainKeepsInteractiveSessionAddress 锁住归一化的边界：
// interactive 子代理的会话地址 "agent:web:web-4/court.shoufu:main"（chatID 含 "/"）
// 是**真会话**，绝不能被剥成 (web, "web-4/court.shoufu:main") —— 那会凭空
// 造出一个 web 渠道的畸形会话（2026-11-03 15:16 实测踩过，建出
// sessions/web/91/web-4_court.shoufu_main-*.db 并跑了一轮空 turn）。
func TestDrainKeepsInteractiveSessionAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(tools.NewBackgroundTaskManager())

	const interactiveKeyAddr = "agent:web:web-4/court.shoufu:main"
	a.enqueueBgNotification(&tools.SubAgentBgNotify{
		Key:       interactiveKeyAddr,
		ParentKey: interactiveKeyAddr,
		Type:      tools.SubAgentBgNotifyCompleted,
		Role:      "court.gongbu",
		Instance:  "xbot-ab-1203",
		Content:   "delivered",
	})

	a.drainAndProcessNotifications(interactiveKeyAddr)

	select {
	case msg := <-a.bus.Inbound:
		if msg.Channel == "web" {
			t.Fatalf("interactive session address was wrongly flattened to %s:%s — "+
				"this creates a phantom web session", msg.Channel, msg.ChatID)
		}
		if msg.Channel != "agent" || msg.ChatID != "web:web-4/court.shoufu:main" {
			t.Fatalf("delivered to %s:%s, want agent:web:web-4/court.shoufu:main",
				msg.Channel, msg.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interactive session notification never delivered")
	}
}
