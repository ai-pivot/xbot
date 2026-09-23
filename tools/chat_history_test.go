package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// ChatHistory 必须读会话历史的**唯一权威**（session_messages + Replay）：
//  1. 能读到 DB 里的对话消息，且过滤 tool 等过程噪声；
//  2. limit 生效（只回最近 N 条对话消息）；
//  3. **回溯（RewindToHistoryID 物理删除）之后，被截断的消息不能再被捞到**。
//
// 第 3 条是本工具曾经的 bug：它另存了一份进程内 ring（每条入站消息 Add 一次），
// 而 RewindHistory/Clear 都不清它 —— 用户回溯后仍能用本工具"回忆"起被截断的
// 消息（实测复现：回溯后仍返回 888）。会话内容的第二份副本 = 必漏，故删除。
func TestChatHistoryToolReadsAuthoritativeSessionHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(filepath.Join(dir, "xbot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sess := sqlite.NewSessionService(db)
	tenantID, err := sqlite.NewTenantService(db).GetOrCreateTenantID("web", "c1")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}

	appendMsg := func(role, content string) int64 {
		t.Helper()
		id, err := sess.AppendMessage(tenantID, llm.ChatMessage{Role: role, Content: content})
		if err != nil {
			t.Fatalf("append %s: %v", role, err)
		}
		return id
	}
	appendMsg("user", "第一句 777")
	appendMsg("assistant", "收到 111")
	id2 := appendMsg("user", "第二句 888")
	appendMsg("tool", "tool noise 999")

	tool := NewChatHistoryTool()
	ctx := &ToolContext{TenantID: tenantID, SessionSvc: sess, Channel: "web", ChatID: "c1"}

	res, err := tool.Execute(ctx, `{"limit": 10}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := res.Summary
	for _, want := range []string{"777", "111", "888", "<user>", "<assistant>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("history 缺少 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "tool noise") {
		t.Fatalf("tool 行不该出现在对话历史里：\n%s", out)
	}

	// limit 生效：只要最近 1 条对话消息。
	res, err = tool.Execute(ctx, `{"limit": 1}`)
	if err != nil {
		t.Fatalf("execute limit=1: %v", err)
	}
	if strings.Count(res.Summary, "<") != 1 || !strings.Contains(res.Summary, "888") {
		t.Fatalf("limit=1 应只返回最后一条对话消息：\n%s", res.Summary)
	}

	// 关键回归：回溯到"第二句 888"→ DB 里它（及之后）被物理删除 → 工具必须立即读不到。
	if _, _, err := sess.RewindToHistoryID(tenantID, id2); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	res, err = tool.Execute(ctx, `{"limit": 10}`)
	if err != nil {
		t.Fatalf("execute after rewind: %v", err)
	}
	if strings.Contains(res.Summary, "888") {
		t.Fatalf("回溯后仍能捞到被截断的消息（第二份副本没清干净）：\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "777") {
		t.Fatalf("回溯只应截断其后的消息，777 应保留：\n%s", res.Summary)
	}

	// 无会话上下文时给出明确结果（而不是静默返回历史）。
	res, err = tool.Execute(&ToolContext{}, `{"limit": 5}`)
	if err != nil {
		t.Fatalf("execute without ctx: %v", err)
	}
	if !strings.Contains(res.Summary, "No active conversation context") {
		t.Fatalf("缺少会话上下文时应明确说明：%q", res.Summary)
	}
}
