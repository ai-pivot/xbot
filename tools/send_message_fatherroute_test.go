package tools

// send_message_fatherroute_test.go — SendMessage(to="father") 上级（父会话）路由。
//
// 语义（2026-11-03 派发单 C，与 to="user" 同构）：
//   - 落点是**调用方的直接父会话**（物理链路推导，不靠调用方自称）；
//   - 顶层会话（无 parent）必须**明确报错**，绝不静默降级成 IM 发送；
//   - 投递走 PeerMessageFn（busy→伪工具结果 / idle→user message），
//     不是用户看板卡片（不走 Structured / Silent）。
//
// 关于解析器：tools 不能 import agent（成环），所以这里注入的
// stubFatherResolver 是生产实现（agent.fatherParentKey = parseInteractiveKeyParent
// 薄封装 + role:instance 形状闸门）的**镜像**；真实解析由
// agent/father_session_test.go 的 TestFatherRouteParentKey 用生产函数钉住。
// 两处必须同口径 —— 改一边不改另一边，至少一侧会红。

import (
	"fmt"
	"strings"
	"testing"
)

type fatherDelivery struct {
	calls   int
	target  string
	message string
}

func (d *fatherDelivery) fn(targetSessionKey, message string) string {
	d.calls++
	d.target, d.message = targetSessionKey, message
	return "✅ queued for " + targetSessionKey
}

// stubFatherResolver 镜像 agent.fatherParentKey（见文件头注释）。
func stubFatherResolver(sessionKey string) (string, bool) {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return "", false
	}
	slash := strings.LastIndex(key, "/")
	if slash <= 0 || slash == len(key)-1 {
		return "", false
	}
	// 尾段必须是 "<role>:<instance>"：顶层 CLI 会话 "cli:/a/b/c" 不能被
	// 解成同级别人的 "cli:/a/b"。
	tail := key[slash+1:]
	if strings.Index(tail, ":") <= 0 {
		return "", false
	}
	colon := strings.Index(key, ":")
	if colon < 0 || colon >= slash {
		return "", false
	}
	channel, chatID := key[:colon], key[colon+1:slash]
	if channel == "" || chatID == "" {
		return "", false
	}
	return channel + ":" + chatID, true
}

// fatherCtx 构造一个子代理的工具上下文：CallerSessionKey 是它的物理会话 key，
// FatherSessionKeyFn / PeerMessageFn 都指向测试桩。
func fatherCtx(callerKey, sessionKey string, sink *fatherDelivery) *ToolContext {
	return &ToolContext{
		Channel:            "agent",
		ChatID:             callerKey,
		SessionKey:         sessionKey,
		CallerSessionKey:   callerKey,
		AgentID:            "main/proj.xbot.dev",
		OriginUserID:       "web-4",
		SenderID:           "main",
		AgentDepth:         2,
		FatherSessionKeyFn: stubFatherResolver,
		PeerMessageFn:      sink.fn,
	}
}

// ① 有 parent：消息投进父会话，target 是父会话 key，内容原样透传。
func TestSendMessageFatherRoute_ResolvesToParentSession(t *testing.T) {
	sink := &fatherDelivery{}
	ctx := fatherCtx("web:web-4/court.shoufu:main", "main/send_message", sink)

	tool := &SendMessageTool{}
	res, err := tool.Execute(ctx, `{"to":"father","message":"N1 后端闭环完成"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("PeerMessageFn calls = %d, want 1", sink.calls)
	}
	if sink.target != "web:web-4" {
		t.Fatalf("target = %q, want web:web-4 (the caller's parent session)", sink.target)
	}
	if sink.message != "N1 后端闭环完成" {
		t.Fatalf("message = %q, want verbatim passthrough", sink.message)
	}
	if res == nil || !strings.Contains(res.Detail+res.Summary, "web:web-4") {
		t.Fatalf("result = %v, want it to name the parent session", res)
	}
}

// ② 无 parent（顶层会话）：报错 + 错误原文 + 绝不静默降级（PeerMessageFn 零调用）。
func TestSendMessageFatherRoute_TopLevelSessionErrors(t *testing.T) {
	sink := &fatherDelivery{}
	// 顶层会话：CallerSessionKey 为空（引擎对主会话就是这么传的），
	// SessionKey 是主会话自带的 key —— 必须被当成"没有父会话"。
	ctx := fatherCtx("", "web:web-4", sink)

	tool := &SendMessageTool{}
	res, err := tool.Execute(ctx, `{"to":"father","message":"顶层不该有上级"}`)
	if err == nil {
		t.Fatalf("top-level session must NOT be able to message %q, got result %v", "father", res)
	}
	if !strings.Contains(err.Error(), "father route unavailable:") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "father route unavailable:")
	}
	if !strings.Contains(err.Error(), "has no parent session") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "has no parent session")
	}
	if !strings.Contains(err.Error(), "web:web-4") {
		t.Fatalf("error = %q, want it to name the caller key (web:web-4)", err.Error())
	}
	if sink.calls != 0 {
		t.Fatalf("PeerMessageFn calls = %d, want 0 (no silent downgrade to IM/session delivery)", sink.calls)
	}
}

// ③ 多层嵌套：target 必须是**直接父**，不是根会话。
func TestSendMessageFatherRoute_NestedResolvesDirectParent(t *testing.T) {
	sink := &fatherDelivery{}
	nested := "agent:web:web-4/court.shoufu:main/proj.xbot.dev:dev"
	ctx := fatherCtx(nested, "main/send_message", sink)

	tool := &SendMessageTool{}
	if _, err := tool.Execute(ctx, `{"to":"father","message":"孙代理上报"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("PeerMessageFn calls = %d, want 1", sink.calls)
	}
	if sink.target != "agent:web:web-4/court.shoufu:main" {
		t.Fatalf("target = %q, want agent:web:web-4/court.shoufu:main (the DIRECT parent, not the root web:web-4)", sink.target)
	}
	if sink.target == "web:web-4" {
		t.Fatal("father route must not collapse to the root session")
	}
}

// kind != text：显式报错（不静默丢 payload），并钉住这个选择。
func TestSendMessageFatherRoute_StructuredKindRejected(t *testing.T) {
	for _, kind := range []string{"report", "ask_user", "answer", "decision"} {
		sink := &fatherDelivery{}
		ctx := fatherCtx("web:web-4/court.shoufu:main", "main/send_message", sink)

		tool := &SendMessageTool{}
		_, err := tool.Execute(ctx, fmt.Sprintf(
			`{"to":"father","kind":%q,"message":"带 payload 的结构化消息","payload":"{\"summary\":\"x\"}"}`, kind))
		if err == nil {
			t.Fatalf("kind=%s: want an explicit error (payload must not be silently dropped)", kind)
		}
		if !strings.Contains(err.Error(), "plain conversation messages only") {
			t.Fatalf("kind=%s: error = %q, want it to explain father carries plain text only", kind, err.Error())
		}
		if sink.calls != 0 {
			t.Fatalf("kind=%s: PeerMessageFn calls = %d, want 0", kind, sink.calls)
		}
	}
}

// "father" 是严格等值（与 "user" 一致，大小写敏感）：别把 "Father"/"father:x"
// 当特殊路由 —— 它们没有父会话语义，落回常规地址分支。
func TestSendMessageFatherRoute_IsStrictEquality(t *testing.T) {
	for _, addr := range []string{"Father", "FATHER", "father:x", "fathere"} {
		sink := &fatherDelivery{}
		ctx := fatherCtx("web:web-4/court.shoufu:main", "main/send_message", sink)

		tool := &SendMessageTool{}
		_, err := tool.Execute(ctx, fmt.Sprintf(`{"to":%q,"message":"x"}`, addr))
		if err == nil {
			t.Fatalf("addr=%q: expected the generic IM path to fail in this context, got nil error", addr)
		}
		if sink.calls != 0 {
			t.Fatalf("addr=%q: father route must not fire for a non-exact address (PeerMessageFn calls = %d)", addr, sink.calls)
		}
	}
}
