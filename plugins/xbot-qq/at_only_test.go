package main

// 约束 2（御前令）：**只处理 @ 消息**。
//
// 官方 intents 只有 GROUP_AND_C2C_EVENT (1<<25 = 33554432) 这一个位同时覆盖
// GROUP_AT_MESSAGE_CREATE 与 GROUP_MESSAGE_CREATE（群全量），订阅层拆不开
// —— 御前已认这条纠正。因此拆分必须发生在分发层：
//   - GROUP_MESSAGE_CREATE → 显式丢弃（debug 日志，不进消息循环，不计 metrics）
//   - 群聊只认 GROUP_AT_MESSAGE_CREATE，私聊只认 C2C_MESSAGE_CREATE
//
// 出处：event-emit.html §事件订阅Intents；群消息事件页「机器人被添加到群且开启
// 了接收所有消息时都会推送此事件」。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestIntentsAreExactlyTheMessageBits 锁死订阅面：只允许
//
//	GROUP_AND_C2C (1<<25)        → 私聊 C2C + QQ群 @
//	PUBLIC_GUILD_MESSAGES (1<<30) → QQ频道 AT_MESSAGE_CREATE
//
// 这两个**消息类**位，任何其他位（GUILDS/GUILD_MEMBERS/DIRECT_MESSAGE/INTERACTION）
// 都不许叠加。
//
// 2026-10-26 变更理由：原先只允许 1<<25。实测私聊可收、群消息零事件；而 QQ 的
// 「群」有两类，频道(guild)走 AT_MESSAGE_CREATE + 1<<30，是 1<<25 覆盖不到的。
// 同时历史教训：用官方 SDK 的 FULL_INTENTS 时连 C2C 都收不到（说明了"多订无权限
// 的位会静默影响投递"），所以这里严格限定为这两个消息位，不跟随 FULL_INTENTS。
func TestIntentsAreExactlyTheMessageBits(t *testing.T) {
	if IntentGroupAndC2C != 33554432 {
		t.Fatalf("IntentGroupAndC2C = %d, want 33554432 (=1<<25)", IntentGroupAndC2C)
	}
	if IntentPublicGuildMessages != 1<<30 {
		t.Fatalf("IntentPublicGuildMessages = %d, want %d (=1<<30)", IntentPublicGuildMessages, 1<<30)
	}
	want := IntentGroupAndC2C | IntentPublicGuildMessages
	if DefaultIntents != want {
		t.Fatalf("DefaultIntents = %d, want %d", DefaultIntents, want)
	}
	if extra := DefaultIntents &^ want; extra != 0 {
		t.Fatalf("DefaultIntents carries forbidden extra bits 0x%x", extra)
	}
	// 零值配置也必须落到同一组 intents（不能由配置文件放宽订阅面）。
	gw := &Gateway{}
	if got := gw.intents(); got != want {
		t.Fatalf("Gateway.intents() = %d, want %d", got, want)
	}
}

// TestGroupFullMessageDropped 群全量消息（GROUP_MESSAGE_CREATE）必须被丢弃：
// 零 inbound（无 send_inbound frame）、零出站 API 调用。
//
// 即使消息里带 @ 字样、即使发送者在 allow 与 admins 里，也必须丢弃 ——
// 判定依据是**事件类型**，不是内容。
func TestGroupFullMessageDropped(t *testing.T) {
	h := newGateHarness(t, "*", "MEMBER-OPENID")
	h.app.handleGatewayEvent(h.enc, context.Background(), GatewayEvent{
		Type: EventGroupMessageCreate,
		ID:   "EVT-FULL-1",
		Data: json.RawMessage(groupPayload("GROUP-OPENID", "MEMBER-OPENID", "MSG-FULL-1", "@bot 你好")),
	})

	if h.out.Len() != 0 {
		t.Fatalf("GROUP_MESSAGE_CREATE must not reach host, got frame: %s", h.out.String())
	}
	if calls := h.api.Calls(); len(calls) != 0 {
		t.Fatalf("GROUP_MESSAGE_CREATE must make zero outbound API calls, got %+v", calls)
	}
}

// TestGroupFullMessageNotParseable 第二道防线：解析层也不认这个事件类型。
func TestGroupFullMessageNotParseable(t *testing.T) {
	if SupportedEvent(EventGroupMessageCreate) {
		t.Fatalf("SupportedEvent(GROUP_MESSAGE_CREATE) = true, want false")
	}
	if _, ok, err := ParseInbound(EventGroupMessageCreate,
		json.RawMessage(groupPayload("G", "M", "MSG-1", "@bot hi"))); ok || err != nil {
		t.Fatalf("ParseInbound(GROUP_MESSAGE_CREATE) = ok=%t err=%v, want ok=false err=nil", ok, err)
	}
	// 两个真正要处理的事件类型保持可用。
	for _, et := range []string{EventC2CMessageCreate, EventGroupAtMessageCreate} {
		if !SupportedEvent(et) {
			t.Fatalf("SupportedEvent(%s) = false, want true", et)
		}
	}
}

// TestGroupAtAndC2CStillProcessed @ 消息与单聊消息仍然正常进 host。
func TestGroupAtAndC2CStillProcessed(t *testing.T) {
	g := newGateHarness(t, "*", "")
	g.sendGroup(t, "GROUP-OPENID", "MEMBER-OPENID", "@bot 你好")
	if frames := decodeFrames(t, g.out.String()); len(frames) != 1 || frames[0]["method"] != "send_inbound" {
		t.Fatalf("GROUP_AT_MESSAGE_CREATE frames = %v, want one send_inbound", frames)
	}

	c := newGateHarness(t, "*", "")
	c.send(t, "USER-OPENID", "你好")
	if frames := decodeFrames(t, c.out.String()); len(frames) != 1 || frames[0]["method"] != "send_inbound" {
		t.Fatalf("C2C_MESSAGE_CREATE frames = %v, want one send_inbound", frames)
	}
}

// TestGroupFullMessageCommentMentionsDiscard 文档纪律：丢弃必须是"显式"的，
// 代码里要有可检索的说明（防止后人误以为漏处理而"修好"它）。
func TestGroupFullMessageCommentMentionsDiscard(t *testing.T) {
	if !strings.Contains(debugDropReason, "群全量") {
		t.Fatalf("debugDropReason = %q, want explicit 群全量 wording", debugDropReason)
	}
}
