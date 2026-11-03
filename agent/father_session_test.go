package agent

// father_session_test.go — SendMessage(to="father") 的父会话解析（生产实现）。
//
// tools 侧测试（tools/send_message_fatherroute_test.go）注入的是解析器的**镜像**
// （tools 不能 import agent，会成环）。这里是唯一用**生产函数** fatherParentKey
// 钉住语义的地方：一旦它和镜像漂移，至少一侧会红。

import (
	"context"
	"testing"

	"xbot/tools"
)

// TestFatherRouteParentKey —— 父会话解析的四种形态。
func TestFatherRouteParentKey(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		want    string
		wantOK  bool
		comment string
	}{
		{
			name: "top-level interactive session",
			key:  "web:web-4/court.shoufu:main",
			want: "web:web-4", wantOK: true,
			comment: "父是本会话所属的根会话",
		},
		{
			name: "nested (grandchild)",
			key:  "agent:web:web-4/court.shoufu:main/proj.xbot.dev:dev",
			want: "agent:web:web-4/court.shoufu:main", wantOK: true,
			comment: "LastIndex('/') 取最后一段 ⇒ 直接父，不是根 web:web-4",
		},
		{
			name: "root web session",
			key:  "web:web-4",
			want: "", wantOK: false,
			comment: "主会话没有父会话 —— 必须报错，不许降级",
		},
		{
			name: "root CLI session (absolute path with slashes)",
			key:  "cli:/home/smith/src/xbot",
			want: "", wantOK: false,
			comment: "关键反例：key 里有斜杠 ≠ 有父会话；否则会静默投给同级会话 cli:/home/smith/src",
		},
		{
			name: "interactive session under a CLI root",
			key:  "cli:/home/smith/src/xbot/proj.xbot.dev:dev",
			want: "cli:/home/smith/src/xbot", wantOK: true,
			comment: "chatID 本身可以含斜杠（绝对路径），尾段 role:instance 才是判据",
		},
		{name: "empty", key: "", want: "", wantOK: false},
		{name: "no slash", key: "garbage", want: "", wantOK: false},
		{name: "trailing slash", key: "web:web-4/", want: "", wantOK: false},
	}
	for _, c := range cases {
		got, ok := fatherParentKey(c.key)
		if ok != c.wantOK || got != c.want {
			t.Errorf("%s: fatherParentKey(%q) = (%q, %v), want (%q, %v) — %s",
				c.name, c.key, got, ok, c.want, c.wantOK, c.comment)
		}
	}
}

// TestSubAgentRunConfig_WiresFatherRoute —— 交互式子代理是 father 路由的**唯一实际
// 调用方**：buildSubAgentRunConfig 必须把解析器与投递通道都接到 ToolContext 上。
// 漏接时 tools 侧测试（注入桩）照样全绿，只有这里能发现 —— 同 BgTaskManager 前例
// （subagent_wiring_test.go）。
func TestSubAgentRunConfig_WiresFatherRoute(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
	}

	ctx := WithUserContext(context.Background(), &UserContext{})
	parentCtx := &tools.ToolContext{
		AgentID:  "main",
		Channel:  "web",
		ChatID:   "web-4",
		SenderID: "u1",
		UserID:   42,
		Role:     "admin",
	}

	cfg, err := a.buildSubAgentRunConfig(ctx, parentCtx, "task", "", nil,
		tools.SubAgentCapabilities{}, "proj.xbot.dev", true, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	// 子代理的物理会话 key 由 interactive spawn 填（interactive.go），
	// 这里模拟"已在子会话里执行工具"的状态。
	cfg.CallerSessionKey = "web:web-4/court.shoufu:main/proj.xbot.dev:dev"

	tc := buildToolContext(ctx, &cfg)
	if tc.FatherSessionKeyFn == nil {
		t.Fatal("SubAgent ToolContext.FatherSessionKeyFn must be wired (SendMessage(to=\"father\") depends on it)")
	}
	if tc.PeerMessageFn == nil {
		t.Fatal("SubAgent ToolContext.PeerMessageFn must be wired (father delivery goes through it — no IM fallback)")
	}
	parentKey, ok := tc.FatherSessionKeyFn(tc.CallerSessionKey)
	if !ok || parentKey != "web:web-4/court.shoufu:main" {
		t.Fatalf("FatherSessionKeyFn(%q) = (%q, %v), want (web:web-4/court.shoufu:main, true)",
			tc.CallerSessionKey, parentKey, ok)
	}
}
