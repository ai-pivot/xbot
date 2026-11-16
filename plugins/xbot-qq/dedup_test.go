package main

// 约束 5 的验收：官方「相同 msg_id 可能重复推送，需结合 message_scene.ext 中的
// msg_idx 做去重」。御前指定用例：同一 msg_id **连推 3 次** → 只产生
// **1 条 inbound、1 次 LLM、1 条 outbound**。
//
// 在本插件里：
//   - "1 条 inbound / 1 次 LLM" = 只有 1 个 send_inbound 帧（那是唯一触发
//     host agent 的入口；多一个帧就多一次模型调用）；
//   - "1 条 outbound" = 被准入的那条消息本身不产生任何 QQ 出站调用
//     （回复由 host 后续的 channel_send 触发，不属于本层）；
//     被丢弃的两条也不能产生任何出站调用。

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// scenePayload 造一条带 message_scene.ext 的 C2C 事件 d。
// ext 为 nil 时不带 message_scene（模拟平台没给去重键）。
func scenePayload(openid, msgID, content string, ext []string) string {
	base := map[string]any{
		"id":      msgID,
		"content": content,
		"author":  map[string]any{"user_openid": openid},
	}
	if ext != nil {
		base["message_scene"] = map[string]any{"ext": ext, "source": "default"}
	}
	raw, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func sendScene(h *gateHarness, t *testing.T, openid, msgID, content string, ext []string) {
	t.Helper()
	h.app.handleGatewayEvent(h.enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		ID:   "EVT-" + msgID,
		Data: json.RawMessage(scenePayload(openid, msgID, content, ext)),
	})
}

// TestDuplicatePushDeliveredOnce 御前指定用例：同一 msg_id 连推 3 次（msg_idx 相同）
// → 只 1 条 inbound、0 出站调用。
func TestDuplicatePushDeliveredOnce(t *testing.T) {
	h := newGateHarness(t, "*", "")
	for i := 0; i < 3; i++ {
		sendScene(h, t, "USER-OPENID", "MSG-DUP-1", "你好", []string{"msg_idx=1001", "auth_token=abc"})
	}

	frames := decodeFrames(t, h.out.String())
	if len(frames) != 1 {
		t.Fatalf("send_inbound frames = %d, want exactly 1 (dup push must be dropped): %s",
			len(frames), h.out.String())
	}
	if frames[0]["method"] != "send_inbound" {
		t.Fatalf("frame = %v", frames[0])
	}
	if calls := h.api.Calls(); len(calls) != 0 {
		t.Fatalf("duplicate push must make zero outbound API calls, got %+v", calls)
	}
	if n := h.app.dedup.Len(); n != 1 {
		t.Fatalf("dedup table size = %d, want 1", n)
	}
}

// TestDuplicateWithoutMsgIdxFallsBackToMessageID 平台没给 msg_idx 时，
// 同 msg_id 的重复推送同样要挡住。
func TestDuplicateWithoutMsgIdxFallsBackToMessageID(t *testing.T) {
	h := newGateHarness(t, "*", "")
	for i := 0; i < 3; i++ {
		sendScene(h, t, "USER-OPENID", "MSG-DUP-2", "你好", nil)
	}
	if frames := decodeFrames(t, h.out.String()); len(frames) != 1 {
		t.Fatalf("send_inbound frames = %d, want 1 (fallback dedup by msg_id)", len(frames))
	}
}

// TestDistinctMsgIdxBothDelivered 不同 msg_idx 是不同消息，必须都进来。
func TestDistinctMsgIdxBothDelivered(t *testing.T) {
	h := newGateHarness(t, "*", "")
	sendScene(h, t, "USER-OPENID", "MSG-A", "第一句", []string{"msg_idx=1"})
	sendScene(h, t, "USER-OPENID", "MSG-B", "第二句", []string{"msg_idx=2"})
	if frames := decodeFrames(t, h.out.String()); len(frames) != 2 {
		t.Fatalf("send_inbound frames = %d, want 2 (distinct msg_idx)", len(frames))
	}
}

// TestDedupKeyScopedByChat 同 msg_idx 出现在不同会话里不能被吞掉。
func TestDedupKeyScopedByChat(t *testing.T) {
	h := newGateHarness(t, "*", "")
	sendScene(h, t, "USER-A", "MSG-A", "hi", []string{"msg_idx=7"})
	sendScene(h, t, "USER-B", "MSG-B", "hi", []string{"msg_idx=7"})
	if frames := decodeFrames(t, h.out.String()); len(frames) != 2 {
		t.Fatalf("send_inbound frames = %d, want 2 (msg_idx is per-chat)", len(frames))
	}
}

// TestDedupWindowExpires 窗口过期后同样的键算新消息（至少不会永久静默）。
func TestDedupWindowExpires(t *testing.T) {
	now := time.Unix(1700000000, 0)
	d := newInboundDeduper(time.Minute, 8)
	d.now = func() time.Time { return now }

	if d.Duplicate("k") {
		t.Fatal("first sight must not be duplicate")
	}
	if !d.Duplicate("k") {
		t.Fatal("second sight within window must be duplicate")
	}
	now = now.Add(2 * time.Minute)
	if d.Duplicate("k") {
		t.Fatal("after TTL the key must be accepted again")
	}
}

// TestDedupTableBounded 长跑不能无界增长。
func TestDedupTableBounded(t *testing.T) {
	d := newInboundDeduper(time.Hour, 4)
	for i := 0; i < 50; i++ {
		d.Duplicate("k" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if n := d.Len(); n > 4 {
		t.Fatalf("dedup table size = %d, want <= 4", n)
	}
}

// TestParseSceneExtFormat message_scene.ext 是 **[]string 的 k=v 串**，不是 map。
func TestParseSceneExtFormat(t *testing.T) {
	got := parseSceneExt([]string{"msg_idx=42", "ref_msg_idx=41", "auth_token=tok", "junk", " =x"})
	if got["msg_idx"] != "42" || got["ref_msg_idx"] != "41" || got["auth_token"] != "tok" {
		t.Fatalf("parseSceneExt = %v", got)
	}
	if _, ok := got["junk"]; ok {
		t.Fatalf("entry without '=' must be ignored: %v", got)
	}
	if sceneExtInt(got, "msg_idx") != 42 {
		t.Fatalf("sceneExtInt(msg_idx) = %d, want 42", sceneExtInt(got, "msg_idx"))
	}
	if sceneExtInt(got, "missing") != 0 {
		t.Fatal("missing key must be 0")
	}
}
