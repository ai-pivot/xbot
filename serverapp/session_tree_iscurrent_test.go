package serverapp

import (
	"testing"

	"xbot/channel/web"
)

// TestMarkCurrentSession pins the 2026-10-27 regression where IsCurrent was
// silently dropped for every row.
//
// 背景：行构造统一成一次 listTenantsByChannels 后，那条路径不再接收
// currentChatID，于是没有任何代码设置 IsCurrent。前端 useSessionStore.ts:947
// 靠这个字段恢复当前会话，冷启动因此退化成"选中第一行"。
// 门下省探针：HEAD 82794d3b → IsCurrent 全 false → FAIL；d932b2e0 → PASS。
func TestMarkCurrentSession(t *testing.T) {
	rows := []web.UserChatWithPreview{
		{Channel: "web", ChatID: "chat_a"},
		{Channel: "web", ChatID: "chat_b"},
		{Channel: "cli", ChatID: "chat_a"}, // 同 chatID 不同 channel，不能误判
		{Channel: "qq", ChatID: "group_x"},
	}

	markCurrentSession(rows, web.SessionSelector{Channel: "web", ChatID: "chat_b"})

	want := map[int]bool{0: false, 1: true, 2: false, 3: false}
	for i, expect := range want {
		if rows[i].IsCurrent != expect {
			t.Errorf("row[%d] (%s:%s) IsCurrent = %v, want %v",
				i, rows[i].Channel, rows[i].ChatID, rows[i].IsCurrent, expect)
		}
	}

	// 换一个选择必须重新计算（不是一次性固化）。
	markCurrentSession(rows, web.SessionSelector{Channel: "cli", ChatID: "chat_a"})
	if !rows[2].IsCurrent {
		t.Error("cli:chat_a should be current after the selection changed")
	}
	if rows[1].IsCurrent {
		t.Error("web:chat_b must be cleared when the selection moves away")
	}
}

// TestMarkCurrentSession_EmptySelection ensures an unknown selection marks
// nothing (the sidebar then falls back to its own default, rather than
// highlighting a wrong row).
func TestMarkCurrentSession_EmptySelection(t *testing.T) {
	rows := []web.UserChatWithPreview{
		{Channel: "web", ChatID: "chat_a"},
	}
	markCurrentSession(rows, web.SessionSelector{})
	if rows[0].IsCurrent {
		t.Error("empty selector must not mark any row current")
	}
}
