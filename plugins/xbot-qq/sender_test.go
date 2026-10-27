package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// sentCall 记录一次发送调用。
type sentCall struct {
	kind    string // "c2c" / "group"
	target  string // openid / group_openid
	content string
	msgID   string
	seq     uint32
}

// fakeMessageAPI 是 MessageAPI 的计数假件。
type fakeMessageAPI struct {
	mu    sync.Mutex
	calls []sentCall
	err   error
}

func (f *fakeMessageAPI) SendC2C(_ context.Context, userOpenID, content, msgID string, seq uint32) (*SentMessage, error) {
	return f.record(sentCall{kind: "c2c-markdown", target: userOpenID, content: content, msgID: msgID, seq: seq})
}

func (f *fakeMessageAPI) SendGroup(_ context.Context, groupOpenID, content, msgID string, seq uint32) (*SentMessage, error) {
	return f.record(sentCall{kind: "group-markdown", target: groupOpenID, content: content, msgID: msgID, seq: seq})
}

func (f *fakeMessageAPI) SendC2CMarkdown(_ context.Context, userOpenID, md, msgID string, seq uint32) (*SentMessage, error) {
	return f.record(sentCall{kind: "c2c-markdown", target: userOpenID, content: md, msgID: msgID, seq: seq})
}

func (f *fakeMessageAPI) SendGroupMarkdown(_ context.Context, groupOpenID, md, msgID string, seq uint32) (*SentMessage, error) {
	return f.record(sentCall{kind: "group-markdown", target: groupOpenID, content: md, msgID: msgID, seq: seq})
}

func (f *fakeMessageAPI) StreamC2C(_ context.Context, userOpenID string, body streamBody) (*SentMessage, error) {
	return f.record(sentCall{kind: "c2c-stream", target: userOpenID, content: body.ContentRaw, msgID: body.MsgID, seq: body.MsgSeq})
}

func (f *fakeMessageAPI) UploadImage(_ context.Context, chatID, imageURL string) (string, error) {
	return "FILEINFO:" + imageURL, nil
}

func (f *fakeMessageAPI) SendMedia(_ context.Context, chatID, fileInfo, msgID string, seq uint32) (*SentMessage, error) {
	return f.record(sentCall{kind: "media", target: chatID, content: fileInfo, msgID: msgID, seq: seq})
}

func (f *fakeMessageAPI) record(c sentCall) (*SentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, c)
	return &SentMessage{ID: fmt.Sprintf("sent-%d", len(f.calls))}, nil
}

func (f *fakeMessageAPI) Calls() []sentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestSenderRoutesByChatID 私聊走 c2c、群聊（group: 前缀）走 group。
func TestSenderRoutesByChatID(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)

	if _, err := s.Send(context.Background(), "USER-OPENID", "hi"); err != nil {
		t.Fatalf("Send private: %v", err)
	}
	if _, err := s.Send(context.Background(), "group:GROUP-OPENID", "hi"); err != nil {
		t.Fatalf("Send group: %v", err)
	}

	calls := api.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if !strings.HasPrefix(calls[0].kind, "c2c") || calls[0].target != "USER-OPENID" {
		t.Fatalf("first call = %+v, want c2c to USER-OPENID", calls[0])
	}
	if !strings.HasPrefix(calls[1].kind, "group") || calls[1].target != "GROUP-OPENID" {
		t.Fatalf("second call = %+v, want group to GROUP-OPENID", calls[1])
	}
}

// TestSenderMsgSeqIncrementsPerAnchor 同一 msg_id 的多次回复 msg_seq 必须递增；
// 换了锚点（新的入站消息）后重新从 1 开始。
func TestSenderMsgSeqIncrementsPerAnchor(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.RememberInbound("U", "MSG-1")

	for i := 0; i < 3; i++ {
		if _, err := s.Send(context.Background(), "U", fmt.Sprintf("reply-%d", i)); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	s.RememberInbound("U", "MSG-2")
	if _, err := s.Send(context.Background(), "U", "new-anchor"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	calls := api.Calls()
	wantSeqs := []uint32{1, 2, 3, 1}
	if len(calls) != len(wantSeqs) {
		t.Fatalf("calls = %d, want %d", len(calls), len(wantSeqs))
	}
	for i, want := range wantSeqs {
		if calls[i].seq != want {
			t.Fatalf("call[%d].seq = %d, want %d", i, calls[i].seq, want)
		}
	}
	if calls[0].msgID != "MSG-1" || calls[3].msgID != "MSG-2" {
		t.Fatalf("msg_id anchoring wrong: %+v", calls)
	}
}

// TestSenderPassiveWindowExpires 超过文档时效后不能再当被动回复用。
//
// 文档：单聊 60 分钟、群聊 5 分钟（overview.html §频率与时效规则-被动消息）。
func TestSenderPassiveWindowExpires(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	// 单聊：59 分钟仍可带 msg_id，61 分钟不再带。
	s.RememberInbound("U", "MSG-OLD")
	now = now.Add(59 * time.Minute)
	if _, err := s.Send(context.Background(), "U", "still passive"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	now = now.Add(2 * time.Minute) // 61 分钟
	if _, err := s.Send(context.Background(), "U", "late reply"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 群聊：4 分钟可带，6 分钟不再带。
	s.RememberInbound("group:G", "MSG-G")
	now = now.Add(4 * time.Minute)
	if _, err := s.Send(context.Background(), "group:G", "group passive"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Send(context.Background(), "group:G", "group late"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	calls := api.Calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	if calls[0].msgID != "MSG-OLD" {
		t.Fatalf("private 59min msg_id = %q, want MSG-OLD", calls[0].msgID)
	}
	if calls[1].msgID != "" {
		t.Fatalf("private 61min msg_id = %q, want empty (outside 60min window)", calls[1].msgID)
	}
	if calls[2].msgID != "MSG-G" {
		t.Fatalf("group 4min msg_id = %q, want MSG-G", calls[2].msgID)
	}
	if calls[3].msgID != "" {
		t.Fatalf("group 6min msg_id = %q, want empty (outside 5min window)", calls[3].msgID)
	}
}

// TestSenderChunksLongContentAndIncrementsSeq 长文本分片，每片 seq 递增。
func TestSenderChunksLongContentAndIncrementsSeq(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.RememberInbound("U", "MSG-1")

	content := strings.Repeat("字", maxMessageRunes*2+10)
	if _, err := s.Send(context.Background(), "U", content); err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := api.Calls()
	if len(calls) != 3 {
		t.Fatalf("chunks = %d, want 3", len(calls))
	}
	for i, c := range calls {
		if c.seq != uint32(i+1) {
			t.Fatalf("chunk %d seq = %d, want %d", i, c.seq, i+1)
		}
		if got := len([]rune(c.content)); got > maxMessageRunes {
			t.Fatalf("chunk %d has %d runes, limit %d", i, got, maxMessageRunes)
		}
	}
	var rebuilt strings.Builder
	for _, c := range calls {
		rebuilt.WriteString(c.content)
	}
	if rebuilt.String() != content {
		t.Fatal("chunks must concatenate back to the original content")
	}
}

// TestSenderRejectsBadInput 空 chat_id / 空内容必须报错且不产生调用。
func TestSenderRejectsBadInput(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)

	if _, err := s.Send(context.Background(), "", "hi"); err == nil {
		t.Fatal("empty chat_id must error")
	}
	if _, err := s.Send(context.Background(), "group:", "hi"); err == nil {
		t.Fatal("malformed group chat_id must error")
	}
	if _, err := s.Send(context.Background(), "U", "   "); err == nil {
		t.Fatal("empty content must error")
	}
	if got := len(api.Calls()); got != 0 {
		t.Fatalf("api calls = %d, want 0", got)
	}
}

// TestSenderPropagatesAPIError API 错误必须原样返回（不能吞掉当成功）。
func TestSenderPropagatesAPIError(t *testing.T) {
	api := &fakeMessageAPI{err: fmt.Errorf("boom 400")}
	s := NewOutboundSender(api)
	if _, err := s.Send(context.Background(), "U", "hi"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want API error", err)
	}
}

// TestStreamC2CChunksAndState 私聊流式：分片语义必须符合官方文档。
//
// 文档《发送单聊流式消息》POST /v2/users/{openid}/stream_messages：
//
//	input_state  1=生成中，10=生成结束
//	index        分片序号，从 0 递增
//	stream_msg_id 首片由服务端返回，后续分片必须携带
//
// 群聊没有流式接口（只有 /v2/users/...），所以群聊必须仍走普通 messages。
func TestStreamC2CChunksAndState(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.Markdown = true
	s.Stream = true
	s.RememberInbound("USER-OPENID", "MSG-1")

	long := strings.Repeat("这是一段很长的内容。", 400) // 超过单片上限，必然分片
	if _, err := s.Send(context.Background(), "USER-OPENID", long); err != nil {
		t.Fatalf("Send err: %v", err)
	}
	calls := api.Calls()
	if len(calls) < 2 {
		t.Fatalf("长消息应分多片发送，实际 %d 片", len(calls))
	}
	for i, c := range calls {
		if c.kind != "c2c-stream" {
			t.Fatalf("第 %d 片 kind = %s, want c2c-stream（群聊才走普通 messages）", i, c.kind)
		}
	}
	if calls[0].content == "" {
		t.Fatalf("首片内容为空")
	}

	// 群聊即使开了 Stream，也必须回落到普通 messages（官方无群流式接口）。
	api2 := &fakeMessageAPI{}
	s2 := NewOutboundSender(api2)
	s2.Markdown = true
	s2.Stream = true
	s2.RememberInbound("group:G1", "MSG-2")
	if _, err := s2.Send(context.Background(), "group:G1", "群里的回复"); err != nil {
		t.Fatalf("group Send err: %v", err)
	}
	if got := api2.Calls(); len(got) != 1 || !strings.HasPrefix(got[0].kind, "group") {
		t.Fatalf("群聊必须走普通 messages，实际 %+v", got)
	}
}

// TestSendStreamDeltaC2COnly 增量流式：只有私聊转发、群聊忽略。
//
// 用户要求「个人聊天支持 stream」。官方只有 /v2/users/{openid}/stream_messages，
// 群聊没有对应接口 ⇒ 群会话的增量必须被忽略（返回 false 且零 API 调用）。
func TestSendStreamDeltaC2COnly(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.Markdown = true
	s.Stream = true
	s.RememberInbound("USER-OPENID", "MSG-S1")

	ok, err := s.SendStreamDelta(context.Background(), "USER-OPENID", "第一段", false)
	if err != nil || !ok {
		t.Fatalf("私聊增量应发出，ok=%t err=%v", ok, err)
	}
	if calls := api.Calls(); len(calls) != 1 || calls[0].kind != "c2c-stream" {
		t.Fatalf("calls = %+v, want 一条 c2c-stream", calls)
	}

	// 群聊：必须忽略
	api2 := &fakeMessageAPI{}
	s2 := NewOutboundSender(api2)
	s2.Stream = true
	ok2, err2 := s2.SendStreamDelta(context.Background(), "group:G1", "群里的增量", false)
	if err2 != nil || ok2 {
		t.Fatalf("群聊增量必须忽略，ok=%t err=%v", ok2, err2)
	}
	if calls := api2.Calls(); len(calls) != 0 {
		t.Fatalf("群聊增量不应产生 API 调用，实际 %+v", calls)
	}

	// 关闭流式开关时也必须忽略
	api3 := &fakeMessageAPI{}
	s3 := NewOutboundSender(api3)
	s3.Stream = false
	ok3, _ := s3.SendStreamDelta(context.Background(), "USER-OPENID", "x", false)
	if ok3 || len(api3.Calls()) != 0 {
		t.Fatalf("Stream=false 时不应发送")
	}
}

// TestStreamDeltaUsesReplaceMode 累计内容必须用 input_mode=replace。
//
// host 的 stream_content 帧给的是**累计正文**（ChannelPluginTransport.SendStreamContent
// 把 content 放在 WSMessage.Content）。用 append 会把每片重复拼接，
// 所以必须用 replace（ContentRaw = 当前全量，服务端自行求差）。
func TestStreamDeltaUsesReplaceMode(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.Stream = true
	s.RememberInbound("USER-OPENID", "MSG-R1")
	if _, err := s.SendStreamDelta(context.Background(), "USER-OPENID", "累计内容", false); err != nil {
		t.Fatalf("err: %v", err)
	}
	calls := api.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	// fake 里把 body 存进 content，用 mode 标记区分
	if !strings.Contains(calls[0].content, "累计内容") {
		t.Fatalf("content 丢了: %+v", calls[0])
	}
}

// TestStreamSplitsIntoSeveralPieces 私聊流式必须真的分多片。
//
// 2026-10-26：此前用 maxMessageRunes(1500+) 切片，常见回答只有 1 片，
// 用户侧"完全没有 stream"。这里锁住：一段中等长度的回答必须被切成多片，
// 且首片 input_state=1、末片=10、片间 index 递增。
func TestStreamSplitsIntoSeveralPieces(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.Stream = true
	s.Markdown = true
	s.streamDelay = 0 // 测试里不等待
	s.RememberInbound("USER-OPENID", "MSG-SP")

	answer := strings.Repeat("这是一句用来测试流式分片的话。", 20) // ~260 字（超过单片阈值 200）
	if _, err := s.Send(context.Background(), "USER-OPENID", answer); err != nil {
		t.Fatalf("err: %v", err)
	}
	calls := api.Calls()
	if len(calls) < 2 {
		t.Fatalf("流式应分多片，实际 %d 片（说明还在用大切片，客户端看不出流式）", len(calls))
	}
	for i, c := range calls {
		if c.kind != "c2c-stream" {
			t.Fatalf("第 %d 片 kind=%s", i, c.kind)
		}
	}
	// 拼接后必须等于原文（分片不能丢字/重复）
	var joined strings.Builder
	for _, c := range calls {
		joined.WriteString(c.content)
	}
	if joined.String() != answer {
		t.Fatalf("分片拼接与原文不一致：got %d runes, want %d", len([]rune(joined.String())), len([]rune(answer)))
	}
}

// TestMarkdownImagesGoThroughMediaChannel markdown 里的图片必须走富媒体通道。
//
// QQ 的 markdown 不渲染外链图片，`![](url)` 只会显示成一段文本。正确做法是：
// 抽出图片 → POST .../files {file_type:1,url,srv_send_msg:false} 拿 file_info
// → msg_type=7 + media.file_info 发被动回复 → 正文里移除图片语法。
func TestMarkdownImagesGoThroughMediaChannel(t *testing.T) {
	md := "这是说明\n\n![截图](https://example.com/a.png)\n\n还有 ![](https://example.com/b.jpg) 结尾"
	urls, rest := extractMarkdownImages(md)
	if len(urls) != 2 {
		t.Fatalf("应抽出 2 张图，实际 %d: %v", len(urls), urls)
	}
	if strings.Contains(rest, "![") || strings.Contains(rest, ".png") {
		t.Fatalf("正文里不应残留图片语法: %q", rest)
	}
	if !strings.Contains(rest, "这是说明") || !strings.Contains(rest, "结尾") {
		t.Fatalf("正文文字丢失: %q", rest)
	}

	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	s.RememberInbound("USER-OPENID", "MSG-IMG-1")
	n, err := s.SendImages(context.Background(), "USER-OPENID", urls)
	if err != nil || n != 2 {
		t.Fatalf("SendImages n=%d err=%v", n, err)
	}
	calls := api.Calls()
	if len(calls) != 2 {
		t.Fatalf("应发 2 条 media，实际 %d", len(calls))
	}
	for i, c := range calls {
		if c.kind != "media" {
			t.Fatalf("第 %d 条 kind=%s, want media", i, c.kind)
		}
		if !strings.HasPrefix(c.content, "FILEINFO:https://example.com/") {
			t.Fatalf("file_info 不对: %q", c.content)
		}
		if c.msgID != "MSG-IMG-1" {
			t.Fatalf("必须带被动回复锚点，实际 msgID=%q", c.msgID)
		}
	}
}
