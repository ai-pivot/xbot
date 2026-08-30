package serverapp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/llm"
	"xbot/protocol"
)

// newTestAgentForExport creates an in-memory-DB agent for export/import testing.
func newTestAgentForExport(t *testing.T) *agent.Agent {
	t.Helper()
	dir := t.TempDir()
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag
}

// callRPC dispatches an RPC with admin identity injected.
func callRPC(t *testing.T, table RPCTable, method string, params any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRPCCtxResolved(context.Background(), "web-admin", "web-admin", 0, "admin")
	out, err := table.Dispatch(ctx, method, data)
	if err != nil {
		t.Fatalf("RPC %s: %v", method, err)
	}
	return out
}

// TestExportImportSessionRPC verifies the full export → import round trip
// through the RPC table: messages appended to a session are exported with
// full history (records), then imported into a fresh session.
func TestExportImportSessionRPC(t *testing.T) {
	ag := newTestAgentForExport(t)
	cfg := config.Load()
	table := BuildRPCTable(cfg, ag, &channel.Dispatcher{}, bus.NewMessageBus(), nil)

	const (
		channelName = "web"
		chatID      = "export-test-chat"
	)

	// Seed the session with messages (system extracted separately).
	sess, err := ag.MultiSession().GetOrCreateSession(channelName, chatID)
	if err != nil {
		t.Fatal(err)
	}
	seed := []llm.ChatMessage{
		llm.NewSystemMessage("You are xbot."),
		{Role: "user", Content: "Hello", TurnID: 1},
		{
			Role:             "assistant",
			Content:          "Reading file…",
			ReasoningContent: "need to read",
			ToolCalls:        []llm.ToolCall{{ID: "c1", Name: "Read", Arguments: `{"path":"a.go"}`}},
			TurnID:           1,
		},
		{Role: "tool", Content: "package a", ToolCallID: "c1", ToolName: "Read", Detail: "diff", TurnID: 1},
	}
	if _, err := sess.AppendMessages(seed); err != nil {
		t.Fatal(err)
	}

	// 1) Export the session.
	raw := callRPC(t, table, "export_session", map[string]string{
		"channel": channelName, "chat_id": chatID,
	})
	var exported protocol.ExportedSession
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if exported.SystemInstructions != "You are xbot." {
		t.Errorf("SystemInstructions = %q", exported.SystemInstructions)
	}
	// system extracted → 3 messages in Messages; records = 4 raw rows.
	if len(exported.Messages) != 3 {
		t.Errorf("exported.Messages len = %d, want 3", len(exported.Messages))
	}
	if len(exported.Records) != 4 {
		t.Errorf("exported.Records len = %d, want 4 (full history)", len(exported.Records))
	}

	// 2) Import into a fresh session.
	imported := protocol.ImportSession(&exported)
	if len(imported) != 4 { // system + 3
		t.Fatalf("imported len = %d, want 4", len(imported))
	}
	if imported[0].Role != "system" || imported[0].Content != "You are xbot." {
		t.Errorf("imported[0] = %+v", imported[0])
	}
	if imported[2].ReasoningContent != "need to read" || len(imported[2].ToolCalls) != 1 {
		t.Errorf("imported[2] reasoning/toolcalls lost: %+v", imported[2])
	}
	if imported[3].Detail != "diff" {
		t.Errorf("imported[3] detail lost: %+v", imported[3])
	}

	// 3) Verify import RPC writes them to a new session.
	newChat := "export-import-target"
	callRPC(t, table, "import_session", map[string]any{
		"channel": channelName, "chat_id": newChat, "session": &exported,
	})
	sess2, err := ag.MultiSession().GetOrCreateSession(channelName, newChat)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sess2.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("imported session messages len = %d, want 4", len(got))
	}
	if got[2].ReasoningContent != "need to read" {
		t.Errorf("imported session reasoning lost: %+v", got[2])
	}
}

// TestExportSessionEmpty ensures an empty session exports without error.
func TestExportSessionEmpty(t *testing.T) {
	ag := newTestAgentForExport(t)
	cfg := config.Load()
	table := BuildRPCTable(cfg, ag, &channel.Dispatcher{}, bus.NewMessageBus(), nil)

	raw := callRPC(t, table, "export_session", map[string]string{
		"channel": "web", "chat_id": "empty-chat",
	})
	var exported protocol.ExportedSession
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if len(exported.Messages) != 0 {
		t.Errorf("empty session exported %d messages", len(exported.Messages))
	}
}
