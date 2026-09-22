package sqlite

import (
	"testing"

	"xbot/llm"
)

// 命令行（`!cmd` / slash）的落库语义：**只给 UI 看**（刷新后仍在），**绝不进 LLM 上下文**。
//
// 用户报告（2026-09-21）：「为什么 !cmd 消息的输入输出在页面刷新之后就消失了？」——
// 根因是命令此前完全不落库（命令不走 processMessage，只 sendCommandReply）⇒ 刷新 =
// 从 DB 重建渲染状态 ⇒ 输入与输出都消失。本测试锁死落库后的两个方向：
//
//	① 展示回放（ReplayForDisplay，web 历史路径）**包含**这两行，且带 CommandRow 标记
//	   （转换层据此写 standalone + 锚点，前端插回原位）；
//	② LLM 回放（Replay，构造 prompt 用）**不含**它们（display_only=1 + record_type='command'
//	   双保险）。
func TestAppendCommandMessage_DisplayOnlyRoundTrip(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// 先放一个普通 turn，便于观察命令行在历史里的相对位置。
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "hi", TurnID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "hello", TurnID: 1}); err != nil {
		t.Fatal(err)
	}
	// 命令行：输入 + 输出（落库 = 只给 UI）。
	if _, err := svc.AppendCommandMessage(tenantID, "user", "!pwd"); err != nil {
		t.Fatalf("AppendCommandMessage(user): %v", err)
	}
	if _, err := svc.AppendCommandMessage(tenantID, "assistant", "/root"); err != nil {
		t.Fatalf("AppendCommandMessage(assistant): %v", err)
	}

	// ① 展示回放必须包含命令行（刷新后仍在），且带 CommandRow 标记。
	disp, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatalf("ReplayForDisplay: %v", err)
	}
	var cmdRows []llm.ChatMessage
	for _, m := range disp.Messages {
		if m.CommandRow {
			cmdRows = append(cmdRows, m)
		}
	}
	if len(cmdRows) != 2 {
		t.Fatalf("展示回放必须包含命令行输入+输出（且带 CommandRow 标记），got %d 行: %+v", len(cmdRows), disp.Messages)
	}
	if cmdRows[0].Role != "user" || cmdRows[0].Content != "!pwd" {
		t.Fatalf("命令行第一行应为 user/!pwd，got %s/%q", cmdRows[0].Role, cmdRows[0].Content)
	}
	if cmdRows[1].Role != "assistant" || cmdRows[1].Content != "/root" {
		t.Fatalf("命令行第二行应为 assistant//root，got %s/%q", cmdRows[1].Role, cmdRows[1].Content)
	}

	// ② LLM 回放绝不能包含命令行。
	llmCtx, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	for _, m := range llmCtx.Messages {
		if m.Content == "!pwd" || m.Content == "/root" {
			t.Fatalf("命令行绝不能进 LLM 上下文（Replay 必须排除）: role=%s content=%q", m.Role, m.Content)
		}
	}
}
