package tools

// send_message_origin_fallback_test.go — 2026-11-04 P0
// 「我批的是首辅的 report，批复却发给了御前」的回归。
//
// 现象：首辅用 SendMessage(to="user", kind="report") 提卡片，卡片落库的
// `from.session` 记成了**御前根会话** `web:web-4`，于是用户批复（decision）
// 按 origin_session 回流到御前，而不是发起 report 的首辅。
//
// 根因分两层（第一层修完端到端复测仍然错，才挖出第二层）：
//   ① cfg.CallerSessionKey 只在 spawn 路径（interactive.go）设置，restore 路径漏了；
//   ② **更关键**：子代理的 turn 有两条执行路径 ——
//      SendToInteractiveSession（用交互会话快照，带 CallerSessionKey）与
//      会话队列/processMessage（通知注入、看板回流唤醒，走 buildMainRunConfig，
//      **根本不设 CallerSessionKey**，AgentDepth=0）。
//      实测那次走的就是 ②，所以只修 ① 无效。
//
// 修复后约定：CallerSessionKey 缺失时，从本次 Run 自己还原子代理会话
// （ctx.SessionKey="agent:<origin>/<role>:<inst>" 或 agent 通道的 ctx.ChatID），
// 返回值必须是不带 "agent:" 前缀的 interactive key —— 消费端
// serverapp.splitPhysicalSessionKey 依赖该形态开 ("agent", "web:web-4/role:inst") 租户。

import (
	"testing"
)

// TestUserBoardOrigin_QueuePathSubAgentSession 锁住**第二层**修复（真实故障路径）：
// 走通用会话队列的 Run 没有 CallerSessionKey、AgentDepth=0，
// 回流目标仍必须是发起者自己的会话，不是御前根会话。
func TestUserBoardOrigin_QueuePathSubAgentSession(t *testing.T) {
	got := stubUserNotice(t, 501)

	ctx := &ToolContext{
		// ② 会话队列路径的真实形态（processMessage(channel="agent", chatID=key)）
		Channel:          "agent",
		ChatID:           "web:web-4/court.shoufu:main",
		SessionKey:       "agent:web:web-4/court.shoufu:main",
		RootSessionKey:   "agent:web:web-4/court.shoufu:main",
		AgentDepth:       0, // 通用路径没有深度
		AgentID:          "",
		CallerSessionKey: "",
		OriginUserID:     "web-4",
		SenderID:         "agent",
	}

	tool := &SendMessageTool{}
	if _, err := tool.Execute(ctx,
		`{"to":"user","kind":"report","message":"计划已调整","idempotency_key":"t-1"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.calls != 1 {
		t.Fatalf("user notice calls = %d, want 1", got.calls)
	}
	// 落点仍是用户可见会话
	if got.channel != "web" || got.chatID != "web-4" {
		t.Fatalf("target = %s:%s, want web:web-4", got.channel, got.chatID)
	}
	// 回流目标 = 发起者自己的会话（不带 agent: 前缀，供
	// serverapp.splitPhysicalSessionKey 解析成 ("agent", key)）
	const want = "web:web-4/court.shoufu:main"
	if got.in.OriginSession != want {
		t.Fatalf("origin session = %q, want %q "+
			"(批复必须回到发起 report 的子代理，而不是用户根会话)", got.in.OriginSession, want)
	}
}

// TestUserBoardOrigin_UsesCallerSessionKey 锁住**第①层**（SendToInteractiveSession
// 路径）：CallerSessionKey 有值时逐字用它，不能被下面的推断覆盖。
func TestUserBoardOrigin_UsesCallerSessionKey(t *testing.T) {
	got := stubUserNotice(t, 503)

	ctx := &ToolContext{
		Channel: "web", ChatID: "web-4", RootSessionKey: "web:web-4",
		SessionKey:       "web:web-4/court.shoufu:main",
		CallerSessionKey: "web:web-4/court.shoufu:main",
		AgentDepth:       1,
		OriginUserID:     "web-4",
	}
	tool := &SendMessageTool{}
	if _, err := tool.Execute(ctx,
		`{"to":"user","kind":"report","message":"x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.in.OriginSession != "web:web-4/court.shoufu:main" {
		t.Fatalf("origin session = %q, want the caller's session key verbatim", got.in.OriginSession)
	}
}

// TestUserBoardOrigin_MainSessionStillUsesRoot：主会话没有自己的 agent 会话，
// 回流目标仍是用户根会话 —— 推断不能误伤这条路径。
func TestUserBoardOrigin_MainSessionStillUsesRoot(t *testing.T) {
	got := stubUserNotice(t, 502)

	ctx := &ToolContext{
		Channel: "web", ChatID: "web-4", AgentDepth: 0,
		AgentID: "main", OriginUserID: "web-4",
		// 主会话这三个都可能为空
	}
	tool := &SendMessageTool{}
	if _, err := tool.Execute(ctx,
		`{"to":"user","kind":"report","message":"主会话汇报"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.in.OriginSession != "web:web-4" {
		t.Fatalf("origin session = %q, want web:web-4 (main session reports back to itself)",
			got.in.OriginSession)
	}
}

// TestInteractiveOriginSession_DoesNotMisreadCLIPaths：CLI 会话的 chatID 是文件
// 路径（含 "/"），不得被当成 interactive key —— 否则回落目标会变成一个
// 不存在（而会被 GetOrCreateSession 造出来）的畸形租户。
func TestInteractiveOriginSession_DoesNotMisreadCLIPaths(t *testing.T) {
	cases := []struct {
		name string
		ctx  *ToolContext
		want string
	}{
		{"web main", &ToolContext{Channel: "web", ChatID: "web-4"}, ""},
		{"cli path", &ToolContext{
			Channel: "cli", ChatID: "/home/smith/src/xbot-exp",
			RootSessionKey: "cli:/home/smith/src/xbot-exp",
		}, ""},
		{"agent queue path", &ToolContext{
			Channel: "agent", ChatID: "web:web-4/court.shoufu:main",
			SessionKey: "agent:web:web-4/court.shoufu:main",
		}, "web:web-4/court.shoufu:main"},
		{"agent path via SessionKey only", &ToolContext{
			Channel: "agent", ChatID: "web-4", // ChatID 不携带 "/"
			SessionKey: "agent:web:web-4/court.shoufu:main",
		}, "web:web-4/court.shoufu:main"},
		{"nil ctx", nil, ""},
	}
	for _, c := range cases {
		if got := interactiveOriginSession(c.ctx); got != c.want {
			t.Fatalf("%s: interactiveOriginSession = %q, want %q", c.name, got, c.want)
		}
	}
}
