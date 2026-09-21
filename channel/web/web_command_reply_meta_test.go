package web

import (
	"testing"
	"time"

	ch "xbot/channel"
	"xbot/protocol"
)

// 命令回复（`!cmd` / slash）的 `command_reply` 标记**必须透传进 WS 消息**。
//
// 前端 `normalize` 只认这个**显式标记**把 turn-less 的 text 渲染成 standalone 行
//（CR 2026-09-21 P1-1：判别式从"turn_id 缺失"改为显式 metadata —— 因为服务端重启
// 恢复也会让普通 turn 的 text 丢 turn_id）。`WebChannel.Send` 只白名单转发少量
// metadata 键，曾经漏掉 `command_reply` ⇒ 前端把命令回复当普通 turn 回复 ⇒
// `activeTurn === null`（会话空闲）时**静默丢弃** ⇒ 用户报告「!cmd 输出不显示」
//（2026-09-21 P0 实测：SSE 载荷 `{"type":"text","content":"...","seq":2}` 里没有 metadata）。
//
// ⚠️ 这条守护必须走**真实 Send 链路**（不是前端 E2E 的 mock）—— 本次事故正是
// "E2E 手写 metadata、真机却不带" 的 mock 漂移。
func TestWebSendForwardsCommandReplyMetadata(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	const chatID = "web-1"
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 4),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "web",
		id:             "cmd-reply-meta",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))

	if _, err := wc.Send(ch.OutboundMsg{
		Channel:  "web",
		ChatID:   chatID,
		Content:  "```\n/root\n```",
		Metadata: map[string]string{"command_reply": "true"},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-client.sendCh:
		if msg.Type != "text" {
			t.Fatalf("type = %q, want text", msg.Type)
		}
		if msg.Metadata["command_reply"] != "true" {
			t.Fatalf("命令回复必须透传 metadata.command_reply（前端唯一判别式）: metadata=%#v", msg.Metadata)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("命令回复没有投递到订阅的 SSE 客户端")
	}
}

// 反向守护：普通消息不得带 `command_reply`（否则会被前端渲染成 standalone 行、
// 并排到命令行的位置，而不是它自己的 turn 里）。
func TestWebSendDoesNotMarkOrdinaryMessages(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	const chatID = "web-1"
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 4),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "web",
		id:             "ordinary-msg",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))

	if _, err := wc.Send(ch.OutboundMsg{
		Channel: "web",
		ChatID:  chatID,
		Content: "hello",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-client.sendCh:
		if msg.Metadata["command_reply"] == "true" {
			t.Fatalf("普通消息不得带 command_reply: %#v", msg.Metadata)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("普通消息没有投递到订阅的 SSE 客户端")
	}
}
