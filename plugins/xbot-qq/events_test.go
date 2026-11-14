package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseInboundC2C 私聊事件：身份取 author.user_openid，会话是 openid 本身。
func TestParseInboundC2C(t *testing.T) {
	raw := json.RawMessage(`{
		"id":"MSG-1",
		"content":"  你好  ",
		"timestamp":"2026-10-26T12:00:00+00:00",
		"author":{"id":"INTERNAL-ID","user_openid":"USER-OPENID","union_openid":"U-UNION"}
	}`)
	in, ok, err := ParseInbound(EventC2CMessageCreate, raw)
	if err != nil || !ok {
		t.Fatalf("ParseInbound = ok:%t err:%v", ok, err)
	}
	if in.SenderOpenID != "USER-OPENID" {
		t.Fatalf("sender = %q, want user_openid (NOT author.id)", in.SenderOpenID)
	}
	if in.SenderOpenID == "INTERNAL-ID" {
		t.Fatal("author.id must never be used as identity")
	}
	if in.Content != "你好" {
		t.Fatalf("content = %q, want trimmed", in.Content)
	}
	if in.MessageID != "MSG-1" {
		t.Fatalf("message id = %q", in.MessageID)
	}
	if in.IsGroup() || in.ChatID() != "USER-OPENID" {
		t.Fatalf("chat = %q isGroup=%t, want private USER-OPENID", in.ChatID(), in.IsGroup())
	}
	if in.Timestamp.IsZero() {
		t.Fatal("timestamp not parsed")
	}
	if id := in.Identity(); id.SenderID != "USER-OPENID" || id.ChatType != ChatTypeP2P {
		t.Fatalf("identity = %+v", id)
	}
}

// TestParseInboundGroupAt 群 @ 事件：身份取 member_openid，会话是 group:<group_openid>。
func TestParseInboundGroupAt(t *testing.T) {
	raw := json.RawMessage(`{
		"id":"MSG-2",
		"content":"  /help",
		"group_openid":"GROUP-OPENID",
		"author":{"id":"INTERNAL-ID","member_openid":"MEMBER-OPENID","username":"小明"}
	}`)
	in, ok, err := ParseInbound(EventGroupAtMessageCreate, raw)
	if err != nil || !ok {
		t.Fatalf("ParseInbound = ok:%t err:%v", ok, err)
	}
	if in.SenderOpenID != "MEMBER-OPENID" {
		t.Fatalf("sender = %q, want member_openid", in.SenderOpenID)
	}
	if !in.IsGroup() || in.GroupOpenID != "GROUP-OPENID" {
		t.Fatalf("group = %q", in.GroupOpenID)
	}
	if in.ChatID() != "group:GROUP-OPENID" {
		t.Fatalf("chat id = %q", in.ChatID())
	}
	if in.SenderName != "小明" {
		t.Fatalf("sender name = %q, want username 小明 (display only)", in.SenderName)
	}
	if id := in.Identity(); id.ChatType != ChatTypeGroup || id.GroupID != "GROUP-OPENID" {
		t.Fatalf("identity = %+v", id)
	}
}

// TestParseInboundGroupFullMessageDropped 群全量消息（GROUP_MESSAGE_CREATE）**不解析**。
//
// 约束 2（御前令）：本渠道只处理 @ 消息。群全量与 @ 消息共用同一个 intent 位
// （订阅层拆不开），所以丢弃点在分发层（main.go handleGatewayEvent）；这里断言
// 解析层是第二道防线，不允许把群全量消息变成一条 inbound。
func TestParseInboundGroupFullMessageDropped(t *testing.T) {
	raw := json.RawMessage(`{
		"id":"MSG-3","content":"hello","group_openid":"G","author":{"member_openid":"M"}
	}`)
	if _, ok, err := ParseInbound(EventGroupMessageCreate, raw); ok || err != nil {
		t.Fatalf("ParseInbound(GROUP_MESSAGE_CREATE) = ok:%t err:%v, want ok=false err=nil", ok, err)
	}
}

// TestParseInboundUnsupportedEvent 不关心的事件静默跳过（ok=false, err=nil）。
func TestParseInboundUnsupportedEvent(t *testing.T) {
	in, ok, err := ParseInbound("GUILD_CREATE", json.RawMessage(`{"id":"x"}`))
	if ok || err != nil || in.MessageID != "" {
		t.Fatalf("ParseInbound = %+v ok:%t err:%v, want skipped", in, ok, err)
	}
}

// TestParseInboundRejectsAuthorIDOnly 没有 openid 时必须报错，不许退回 author.id。
func TestParseInboundRejectsAuthorIDOnly(t *testing.T) {
	raw := json.RawMessage(`{"id":"M","content":"hi","author":{"id":"INTERNAL"}}`)
	_, ok, err := ParseInbound(EventC2CMessageCreate, raw)
	if !ok || err == nil || !strings.Contains(err.Error(), "openid") {
		t.Fatalf("ok=%t err=%v, want explicit openid error", ok, err)
	}
}

// TestParseInboundRequiresGroupOpenID 群事件缺 group_openid 必须报错。
func TestParseInboundRequiresGroupOpenID(t *testing.T) {
	raw := json.RawMessage(`{"id":"M","content":"hi","author":{"member_openid":"MEM"}}`)
	_, ok, err := ParseInbound(EventGroupAtMessageCreate, raw)
	if !ok || err == nil || !strings.Contains(err.Error(), "group_openid") {
		t.Fatalf("ok=%t err=%v, want group_openid error", ok, err)
	}
}

// TestParseInboundRequiresMessageID 被动回复依赖 d.id，缺了必须报错。
func TestParseInboundRequiresMessageID(t *testing.T) {
	raw := json.RawMessage(`{"content":"hi","author":{"user_openid":"U"}}`)
	_, ok, err := ParseInbound(EventC2CMessageCreate, raw)
	if !ok || err == nil || !strings.Contains(err.Error(), "message id") {
		t.Fatalf("ok=%t err=%v, want message id error", ok, err)
	}
}

// TestParseInboundPrivateWithGroupOpenIDIsError 私聊事件带 group_openid 属于异常报文。
func TestParseInboundPrivateWithGroupOpenIDIsError(t *testing.T) {
	raw := json.RawMessage(`{"id":"M","content":"hi","group_openid":"G","author":{"user_openid":"U"}}`)
	_, ok, err := ParseInbound(EventC2CMessageCreate, raw)
	if !ok || err == nil {
		t.Fatalf("ok=%t err=%v, want error", ok, err)
	}
}

// TestInboundValid 空正文/空 id 视为不完整。
func TestInboundValid(t *testing.T) {
	base := Inbound{MessageID: "M", SenderOpenID: "U", Content: "hi"}
	if !base.Valid() {
		t.Fatal("base inbound should be valid")
	}
	noContent := base
	noContent.Content = "   "
	if noContent.Valid() {
		t.Fatal("blank content must be invalid")
	}
	noID := base
	noID.MessageID = ""
	if noID.Valid() {
		t.Fatal("missing message id must be invalid")
	}
}
