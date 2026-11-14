package agent

// user_notice_outflow_test.go — N1 断言④：`to="user"` 卡片必须**出流**。
//
// 判据三条（卡片正文在下面每一处都必须 0 命中）：
//   a) 会话运行态 s.messages（引擎真正持有的消息列表）；
//   b) history 回放（聊天流 ReplayForDisplay + LLM 回放 Replay）；
//   c) **下一次 LLM 请求**的 messages（mockLLM 抓到的真实请求体，不是断言"日志里没有"）。
//
// 存储形态与生产一致：serverapp 的 appendFn 对 Silent 卡片写 record_type='notice'
// （写侧真实路径由 serverapp.TestUserNoticeCard_StaysOutOfStream 覆盖）；本用例负责
// 引擎侧——卡片进了库之后，绝不能漏进上下文。
//
// 复用既有夹具：mockLLM（engine_test.go）抓请求体，newRunState/llmMessages 抓运行态，
// 不新造 LLM 桩。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestUserNoticeCard_OutOfRunStateAndLLMRequest(t *testing.T) {
	dir := t.TempDir()
	ag, err := New(Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
		Bus:            bus.NewMessageBus(),
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })

	const cardText = "N1 断言④：这张卡片不得进入任何 LLM 上下文"
	sess, err := ag.MultiSession().GetOrCreateSession("web", "web-4")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("unexpected session backend")
	}
	tenantID := sess.TenantID()

	// 卡片：与生产同一形态（record_type=notice，silent）。
	cardID, _, err := svc.AppendStructuredMessage(tenantID, sqlite.StructuredMessage{
		ChatID:         "web-4",
		RecordType:     sqlite.HistoryRecordNotice,
		Kind:           sqlite.StructuredKindReport,
		Content:        cardText,
		Direction:      "in",
		SenderType:     "agent",
		SenderID:       "agent:main/court.shoufu",
		SenderName:     "agent:main/court.shoufu",
		StructuredJSON: `{"schema":"xbot.message/v1","kind":"report","payload":{"summary":"待批"},"origin":{"session":"web:web-4/court.shoufu:main","agent":"agent:main/court.shoufu"},"silent":true}`,
		IdempotencyKey: "n1-outflow-1",
		CreatedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendStructuredMessage: %v", err)
	}
	// 前置：行确实以 record_type=notice 落库（否则下面的"0 命中"可能只是因为
	// 根本没写进去）。GetStructuredMessageByID 不返回 record_type，改从历史记录读。
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatalf("GetFullHistory: %v", err)
	}
	noticeRows := 0
	for _, rec := range records {
		if rec.Type == sqlite.HistoryRecordNotice {
			noticeRows++
		}
	}
	if noticeRows != 1 {
		t.Fatalf("card %d: notice 行 = %d, want 1（record_type='notice' 落库）", cardID, noticeRows)
	}
	cardRow, err := svc.GetStructuredMessageByID(tenantID, cardID)
	if err != nil {
		t.Fatalf("GetStructuredMessageByID: %v", err)
	}
	if cardRow.Content != cardText {
		t.Fatalf("card %d content = %q, want %q", cardID, cardRow.Content, cardText)
	}
	// 同会话里再放一条普通用户消息，保证回放/请求非空（判据才有判别力）。
	if err := sess.AddMessage(llm.NewUserMessage("这条是用户真实消息")); err != nil {
		t.Fatalf("seed user message: %v", err)
	}

	contains := func(msgs []llm.ChatMessage) int {
		n := 0
		for _, m := range msgs {
			if strings.Contains(m.Content, cardText) {
				n++
			}
		}
		return n
	}

	// ── b) history 回放：display 与 LLM 两条都必须 0 命中 ────────────────────
	display, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatalf("ReplayForDisplay: %v", err)
	}
	if n := contains(display.Messages); n != 0 {
		t.Errorf("卡片进入了聊天流回放：%d 条命中", n)
	}
	// 条数也要卡：notice 行若被当普通消息回放，即便内容为空也会多出一条（空气泡）。
	if len(display.Messages) != 1 {
		t.Errorf("聊天流回放条数 = %d, want 1（只应有那条真实用户消息）", len(display.Messages))
	}
	llmReplay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if n := contains(llmReplay.Messages); n != 0 {
		t.Errorf("卡片进入了 LLM 回放：%d 条命中", n)
	}
	if len(llmReplay.Messages) != 1 {
		t.Errorf("LLM 回放条数 = %d, want 1（卡片不得占位）", len(llmReplay.Messages))
	}

	cfg := RunConfig{
		Model:      "test-model",
		AgentID:    "main/court.shoufu",
		Channel:    "web",
		ChatID:     "web-4",
		SessionKey: "web:web-4",
		Messages:   llmReplay.Messages,
	}
	state := newRunState(cfg)
	// ── a) 会话运行态 s.messages 0 命中 ─────────────────────────────────────
	if n := contains(state.messages); n != 0 {
		t.Errorf("卡片进入了 s.messages：%d 条命中", n)
	}
	if n := contains(state.llmMessages(1)); n != 0 {
		t.Errorf("卡片进入了本轮待发消息：%d 条命中", n)
	}

	// ── c) 下一次真实 LLM 请求 0 命中 ───────────────────────────────────────
	mock := &mockLLM{responses: []llm.LLMResponse{{Content: "ok"}}}
	msgs := append([]llm.ChatMessage{llm.NewSystemMessage("You are a test agent.")}, llmReplay.Messages...)
	if out := Run(context.Background(), RunConfig{
		LLMClient:  mock,
		Model:      "test-model",
		Tools:      newTestRegistry(),
		Messages:   msgs,
		AgentID:    "main/court.shoufu",
		Channel:    "web",
		ChatID:     "web-4",
		SessionKey: "web:web-4",
	}); out.Error != nil {
		t.Fatalf("Run: %v", out.Error)
	}
	if len(mock.calls) == 0 {
		t.Fatal("mockLLM 没有收到任何请求，无法断言请求体")
	}
	for i, call := range mock.calls {
		if n := contains(call.Messages); n != 0 {
			t.Errorf("第 %d 次 LLM 请求的 messages 里卡片命中 %d 次（必须 0）", i+1, n)
		}
	}
}
