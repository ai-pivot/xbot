package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// frameOut 是测试侧解出的 host 应答帧形状（只关心断言用到的字段）。
type frameOut struct {
	ID              string              `json:"id"`
	Result          json.RawMessage     `json:"result"`
	Error           string              `json:"error"`
	ChannelProvider *ChannelDeclaration `json:"channel_provider"`
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
	if len(frames) != 4 {
		t.Fatalf("got %d response frames, want 4 (raw=%q)", len(frames), out.String())
	}

	// 1) activate → channel_provider.name == qq
	if frames[0].ChannelProvider == nil || frames[0].ChannelProvider.Name != "qq" {
		t.Fatalf("activate response = %+v, want channel_provider.name=qq", frames[0])
	}

	// 2) channel_send → 必须诚实报错（阶段 1 没有连接，不许假成功）
	if frames[1].ID != "srv-1" || frames[1].Error == "" {
		t.Fatalf("channel_send response = %+v, want {id:srv-1 error:...}", frames[1])
	}

	// 3) 未知 method → 必须应答，否则 host 侧等到超时
	if frames[2].ID != "srv-2" || frames[2].Error == "" {
		t.Fatalf("unknown-method response = %+v, want {id:srv-2 error:...}", frames[2])
	}

	// 4) deactivate → 带同一个 id 的 ok
	if frames[3].ID != "srv-3" || string(frames[3].Result) != `"ok"` {
		t.Fatalf("deactivate response = %+v, want {id:srv-3 result:ok}", frames[3])
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
	a.handleChannelConfig(frame{
		Type:     "channel_config",
		Metadata: map[string]string{"config": `{"enabled":"true","ws_url":"ws://x:1","allow":"10001"}`},
	})
	if !a.config().Policy().ShouldHandle("10001") {
		t.Fatal("setup: config not applied")
	}

	a.handleChannelConfig(frame{
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
	a.handleChannelConfig(frame{
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
	a.handleChannelConfig(frame{
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
