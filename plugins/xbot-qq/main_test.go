package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// frameOut 是测试侧解出的帧形状，覆盖两个方向：
//   - host → plugin 的应答（activate / RPC 应答）
//   - plugin → host 的声明与入站消息（channel_prompt / send_inbound）
type frameOut struct {
	ID              string              `json:"id"`
	Result          json.RawMessage     `json:"result"`
	Error           string              `json:"error"`
	ChannelProvider *ChannelDeclaration `json:"channel_provider"`
	Type            string              `json:"type"`
	SystemParts     map[string]string   `json:"system_parts"`
	Method          string              `json:"method"`
	Params          map[string]any      `json:"params"`
}

// TestActivateDeclaresQQChannel 验证渠道注册声明：名字、配置键集合、类型合法。
func TestActivateDeclaresQQChannel(t *testing.T) {
	var out bytes.Buffer
	a := newApp(&bytes.Buffer{})
	a.handleActivate(json.NewEncoder(&out))

	var resp struct {
		Result          string             `json:"result"`
		ChannelProvider ChannelDeclaration `json:"channel_provider"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal activate response: %v (raw=%q)", err, out.String())
	}
	if resp.Result != "ok" {
		t.Fatalf("result = %q, want ok", resp.Result)
	}
	if resp.ChannelProvider.Name != "qq" {
		t.Fatalf("channel name = %q, want qq", resp.ChannelProvider.Name)
	}

	// 内置渠道名不可占用。
	switch resp.ChannelProvider.Name {
	case "feishu", "web", "cli":
		t.Fatalf("channel name %q collides with a built-in channel", resp.ChannelProvider.Name)
	}

	// 配置键必须齐全且不重复，类型必须是 host 认识的。
	validTypes := map[string]bool{
		"text": true, "number": true, "select": true, "toggle": true,
		"textarea": true, "combo": true, "password": true,
	}
	wantKeys := []string{KeyEnabled, KeyWSURL, KeyAccessToken, KeyAllow, KeyAdmins}
	seen := map[string]bool{}
	for _, s := range resp.ChannelProvider.ConfigSchema {
		if seen[s.Key] {
			t.Fatalf("duplicate config key %q", s.Key)
		}
		seen[s.Key] = true
		if !validTypes[s.Type] {
			t.Fatalf("config key %q has unknown type %q", s.Key, s.Type)
		}
		if s.Label == "" {
			t.Fatalf("config key %q has no label (UI panel would show a blank row)", s.Key)
		}
	}
	for _, k := range wantKeys {
		if !seen[k] {
			t.Fatalf("config schema is missing key %q", k)
		}
	}
	if len(resp.ChannelProvider.ConfigSchema) != len(wantKeys) {
		t.Fatalf("config schema has %d keys, want exactly %d",
			len(resp.ChannelProvider.ConfigSchema), len(wantKeys))
	}
}

// TestRunProtocolRoundTrip 驱动完整协议循环：activate → channel_config →
// channel_send → 未知 method → deactivate。deactivate 之后 run 必须返回。
//
// 期望 5 帧输出：activate 应答、channel_prompt 声明、channel_send 错误、
// 未知 method 错误、deactivate ok。
func TestRunProtocolRoundTrip(t *testing.T) {
	cfgJSON := `{"enabled":"true","ws_url":"ws://127.0.0.1:3001","allow":"*","admins":"10001"}`
	lines := []string{
		`{"method":"activate","params":{"pluginId":"xbot.qq"}}`,
		`{"type":"channel_config","metadata":{"config":` + jsonString(cfgJSON) + `}}`,
		`{"id":"srv-1","method":"channel_send","params":{"chat_id":"group:1","content":"hi"}}`,
		`{"id":"srv-2","method":"totally_unknown"}`,
		`{"id":"srv-3","method":"deactivate"}`,
	}

	var out, logs bytes.Buffer
	a := newApp(&logs)
	if err := a.run(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	frames := decodeFrames(t, out.String())
	if len(frames) != 5 {
		t.Fatalf("got %d frames, want 5 (raw=%q)", len(frames), out.String())
	}

	// 1) activate → channel_provider.name == qq
	if frames[0].ChannelProvider == nil || frames[0].ChannelProvider.Name != "qq" {
		t.Fatalf("frame[0] = %+v, want activate response with channel_provider.name=qq", frames[0])
	}

	// 2) channel_config → 立即声明渠道提示词
	if frames[1].Type != "channel_prompt" {
		t.Fatalf("frame[1].type = %q, want channel_prompt", frames[1].Type)
	}
	if frames[1].SystemParts[ChannelPromptKey] == "" {
		t.Fatalf("frame[1].system_parts[%q] is empty", ChannelPromptKey)
	}

	// 3) channel_send → 必须诚实报错（阶段 2 没有 OneBot 连接，不许假成功）
	if frames[2].ID != "srv-1" || frames[2].Error == "" {
		t.Fatalf("frame[2] = %+v, want {id:srv-1 error:...}", frames[2])
	}

	// 4) 未知 method → 必须应答，否则 host 侧等到超时
	if frames[3].ID != "srv-2" || frames[3].Error == "" {
		t.Fatalf("frame[3] = %+v, want {id:srv-2 error:...}", frames[3])
	}

	// 5) deactivate → 带同一个 id 的 ok
	if frames[4].ID != "srv-3" || string(frames[4].Result) != `"ok"` {
		t.Fatalf("frame[4] = %+v, want {id:srv-3 result:ok}", frames[4])
	}

	// 配置已生效并接进 Policy。
	cfg := a.config()
	if !cfg.Enabled || cfg.WSURL != "ws://127.0.0.1:3001" {
		t.Fatalf("config after channel_config = %s, want enabled with the pushed URL", cfg.Summary())
	}
	p := cfg.Policy()
	if !p.ShouldHandle("anybody") {
		t.Fatal(`allow="*" must admit everyone after config is applied`)
	}
	if !p.IsAdmin("10001") || p.IsAdmin("10002") {
		t.Fatal("admins from channel_config not wired into policy")
	}
}

// TestEmitInboundSendsParams 驱动真实的发送路径，并打印两条合成入站帧的
// params JSON（admin / 非 admin）—— 阶段 2 验收要求的实证。
func TestEmitInboundSendsParams(t *testing.T) {
	a := newApp(&bytes.Buffer{})
	a.setConfig(ParseConfig(map[string]string{
		KeyEnabled: "true",
		KeyWSURL:   "ws://127.0.0.1:3001",
		KeyAllow:   "*",
		KeyAdmins:  "10001",
	}))

	var out bytes.Buffer
	enc := json.NewEncoder(&out)

	d1 := a.emitInbound(enc, adminIdentity(), "你好", "req-admin")
	d2 := a.emitInbound(enc, memberIdentity(), "在吗", "req-member")

	if !d1.Handle || !d1.Admin {
		t.Fatalf("admin decision = %+v, want {Handle:true Admin:true}", d1)
	}
	if !d2.Handle || d2.Admin {
		t.Fatalf("member decision = %+v, want {Handle:true Admin:false}", d2)
	}

	frames := decodeFrames(t, out.String())
	if len(frames) != 2 {
		t.Fatalf("got %d send_inbound frames, want 2 (raw=%q)", len(frames), out.String())
	}
	for i, f := range frames {
		if f.Method != "send_inbound" {
			t.Fatalf("frame[%d].method = %q, want send_inbound", i, f.Method)
		}
		if !strings.HasPrefix(f.ID, "plugin-") {
			t.Fatalf("frame[%d].id = %q, want plugin-N", i, f.ID)
		}
		pretty, _ := json.MarshalIndent(f.Params, "", "  ")
		t.Logf("frame[%d] %s params:\n%s", i, f.ID, pretty)
	}

	// 校验 admin / 非 admin 两条帧的关键差异。
	adminMeta := frames[0].Params["metadata"].(map[string]any)
	memberMeta := frames[1].Params["metadata"].(map[string]any)
	if adminMeta[MetaKeyIsAdmin] != "true" {
		t.Fatalf("admin frame metadata[%s] = %v, want \"true\"", MetaKeyIsAdmin, adminMeta[MetaKeyIsAdmin])
	}
	if memberMeta[MetaKeyIsAdmin] != "false" {
		t.Fatalf("member frame metadata[%s] = %v, want \"false\"", MetaKeyIsAdmin, memberMeta[MetaKeyIsAdmin])
	}
	if frames[0].Params["chat_id"] != "group:20002" || frames[0].Params["sender_id"] != "10001" {
		t.Fatalf("admin frame identity mismatch: %v", frames[0].Params)
	}
	if frames[1].Params["sender_id"] != "30003" {
		t.Fatalf("member frame sender_id = %v, want 30003", frames[1].Params["sender_id"])
	}
}

// TestEmitInboundDropsNonAllowlisted 白名单外的消息不发帧（只记日志）。
func TestEmitInboundDropsNonAllowlisted(t *testing.T) {
	var logs bytes.Buffer
	a := newApp(&logs)
	a.setConfig(ParseConfig(map[string]string{
		KeyEnabled: "true",
		KeyWSURL:   "ws://127.0.0.1:3001",
		KeyAllow:   "10001",
		KeyAdmins:  "10001",
	}))

	var out bytes.Buffer
	enc := json.NewEncoder(&out)

	intruder := Identity{SenderID: "99999", SenderName: "陌生人", GroupID: "20002"}
	d := a.emitInbound(enc, intruder, "hi", "req-x")
	if d.Handle {
		t.Fatal("non-allowlisted sender must not be admitted")
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("rejected message must not produce any frame, got %q", out.String())
	}
	if !strings.Contains(logs.String(), "not admitted") {
		t.Fatalf("expected a drop log line, got %q", logs.String())
	}
}

// TestRunBadFramesDoNotKillLoop 坏帧只记日志，协议循环必须活下去。
func TestRunBadFramesDoNotKillLoop(t *testing.T) {
	lines := []string{
		`not json at all`,
		``,
		`{"method":"activate"}`,
		`{"method":"deactivate"}`,
	}
	var out, logs bytes.Buffer
	a := newApp(&logs)
	if err := a.run(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("run returned error on malformed input: %v", err)
	}
	if !strings.Contains(logs.String(), "malformed frame") {
		t.Fatalf("expected a malformed-frame log line, got %q", logs.String())
	}
	if !strings.Contains(out.String(), "channel_provider") {
		t.Fatalf("loop died before handling activate; output=%q", out.String())
	}
}

// TestChannelConfigUnparsableKeepsPrevious 配置 JSON 解析失败时不得清空已有配置。
func TestChannelConfigUnparsableKeepsPrevious(t *testing.T) {
	a := newApp(&bytes.Buffer{})
	enc := json.NewEncoder(&bytes.Buffer{})
	a.handleChannelConfig(enc, frame{
		Type:     "channel_config",
		Metadata: map[string]string{"config": `{"enabled":"true","ws_url":"ws://x:1","allow":"10001"}`},
	})
	if !a.config().Policy().ShouldHandle("10001") {
		t.Fatal("setup: config not applied")
	}

	a.handleChannelConfig(enc, frame{
		Type:     "channel_config",
		Metadata: map[string]string{"config": `{ this is not json`},
	})
	if !a.config().Policy().ShouldHandle("10001") {
		t.Fatal("unparsable config wiped the previous (working) config")
	}
}

// TestChannelConfigEnabledWithoutURLKeepsConfig 启用但没填 ws_url：配置要留存
// （用户可能只是还没填完），同时日志里必须留下拒绝启用的理由。
func TestChannelConfigEnabledWithoutURLKeepsConfig(t *testing.T) {
	var logs bytes.Buffer
	a := newApp(&logs)
	a.handleChannelConfig(json.NewEncoder(&bytes.Buffer{}), frame{
		Type:     "channel_config",
		Metadata: map[string]string{"config": `{"enabled":"true","allow":"*"}`},
	})
	cfg := a.config()
	if !cfg.Enabled {
		t.Fatal("config was dropped instead of stored")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate = nil, want an error for enabled-without-url")
	}
	if !strings.Contains(logs.String(), KeyWSURL) {
		t.Fatalf("expected the log to name the missing key %q, got %q", KeyWSURL, logs.String())
	}
}

// TestChannelConfigEmptyAllowWarns 启用 + 空白名单 = 谁都不处理，日志必须点明。
func TestChannelConfigEmptyAllowWarns(t *testing.T) {
	var logs bytes.Buffer
	a := newApp(&logs)
	a.handleChannelConfig(json.NewEncoder(&bytes.Buffer{}), frame{
		Type:     "channel_config",
		Metadata: map[string]string{"config": `{"enabled":"true","ws_url":"ws://127.0.0.1:3001"}`},
	})
	if !strings.Contains(logs.String(), KeyAllow) {
		t.Fatalf("expected a warning about the empty %q list, got %q", KeyAllow, logs.String())
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

// jsonString 把字符串编码成 JSON 字符串字面量（用于拼 channel_config 帧）。
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// decodeFrames 把 stdout 的 JSON lines 解成 frameOut 列表。
func decodeFrames(t *testing.T, out string) []frameOut {
	t.Helper()
	var frames []frameOut
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var f frameOut
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("decode frame %q: %v", line, err)
		}
		frames = append(frames, f)
	}
	return frames
}
