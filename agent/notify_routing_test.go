package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// TestBgNotifyRoutedToParentInteractiveSession 守住 2026-10-26 的越级投递 bug：
//
// 嵌套 spawn 时子会话的 origin 继承自主会话，通知的 Key 一度恒为
// "channel:chatID"，于是子代理完成后通知被注入主会话，而派发它的父 agent
// （首辅）永远收不到结果、一直 idle。
//
// 修复后：ParentKey 指向一个仍在的 interactive session 时，
// 通知必须交给它，且**不得**残留在主会话的 pending 队列里。
func TestBgNotifyRoutedToParentInteractiveSession(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: context.Background()}
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)
	go a.bgNotifyLoop()

	const parentKey = "web:chat1/court.shoufu:main"
	const originKey = "web:chat1"

	// 注册一个"活的"父会话（投递时会因会话不完整而失败并被记录，不能 panic）
	parent := &interactiveAgent{roleName: "court.shoufu", instance: "main", parentKey: originKey}
	a.interactiveSubAgents.Store(parentKey, parent)

	mgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
		Key:       parentKey,
		ParentKey: parentKey,
		Type:      tools.SubAgentBgNotifyCompleted,
		Role:      "court.gongbu",
		Instance:  "qq-channel-plugin",
		Content:   "阶段 2 完成",
		Sid:       "user-1",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(a.pendingBgNotifications(originKey)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := a.pendingBgNotifications(originKey); len(got) != 0 {
		t.Fatalf("通知越级残留在主会话 pending 队列：%d 条（应交给父会话）", len(got))
	}
}

// TestBgNotifyFallsBackToOriginWhenParentGone 守住兜底：
// 父会话已被 TTL/unload 回收时，通知不能凭空消失，必须落回 origin 会话。
func TestBgNotifyFallsBackToOriginWhenParentGone(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: context.Background()}
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)
	go a.bgNotifyLoop()

	const parentKey = "web:chat2/court.shoufu:main"

	// 不注册父会话 → 走兜底
	mgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
		Key:       parentKey,
		ParentKey: parentKey,
		Type:      tools.SubAgentBgNotifyCompleted,
		Role:      "court.gongbu",
		Instance:  "x",
		Content:   "done",
		Sid:       "user-1",
	})

	// 兜底路径会立刻 drain 并注入 origin 会话，所以断言"消息真的到了 origin"，
	// 而不是"留在 pending"（pending 会在 drain 时被取空）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case msg := <-a.bus.Inbound:
			if msg.Channel != "web" || msg.ChatID != "chat2" {
				t.Fatalf("兜底投递落到错误会话: channel=%q chatID=%q", msg.Channel, msg.ChatID)
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("父会话不存在时通知没落到 origin 会话 —— 会丢")
}

// TestParseInteractiveKeyTailForParentKey 锁住父会话 key 的解析契约，
// deliverNotifyToParentSession 依赖它把 Key 还原成 (channel, chatID, role, instance)。
func TestParseInteractiveKeyTailForParentKey(t *testing.T) {
	ch, chat, role, inst := parseInteractiveKeyTail("web:web-4/court.shoufu:main")
	if ch != "web" || chat != "web-4" || role != "court.shoufu" || inst != "main" {
		t.Fatalf("解析错误: ch=%q chat=%q role=%q inst=%q", ch, chat, role, inst)
	}
}
