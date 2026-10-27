package agent

import (
	"context"
	"strings"
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

// TestAgentPathKeyNotificationIsNotDropped 保命测试：key 是 agent 路径时通知不许丢。
//
// 2026-10-27 真机故障：首辅 DB 已 idle（08:49:24Z），御前却一直收不到完成通知。
// 根因：通知的 Key 有时是 **AgentID 路径**（如 `main/court.shoufu/court.bingbu`），
// 而 drainAndProcessNotifications 是「先 takePending 取走通知 → 再校验 key —— 非法就
// return」，于是通知被**永久吞掉**（日志只有一行 warning: invalid session key）。
//
// 修复：校验提前到 take 之前；非法 key 走 agent-路径兜底（按倒数第二段找父会话），
// 找不到也不丢，留在队列里并高声告警。
func TestAgentPathKeyNotificationIsNotDropped(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: context.Background()}

	const badKey = "main/court.shoufu/court.bingbu"
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)

	// 直接入队一条 key 为非法的通知（模拟现场）
	a.enqueueBgNotification(&tools.SubAgentBgNotify{
		Key:      badKey,
		Type:     tools.SubAgentBgNotifyCompleted,
		Role:     "court.bingbu",
		Instance: "main",
		Content:  "任务完成：已删除 overdue 项",
	})

	// 调用 drain：不允许静默丢弃 —— 通知必须离开该 key 的队列，
	// 要么被投递出去，要么被保留到别处（"recovered"）。
	a.drainAndProcessNotifications(badKey)

	if left := a.pendingBgNotifications(badKey); len(left) != 0 {
		t.Fatalf("非法 key 的通知仍滞留在原队列（%d 条），说明兜底没走通", len(left))
	}
	// 兜底路径必须把内容保留在某处（投递到主会话或 "recovered" 队列），不能蒸发：
	recovered := a.pendingBgNotifications("recovered")
	select {
	case msg := <-a.bus.Inbound:
		if !strings.Contains(msg.Content, "任务完成") {
			t.Fatalf("投递内容丢失: %q", msg.Content)
		}
		return
	default:
	}
	if len(recovered) == 0 {
		t.Fatal("通知既没投递也没保留 —— 又被吞了")
	}
}
