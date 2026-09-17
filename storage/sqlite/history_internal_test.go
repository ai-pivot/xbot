package sqlite

import (
	"testing"

	"xbot/llm"
)

// Internal 消息（模型侧载体，如 view_image 的多模态注入）契约：
//   - 持久化后可读回（Internal=true）—— 保证后续 turn 的 LLM 上下文仍能看到图片；
//   - 仍出现在 Replay（LLM 上下文）里；
//   - 渲染路径负责剔除它（channel/view_image_internal_test.go）。
//
// 用户报告 2026-09-16：web 上传图片后用户自己的消息被该注入行顶掉（两者同 turn_id）。
func TestInternalMessageRoundTrip(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	internal := llm.NewUserMessage("📷 以下图片已通过 view_image 工具加载\n\n![x](/api/files/viewimg/abc.jpeg)")
	internal.TurnID = 904
	internal.Internal = true
	if _, err := svc.AppendMessage(tenantID, internal); err != nil {
		t.Fatalf("append internal: %v", err)
	}

	normal := llm.NewUserMessage("普通消息")
	normal.TurnID = 905
	if _, err := svc.AppendMessage(tenantID, normal); err != nil {
		t.Fatalf("append normal: %v", err)
	}

	replay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var internalSeen, normalInternal bool
	for i := range replay.Messages {
		m := replay.Messages[i]
		if m.TurnID == 904 && m.Role == "user" {
			internalSeen = true
			if !m.Internal {
				t.Fatalf("internal_only 未往返（读回 false）：%+v", m)
			}
		}
		if m.TurnID == 905 {
			normalInternal = normalInternal || m.Internal
		}
	}
	if !internalSeen {
		t.Fatal("Internal 消息必须保留在 LLM 上下文（Replay）里 —— 模型后续 turn 仍要看到图片")
	}
	if normalInternal {
		t.Fatal("普通消息被误标 Internal")
	}
}
