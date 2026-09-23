package feishu

import (
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"xbot/bus"
	"xbot/protocol"
)

// feishu_cot_stop_test.go — 飞书原生 CoT「停止生成」按钮的处理契约（2026-09-23）。
//
// 平台契约：用户点 CoT 消息上的「停止生成」时，飞书发 card.action.trigger 回调，
// action.tag == "cot_stop"（value 带 cot_id/message_id）。此前该回调落到通用卡片
// 路径被当 unknown tag 丢弃 —— 按钮完全无效（用户报告「中止按钮没处理」）。
//
// 处理 = ① 收尾思考过程（RUN_ERROR，后续写入丢弃）；② 取消 agent turn（发 /cancel）。

// 停止必须：emit RUN_ERROR（用户停止）+ 停止后到达的事件全部丢弃（平台侧不"复活"）。
func TestCoTStop_EmitsRunErrorAndDropsLaterEvents(t *testing.T) {
	c, calls := newFakeCoT(t, "oc_chat1")
	r := newFeishuCoTRenderer("chat_1", c)

	// 开一个 run（RUN_STARTED）。
	r.onProgress(&protocol.ProgressEvent{TurnID: 1, Phase: "thinking"})

	// 停止。
	r.stop()

	types := eventTypes(t, calls)
	hasRunError := false
	for _, ty := range types {
		if ty == "RUN_ERROR" {
			hasRunError = true
		}
	}
	if !hasRunError {
		t.Fatalf("stop must emit RUN_ERROR, got events %v", types)
	}
	if !c.stoppedNow() {
		t.Fatal("cot must be marked stopped")
	}

	// 停止后到达的进度/流式事件必须全部丢弃（不再产生任何写请求）。
	before := len(*calls)
	r.onProgress(&protocol.ProgressEvent{TurnID: 1, Phase: "tool_exec",
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", CallID: "call-1"}}})
	r.onStreamContent("more text after stop", "")
	r.onProgress(&protocol.ProgressEvent{TurnID: 1, Phase: "done"})
	if after := len(*calls); after != before {
		t.Fatalf("events after stop must be dropped: before=%d after=%d", before, after)
	}
}

// handleCoTStopAction：匹配到活跃渲染器 → 停止 + 发 /cancel 到消息总线。
func TestCoTStopAction_MatchesAndCancels(t *testing.T) {
	f := NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
	c, _ := newFakeCoT(t, "oc_chat1")
	r := newFeishuCoTRenderer("chat_1", c)
	f.cotRenderers["chat_1"] = r

	// 模拟平台回调：tag=cot_stop（value 带 cot_id；渲染器未创建时按 chatID 匹配）。
	action := &callback.CallBackAction{
		Tag:   "cot_stop",
		Value: map[string]any{"cot_id": "cot-1"},
	}
	resp, ok := f.handleCoTStopAction(action, "oc_chat1", "ou_user")
	if !ok {
		t.Fatal("cot_stop must be intercepted")
	}
	if resp == nil || resp.Toast == nil {
		t.Fatal("must return a toast response")
	}

	// /cancel 必须发到消息总线（取消正在跑的 turn）。
	select {
	case msg := <-f.msgBus.Inbound:
		if msg.Content != "/cancel" {
			t.Fatalf("want /cancel, got %q", msg.Content)
		}
		if msg.ChatID != "chat_1" {
			t.Fatalf("cancel must route to the session key chat_1, got %q", msg.ChatID)
		}
	default:
		t.Fatal("/cancel must be sent to the bus")
	}

	// 渲染器必须已停止。
	if !c.stoppedNow() {
		t.Fatal("renderer must be stopped")
	}
}

// 没有活跃的思考过程（已结束/已停止）→ 按钮仍被消费（回 toast），不 panic、不发 cancel。
func TestCoTStopAction_NoActiveRun(t *testing.T) {
	f := NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
	action := &callback.CallBackAction{Tag: "cot_stop"}
	resp, ok := f.handleCoTStopAction(action, "oc_none", "ou_user")
	if !ok {
		t.Fatal("cot_stop must still be intercepted (consumed) even with no active run")
	}
	if resp == nil || resp.Toast == nil {
		t.Fatal("must return a toast response")
	}
	select {
	case msg := <-f.msgBus.Inbound:
		t.Fatalf("no /cancel should be sent without an active run, got %q", msg.Content)
	default:
	}
}

// 非 cot_stop 的 action.tag 不得被拦截（正常卡片交互不受影响）。
func TestCoTStopAction_NonStopTagIgnored(t *testing.T) {
	f := NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
	action := &callback.CallBackAction{Tag: "button"}
	_, ok := f.handleCoTStopAction(action, "oc_chat1", "ou_user")
	if ok {
		t.Fatal("non cot_stop tag must not be intercepted")
	}
}

// cot_id 精确匹配：渲染器已创建（cot_id 已知）时，value.cot_id 必须命中同一渲染器。
func TestCoTStopAction_MatchesByCotID(t *testing.T) {
	f := NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
	// 渲染器 A（oc_chatA）已创建（cot_id=cot-1，由 fake 的 POST 响应分配）。
	cA, _ := newFakeCoT(t, "oc_chatA")
	rA := newFeishuCoTRenderer("chat_A", cA)
	rA.onProgress(&protocol.ProgressEvent{TurnID: 1, Phase: "thinking"}) // 触发创建 → cot_id=cot-1
	f.cotRenderers["chat_A"] = rA
	// 渲染器 B（oc_chatB）—— 不该被 A 的停止按钮命中。
	cB, _ := newFakeCoT(t, "oc_chatB")
	rB := newFeishuCoTRenderer("chat_B", cB)
	f.cotRenderers["chat_B"] = rB

	// 用 A 的 cot_id 停止：只有 A 被停。
	action := &callback.CallBackAction{
		Tag:   "cot_stop",
		Value: map[string]any{"cot_id": "cot-1"},
	}
	_, ok := f.handleCoTStopAction(action, "oc_chatA", "ou_user")
	if !ok {
		t.Fatal("cot_stop with matching cot_id must be intercepted")
	}
	if !cA.stoppedNow() {
		t.Fatal("renderer A must be stopped")
	}
	if cB.stoppedNow() {
		t.Fatal("renderer B must NOT be stopped by A's stop button")
	}
}
