package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/protocol"
)

// REPRO —— 用户报告：web 输入框里输入以 `!` 开头的指令"没有生效"。
//
// 上游其实已有该功能（agent/bang_command.go：`!cmd` 跳过 LLM，直接在 sandbox 里
// 执行并把输出发回）。但经 web 输入框发出的 `!cmd` **永远拿不到结果**：
// chatWorker 把它当"并发命令"处理（按设计不分配 turn_id，也不产生 turn_started），
// 而 REST 入口的命令豁免只认 `/` 前缀（isSlashCommand），于是 turnID==0 被判成
// "未绑定 turn 的用户消息" → 500 "message accepted without a turn_id"，
// 前端乐观行卡在"发送中"，用户看到的就是"这个功能不存在"。
//
// 服务端日志实证（用户输入 !pwd 三次，每次都真的执行了，但结果被拦下）：
//
//	Command matched in chatWorker | command=!
//	Bang command | command=pwd | sandbox_user=web-1
//	handleMessage: turn_id is 0 for a user message — refusing to return an unbound user message
//
// 修复方向：命令豁免必须与**分发同一份判定**（agent.CommandRegistry.Match，经
// WebCallbacks.MatchesCommand 注入）—— slash `/xxx` 与 bang `!cmd` 一视同仁，
// 而不是用 "/" 前缀启发式。
func bangRestHarness(t *testing.T, matches func(string) bool) (*WebChannel, *bus.MessageBus, chan protocol.WSMessage) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{}, msgBus)
	wc.SetOSSProvider(fixedOSSProvider{})
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	wc.callbacks.MatchesCommand = matches
	sendCh := make(chan protocol.WSMessage, 8)
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         sendCh,
		done:           make(chan struct{}),
		id:             "bang-client",
		sessionChannel: "web",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", "web-1"))
	return wc, msgBus, sendCh
}

// registryLike 模拟真实注册表：`/` 前缀（slash）与 `!` 前缀（bang）都是命令。
func registryLike(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!")
}

// ackEmpty simulates the chatWorker's concurrent-command acknowledgement:
// it hands the message to the agent and acks WITHOUT a turn_id (TurnID=0,
// Queued=false) — exactly what `!cmd` / `/cmd` produce today.
func ackEmpty(msgBus *bus.MessageBus) {
	go func() {
		message := <-msgBus.Inbound
		message.DeliveryAck <- bus.DeliveryResult{}
	}()
}

func postMessage(t *testing.T, wc *WebChannel, msgID, content string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	body := []byte(`{"id":"` + msgID + `","content":"` + content + `"}`)
	wc.handleMessage(recorder, authedAPIRequest(http.MethodPost, "/api/message", body))
	return recorder
}

// 用户报告的主场景：`!cmd` 必须被接受（200，无 turn_id），而不是 500。
func TestRESTMessageBangCommandNeedsNoTurnID(t *testing.T) {
	wc, msgBus, _ := bangRestHarness(t, registryLike)

	ackEmpty(msgBus)
	recorder := postMessage(t, wc, "bang-request", "!echo hello")

	if recorder.Code != http.StatusOK {
		t.Fatalf("bang command status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("turn_id")) {
		t.Fatalf("bang command response must omit turn_id (no turn semantics): %s", recorder.Body.String())
	}
}

// 不变量：正常用户消息（非命令）turnID==0 仍然必须失败 —— 否则前端会把乐观 user 行
// 绑到一个不存在的 turn 上（回复渲染在 user 消息之前）。
func TestRESTMessageNonCommandStillRequiresTurnID(t *testing.T) {
	wc, msgBus, _ := bangRestHarness(t, registryLike)

	ackEmpty(msgBus)
	recorder := postMessage(t, wc, "plain-request", "hello there")

	if recorder.Code == http.StatusOK {
		t.Fatalf("plain message with turnID=0 must fail, got 200: %s", recorder.Body.String())
	}
}

// slash 命令的既有行为不能回归。
func TestRESTMessageSlashCommandNeedsNoTurnID(t *testing.T) {
	wc, msgBus, _ := bangRestHarness(t, registryLike)

	ackEmpty(msgBus)
	recorder := postMessage(t, wc, "slash-request", "/new")

	if recorder.Code != http.StatusOK {
		t.Fatalf("slash command status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

// CR 2026-09-21 P2-2：**无 registry**（单测 / 嵌入式）时的降级分支也必须认 bang ——
// 只认 `/` 前缀会让 `!cmd` 落到 handleMessage 的 fail-fast
// （"message accepted without a turn_id"），与本次修复目标自相矛盾。
func TestIsCommandMessage_FallbackCoversBang(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	// 注意：这里刻意**不**注入 callbacks.MatchesCommand ⇒ 走降级分支。
	cases := []struct {
		content string
		want    bool
	}{
		{"!pwd", true},
		{"   !ls -la", true}, // 前导空白（与 isSlashCommand 同款 trim 语义）
		{"/help", true},
		{"hello world", false},
		{"echo hi", false},
	}
	for _, tc := range cases {
		if got := wc.isCommandMessage(tc.content); got != tc.want {
			t.Errorf("isCommandMessage(%q) = %v, want %v（降级分支必须覆盖 bang 前缀）", tc.content, got, tc.want)
		}
	}
}
