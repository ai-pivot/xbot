package main

// 群/私聊会话标识与入站日志的对照实验。
//
// 全部用**官方文档的事件示例体**（group_at_message_create.html 示例1 / 示例2、
// c2c_message_create.html）作为固定 mock，不自己编字段名 —— 这样字段名写错
// 时用例必红，而不是测试和实现一起错。
//
// 判别力（删掉对应逻辑，哪个用例红）：
//   - 删掉 events.go 里 `GroupOpenID: d.GroupOpenID` 的取值 → 群用例全红
//     （解析直接报 "payload has no group_openid"，收不到 send_inbound）。
//   - 删掉 ChatID() 的 group: 分支 → 群用例红（chat 退化成裸 openid）。
//   - 删掉 looksLikePlatformToken 护栏 → 令牌正文用例红（令牌会被喂给 agent）。

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// officialGroupEvent 是官方《群@机器人消息》示例1（去掉了无关字段）。
const officialGroupEvent = `{
  "id": "ROBOT1.0_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "author": {
    "id": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4",
    "member_openid": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4",
    "member_role": "member",
    "username": "小明",
    "bot": false
  },
  "content": " 测试群回复 ",
  "group_openid": "B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5",
  "message_type": 0,
  "timestamp": "2026-07-21T10:00:00+08:00",
  "message_scene": {"source": "default", "ext": ["msg_idx=REFIDX_g1=="]}
}`

// officialC2CEvent 是官方《单聊消息事件》示例体形态。
const officialC2CEvent = `{
  "id": "ROBOT1.0_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
  "author": {"id": "C3D4E5F6", "user_openid": "C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6", "username": "小红"},
  "content": "在吗",
  "message_type": 0,
  "timestamp": "2026-07-21T10:00:00+08:00"
}`

const (
	officialGroupOpenID  = "B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5"
	officialMemberOpenID = "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4"
)

// TestOfficialGroupEventChatIDIsGroupOpenID 群消息的 chat 必须是 group:<group_openid>，
// 且与 sender 不同（sender 与 chat 相同 = 回复被当私聊发出去，群里看不到）。
func TestOfficialGroupEventChatIDIsGroupOpenID(t *testing.T) {
	in, ok, err := ParseInbound(EventGroupAtMessageCreate, json.RawMessage(officialGroupEvent))
	if err != nil || !ok {
		t.Fatalf("ParseInbound: ok=%v err=%v", ok, err)
	}
	if in.SenderOpenID != officialMemberOpenID {
		t.Fatalf("sender = %q, want member_openid %q", in.SenderOpenID, officialMemberOpenID)
	}
	if got, want := in.ChatID(), "group:"+officialGroupOpenID; got != want {
		t.Fatalf("chat = %q, want %q", got, want)
	}
	if in.ChatID() == in.SenderOpenID {
		t.Fatalf("chat 退化成了 sender（%q）—— 群消息会被当私聊回复", in.SenderOpenID)
	}
	if !strings.HasPrefix(in.ChatID(), "group:") {
		t.Fatalf("chat %q 缺 group: 前缀", in.ChatID())
	}
	if !in.IsGroup() || in.Identity().ChatType != ChatTypeGroup {
		t.Fatalf("chat_type = %q, want group", in.Identity().ChatType)
	}
	if in.Content != "测试群回复" {
		t.Fatalf("content = %q, want 测试群回复（官方会去掉@前缀与空白）", in.Content)
	}
}

// TestGroupEventWithoutGroupOpenIDRejected 群事件缺 group_openid 必须报错，
// 绝不许退化成私聊（这正是"群里收不到"的成因之一）。
func TestGroupEventWithoutGroupOpenIDRejected(t *testing.T) {
	body := strings.Replace(officialGroupEvent, `"group_openid": "B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5",`, "", 1)
	if body == officialGroupEvent {
		t.Fatal("mock 构造失败：group_openid 没被删掉")
	}
	_, ok, err := ParseInbound(EventGroupAtMessageCreate, json.RawMessage(body))
	if !ok || err == nil {
		t.Fatalf("缺 group_openid 必须报错，实际 ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "group_openid") {
		t.Fatalf("错误信息要指明 group_openid，实际 %v", err)
	}
}

// TestGroupEventGroupOpenIDEqualsSenderRejected group_openid == member_openid 是
// 身份取错的信号（回复会发给个人），必须挡在解析层。
func TestGroupEventGroupOpenIDEqualsSenderRejected(t *testing.T) {
	body := strings.Replace(officialGroupEvent, `"group_openid": "B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5",`,
		`"group_openid": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4",`, 1)
	_, ok, err := ParseInbound(EventGroupAtMessageCreate, json.RawMessage(body))
	if !ok || err == nil {
		t.Fatalf("group_openid==member_openid 必须报错，实际 ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "member_openid") {
		t.Fatalf("错误信息要点明字段冲突，实际 %v", err)
	}
}

// TestC2CEventChatIsSender 私聊 chat 就是 sender openid（此时 A==B 是正确的，
// 这正是群里那条日志被误判的原因 —— 日志必须能区分场景）。
func TestC2CEventChatIsSender(t *testing.T) {
	in, ok, err := ParseInbound(EventC2CMessageCreate, json.RawMessage(officialC2CEvent))
	if err != nil || !ok {
		t.Fatalf("ParseInbound: ok=%v err=%v", ok, err)
	}
	if in.ChatID() != in.SenderOpenID {
		t.Fatalf("c2c chat=%q sender=%q，应相等", in.ChatID(), in.SenderOpenID)
	}
	if got := chatLogTarget(in); got != "c2c:"+in.SenderOpenID {
		t.Fatalf("日志 chat 标识 = %q, want c2c:<sender>", got)
	}
}

// TestGroupChatLogTargetHasGroupPrefix 日志里群会话必须带 group: 前缀，
// 且与 sender 不同（用户给的验收判据就是照着这行 grep）。
func TestGroupChatLogTargetHasGroupPrefix(t *testing.T) {
	in, _, err := ParseInbound(EventGroupAtMessageCreate, json.RawMessage(officialGroupEvent))
	if err != nil {
		t.Fatal(err)
	}
	got := chatLogTarget(in)
	if got != "group:"+officialGroupOpenID {
		t.Fatalf("日志 chat = %q, want group:<group_openid>", got)
	}
	if strings.TrimPrefix(got, "group:") == in.SenderOpenID {
		t.Fatalf("日志 chat 与 sender 相同（%q）", got)
	}
}

// TestInboundAcceptedLogStructured 「inbound accepted」必须是可 grep 的结构化行：
// sender / chat / chat_type / event / body_len / body 六项齐全。
//
// 这条用例的价值在于：把群消息当私聊处理、或者正文没被正确解析时必红。
func TestInboundAcceptedLogStructured(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "*"})
	var logBuf bytes.Buffer
	a := newApp(&logBuf)
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: &fakeMessageAPI{}}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventGroupAtMessageCreate,
		Data: json.RawMessage(officialGroupEvent),
	})

	line := ""
	for _, l := range strings.Split(logBuf.String(), "\n") {
		if strings.Contains(l, "inbound accepted:") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("没有 inbound accepted 日志：%s", logBuf.String())
	}
	for _, want := range []string{
		"sender=" + officialMemberOpenID,
		"chat=group:" + officialGroupOpenID,
		"chat_type=group",
		"event=" + EventGroupAtMessageCreate,
		"body_len=5", // "测试群回复" = 5 个字符
		"body=测试群回复",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("日志缺 %q\n实际: %s", want, line)
		}
	}
	if strings.Contains(line, "sender="+officialMemberOpenID+" chat="+officialMemberOpenID) {
		t.Fatalf("chat 退化成 sender：%s", line)
	}

	// 同一条消息必须真的走了群发送接口的会话 id。
	params := decodeFrames(t, out.String())[0]["params"].(map[string]any)
	if params["chat_id"] != "group:"+officialGroupOpenID {
		t.Fatalf("send_inbound chat_id = %v", params["chat_id"])
	}
}

// TestPlatformTokenBodyDropped 正文若是平台令牌（ROBOT1.0_ 长串），必须报错并丢弃，
// 不许喂给 agent。
func TestPlatformTokenBodyDropped(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "*"})
	var logBuf bytes.Buffer
	a := newApp(&logBuf)
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: &fakeMessageAPI{}}, nil)

	token := "ROBOT1.0_XRI2-UouSyizgPaBYjUG1md09t3Cfk9zUSr3LGboxzV.3zvXyGddoaYaGCQKf24VWPjGQxOHtSwqK-YmZprdVL3ngGb.Wl-sLP-ZQpRmBmM!"
	body := strings.Replace(officialGroupEvent, `"content": " 测试群回复 "`, `"content": "`+token+`"`, 1)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventGroupAtMessageCreate,
		Data: json.RawMessage(body),
	})

	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("令牌正文不许产生 send_inbound，实际: %s", out.String())
	}
	if !strings.Contains(logBuf.String(), "body is a platform token") {
		t.Fatalf("必须明确报错并丢弃，实际日志: %s", logBuf.String())
	}
}

// TestLooksLikePlatformTokenBoundary 护栏不能误伤普通文本（含用户手打 ROBOT1.0_）。
func TestLooksLikePlatformTokenBoundary(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"测试群回复", false},
		{"ROBOT1.0_", false},
		{"ROBOT1.0_short", false},
		{"消息 ROBOT1.0_abcdefghijklmnopqrstuvwxyz", false},
		{"ROBOT1.0_abcdefghijklmnopqrstuvwxyz", true},
		{"ROBOT1.0_XRI2-UouSyizgPaBYjUG1md09t3Cfk9zUSr3LGboxzV.3zvXyGddoaYaGCQKf24VWPjGQxOHtSwqK-YmZprdVL3ngGb.Wl-sLP-ZQpRmBmM!", true},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikePlatformToken(c.body); got != c.want {
			t.Fatalf("looksLikePlatformToken(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

// TestLogPreviewTruncatesByRune 日志正文按字符截断且压成单行。
func TestLogPreviewTruncatesByRune(t *testing.T) {
	short := "测试群回复"
	if got := logPreview(short); got != short {
		t.Fatalf("logPreview(%q) = %q", short, got)
	}
	long := strings.Repeat("群", logBodyLimit+10)
	got := logPreview(long)
	if n := bodyRuneLen(strings.TrimSuffix(got, "…")); n != logBodyLimit {
		t.Fatalf("截断后字符数 = %d, want %d", n, logBodyLimit)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断必须带省略号: %q", got)
	}
	if strings.Contains(logPreview("a\nb\tc"), "\n") {
		t.Fatal("正文里的换行必须转义，否则一条入站会撕成多行日志")
	}
}
