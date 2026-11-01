package agent

import (
	"context"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
	"xbot/tools"
)

// 审计判据（2026-10-21）：`turn_started` 会**覆盖** `lastProgressSnapshot`（见 emitTurnStarted
// 末尾的 Store）—— 它必须携带**会话级字段**（todos / goal），否则「从 turn_started 到首个
// iteration 事件」这段窗口里断线/心跳重连时，`GetActiveProgress` 返回的快照把 todos 折成
// null、把 goal 丢掉 ⇒ 前端面板/横幅空一段（用户 2026-10-21 亲报的「发消息后 todo/goal 闪」）。
func TestEmitTurnStarted_CarriesSessionFields(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel{}
	a := &Agent{
		bus:          bus.NewMessageBus(),
		agentCtx:     ctx,
		channelRange: func(fn func(string, channel.Channel) bool) { fn("mock", mockCh) },
	}
	msg := bus.InboundMessage{Channel: "web", ChatID: "chat_SF1", SenderID: "u1", Content: "hi", RequestID: "r1"}
	a.emitTurnStarted(msg, 7)

	evs := mockCh.getEvents()
	if len(evs) == 0 {
		t.Fatalf("emitTurnStarted 没有发出任何 progress 事件")
	}
	p := evs[0]
	if p.Phase != "turn_started" {
		t.Fatalf("phase=%q want turn_started", p.Phase)
	}
	// ⛔ `Todos` 无 omitempty：`null` = 未携带（客户端保持旧值），`[]` = 显式清空。
	//   turn_started 快照必须给出**权威**列表（即便是空数组），不能是 null。
	if p.Todos == nil {
		t.Errorf("turn_started 快照缺 todos（null）—— 重连窗口内前端会丢 todo 列表")
	}
	_ = tools.TodoItem{}
}

// 审计判据（2026-10-21）：`cloneProgressEvent` 必须保持 `[]` 与 `nil` 的区别 ——
// `Todos` 没有 omitempty，客户端按 `null`=未携带 / `[]`=显式清空 区分。
// 旧实现 `append([]T(nil), src...)` 把空数组折成 nil ⇒ `todo_write([])` 被当成"没带"。
func TestCloneProgressEvent_PreservesEmptyTodos(t *testing.T) {
	empty := []protocol.TodoItem{}
	src := &protocol.ProgressEvent{ChatID: "web:c1", Todos: empty}
	clone := cloneProgressEvent(src)
	if clone.Todos == nil {
		t.Fatalf("空 todos 被折成 nil（JSON null）—— 显式清空语义丢失")
	}
	if len(clone.Todos) != 0 {
		t.Fatalf("len=%d want 0", len(clone.Todos))
	}
	// nil 必须保持 nil（= 未携带）
	src2 := &protocol.ProgressEvent{ChatID: "web:c1"}
	if clone2 := cloneProgressEvent(src2); clone2.Todos != nil {
		t.Fatalf("nil todos 应保持 nil")
	}
}
