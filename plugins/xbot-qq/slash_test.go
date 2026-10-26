package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestClassifyCommand 覆盖斜杠/bang 的识别、风险分类、别名改写与边界解析。
func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		content   string
		ok        bool
		name      string
		category  string
		dangerous bool
		known     bool
		forward   string
	}{
		// 正常命令
		{"/compress", true, "/compress", RiskCompression, false, true, "/compress"},
		{"/help", true, "/help", RiskInfo, false, true, "/help"},
		{"/rm -rf /tmp/x", true, "/rm", RiskUnknown, true, false, "/rm -rf /tmp/x"},

		// 大小写：命令名小写化，正文（转发）保留原始大小写
		{"/RESTART", true, "/restart", RiskUnknown, true, false, "/RESTART"},
		{"/Compress", true, "/compress", RiskCompression, false, true, "/Compress"},

		// 首尾空白被裁掉
		{"  /status  ", true, "/status", RiskUnknown, true, false, "/status"},
		{"\t/reset\n", true, "/reset", RiskSession, false, true, "/new"},

		// 别名改写保留参数与参数内部空白
		{"/compact", true, "/compact", RiskCompression, false, true, "/compress"},
		{"/compact 现在压一下", true, "/compact", RiskCompression, false, true, "/compress 现在压一下"},
		{"/reset", true, "/reset", RiskSession, false, true, "/new"},
		{"/reset 干净点", true, "/reset", RiskSession, false, true, "/new 干净点"},
		{"/quit", true, "/quit", RiskSession, false, true, "/stop"},

		// 参数分割：第一段是命令名，后续原样保留（含多个空格）
		{"/echo  a b", true, "/echo", RiskUnknown, true, false, "/echo  a b"},
		{"/set-llm foo api_key=bar", true, "/set-llm", RiskCredentials, true, true, "/set-llm foo api_key=bar"},
		{"/set-llm\tfoo", true, "/set-llm", RiskCredentials, true, true, "/set-llm\tfoo"},

		// 只有前缀：当成未知命令（不是普通消息，也不会被转发）
		{"/", true, "/", RiskUnknown, true, false, "/"},

		// bang：整体交给 host 的 bangCmd，按最危险处理
		{"!ls -la", true, "!", RiskDestructive, true, true, "!ls -la"},

		// 不是命令
		{"hello /compress", false, "", "", false, false, ""},
		{"", false, "", "", false, false, ""},
		{"   ", false, "", "", false, false, ""},
		{"／全角", false, "", "", false, false, ""},
	}
	for _, tc := range cases {
		cmd, ok := ClassifyCommand(tc.content)
		if ok != tc.ok {
			t.Fatalf("ClassifyCommand(%q) ok = %t, want %t", tc.content, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if cmd.Name != tc.name || cmd.Category != tc.category || cmd.Dangerous != tc.dangerous ||
			cmd.Known != tc.known || cmd.Forward != tc.forward {
			t.Fatalf("ClassifyCommand(%q) = %+v, want name=%q category=%q dangerous=%t known=%t forward=%q",
				tc.content, cmd, tc.name, tc.category, tc.dangerous, tc.known, tc.forward)
		}
	}
}

// TestKnownCommandsMatchHostRegistry 命令表里的每一项都必须是"host 真的注册了"
// 或"被本插件映射到已注册命令"——否则放行就等于把未知命令喂给模型。
func TestKnownCommandsMatchHostRegistry(t *testing.T) {
	// 只读核对自 agent/command_builtin.go:569-600 与 agent/agent.go:1555。
	hostCommands := map[string]bool{
		"/new": true, "/version": true, "/help": true, "/prompt": true,
		"/set-llm": true, "/unset-llm": true, "/llm": true, "/llms": true,
		"/compress": true, "/continue": true, "/usage": true, "/context": true,
		"/models": true, "/set-model": true, "/settings": true, "/plugin": true,
		"/goal": true, "/info": true, "/export": true,
		"/cancel": true, "/stop": true,
	}
	for name, spec := range knownCommands {
		target := name
		if spec.Alias != "" {
			target = spec.Alias
		}
		if !hostCommands[target] {
			t.Fatalf("knownCommands[%q] → %q is not registered by host; it would fall through to the LLM",
				name, target)
		}
	}
}

// TestRefusalTextDistinguishesReasons 三种拒绝理由必须不同：
// 白名单问题不能回"你不是管理员"；未知命令要说清"被拦截、没送模型"。
func TestRefusalTextDistinguishesReasons(t *testing.T) {
	notAdmin := refusalText("!compress", RefuseNotAdmin)
	notAllowed := refusalText("!compress", RefuseNotAllowlisted)
	unknown := refusalText("!echo", RefuseUnknown)

	if !strings.Contains(notAdmin, "需要管理员权限") {
		t.Fatalf("not_admin text = %q", notAdmin)
	}
	// 非 admin 不许看到命令清单（泄漏面）。
	if strings.Contains(notAdmin, "可用命令") {
		t.Fatalf("not_admin text leaks command list: %q", notAdmin)
	}
	if !strings.Contains(notAllowed, "白名单") || !strings.Contains(notAllowed, "管理员身份不豁免") {
		t.Fatalf("not_allowlisted text = %q, want whitelist reason", notAllowed)
	}
	if !strings.Contains(unknown, "未知命令") || !strings.Contains(unknown, "!echo") {
		t.Fatalf("unknown text = %q, want user form !echo", unknown)
	}
	if !strings.Contains(unknown, "可用命令") || !strings.Contains(unknown, "!compress") {
		t.Fatalf("unknown text should list allowed commands in ! form, got %q", unknown)
	}
	if strings.Contains(unknown, "/compress") {
		t.Fatalf("unknown text must not use host-internal / names: %q", unknown)
	}
}

// gateHarness 构造一个带假运行时的 app（拒绝回复会走假 API，可计数）。
type gateHarness struct {
	app *app
	api *fakeMessageAPI
	out *bytes.Buffer
	enc *json.Encoder
}

func newGateHarness(t *testing.T, allow, admins string) *gateHarness {
	t.Helper()
	cfg := ParseConfig(map[string]string{
		KeyAllow:  allow,
		KeyAdmins: admins,
	})
	api := &fakeMessageAPI{}
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, nil)
	var out bytes.Buffer
	return &gateHarness{app: a, api: api, out: &out, enc: json.NewEncoder(&out)}
}

// send 喂一条 C2C 消息进网关事件处理。
func (h *gateHarness) send(t *testing.T, openid, content string) {
	t.Helper()
	h.app.handleGatewayEvent(h.enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		Data: json.RawMessage(c2cPayload(openid, "MSG-1", content)),
	})
}

// sendGroup 喂一条群 @ 消息进网关事件处理。
func (h *gateHarness) sendGroup(t *testing.T, groupOpenID, memberOpenID, content string) {
	t.Helper()
	h.app.handleGatewayEvent(h.enc, context.Background(), GatewayEvent{
		Type: EventGroupAtMessageCreate,
		Data: json.RawMessage(groupPayload(groupOpenID, memberOpenID, "MSG-G", content)),
	})
}

// TestNonAdminSlashRejected 非 admin 的危险/压缩/会话命令：
// 不产生 send_inbound（= host 不会执行、不压缩、不写状态），只回一条拒绝。
func TestNonAdminSlashRejected(t *testing.T) {
	cases := []string{
		"/compress", "/reset", "/quit", "/compact", "/new",
		"/set-llm foo api_key=bar", "/settings", "/rm -rf /tmp/x", "/restart",
	}
	for _, content := range cases {
		h := newGateHarness(t, "*", "OTHER-ADMIN")
		h.send(t, "NORMAL-USER", content)

		if h.out.Len() != 0 {
			t.Fatalf("%q: must not produce send_inbound (host would execute it), got %s", content, h.out.String())
		}
		calls := h.api.Calls()
		if len(calls) != 1 {
			t.Fatalf("%q: refusal API calls = %d, want exactly 1 (only the refusal)", content, len(calls))
		}
		if !strings.Contains(calls[0].content, "需要管理员权限") {
			t.Fatalf("%q: refusal = %q", content, calls[0].content)
		}
		// 非 admin 不该知道有哪些命令：文案里不许出现命令清单。
		if strings.Contains(calls[0].content, "可用命令") {
			t.Fatalf("%q: non-admin refusal leaks command list: %q", content, calls[0].content)
		}
		if calls[0].kind != "c2c" || calls[0].target != "NORMAL-USER" {
			t.Fatalf("%q: refusal routed to %s/%s", content, calls[0].kind, calls[0].target)
		}
		if calls[0].msgID != "MSG-1" {
			t.Fatalf("%q: refusal should be passive (msg_id=MSG-1), got %q", content, calls[0].msgID)
		}
	}
}

// TestAdminSlashAllowed admin 的同一批命令必须放行到 host 命令通道；
// 渠道别名要改写成 host 认识的名字（这是"命令真的会执行"的唯一可见证据）。
func TestAdminSlashAllowed(t *testing.T) {
	cases := []struct{ content, forwarded string }{
		{"/compress", "/compress"},
		{"/compact", "/compress"},
		{"/reset", "/new"},
		{"/quit", "/stop"},
		{"/set-llm foo api_key=bar", "/set-llm foo api_key=bar"},
		{"/settings", "/settings"},
	}
	for _, tc := range cases {
		h := newGateHarness(t, "*", "ADMIN-OPENID")
		h.send(t, "ADMIN-OPENID", tc.content)

		frames := decodeFrames(t, h.out.String())
		if len(frames) != 1 || frames[0]["method"] != "send_inbound" {
			t.Fatalf("%q: frames = %v, want one send_inbound", tc.content, frames)
		}
		params := frames[0]["params"].(map[string]any)
		if params["content"] != tc.forwarded {
			t.Fatalf("%q: forwarded content = %v, want %q", tc.content, params["content"], tc.forwarded)
		}
		if params["sender_id"] != "ADMIN-OPENID" {
			t.Fatalf("%q: sender = %v", tc.content, params["sender_id"])
		}
		if params["chat_id"] != "ADMIN-OPENID" {
			t.Fatalf("%q: chat_id = %v", tc.content, params["chat_id"])
		}
		if meta := params["metadata"].(map[string]any); meta[MetaKeyIsAdmin] != "true" {
			t.Fatalf("%q: metadata.is_admin = %v", tc.content, meta[MetaKeyIsAdmin])
		}
		if calls := h.api.Calls(); len(calls) != 0 {
			t.Fatalf("%q: admin command must not send a refusal, got %+v", tc.content, calls)
		}
	}
}

// TestUnknownSlashNeverForwarded 未知命令无论 admin 与否都不许转发 ——
// host 的 commands.Match 匹配不到会落进普通消息路径，那就是一次模型调用。
//
// 优先级（2026-10-26 修正）：非 admin 先被拒（只说"需要管理员权限"，不泄漏清单）；
// 只有 admin 才拿到"未知命令 + 可用命令"。
func TestUnknownSlashNeverForwarded(t *testing.T) {
	// 2026-10-26 用户令：admin 的 slash 命令「包括危险命令」都要能用。
	// 因此：
	//   非 admin 的 /未知 → 拒绝（不泄漏命令清单）
	//   admin   的 /未知 → 等价于 "!未知" 交给 host 执行（/ls == !ls）
	for _, content := range []string{"/echo a b", "/foo", "/ls"} {
		np := newGateHarness(t, "*", "")
		np.send(t, "NORMAL-USER", content)
		if np.out.Len() != 0 {
			t.Fatalf("%q: non-admin unknown command must not reach host, got %s", content, np.out.String())
		}
		if calls := np.api.Calls(); len(calls) != 1 || !strings.Contains(calls[0].content, "需要管理员权限") {
			t.Fatalf("%q: non-admin refusal = %+v", content, calls)
		} else if strings.Contains(calls[0].content, "可用命令") {
			t.Fatalf("%q: non-admin refusal leaks command list: %q", content, calls[0].content)
		}

		ap := newGateHarness(t, "*", "ADMIN-OPENID")
		ap.send(t, "ADMIN-OPENID", content)
		out := ap.out.String()
		if out == "" {
			t.Fatalf("%q: admin slash 应转发为 bang，实际没有 send_inbound", content)
		}
		if !strings.Contains(out, "send_inbound") {
			t.Fatalf("%q: admin slash 应产生 send_inbound，实际=%s", content, out)
		}
		wantBang := "!" + strings.TrimSpace(strings.TrimPrefix(content, "/"))
		if !strings.Contains(out, wantBang) {
			t.Fatalf("%q: admin slash 应转成 %q，实际帧=%s", content, wantBang, out)
		}
	}
}

func TestAdminNotAllowlistedGetsWhitelistReason(t *testing.T) {
	h := newGateHarness(t, "OTHER-USER", "ADMIN-OPENID")
	h.send(t, "ADMIN-OPENID", "/compress")

	if h.out.Len() != 0 {
		t.Fatalf("admin-not-allowlisted must not produce send_inbound, got %s", h.out.String())
	}
	calls := h.api.Calls()
	if len(calls) != 1 {
		t.Fatalf("refusal API calls = %d, want 1", len(calls))
	}
	got := calls[0].content
	if !strings.Contains(got, "白名单") {
		t.Fatalf("refusal = %q, want whitelist reason", got)
	}
	if strings.Contains(got, "仅管理员") {
		t.Fatalf("refusal = %q, must not claim 'not admin'", got)
	}
}

// TestNonAdminBangRejected bang 命令（host 会直接在 sandbox 执行）同样只允许 admin。
func TestNonAdminBangRejected(t *testing.T) {
	h := newGateHarness(t, "*", "")
	h.send(t, "NORMAL-USER", "!rm -rf /tmp/x")
	if h.out.Len() != 0 {
		t.Fatalf("bang from non-admin must not produce send_inbound: %s", h.out.String())
	}
	if calls := h.api.Calls(); len(calls) != 1 || !strings.Contains(calls[0].content, "需要管理员权限") {
		t.Fatalf("bang refusal calls = %+v", calls)
	} else if !strings.Contains(calls[0].content, "!rm") || strings.Contains(calls[0].content, "/rm") {
		// 面向用户必须是 "!rm" 形态，不能回显 host 内部名 "/rm"。
		t.Fatalf("bang refusal must echo user form !rm, got %q", calls[0].content)
	}

	admin := newGateHarness(t, "*", "ADMIN-OPENID")
	admin.send(t, "ADMIN-OPENID", "!ls")
	frames := decodeFrames(t, admin.out.String())
	if len(frames) != 1 || frames[0]["params"].(map[string]any)["content"] != "!ls" {
		t.Fatalf("admin bang frames = %v", frames)
	}
}

// TestGroupSlashAdminByOpenIDNotGroupMembership 群里的 admin 判定只依据
// 发送者 member_openid 与配置里的 admins —— 群 openid 本身、群成员身份都不算。
func TestGroupSlashAdminByOpenIDNotGroupMembership(t *testing.T) {
	// 群 openid 恰好写在 admins 里：群成员发命令仍然不是 admin。
	h := newGateHarness(t, "*", "GROUP-OPENID")
	h.sendGroup(t, "GROUP-OPENID", "MEMBER-OPENID", "/compress")
	if h.out.Len() != 0 {
		t.Fatalf("group openid in admins must not make members admin: %s", h.out.String())
	}
	calls := h.api.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].content, "需要管理员权限") {
		t.Fatalf("group refusal calls = %+v", calls)
	}
	if calls[0].kind != "group" || calls[0].target != "GROUP-OPENID" {
		t.Fatalf("group refusal routed to %s/%s", calls[0].kind, calls[0].target)
	}

	// member_openid 在 admins 里：放行，且会话是 group:<群 openid>。
	ok := newGateHarness(t, "*", "MEMBER-OPENID")
	ok.sendGroup(t, "GROUP-OPENID", "MEMBER-OPENID", "/compact")
	frames := decodeFrames(t, ok.out.String())
	if len(frames) != 1 || frames[0]["method"] != "send_inbound" {
		t.Fatalf("admin group slash frames = %v", frames)
	}
	params := frames[0]["params"].(map[string]any)
	if params["content"] != "/compress" || params["sender_id"] != "MEMBER-OPENID" ||
		params["chat_id"] != "group:GROUP-OPENID" || params["chat_type"] != "group" {
		t.Fatalf("admin group slash params = %v", params)
	}
}

// TestNonSlashNonAllowlistedStillZeroCalls 普通（非命令）非白名单消息仍然
// 不产生任何外部 API 调用 —— 门禁只为命令回复，不改变这条纪律。
func TestNonSlashNonAllowlistedStillZeroCalls(t *testing.T) {
	h := newGateHarness(t, "SOMEONE-ELSE", "")
	h.send(t, "STRANGER", "hello")
	if h.out.Len() != 0 {
		t.Fatalf("ordinary message must not produce send_inbound: %s", h.out.String())
	}
	if calls := h.api.Calls(); len(calls) != 0 {
		t.Fatalf("ordinary message must not call API, got %+v", calls)
	}
}
