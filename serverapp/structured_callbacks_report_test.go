package serverapp

import (
	"testing"
	"time"
)

// structured_callbacks_report_test.go —— report 唤醒冷却的回归。
//
// 2026-10-22 review 发现的两个缺陷（均已修）：
//  1. report 进入 deliverable 后加了 60s 冷却，但冷却期内只把 deliver 置 false，
//     没有任何补投者 ⇒ 被压掉的 report 永远不会醒（静默丢弃）。真实拓扑是 6 个
//     子代理上报同一个 chatID，最坏情况只唤醒第一条。
//  2. 冷却只看 chatID 不看 payload ⇒ needs_approval / urgent 的报告也被压掉，
//     违反 上报规范。

func TestStructuredDeliverableKinds(t *testing.T) {
	for _, kind := range []string{"answer", "decision", "command", "text", "report"} {
		if !structuredDeliverable(kind) {
			t.Errorf("structuredDeliverable(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"event", "ask_user", "", "unknown"} {
		if structuredDeliverable(kind) {
			t.Errorf("structuredDeliverable(%q) = true, want false", kind)
		}
	}
}

func TestPayloadBypassesReportCooldown(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"nil", nil, false},
		{"empty", map[string]any{}, false},
		{"needs_approval bool true", map[string]any{"needs_approval": true}, true},
		{"needs_approval string true", map[string]any{"needs_approval": "true"}, true},
		{"needs_approval string TRUE", map[string]any{"needs_approval": " TRUE "}, true},
		{"needs_approval false", map[string]any{"needs_approval": false}, false},
		{"urgent true", map[string]any{"urgent": true}, true},
		{"unrelated", map[string]any{"reason": "done"}, false},
	}
	for _, c := range cases {
		if got := payloadBypassesReportCooldown(c.payload); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReportDeliverDelayCooldown(t *testing.T) {
	resetReportDeliverSeen()
	t.Cleanup(resetReportDeliverSeen)

	payload := map[string]any{"summary": "盘库完成"}

	if d := reportDeliverDelay("web", "chat-A", payload); d != 0 {
		t.Fatalf("first report: delay = %v, want 0", d)
	}
	d := reportDeliverDelay("web", "chat-A", payload)
	if d <= 0 || d > reportDeliverCooldown {
		t.Fatalf("second report inside cooldown: delay = %v, want (0,60s]", d)
	}

	// 回拨 61s：冷却已过期，必须重新立即唤醒。
	reportDeliverMu.Lock()
	reportDeliverSeen[reportDeliverKey("web", "chat-A")] = time.Now().Add(-reportDeliverCooldown - time.Second)
	reportDeliverMu.Unlock()
	if d := reportDeliverDelay("web", "chat-A", payload); d != 0 {
		t.Fatalf("after cooldown expiry: delay = %v, want 0", d)
	}
}

func TestReportDeliverDelayPerChannel(t *testing.T) {
	resetReportDeliverSeen()
	t.Cleanup(resetReportDeliverSeen)

	payload := map[string]any{"summary": "x"}
	if d := reportDeliverDelay("web", "chat-A", payload); d != 0 {
		t.Fatalf("web first: delay = %v", d)
	}
	// 同 chatID、不同 channel 不得互相压制。
	if d := reportDeliverDelay("cli", "chat-A", payload); d != 0 {
		t.Fatalf("cli same chatID must not be suppressed by web, delay = %v", d)
	}
}

// TestReportDeliverDelayUrgentNeverDelayed：冷却窗口内的 urgent / needs_approval
// 必须立刻唤醒（delay=0），这是 上报规范 的硬要求。
func TestReportDeliverDelayUrgentNeverDelayed(t *testing.T) {
	resetReportDeliverSeen()
	t.Cleanup(resetReportDeliverSeen)

	if d := reportDeliverDelay("web", "chat-B", map[string]any{"summary": "普通"}); d != 0 {
		t.Fatalf("first: delay = %v", d)
	}
	if d := reportDeliverDelay("web", "chat-B", map[string]any{"needs_approval": true}); d != 0 {
		t.Fatalf("needs_approval inside cooldown must not be delayed, got %v", d)
	}
	if d := reportDeliverDelay("web", "chat-B", map[string]any{"urgent": true}); d != 0 {
		t.Fatalf("urgent inside cooldown must not be delayed, got %v", d)
	}
}
