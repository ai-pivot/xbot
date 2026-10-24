package serverapp

import (
	"strings"
	"testing"

	"xbot/tools"
)

// structured_callbacks_selfguard_test.go —— D-07 自激守卫的**端到端**用例。
//
// 这是唯一能证明 D-07 真的生效的测试。链路：
//
//	ToolContext.CallerSessionKey（引擎注入的物理会话 key）
//	  → tools/send_message.go caller.SessionKey
//	  → 本包 SetStructuredAppend 闭包：resolve 出目标物理 key 后与它比较
//	  → caller.SessionKey == 目标物理 key ⇒ 拒绝（否则消息落回自己会话、再拉起一轮 = 自激）
//
// 修复前的缺陷链：engine.go 的 ToolContext 构造没填 CallerSessionKey ⇒ 这里恒为空串
// ⇒ `caller.SessionKey != ""` 永假 ⇒ 守卫形同不存在。因此除了"命中要被拒"，
// 必须有"CallerSessionKey 为空时同一个目标拦不住"的反证，否则这条测试证明不了 D-07。
//
// 全部用 kind=ask_user：它不在 structuredDeliverable 里，只落库、不投递，
// 这样测试不会拉起真实 turn（避免把投递链路的不确定性混进守卫断言）。
func TestStructuredAppendRejectsSelfDelivery(t *testing.T) {
	ag := newTestAgentForCWD(t) // cwd_test.go 的临时库 agent（真实 MultiTenantSession）
	tools.SetStructuredAppend(nil)
	t.Cleanup(func() { tools.SetStructuredAppend(nil) })
	structuredCallbacks(ag) // 注册真实实现（副作用），返回值这里用不到

	// 调用方自己的物理会话 key（interactive 子代理：chatID 就是这段 key）。
	const ownSession = "web:web-4/team.data:main"
	ask := func(target string) string {
		return `{"to":"` + target + `","message":"汇总","kind":"ask_user","payload":"{\"questions\":[{\"id\":\"q\",\"text\":\"继续?\"}]}","correlation_id":"selfguard-1"}`
	}

	tool := &tools.SendMessageTool{}

	// ① 目标 == 调用方自己的物理会话 key：必须被拒，且根本走不到落库。
	ownCtx := &tools.ToolContext{
		Channel:          "web",
		ChatID:           "web-4",
		CallerSessionKey: ownSession, // 引擎注入（D-07 修的就是这一行）
		OriginUserID:     "web-4",
	}
	_, err := tool.Execute(ownCtx, ask("agent:"+ownSession))
	if err == nil {
		t.Fatal("posting to the caller's own session must be rejected (self-trigger loop)")
	}
	if !strings.Contains(err.Error(), "would re-trigger the caller") {
		t.Fatalf("want the D-07 self-trigger rejection, got %v", err)
	}

	// ② 反证：CallerSessionKey 为空（= D-07 修复前的真实状态）时，同一个目标**拦不住**，
	// 消息会被写进自己的会话 —— 这正是修复前的线上行为。
	blind := &tools.ToolContext{Channel: "web", ChatID: "web-4", OriginUserID: "web-4"}
	res, err := tool.Execute(blind, ask("agent:"+ownSession))
	if err != nil {
		t.Fatalf("with an empty CallerSessionKey the guard must NOT fire (that emptiness was the D-07 bug); got error %v", err)
	}
	if res == nil || !strings.Contains(res.Summary, "已提交") {
		t.Fatalf("expected the message to be stored when the caller key is unknown, got %+v", res)
	}

	// ③ 正路对照：目标不是自己的会话（另一个子代理的会话，同一顶层会话子树内）→
	// 不被守卫拦，正常落库。
	other := &tools.ToolContext{
		Channel:          "web",
		ChatID:           "web-4",
		CallerSessionKey: "web:web-4/team.backend:main",
		OriginUserID:     "web-4",
	}
	res, err = tool.Execute(other, ask("agent:"+ownSession))
	if err != nil {
		if strings.Contains(err.Error(), "would re-trigger the caller") {
			t.Fatalf("a different target must not trip the self-trigger guard: %v", err)
		}
		t.Fatalf("structured send to a legit target should succeed: %v", err)
	}
	if res == nil || !strings.Contains(res.Summary, "已提交") {
		t.Fatalf("expected stored message for a legit target, got %+v", res)
	}
}
