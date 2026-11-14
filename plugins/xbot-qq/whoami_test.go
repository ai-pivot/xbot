package main

// !whoami / 白名单三态 / 见过发送者台账的验收用例。
//
// 这三件事的共同纪律：**零 LLM**（不产生 send_inbound）+ **省配额**（限流）。
// 任何一条用例变红都意味着用户拿不到自己的 openid，或被动回复配额被烧。

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain 把 XBOT_HOME 指到临时目录：台账默认路径不能落到真实用户目录。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "xbot-qq-test-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XBOT_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// newSeenTestApp 构造 app，并把台账指到 t 的临时目录（用例之间互不污染）。
func newSeenTestApp(t *testing.T, cfg Config) (*app, *fakeMessageAPI, *bytes.Buffer) {
	t.Helper()
	a := newApp(discardWriter{})
	a.setConfig(cfg)
	api := &fakeMessageAPI{}
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, nil)
	a.seen = NewSeenSenderStore(filepath.Join(t.TempDir(), seenSendersFileName), nil)
	var out bytes.Buffer
	return a, api, &out
}

// discardWriter 是测试用的空日志写入器。
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestRejectedSenderIsRecordedButSilent 白名单**已配置**且不含本人：
// 完全无反应（零 send_inbound、零 API 调用），但台账必须记一条 handled=false。
func TestRejectedSenderIsRecordedButSilent(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "OPENID-ALLOWED"})
	a, api, out := newSeenTestApp(t, cfg)
	enc := json.NewEncoder(out)

	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		Data: json.RawMessage(c2cPayload("OPENID-STRANGER", "m-r1", "let me in")),
	})

	if out.Len() != 0 {
		t.Fatalf("被拒消息必须零 send_inbound，实际输出 %q", out.String())
	}
	if calls := api.Calls(); len(calls) != 0 {
		t.Fatalf("白名单已配置且不含本人时必须静默，实际 API 调用 %+v", calls)
	}
	got, ok := a.seen.Lookup("OPENID-STRANGER")
	if !ok {
		t.Fatalf("被拒发送者必须写进台账（用户只能靠它拿到 openid）")
	}
	if got.Handled {
		t.Fatalf("被拒消息 handled 必须为 false，实际 %+v", got)
	}
	if got.ChatType != "c2c" || got.SenderOpenID != "OPENID-STRANGER" {
		t.Fatalf("台账字段不对：%+v", got)
	}
}

// TestWhoamiAnsweredBeforeWhitelist 非白名单用户发 !whoami：
// 必须拿到自己的 openid（零 LLM），且只发一条 QQ 消息。
func TestWhoamiAnsweredBeforeWhitelist(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "OPENID-OTHER"})
	a, api, out := newSeenTestApp(t, cfg)
	enc := json.NewEncoder(out)

	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		Data: json.RawMessage(c2cPayload("OPENID-STRANGER", "m-w1", "!whoami")),
	})

	if out.Len() != 0 {
		t.Fatalf("!whoami 不得触发 send_inbound（零 LLM），实际 %q", out.String())
	}
	calls := api.Calls()
	if len(calls) != 1 {
		t.Fatalf("!whoami 必须恰好回一条消息，实际 %d 条: %+v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0].kind, "c2c") || calls[0].target != "OPENID-STRANGER" {
		t.Fatalf("回复路由不对：%+v", calls[0])
	}
	if !strings.Contains(calls[0].content, "OPENID-STRANGER") {
		t.Fatalf("回复里必须带 openid，实际 %q", calls[0].content)
	}
	if !strings.Contains(calls[0].content, "不在 allow") {
		t.Fatalf("回复里必须说明白名单状态，实际 %q", calls[0].content)
	}
	if got, ok := a.seen.Lookup("OPENID-STRANGER"); !ok || !got.Handled {
		t.Fatalf("!whoami 的发送者必须入台账且 handled=true，实际 %+v ok=%v", got, ok)
	}
}

// TestWhoamiIsRateLimited 同一 openid 60s 内只回一次（保护被动回复配额）。
func TestWhoamiIsRateLimited(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "OPENID-OTHER"})
	a, api, out := newSeenTestApp(t, cfg)
	enc := json.NewEncoder(out)
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	now := base
	a.notice = NewNoticeLimiter(noticeCooldown, func() time.Time { return now })

	send := func(msgID string) {
		a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
			Type: EventC2CMessageCreate,
			Data: json.RawMessage(c2cPayload("OPENID-STRANGER", msgID, "!whoami")),
		})
	}
	send("m-1")
	send("m-2") // 冷却期内：必须被抑制
	if got := len(api.Calls()); got != 1 {
		t.Fatalf("冷却期内应只回 1 条，实际 %d 条", got)
	}
	now = base.Add(noticeCooldown + time.Second)
	send("m-3")
	if got := len(api.Calls()); got != 2 {
		t.Fatalf("冷却结束后应再回 1 条（共 2 条），实际 %d 条", got)
	}
}

// TestAllowEmptyAutoReply allow 未配置时：自动回一条 openid 指引（零 LLM）。
// 注意这与"allow 已配置但不含本人"是**两种语义**，不许混成一条。
func TestAllowEmptyAutoReply(t *testing.T) {
	cfg := ParseConfig(map[string]string{}) // allow 为空
	a, api, out := newSeenTestApp(t, cfg)
	enc := json.NewEncoder(out)

	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventGroupAtMessageCreate,
		Data: json.RawMessage(groupPayload("GROUP-OPENID", "MEMBER-OPENID", "m-e1", "你好")),
	})

	if out.Len() != 0 {
		t.Fatalf("allow 为空时也不许进 LLM，实际 %q", out.String())
	}
	calls := api.Calls()
	if len(calls) != 1 {
		t.Fatalf("allow 为空时必须自动回一条指引，实际 %d 条", len(calls))
	}
	if !strings.HasPrefix(calls[0].kind, "group") || calls[0].target != "GROUP-OPENID" {
		t.Fatalf("回复应走群会话：%+v", calls[0])
	}
	if !strings.Contains(calls[0].content, "MEMBER-OPENID") || !strings.Contains(calls[0].content, "allow") {
		t.Fatalf("指引里必须带 openid 与 allow 说明，实际 %q", calls[0].content)
	}
	if got, ok := a.seen.Lookup("MEMBER-OPENID"); !ok || got.Handled {
		t.Fatalf("allow 为空时台账 handled 必须为 false，实际 %+v ok=%v", got, ok)
	}
	if got, _ := a.seen.Lookup("MEMBER-OPENID"); got.GroupOpenID != "GROUP-OPENID" || got.ChatType != "group" {
		t.Fatalf("群台账字段不对：%+v", got)
	}
}

// TestSeenSendersDedupAndCap 同一 openid 只留最新一条；总数不超过 200。
func TestSeenSendersDedupAndCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), seenSendersFileName)
	s := NewSeenSenderStore(path, nil)

	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	if err := s.Record(SeenSender{ChatType: "c2c", SenderOpenID: "A", SenderName: "旧名", Handled: false}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.Record(SeenSender{ChatType: "c2c", SenderOpenID: "A", SenderName: "新名", Handled: true}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("同一 openid 去重后应为 1 条，实际 %d", s.Len())
	}
	got, _ := s.Lookup("A")
	if got.SenderName != "新名" || !got.Handled {
		t.Fatalf("必须保留最新一条，实际 %+v", got)
	}

	for i := 0; i < seenSendersMax+30; i++ {
		id := "S" + strings.Repeat("x", 1) + itoaSeen(i)
		if err := s.Record(SeenSender{ChatType: "group", GroupOpenID: "G", SenderOpenID: id, SenderName: "n"}); err != nil {
			t.Fatalf("record %s: %v", id, err)
		}
	}
	if s.Len() != seenSendersMax {
		t.Fatalf("台账上限应为 %d，实际 %d", seenSendersMax, s.Len())
	}
	lines := readLines(t, path)
	if len(lines) != seenSendersMax {
		t.Fatalf("文件里应恰好 %d 行（去重+上限），实际 %d", seenSendersMax, len(lines))
	}
	// 最旧的 A 应已被淘汰（230 条之后）。
	if _, ok := s.Lookup("A"); ok {
		t.Fatalf("超过上限后最旧记录应被淘汰")
	}
}

// TestSeenSendersFileIsJSONLWithContractFields 落盘字段是契约，必须齐全。
func TestSeenSendersFileIsJSONLWithContractFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), seenSendersFileName)
	s := NewSeenSenderStore(path, nil)
	if err := s.Record(SeenSender{
		TS: "2026-10-26T12:00:00Z", ChatType: "group", GroupOpenID: "G1",
		SenderOpenID: "U1", SenderName: "鲸鱼娘", Handled: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("不是合法 JSON: %v (%q)", err, lines[0])
	}
	for _, k := range []string{"ts", "chat_type", "group_openid", "sender_openid", "sender_name", "handled"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺字段 %q: %v", k, m)
		}
	}
	if m["handled"] != true || m["sender_openid"] != "U1" {
		t.Fatalf("字段值不对: %v", m)
	}
}

// TestSeenSendersReloadFromDisk 进程重启后台账仍能读到（否则报 id 会丢历史）。
func TestSeenSendersReloadFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), seenSendersFileName)
	s1 := NewSeenSenderStore(path, nil)
	if err := s1.Record(SeenSender{ChatType: "c2c", SenderOpenID: "U1", SenderName: "甲"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	s2 := NewSeenSenderStore(path, nil)
	if got, ok := s2.Lookup("U1"); !ok || got.SenderName != "甲" {
		t.Fatalf("重启后应能从磁盘恢复，实际 %+v ok=%v", got, ok)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// itoaSeen 避免为了一个数字引入 strconv（本仓库风格：零依赖）。
// 注意：不能叫 itoa——attachments_test.go 里已有一个包级 itoa，同包会重复声明。
func itoaSeen(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
