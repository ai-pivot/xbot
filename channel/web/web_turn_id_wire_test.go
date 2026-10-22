package web

import (
	"encoding/json"
	"testing"
	"time"

	ch "xbot/channel"
	"xbot/protocol"
)

// 判据（2026-10-22）：**turn 域出站消息的 TurnID 必须真的进 SSE 载荷**。
//
// 背景：`agent.handleCancelledRun` 的 cancel ack 曾经不带 TurnID（直接构造
// `channel.OutboundMsg{}`，绕过 `sendMessage` 的盖章）⇒ 载荷里 `turn_id` 缺省
// （`omitempty`）⇒ 原生客户端只能把这条收尾帧丢弃 ⇒ 被取消的回合永远没有
// `final` ⇒ 行一直 `isLive`（cancel 后发新消息会把那条 assistant 搬到列表末尾
// 并显示 busy）。这里钉住"渠道层确实把 TurnID 透传成 `turn_id`"这一环，
// 避免"agent 打了 id、渠道又漏了"的二次漂移。
//
// ⛔ mutation 自证：把 `wsMsg.TurnID = msg.TurnID` 删掉 ⇒ 本文件红。
func TestWebSendForwardsTurnIDOnCancelAck(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	const chatID = "web-cancel-1"
	const wantTurn = uint64(7)
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 4),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "web",
		id:             "cancel-ack-turn-id",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))

	// 与 agent.handleCancelledRun 的 cancel ack 同形：空正文 + cancelled + final_reply + TurnID。
	if _, err := wc.Send(ch.OutboundMsg{
		Channel:  "web",
		ChatID:   chatID,
		Content:  "",
		Metadata: map[string]string{"cancelled": "true", ch.MetaFinalReply: "true"},
		TurnID:   wantTurn,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-client.sendCh:
		if msg.TurnID != wantTurn {
			t.Fatalf("SSE 载荷必须带被取消那一轮的 turn_id；got TurnID=%d want %d", msg.TurnID, wantTurn)
		}
		if !msg.Cancelled {
			t.Fatalf("cancel ack 必须仍带 cancelled=true")
		}
		// 线上形态：`turn_id` 必须真的序列化出来（omitempty 下 0 会缺字段）。
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if got, ok := decoded["turn_id"]; !ok {
			t.Fatalf("线上 JSON 缺 turn_id 字段: %s", string(raw))
		} else if got != float64(wantTurn) {
			t.Fatalf("线上 JSON turn_id = %v, want %d", got, wantTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel ack 没有投递到订阅的 SSE 客户端")
	}
}
