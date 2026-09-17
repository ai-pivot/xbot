package serverapp

import (
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/llm"
)

func newTestAgentForWaitingStatus(t *testing.T) *agent.Agent {
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

// T5: WaitingUser 暂停（pending AskUser、无活跃 Run）必须对外报告
// running=false && status="waiting_input" —— busy 与 waiting 互斥，侧边栏
// 不再显示假的 running 行。
func TestWaitingInputRowIsNotRunning(t *testing.T) {
	ag := newTestAgentForWaitingStatus(t)
	sess, err := ag.MultiSession().GetOrCreateSession("web", "chat-wait")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewToolMessage("AskUser", "ask", `{}`, "waiting")); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendAskQuestion(map[string]string{"request_id": "req-wait"}); err != nil {
		t.Fatal(err)
	}

	row := web.UserChatWithPreview{Channel: "web", ChatID: "chat-wait"}
	applyWebRunningStatus(ag, &row)
	if row.Status != "waiting_input" {
		t.Fatalf("status = %q, want waiting_input", row.Status)
	}
	if row.Running {
		t.Fatal("waiting_input row reported running=true (busy and waiting must be mutually exclusive)")
	}
}
