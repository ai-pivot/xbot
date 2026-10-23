package serverapp

// structured_activity_index_test.go — 活动索引的写入路径验收。
//
// 重构要的是「跨会话视图只打主库」，前提是**每条结构化消息落库成功后**都在主库
// 留下一行摘要。这里走真实的 buildWebCallbacks → StructuredAppend 链路（不是打桩），
// 断言：
//   - kind=event（不可投递 ⇒ 不会拉起 agent turn，测试无副作用）写进了索引；
//   - payload.summary 优先进 summary，没有则退回正文前 200 字符；
//   - kind=text 不进索引（否则索引会被聊天文本撑成第二个消息库）；
//   - 同一 (chat, message) 重复调用只留一行（UNIQUE 幂等）。

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/config"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

func newActivityAgent(t *testing.T) *agent.Agent {
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

func TestStructuredAppendIndexesActivity(t *testing.T) {
	ag := newActivityAgent(t)
	callbacks := buildWebCallbacks(&config.Config{}, ag)
	if callbacks.StructuredAppend == nil {
		t.Fatal("StructuredAppend callback is nil")
	}
	sel := web.SessionSelector{Channel: "web", ChatID: "chat-activity"}

	// ① payload.summary 优先。
	msg, dup, err := callbacks.StructuredAppend("web-1", sel, protocol.StructuredMessageInput{
		Kind: "event",
		Payload: map[string]any{
			"summary": "巡检完成：机器台账无异常",
			"text":    "这段正文不应该成为 summary",
		},
		IdempotencyKey: "evt-1",
	})
	if err != nil {
		t.Fatalf("StructuredAppend(event): %v", err)
	}
	if dup {
		t.Fatal("first append reported duplicate")
	}
	// ② 重复 append（同 idempotency_key）不得产生第二行索引。
	if _, _, err := callbacks.StructuredAppend("web-1", sel, protocol.StructuredMessageInput{
		Kind: "event", IdempotencyKey: "evt-1",
		Payload: map[string]any{"summary": "巡检完成：机器台账无异常"},
	}); err != nil {
		t.Fatalf("duplicate StructuredAppend: %v", err)
	}

	// ③ 没有 payload.summary 的 report-ish 行（用 event 避免拉起 turn）退回正文。
	longText := strings.Repeat("这是一段很长的汇报正文。", 40) // > 200 runes
	if _, _, err := callbacks.StructuredAppend("web-1", sel, protocol.StructuredMessageInput{
		Kind: "event", Text: longText, IdempotencyKey: "evt-2",
	}); err != nil {
		t.Fatalf("StructuredAppend(event/text fallback): %v", err)
	}

	// ④ 普通 text 不进索引。
	//
	// 注意：text 是「可投递」kind，投递需要完整装配的 agent（队列/上下文），
	// 本测试用的是最小 agent，投递阶段会 panic。索引写入在投递**之前**完成，
	// 所以这里吞掉投递 panic、只断言索引状态 —— 投递语义由
	// structured_callbacks_report_test.go 等既有测试覆盖。
	func() {
		defer func() { _ = recover() }()
		_, _, _ = callbacks.StructuredAppend("web-1", sel, protocol.StructuredMessageInput{
			Kind: "text", Text: "普通聊天", IdempotencyKey: "txt-1",
		})
	}()

	idx := sqlite.NewActivityIndex(ag.MultiSession().DB().Conn())
	items, _, err := idx.List(50, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("activity rows = %d, want 2 (event×2, text excluded, duplicate collapsed): %+v", len(items), items)
	}
	// List 是 created_at DESC：先取到的是最后写入的那条（正文回落）。
	byMessageID := map[int64]sqlite.ActivityItem{}
	for _, it := range items {
		byMessageID[it.MessageID] = it
		if it.ChatID != "chat-activity" || it.Channel != "web" || it.Kind != "event" {
			t.Fatalf("unexpected row: %+v", it)
		}
		if it.SenderName == "" {
			t.Fatalf("sender_name empty: %+v", it)
		}
	}
	first, ok := byMessageID[msg.MessageID]
	if !ok {
		t.Fatalf("message %d missing from activity index", msg.MessageID)
	}
	if first.Summary != "巡检完成：机器台账无异常" {
		t.Fatalf("summary = %q, want payload.summary", first.Summary)
	}
	for _, it := range items {
		if it.MessageID != msg.MessageID {
			if n := len([]rune(it.Summary)); n != sqlite.ActivitySummaryMaxRunes {
				t.Fatalf("fallback summary runes = %d, want truncation to %d", n, sqlite.ActivitySummaryMaxRunes)
			}
		}
	}
}
