package sqlite

import (
	"testing"
	"time"

	"xbot/llm"
)

func TestPendingResume_CRUD(t *testing.T) {
	db := openTestDB(t)

	// Initially empty
	list, err := db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}

	// Add a pending resume
	if err := db.AddPendingResume("web", "chat-1", "web-1"); err != nil {
		t.Fatalf("AddPendingResume: %v", err)
	}

	// List should have 1 entry
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0].Channel != "web" || list[0].ChatID != "chat-1" || list[0].SenderID != "web-1" {
		t.Fatalf("unexpected entry: %+v", list[0])
	}

	// Upsert (same key) replaces
	if err := db.AddPendingResume("web", "chat-1", "web-1"); err != nil {
		t.Fatalf("AddPendingResume upsert: %v", err)
	}
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d", len(list))
	}

	// Clear single
	if err := db.ClearPendingResume("web", "chat-1"); err != nil {
		t.Fatalf("ClearPendingResume: %v", err)
	}
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes after clear: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty after clear, got %d", len(list))
	}

	// Clear multiple entries individually
	db.AddPendingResume("web", "chat-1", "web-1")
	db.AddPendingResume("feishu", "chat-2", "ou_xxx")
	if err := db.ClearPendingResume("web", "chat-1"); err != nil {
		t.Fatalf("ClearPendingResume web: %v", err)
	}
	if err := db.ClearPendingResume("feishu", "chat-2"); err != nil {
		t.Fatalf("ClearPendingResume feishu: %v", err)
	}
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes after clear all: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty after clear all, got %d", len(list))
	}
}

// v71（one session, one DB）之后消息内容存在【会话库】——resume 相关的
// 内容/回复查询必须经会话库的 SessionService（TenantSession 同名代理转发
// 到这里），主库的旧 session_messages 已不是查询目标（恒空）。主库只保留
// 身份半边（user_chats 的 sender_id）。

// TestSessionService_GetLastUserMessageContent 会话库版「最后一条用户消息」：
// 多轮取最后一条；internal_only 注入行（view_image follow-up 复用触发消息
// 的 turn、排在真实 user 之后）绝不能被选成 resume 内容。
func TestSessionService_GetLastUserMessageContent(t *testing.T) {
	_, svc := openSessionDBForTest(t)
	const tenantID = int64(42)

	// 空会话 → ("", nil)，不报错
	content, err := svc.GetLastUserMessageContent(tenantID)
	if err != nil {
		t.Fatalf("GetLastUserMessageContent on empty: %v", err)
	}
	if content != "" {
		t.Fatalf("expected empty, got %q", content)
	}

	// user → assistant → user：取最后一条 user
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "first question", Timestamp: time.Now()})
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "first answer", Timestamp: time.Now()})
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "second question", Timestamp: time.Now()})
	content, err = svc.GetLastUserMessageContent(tenantID)
	if err != nil {
		t.Fatalf("GetLastUserMessageContent: %v", err)
	}
	if content != "second question" {
		t.Fatalf("expected 'second question', got %q", content)
	}

	// view_image 注入行：Internal=true、role=user、排在真实 user 之后 ——
	// 若不过滤 internal_only，重启 resume 会把「📷 …」注入行当用户输入。
	injected := llm.ChatMessage{Role: "user", Content: "![img](/api/files/viewimg/xxx)", Timestamp: time.Now()}
	injected.Internal = true
	svc.AddMessage(tenantID, injected)
	content, err = svc.GetLastUserMessageContent(tenantID)
	if err != nil {
		t.Fatalf("GetLastUserMessageContent after internal row: %v", err)
	}
	if content != "second question" {
		t.Fatalf("internal injection must not shadow the real user message, got %q", content)
	}
}

// TestGetSessionSenderID 主库 user_chats 注册表解析 sender_id（resume 的身份
// 半边）。无行时返回 ""，不得 NULL→crash（CLI 本地会话形态）。
func TestGetSessionSenderID(t *testing.T) {
	db := openTestDB(t)

	// No user_chats row (CLI sessions created locally) → empty, no crash
	senderID, err := db.GetSessionSenderID("cli", "/workspace")
	if err != nil {
		t.Fatalf("GetSessionSenderID with no row: %v", err)
	}
	if senderID != "" {
		t.Fatalf("expected empty senderID, got %q", senderID)
	}

	conn := db.Conn()
	if _, err := conn.Exec(`INSERT OR IGNORE INTO user_chats (channel, sender_id, chat_id) VALUES (?, ?, ?)`,
		"web", "web-1", "chat-1"); err != nil {
		t.Fatalf("insert user_chats: %v", err)
	}
	senderID, err = db.GetSessionSenderID("web", "chat-1")
	if err != nil {
		t.Fatalf("GetSessionSenderID: %v", err)
	}
	if senderID != "web-1" {
		t.Fatalf("expected 'web-1', got %q", senderID)
	}
}

// TestSessionService_HasAssistantReplyAfterLastUser 会话库版「最后一条 user
// 之后是否已有最终回复」：语义与旧主库版完全一致 —— display_only 不算、带
// tool_calls 的中间行不算（IncrementalPersist 落的中间态不是最终回复）。
func TestSessionService_HasAssistantReplyAfterLastUser(t *testing.T) {
	_, svc := openSessionDBForTest(t)
	const tenantID = int64(7)

	// No messages: no reply
	hasReply, err := svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser empty: %v", err)
	}
	if hasReply {
		t.Fatal("expected false on empty")
	}

	// user only, no assistant reply yet
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "hello", Timestamp: time.Now()})
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after user: %v", err)
	}
	if hasReply {
		t.Fatal("expected false: no assistant reply after last user")
	}

	// assistant reply added → true
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "hi there", Timestamp: time.Now()})
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after reply: %v", err)
	}
	if !hasReply {
		t.Fatal("expected true: assistant reply exists after last user")
	}

	// new user message (turn 2), no reply yet → false
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "again", Timestamp: time.Now()})
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser turn2: %v", err)
	}
	if hasReply {
		t.Fatal("expected false: turn 2 has no assistant reply yet")
	}

	// display_only assistant (e.g. user_cancelled synthetic) should NOT count
	// as a reply — key for graceful shutdown resume.
	cancelMsg := llm.ChatMessage{Role: "assistant", Content: "cancelled", Timestamp: time.Now()}
	cancelMsg.DisplayOnly = true
	svc.AddMessage(tenantID, cancelMsg)
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after display_only: %v", err)
	}
	if hasReply {
		t.Fatal("expected false: display_only assistant should not count as reply")
	}

	// real (non-display-only) assistant reply → true
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "real reply", Timestamp: time.Now()})
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after real reply: %v", err)
	}
	if !hasReply {
		t.Fatal("expected true: real assistant reply exists after last user")
	}

	// new user message (turn 3), then an intermediate assistant with tool_calls
	// (persisted mid-Run by IncrementalPersist) — NOT a final reply
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "turn 3", Timestamp: time.Now()})
	svc.AddMessage(tenantID, llm.ChatMessage{
		Role:      "assistant",
		Content:   "I'll call a tool",
		ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "Shell", Arguments: "{}"}},
		Timestamp: time.Now(),
	})
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after tool-call assistant: %v", err)
	}
	if hasReply {
		t.Fatal("expected false: assistant with tool_calls is not a final reply")
	}

	// final reply (no tool_calls) → true
	svc.AddMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "final answer", Timestamp: time.Now()})
	hasReply, err = svc.HasAssistantReplyAfterLastUser(tenantID)
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after final reply: %v", err)
	}
	if !hasReply {
		t.Fatal("expected true: final reply (no tool_calls) exists after last user")
	}
}
