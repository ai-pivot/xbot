package llm

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGenerateStream_FirstChunkTimeout —— 2026-10-01 cancel 无效事故的姊妹守护。
//
// 事故链（chat_D3D036023DB7 turn 3）：10:40:47 的 LLM 请求发出后上游挂死（无响应头 /
// 无首 chunk），请求永挂 → Run 卡在上游等待里（cancel 只能标记 ctx，等不到任何错误）
// → 8 分钟后用户 cancel 时 Run 已消失、cancel state 残留 → busy 永卡 + queue 永挂。
//
// 本守护钉死「上游挂死不能让请求永挂」：firstChunkTimeout 到期掐断连接 → 错误冒泡走
// 既有 retry → 最终失败 → processMessage err 分支正常收尾（busy/cancel state 不残留）。
//
// mutation 判别力：删掉 GenerateStream 的首帧超时（guard/timer）⇒ GenerateStream 在挂死
// 上游上永不返回 ⇒ 本条 5s 判定必红。
func TestGenerateStream_FirstChunkTimeout(t *testing.T) {
	// 挂死上游：接受连接但永不写响应（连响应头都不发——复刻上游挂死的形态之一）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 挂住直到客户端断开（超时掐断时）
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	old := firstChunkTimeout
	firstChunkTimeout = 300 * time.Millisecond
	defer func() { firstChunkTimeout = old }()

	o := NewOpenAILLM(OpenAIConfig{BaseURL: "http://" + ln.Addr().String(), APIKey: "k", MaxTokens: 10})

	done := make(chan error, 1)
	go func() {
		_, err := o.GenerateStream(context.Background(), "test-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("挂死上游必须返回错误（首帧超时应掐断连接并冒泡错误）")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateStream 未在首帧超时内返回——修复前形态：上游挂死时请求永挂，Run 卡死无法收尾（2026-10-01 事故）")
	}
}

// TestGenerateStream_FirstChunkArrivedClearsTimeout —— 对照面：首帧按时到达后超时解除，
// 慢速长流不会被首帧超时误杀（短超时设置下正常流必须完整消费）。
func TestGenerateStream_FirstChunkArrivedClearsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}"+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	old := firstChunkTimeout
	firstChunkTimeout = 200 * time.Millisecond // 首帧必须立即到达——超时窗口极短但不应误杀
	defer func() { firstChunkTimeout = old }()

	o := NewOpenAILLM(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", MaxTokens: 10})

	eventCh, err := o.GenerateStream(context.Background(), "test-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("正常流不得因首帧超时报错：%v", err)
	}
	var sawContent, sawDone bool
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				break loop
			}
			if ev.Type == EventContent {
				sawContent = true
			}
			if ev.Type == EventDone {
				sawDone = true
			}
		case <-deadline:
			t.Fatal("事件流未在期限内完成——正常流被首帧超时误杀")
		}
	}
	if !sawContent {
		t.Fatal("正常流的内容事件丢失")
	}
	_ = sawDone
}
