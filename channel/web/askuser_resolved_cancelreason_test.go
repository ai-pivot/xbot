package web

import (
	"encoding/json"
	"testing"

	"xbot/protocol"
)

// D-03 返工（门下省 final-review-20261029 REJECT）：
// TTL 收回的 ask_user_resolved 必须在 **wire 层** 带上 cancel_reason。
//
// wire 层 = protocol.WSMessage —— WebSocket 与 SSE 共用同一个序列化结构
// （hub.sendToSession 对 isSSEEventType 双投递，normalizeSSEEvent 只改 Type，
// 不清字段），所以本测试用 json.Marshal(msg) 直接钉住线上字节。
//
// 变异自证（两条都必须让本测试变红）：
//   - 删掉 web.go 中 `AskUserResolvedCancelReason: ev.CancelReason,` → RED
//   - 把 protocol/ws.go 的 json tag 改成 cancel_reason_x → RED
func TestAskUserResolvedMessageCarriesCancelReason(t *testing.T) {
	msg := askUserResolvedMessage(protocol.AskUserResolvedEvent{
		Channel:      "web",
		ChatID:       "chat-cancelreason",
		RequestID:    "req-cancelreason",
		Reason:       "cancelled",
		CancelReason: "ttl_expired",
	})

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal ws message: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal ws message: %v", err)
	}

	if got, _ := wire["reason"].(string); got != "cancelled" {
		t.Fatalf("wire reason = %q, want cancelled; raw=%s", got, raw)
	}
	got, ok := wire["cancel_reason"].(string)
	if !ok {
		t.Fatalf("wire 层丢了 cancel_reason（浏览器/远程 CLI 无法区分'超时关掉'与'用户主动取消'）; raw=%s", raw)
	}
	if got != "ttl_expired" {
		t.Fatalf("wire cancel_reason = %q, want ttl_expired; raw=%s", got, raw)
	}
}

// 契约的另一半：用户主动取消时 CancelReason 为空，wire 上不得出现该键
// （omitempty），前端把「键缺失」读成「人取消的」。缺了这条，
// cancel_reason="" 与 "ttl_expired" 之外的取值会开始互相污染。
func TestAskUserResolvedMessageOmitsEmptyCancelReason(t *testing.T) {
	msg := askUserResolvedMessage(protocol.AskUserResolvedEvent{
		Channel:   "web",
		ChatID:    "chat-cancelreason-empty",
		RequestID: "req-cancelreason-empty",
		Reason:    "cancelled",
	})

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal ws message: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal ws message: %v", err)
	}
	if v, present := wire["cancel_reason"]; present {
		t.Fatalf("主动取消不该带 cancel_reason，实际 = %v; raw=%s", v, raw)
	}
}
