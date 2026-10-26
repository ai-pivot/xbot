package main

// 约束 1 验收（御前四条硬约束之一，用户最看重）：
//
//  1. **禁止发进度**：群聊一条入站 = 最多一条出站；单聊默认也只发一条。
//     host 的 progress_structured / stream_content 帧一律丢弃，只发最终 text。
//  2. **配额自检**：每个锚点（msg_id）被动回复上限 单聊 4 / 群聊 5，
//     超出拒发并记 ERROR —— 不把 40034128 留给平台去发现。
//
// 文档依据：
//   - 群消息不支持流式：v2_groups_group_openid_messages.post.html「注意: 群消息不支持流式参数」
//   - 单聊流式存在：POST /v2/users/{user_openid}/stream_messages
//     （input_mode / input_state(1=中间片,10=结束片) / index / content_type / content_raw /
//      msg_id / stream_msg_id / msg_seq，50 QPS）
//   - 配额：server-inter/message/overview.html「单聊 60 分钟 / 4 次；群聊 5 分钟 / 5 次」
//   - 错误码 40034128「被动回复时间或次数超限」、40054005「同 msg_id+msg_seq 重复」
//
// host 增量能力核对结论（只读，未改主模块）：
//   protocol/ws.go:12  MsgTypeProgress      = "progress_structured"
//   protocol/ws.go:13  MsgTypeStreamContent = "stream_content"
//   agent/transport_channel_plugin.go:276  _ channel.ProgressSender = (*ChannelPluginTransport)(nil)
//   即 host **有**增量输出能力（SendProgress / SendStreamContent 会推到插件 stdin）。
//   本渠道按御前硬约束**主动丢弃**这两类帧（main.go handleLine 的两个 case），
//   所以不是"能力缺失"，是策略选择：一条入站 = 至多一条出站。
//   若将来要做单聊流式，入口就在那两个 case。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// frameHarness 构造带假 runtime（假 API）的 app，并暴露日志缓冲。
type frameHarness struct {
	app *app
	api *fakeMessageAPI
	out *bytes.Buffer
	enc *json.Encoder
	log *bytes.Buffer
}

func newFrameHarness(t *testing.T) *frameHarness {
	t.Helper()
	cfg := ParseConfig(map[string]string{
		KeyEnabled: "true", KeyAllow: "*", KeyAppID: "123456", KeyAppSecret: "s",
	})
	api := &fakeMessageAPI{}
	logBuf := &bytes.Buffer{}
	a := newApp(logBuf)
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, func(format string, args ...any) {
		logBuf.WriteString(format + "\n")
	})
	var out bytes.Buffer
	return &frameHarness{app: a, api: api, out: &out, enc: json.NewEncoder(&out), log: logBuf}
}

// pushFrame 直接喂一帧 host → plugin 的事件。
func (h *frameHarness) pushFrame(t *testing.T, raw string) {
	t.Helper()
	if stop := h.app.handleLine(context.Background(), h.enc, raw); stop {
		t.Fatalf("handleLine asked to stop on %s", raw)
	}
}

// rememberInbound 记一个锚点（模拟收到一条入站消息）。
func (h *frameHarness) rememberInbound(chatID, msgID string) {
	h.app.rt.RememberInbound(chatID, msgID)
}

// TestOneReplyPerInboundTurn 御前指定的核心验收：
//   - 3 条中间进度（thinking / tool / 增量流）+ 1 条最终答案 → 出站 API 调用 == 1
//   - 再做 2 轮问答（2 个 msg_id）→ 出站 == 2
func TestOneReplyPerInboundTurn(t *testing.T) {
	h := newFrameHarness(t)
	h.rememberInbound("USER-1", "MSG-1")

	for _, p := range []string{
		`{"type":"progress_structured","chat_id":"USER-1","progress":{"phase":"thinking","text":"正在思考"}}`,
		`{"type":"progress_structured","chat_id":"USER-1","progress":{"phase":"tool","text":"调用 shell"}}`,
		// 若 host 把文本塞在 content 里（不同实现细节），也绝不能当正文发出去。
		`{"type":"progress_structured","chat_id":"USER-1","content":"LEAKED-PROGRESS"}`,
		`{"type":"stream_content","chat_id":"USER-1","content":"增量片段"}`,
	} {
		h.pushFrame(t, p)
	}
	h.pushFrame(t, `{"type":"text","chat_id":"USER-1","content":"最终答案"}`)

	calls := h.api.Calls()
	if len(calls) != 1 {
		t.Fatalf("outbound API calls = %d, want exactly 1 (progress must never be sent): %+v", len(calls), calls)
	}
	if calls[0].content != "最终答案" {
		t.Fatalf("outbound content = %q, want 最终答案", calls[0].content)
	}
	for _, c := range calls {
		if strings.Contains(c.content, "LEAKED-PROGRESS") {
			t.Fatalf("progress payload leaked to QQ: %+v", c)
		}
	}

	// 第二轮：新锚点 → 再一条出站，总计 2。
	h.rememberInbound("USER-1", "MSG-2")
	h.pushFrame(t, `{"type":"progress_structured","chat_id":"USER-1","progress":{"phase":"thinking"}}`)
	h.pushFrame(t, `{"type":"text","chat_id":"USER-1","content":"第二轮答案"}`)

	calls = h.api.Calls()
	if len(calls) != 2 {
		t.Fatalf("outbound API calls = %d, want 2 (one per inbound turn)", len(calls))
	}
	if calls[1].msgID != "MSG-2" || calls[1].seq != 1 {
		t.Fatalf("2nd turn call = msgID:%q seq:%d, want MSG-2/1", calls[1].msgID, calls[1].seq)
	}
}

// TestGroupOneInboundOneOutbound 群聊一条入站 = 至多一条出站。
func TestGroupOneInboundOneOutbound(t *testing.T) {
	h := newFrameHarness(t)
	h.rememberInbound("group:GROUP-1", "MSG-G1")
	h.pushFrame(t, `{"type":"progress_structured","chat_id":"group:GROUP-1","progress":{"phase":"thinking"}}`)
	h.pushFrame(t, `{"type":"text","chat_id":"group:GROUP-1","content":"群里的一条回答"}`)

	calls := h.api.Calls()
	if len(calls) != 1 {
		t.Fatalf("group outbound calls = %d, want 1", len(calls))
	}
	if calls[0].kind != "group" || calls[0].target != "GROUP-1" {
		t.Fatalf("group routing = %s/%s", calls[0].kind, calls[0].target)
	}
}

// TestSenderQuotaExhaustedRefuses 单聊同一锚点第 5 次回复：拒发 + ERROR，出站停在 4。
func TestSenderQuotaExhaustedRefuses(t *testing.T) {
	h := newFrameHarness(t)
	h.rememberInbound("USER-1", "MSG-Q")
	for i := 0; i < 5; i++ {
		h.pushFrame(t, `{"type":"text","chat_id":"USER-1","content":"reply"}`)
	}

	calls := h.api.Calls()
	if len(calls) != 4 {
		t.Fatalf("outbound calls = %d, want 4 (单聊每锚点上限 4)", len(calls))
	}
	for i, c := range calls {
		if c.seq != uint32(i+1) {
			t.Fatalf("call %d seq = %d, want %d", i, c.seq, i+1)
		}
	}
	logs := h.log.String()
	if !strings.Contains(logs, "ERROR") || !strings.Contains(logs, "quota exhausted") {
		t.Fatalf("quota rejection must log ERROR, got: %s", logs)
	}
	if !strings.Contains(logs, "MSG-Q") {
		t.Fatalf("ERROR log must carry the anchor id: %s", logs)
	}
	if !strings.Contains(logs, "refusing to send") {
		t.Fatalf("ERROR log must say it refused to send: %s", logs)
	}
}

// TestSenderQuotaGroupLimit 群聊同一锚点第 6 次回复：拒发 + ERROR，出站停在 5。
func TestSenderQuotaGroupLimit(t *testing.T) {
	h := newFrameHarness(t)
	h.rememberInbound("group:G-1", "MSG-GQ")
	for i := 0; i < 6; i++ {
		h.pushFrame(t, `{"type":"text","chat_id":"group:G-1","content":"reply"}`)
	}
	if calls := h.api.Calls(); len(calls) != 5 {
		t.Fatalf("group outbound calls = %d, want 5 (群聊每锚点上限 5)", len(calls))
	}
	if !strings.Contains(h.log.String(), "quota exhausted") {
		t.Fatalf("group quota rejection must log ERROR: %s", h.log.String())
	}
}

// TestSenderQuotaPerAnchorIndependent 配额按锚点计：两个不同 msg_id 各发 4 次都成功。
func TestSenderQuotaPerAnchorIndependent(t *testing.T) {
	h := newFrameHarness(t)

	h.rememberInbound("USER-1", "MSG-P1")
	for i := 0; i < 4; i++ {
		h.pushFrame(t, `{"type":"text","chat_id":"USER-1","content":"a"}`)
	}
	h.rememberInbound("USER-1", "MSG-P2")
	for i := 0; i < 4; i++ {
		h.pushFrame(t, `{"type":"text","chat_id":"USER-1","content":"b"}`)
	}

	calls := h.api.Calls()
	if len(calls) != 8 {
		t.Fatalf("outbound calls = %d, want 8 (4 per anchor, quota is per msg_id)", len(calls))
	}
	if calls[3].msgID != "MSG-P1" || calls[3].seq != 4 {
		t.Fatalf("4th call on MSG-P1 = %+v", calls[3])
	}
	if calls[7].msgID != "MSG-P2" || calls[7].seq != 4 {
		t.Fatalf("4th call on MSG-P2 = %+v", calls[7])
	}
	if strings.Contains(h.log.String(), "quota exhausted") {
		t.Fatalf("no quota error expected when each anchor stays within 4: %s", h.log.String())
	}
}

// TestSenderQuotaChunkingGuard 长文本分 3 片但只剩 2 次配额 → 只发 2 片 + ERROR。
func TestSenderQuotaChunkingGuard(t *testing.T) {
	h := newFrameHarness(t)
	h.rememberInbound("USER-1", "MSG-CHUNK")
	for i := 0; i < 2; i++ {
		h.pushFrame(t, `{"type":"text","chat_id":"USER-1","content":"x"}`)
	}
	long := strings.Repeat("字", 3500) // 每片 1500 rune → 3 片
	raw, _ := json.Marshal(map[string]any{"type": "text", "chat_id": "USER-1", "content": long})
	h.pushFrame(t, string(raw))

	calls := h.api.Calls()
	if len(calls) != 4 {
		t.Fatalf("outbound calls = %d, want 4 (2 + 2 chunks; 3rd chunk must be dropped)", len(calls))
	}
	if calls[3].seq != 4 {
		t.Fatalf("last seq = %d, want 4 (never exceed the per-anchor limit)", calls[3].seq)
	}
	if !strings.Contains(h.log.String(), "would be exceeded by chunking") {
		t.Fatalf("chunk truncation must log ERROR: %s", h.log.String())
	}
}

// 保证 io 被使用（部分假件辅助可能引入/移除）。
var _ = io.Discard

// TestSenderQuotaRefusalAnswersRPC 配额耗尽时，若该帧是 RPC（有 id，如 channel_send），
// 必须**回答 error frame**；如果是异步 text 事件（无 id），只能记 ERROR 日志
// —— 两条路径行为一致：都不调用 API，都留下 ERROR 痕迹。
func TestSenderQuotaRefusalAnswersRPC(t *testing.T) {
	h := newFrameHarness(t)
	h.rememberInbound("USER-1", "MSG-RPC")

	// 先打满 4 次配额（走 RPC 路径，每次都应回 ok）。
	for i := 0; i < 4; i++ {
		h.pushFrame(t, `{"id":"srv-`+string(rune('1'+i))+`","method":"channel_send","params":{"chat_id":"USER-1","content":"r"}}`)
	}
	if calls := h.api.Calls(); len(calls) != 4 {
		t.Fatalf("outbound calls = %d, want 4", len(calls))
	}
	// 第 5 次：必须回 error frame（不是静默失败）。
	h.pushFrame(t, `{"id":"srv-5","method":"channel_send","params":{"chat_id":"USER-1","content":"r"}}`)
	if calls := h.api.Calls(); len(calls) != 4 {
		t.Fatalf("outbound calls = %d after quota exhaustion, want still 4", len(calls))
	}
	out := h.out.String()
	if !strings.Contains(out, `"id":"srv-5"`) || !strings.Contains(out, "quota exhausted") {
		t.Fatalf("5th RPC must be answered with an error frame, got: %s", out)
	}
	if !strings.Contains(h.log.String(), "ERROR") {
		t.Fatalf("quota rejection must log ERROR: %s", h.log.String())
	}
}
