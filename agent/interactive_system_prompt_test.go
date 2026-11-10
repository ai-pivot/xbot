package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
	"xbot/storage/sqlite"
)

// ---------------------------------------------------------------------------
// Bug 3（2026-10-22）：交互子代理会话上下文损坏 —— 恢复出来的会话 systemPrompt
// 是零值，SendToInteractiveSession 拼出 [零值, 历史..., user] ⇒ 一条 system 都没有
// ⇒ engine 断言 "LLM messages must have exactly one system message; got 0" ⇒
// 会话不可用，只能 unload 重建。
//
// 两个根因（agent/interactive_persist.go）：
//   1. restoreOneAgent 建 interactiveAgent 时漏了 systemPrompt（零值 Role=""）；
//   2. 恢复历史用的会话 key 写成 ("agent", "<role>/<instance>")，与 spawn 时的
//      interactiveKey(...) 不是同一个库 ⇒ 历史恒为空。
// ---------------------------------------------------------------------------

// roleWithPrompt 写一个最小 role 定义文件（frontmatter name + body）。
func roleWithPrompt(t *testing.T, dir, name, prompt string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: test role\n---\n\n" + prompt + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write role file: %v", err)
	}
}

// TestRestoreOneAgent_KeepsSystemPromptAndHistory 钉死恢复路径的两个不变量：
//   - ia.systemPrompt 必须是一条 system 消息（否则后续 send 必炸 got 0）；
//   - 历史必须从 spawn 用过的同一个会话 key 恢复（不能另开一个空库）。
func TestRestoreOneAgent_KeepsSystemPromptAndHistory(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roleWithPrompt(t, agentsDir, "reviewer", "You are the reviewer role.")

	mt, err := session.NewMultiTenant(filepath.Join(dir, "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })

	// 恢复路径依赖 tools/agents registry（system prompt 由 buildSubAgentRunConfig 重建）：
	// 用 initStores 装配最小必需品，避免 New() 里启动的 bgNotifyLoop 干扰（它需要
	// agentCtx，本用例不跑 agent loop）。
	cfg := Config{WorkDir: dir, XbotHome: dir, AgentsDir: agentsDir, MemoryProvider: "none"}
	skills, agents, chatHistory, registry, cardBuilder := initStores(cfg)
	a := &Agent{
		multiSession: mt,
		tools:        registry,
		skills:       skills,
		agents:       agents,
		chatHistory:  chatHistory,
		cardBuilder:  cardBuilder,
		workDir:      dir,
		agentsDir:    agentsDir,
		xbotHome:     dir,
	}

	const (
		parentChannel = "web"
		parentChatID  = "web-4"
		roleName      = "reviewer"
		instance      = "lead-1"
	)
	mapKey := interactiveKey(parentChannel, parentChatID, roleName, instance)

	// spawn 时子代理自己的消息库：channel="agent", chatID=mapKey。
	ts, err := mt.GetOrCreateSession("agent", mapKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AppendMessage(llm.NewUserMessage("earlier question")); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AppendMessage(llm.NewAssistantMessage("earlier answer")); err != nil {
		t.Fatal(err)
	}

	uc := &UserContext{SenderID: "u1", Model: "main-model", MaxContextTokens: 1000, MaxOutputTokens: 100}
	ctx := WithUserContext(context.Background(), uc)

	rec := sqlite.InteractiveAgentRecord{
		AgentKey:      "agent:" + roleName + "/" + instance,
		ParentChannel: parentChannel,
		ParentChatID:  parentChatID,
		RoleName:      roleName,
		Instance:      instance,
		Background:    true,
		Status:        sqlite.InteractiveAgentIdle,
		OriginUserID:  "u1",
	}
	// 注册表行（restore 的前置：real 路径下 spawn 时会 upsert）。
	if err := a.interactiveAgentSvc().Upsert(rec); err != nil {
		t.Fatalf("upsert registry row: %v", err)
	}

	if err := a.restoreOneAgent(ctx, rec); err != nil {
		t.Fatalf("restoreOneAgent: %v", err)
	}
	val, ok := a.interactiveSubAgents.Load(mapKey)
	if !ok {
		t.Fatalf("restored session not registered under %q", mapKey)
	}
	ia := val.(*interactiveAgent)

	ia.mu.Lock()
	gotPrompt := ia.systemPrompt
	gotHistory := append([]llm.ChatMessage(nil), ia.messages...)
	ia.mu.Unlock()

	if gotPrompt.Role != "system" || strings.TrimSpace(gotPrompt.Content) == "" {
		t.Fatalf("BUG (got 0 system messages): restored systemPrompt = {role:%q content:%.40q}, want a real system message",
			gotPrompt.Role, gotPrompt.Content)
	}
	if len(gotHistory) != 2 || gotHistory[0].Content != "earlier question" {
		t.Fatalf("restored history = %d messages (%v), want the 2 rows stored under %q — restored sub-agents must not lose their conversation",
			len(gotHistory), gotHistory, mapKey)
	}

	// 端到端：恢复后的会话能接受 send，且请求里恰好一条 system 消息。
	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "answer-2"}}}
	ia.mu.Lock()
	ia.cfg.LLMClient = mock
	ia.cfg.Tools = newTestRegistry()
	ia.cfg.Session = ts
	ia.mu.Unlock()

	out, err := a.SendToInteractiveSession(context.Background(), roleName, bus.InboundMessage{
		Channel:  parentChannel,
		ChatID:   parentChatID,
		SenderID: "u1",
		ChatType: "p2p",
		Content:  "follow-up question",
		Metadata: map[string]string{"instance_id": instance},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("BUG (system prompt lost → session unusable): send after restore failed: %v", out.Error)
	}
	if !waitForCondition(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return !ia.running
	}) {
		t.Fatal("send Run did not finish")
	}
	if len(mock.calls) == 0 {
		t.Fatal("LLM was never called")
	}
	var systemCount int
	for _, m := range mock.calls[0].Messages {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Fatalf("LLM request carried %d system messages, want exactly 1 — messages: %v", systemCount, mock.calls[0].Messages)
	}
	if got := mock.calls[0].Messages[0].Content; !strings.Contains(got, "reviewer") {
		t.Errorf("first message should be the restored role system prompt, got %.80q", got)
	}
}

// TestSendInteractiveSession_HealsLostSystemPrompt 构造"会话快照里的 system 消息
// 丢了"（历史里 system 被删 / 旧版本恢复遗留）的场景：send 必须**自愈**
// （从 RunConfig 找回 system prompt 并继续），而不是让用户拿到 got 0 的错误。
func TestSendInteractiveSession_HealsLostSystemPrompt(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:heal1"

	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "ok"}}}
	ia := &interactiveAgent{
		roleName: "reviewer",
		instance: "heal1",
		// ⛔ 现场形态：零值 systemPrompt（Role=""）。历史里也没有 system 消息。
		messages: []llm.ChatMessage{
			llm.NewUserMessage("first question"),
			llm.NewAssistantMessage("first answer"),
		},
		cfg: &RunConfig{
			LLMClient: mock,
			Model:     "test-model",
			Tools:     newTestRegistry(),
			Channel:   "agent",
			ChatID:    key,
			Session:   sess,
			// 唯一能找回 system prompt 的地方（spawn 时构建的那份）。
			Messages: []llm.ChatMessage{llm.NewSystemMessage("You are reviewer (from cfg)."), llm.NewUserMessage("first question")},
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel: "cli", ChatID: "/w", SenderID: "u1", ChatType: "p2p",
		Content:  "second question",
		Metadata: map[string]string{"instance_id": "heal1"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("BUG (lost system prompt not healed): %v", out.Error)
	}
	if !waitForCondition(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return !ia.running
	}) {
		t.Fatal("send Run did not finish")
	}
	if len(mock.calls) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(mock.calls))
	}
	systemCount := 0
	for _, m := range mock.calls[0].Messages {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Fatalf("LLM request carried %d system messages, want 1 (healed from cfg) — messages: %v", systemCount, mock.calls[0].Messages)
	}
	ia.mu.Lock()
	healed := ia.systemPrompt
	ia.mu.Unlock()
	if healed.Role != "system" || !strings.Contains(healed.Content, "from cfg") {
		t.Errorf("session snapshot was not healed: %+v", healed)
	}
}

// TestSendInteractiveSession_NoSystemPromptAnywhere → 可读错误（不是 assert 崩溃，
// 也不能把零值消息发给模型）。
func TestSendInteractiveSession_NoSystemPromptAnywhere(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:noprompt"

	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "ok"}}}
	ia := &interactiveAgent{
		roleName: "reviewer",
		instance: "noprompt",
		messages: []llm.ChatMessage{llm.NewUserMessage("q")},
		cfg: &RunConfig{
			LLMClient: mock, Model: "m", Tools: newTestRegistry(),
			Channel: "agent", ChatID: key, Session: sess,
			Messages: []llm.ChatMessage{llm.NewUserMessage("task")}, // 也没有 system
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel: "cli", ChatID: "/w", SenderID: "u1", ChatType: "p2p",
		Content:  "q2",
		Metadata: map[string]string{"instance_id": "noprompt"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession returned transport error: %v", err)
	}
	if out == nil || out.Error == nil {
		t.Fatal("expected a readable error when no system prompt can be recovered")
	}
	msg := out.Error.Error()
	for _, want := range []string{"system prompt", "unload", "spawn"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q must mention %q (must be actionable, not an opaque assert)", msg, want)
		}
	}
	if len(mock.calls) != 0 {
		t.Error("LLM must not be called without a system message")
	}
}

// TestRun_SystemMessageZeroIsReadable 守护 engine 层：0 条 system 时的错误必须
// 点名会话 + 给处置动作（旧实现只有 "内部错误：system 消息数量异常" + assert）。
func TestRun_SystemMessageZeroIsReadable(t *testing.T) {
	out := Run(context.Background(), RunConfig{
		LLMClient: &mockLLM{responses: []llm.LLMResponse{{Content: "x"}}},
		Model:     "test",
		Tools:     newTestRegistry(),
		AgentID:   "main/reviewer",
		Channel:   "agent",
		ChatID:    "cli:/w/reviewer:x",
		Messages: []llm.ChatMessage{
			llm.NewUserMessage("hello"),
			llm.NewAssistantMessage("hi"),
		},
	})
	if out.Error == nil {
		t.Fatal("expected an error for 0 system messages")
	}
	msg := out.Error.Error()
	for _, want := range []string{"system message", "chat=", "roles=", "re-spawn"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q (readable + actionable)", msg, want)
		}
	}
	if !strings.Contains(out.Content, "系统提示词") {
		t.Errorf("user-facing content must explain the failure, got %q", out.Content)
	}
}

// TestRun_MultipleSystemMessagesKeepsFirst 守护 engine 层降级：>1 条 system 不再
// 让整个 turn 失败（与 middleware.Assemble 同策略：保留第一条）。
func TestRun_MultipleSystemMessagesKeepsFirst(t *testing.T) {
	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "fine"}}}
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages: []llm.ChatMessage{
			llm.NewSystemMessage("real system prompt"),
			llm.NewSystemMessage("duplicated system prompt"),
			llm.NewUserMessage("hello"),
		},
	})
	if out.Error != nil {
		t.Fatalf("duplicated system messages must be repaired, not fatal: %v", out.Error)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(mock.calls))
	}
	systemCount := 0
	for _, m := range mock.calls[0].Messages {
		if m.Role == "system" {
			systemCount++
			if m.Content != "real system prompt" {
				t.Errorf("kept the wrong system message: %q", m.Content)
			}
		}
	}
	if systemCount != 1 {
		t.Fatalf("system messages sent = %d, want 1", systemCount)
	}
}

// waitForCondition 是本地轮询助手（避免与其它测试文件的 helper 重名）。
func waitForCondition(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

var _ = sync.Mutex{}
