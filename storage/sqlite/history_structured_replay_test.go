package sqlite

import (
	"fmt"
	"testing"
)

// 回放策略：event / command 是「可见但不进上下文」的行。
// 过滤点只有 replayWith（LLM 回放），UI 展示路径必须仍然包含它们。
func TestReplayExcludesEventAndCommandButKeepsDialogue(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	for i := 0; i < 3; i++ {
		mustAppend(t, svc, tenant, StructuredMessage{
			Kind: StructuredKindEvent, Content: fmt.Sprintf("事件 #%d", i),
		})
	}
	mustAppend(t, svc, tenant, StructuredMessage{Kind: StructuredKindCommand, Content: "spawn worker"})
	mustAppend(t, svc, tenant, StructuredMessage{
		Kind: StructuredKindAskUser, CorrelationID: "cor_ctx", Content: "是否扩容？",
	})
	mustAppend(t, svc, tenant, StructuredMessage{
		Kind: StructuredKindReport, CorrelationID: "cor_ctx", Content: "扩容完成",
	})
	if _, _, _, err := svc.AnswerAskUser(tenant, "chat-1", "cor_ctx", "q1:approve", `{}`, "a1", "web-1", "web-1", false); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, svc, tenant, StructuredMessage{Kind: StructuredKindText, Content: "继续"})

	replay, err := svc.Replay(tenant)
	if err != nil {
		t.Fatal(err)
	}
	seenKind := map[string]bool{}
	for _, msg := range replay.Messages {
		if msg.Kind == StructuredKindEvent || msg.Kind == StructuredKindCommand {
			t.Fatalf("replay leaked kind=%s content=%q into LLM context", msg.Kind, msg.Content)
		}
		seenKind[msg.Kind] = true
	}
	for _, want := range []string{StructuredKindAskUser, StructuredKindReport, StructuredKindAnswer, StructuredKindText} {
		if !seenKind[want] {
			t.Fatalf("replay dropped kind=%s (dialogue rows must stay)", want)
		}
	}

	// UI 展示路径不受影响：事件行仍要能看到。
	display, err := svc.ReplayForDisplay(tenant)
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for _, msg := range display.Messages {
		if msg.Kind == StructuredKindEvent {
			events++
		}
	}
	if events != 3 {
		t.Fatalf("display replay events = %d, want 3 (UI must still show events)", events)
	}
}
