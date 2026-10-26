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
	return f.record(sentCall{kind: "c2c", target: userOpenID, content: content, msgID: msgID, seq: seq})
}

func (f *fakeMessageAPI) SendGroup(_ context.Context, groupOpenID, content, msgID string, seq uint32) (*SentMessage, error) {
	return f.record(sentCall{kind: "group", target: groupOpenID, content: content, msgID: msgID, seq: seq})
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
	if calls[0].kind != "c2c" || calls[0].target != "USER-OPENID" {
		t.Fatalf("first call = %+v, want c2c to USER-OPENID", calls[0])
	}
	if calls[1].kind != "group" || calls[1].target != "GROUP-OPENID" {
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

// TestSenderPassiveWindowExpires 超过 5 分钟的锚点不能再当被动回复用。
func TestSenderPassiveWindowExpires(t *testing.T) {
	api := &fakeMessageAPI{}
	s := NewOutboundSender(api)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	s.RememberInbound("U", "MSG-OLD")
	now = now.Add(passiveReplyWindow + time.Second)
	if _, err := s.Send(context.Background(), "U", "late reply"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := api.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].msgID != "" {
		t.Fatalf("msg_id = %q, want empty (outside 5min passive window)", calls[0].msgID)
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
