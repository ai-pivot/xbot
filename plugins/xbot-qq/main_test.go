package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// decodeFrames 把 stdout 里的 JSON lines 解成 map 列表。
func decodeFrames(t *testing.T, out string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode output frame %q: %v", line, err)
		}
		frames = append(frames, m)
	}
	return frames
}

// eventPayload 造一条 C2C 事件 d。
func c2cPayload(openid, msgID, content string) string {
	return `{"id":"` + msgID + `","content":"` + content + `","author":{"user_openid":"` + openid + `"}}`
}

// eventPayload 造一条群 @ 事件 d。
func groupPayload(groupOpenID, memberOpenID, msgID, content string) string {
	return `{"id":"` + msgID + `","content":"` + content + `","group_openid":"` + groupOpenID +
		`","author":{"member_openid":"` + memberOpenID + `"}}`
}

// newTestApp 构造一个不落日志的 app。
func newTestApp() *app { return newApp(io.Discard) }

// TestActivateDeclaresQQChannel activate 必须声明 qq 渠道与官方五项配置键。
func TestActivateDeclaresQQChannel(t *testing.T) {
	a := newTestApp()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc, `{"id":"1","method":"activate"}`)

	frames := decodeFrames(t, out.String())
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	provider, ok := frames[0]["channel_provider"].(map[string]any)
	if !ok {
		t.Fatalf("no channel_provider in %v", frames[0])
	}
	if provider["name"] != ChannelName {
		t.Fatalf("channel name = %v, want %q", provider["name"], ChannelName)
	}
	schema := provider["config_schema"].([]any)
	keys := map[string]bool{}
	for _, item := range schema {
		keys[item.(map[string]any)["key"].(string)] = true
	}
	// 平台托管（qqbot-media://）之后，外部图床相关键全部删除。
	want := []string{KeyEnabled, KeyAppID, KeyAppSecret, KeySandbox, KeyAllow, KeyAdmins, KeyIntents, KeyMarkdown}
	for _, k := range want {
		if !keys[k] {
			t.Fatalf("schema missing key %q (got %v)", k, keys)
		}
	}
	if len(keys) != len(want) {
		t.Fatalf("schema has %d keys, want %d (got %v)", len(keys), len(want), keys)
	}
}

// TestChannelConfigDeclaresPrompt 每次配置推送都要重新声明渠道提示词。
func TestChannelConfigDeclaresPrompt(t *testing.T) {
	a := newTestApp()
	a.newRuntime = func(Config, func(string, ...any)) *channelRuntime {
		t.Fatal("disabled config must not start a runtime")
		return nil
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc,
		`{"type":"channel_config","metadata":{"config":"{\"enabled\":\"false\"}"}}`)

	frames := decodeFrames(t, out.String())
	if len(frames) != 1 || frames[0]["type"] != "channel_prompt" {
		t.Fatalf("frames = %v, want single channel_prompt", frames)
	}
	parts := frames[0]["system_parts"].(map[string]any)
	if parts[ChannelPromptKey] != ChannelPromptText() {
		t.Fatal("channel prompt content mismatch")
	}
}

// TestChannelConfigInvalidDoesNotStartRuntime 启用但缺 app_secret 时必须拒绝启动。
func TestChannelConfigInvalidDoesNotStartRuntime(t *testing.T) {
	a := newTestApp()
	a.newRuntime = func(Config, func(string, ...any)) *channelRuntime {
		t.Fatal("invalid config must not start a runtime")
		return nil
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc,
		`{"type":"channel_config","metadata":{"config":"{\"enabled\":\"true\",\"app_id\":\"102030405\",\"allow\":\"*\"}"}}`)

	if rt := a.runtime(); rt != nil {
		t.Fatal("runtime must stay nil when app_secret is missing")
	}
	if cfg := a.config(); !cfg.Enabled || cfg.AppSecret != "" {
		t.Fatalf("config = %+v", cfg)
	}
}

// TestChannelConfigStartsAndStopsRuntime 启用→启动；热更新为 disabled→同步停止。
func TestChannelConfigStartsAndStopsRuntime(t *testing.T) {
	conn := newFakeConn()
	dialer := &countingDialer{make: func(int) (WSConn, error) { return conn, nil }}
	deps := runtimeDeps{
		tokens:     &recordingTokens{},
		gatewayURL: func(context.Context) (string, error) { return "wss://example.invalid/ws", nil },
		dialer:     dialer,
		api:        &fakeMessageAPI{},
		reconnect:  ReconnectPolicy{Min: time.Millisecond, Max: time.Millisecond},
		sleep:      func(context.Context, time.Duration) bool { return false },
	}
	starts := 0
	a := newTestApp()
	a.newRuntime = func(cfg Config, logf func(string, ...any)) *channelRuntime {
		starts++
		return newRuntime(cfg, deps, logf)
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc,
		`{"type":"channel_config","metadata":{"config":"{\"enabled\":\"true\",\"app_id\":\"102030405\",\"app_secret\":\"s\",\"allow\":\"*\"}"}}`)
	if starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
	if a.runtime() == nil {
		t.Fatal("runtime should be running after valid enabled config")
	}
	// 等网关真的建立了连接，再验证 stop 会关掉它（否则可能停在 dial 之前）。
	waitFor(t, "gateway dial", func() bool { return dialer.Calls() >= 1 })

	a.handleLine(context.Background(), enc,
		`{"type":"channel_config","metadata":{"config":"{\"enabled\":\"false\"}"}}`)
	if a.runtime() != nil {
		t.Fatal("runtime should be stopped after disabled config")
	}
	select {
	case <-conn.closed:
	default:
		t.Fatal("gateway connection should be closed after stop")
	}
}

// TestNonAllowlistedMessageMakesZeroExternalCalls 白名单外的消息：
// 不产生 send_inbound，也不产生任何外部 API 调用，也不记锚点。
func TestNonAllowlistedMessageMakesZeroExternalCalls(t *testing.T) {
	api := &fakeMessageAPI{}
	cfg := ParseConfig(map[string]string{KeyAllow: "OPENID-ALLOWED", KeyAdmins: "OPENID-ALLOWED"})
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		Data: json.RawMessage(c2cPayload("OPENID-STRANGER", "m-1", "let me in")),
	})

	if out.Len() != 0 {
		t.Fatalf("rejected message must not produce send_inbound, got %s", out.String())
	}
	if calls := api.Calls(); len(calls) != 0 {
		t.Fatalf("external API calls = %d, want 0 for non-allowlisted sender", len(calls))
	}
	if anchor := a.rt.sender.passiveMsgID("OPENID-STRANGER"); anchor != "" {
		t.Fatalf("anchor = %q, want none for rejected sender", anchor)
	}
}

// TestAllowedMessageInjectsOpenIDIdentity 白名单内的消息：身份是 openid，
// admin 标记来自 admins 名单，且入站不产生外部 API 调用。
func TestAllowedMessageInjectsOpenIDIdentity(t *testing.T) {
	api := &fakeMessageAPI{}
	cfg := ParseConfig(map[string]string{KeyAllow: "OPENID-ALLOWED OPENID-ADMIN", KeyAdmins: "OPENID-ADMIN"})
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		Data: json.RawMessage(c2cPayload("OPENID-ADMIN", "m-1", "hello")),
	})

	frames := decodeFrames(t, out.String())
	if len(frames) != 1 || frames[0]["method"] != "send_inbound" {
		t.Fatalf("frames = %v, want one send_inbound", frames)
	}
	params := frames[0]["params"].(map[string]any)
	if params["channel"] != ChannelName {
		t.Fatalf("channel = %v", params["channel"])
	}
	if params["sender_id"] != "OPENID-ADMIN" {
		t.Fatalf("sender_id = %v, want openid", params["sender_id"])
	}
	if params["chat_id"] != "OPENID-ADMIN" || params["chat_type"] != ChatTypeP2P {
		t.Fatalf("chat = %v/%v", params["chat_id"], params["chat_type"])
	}
	if params["request_id"] != "m-1" {
		t.Fatalf("request_id = %v, want message id", params["request_id"])
	}
	meta := params["metadata"].(map[string]any)
	if meta[MetaKeyIsAdmin] != "true" {
		t.Fatalf("metadata.is_admin = %v, want true", meta[MetaKeyIsAdmin])
	}
	if calls := api.Calls(); len(calls) != 0 {
		t.Fatalf("inbound handling made %d external calls, want 0", len(calls))
	}
	if anchor := a.rt.sender.passiveMsgID("OPENID-ADMIN"); anchor != "m-1" {
		t.Fatalf("anchor = %q, want m-1", anchor)
	}
}

// TestGroupMessageUsesGroupChatID 群消息的 chat_id 是 group:<group_openid>。
func TestGroupMessageUsesGroupChatID(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "*"})
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: &fakeMessageAPI{}}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventGroupAtMessageCreate,
		Data: json.RawMessage(groupPayload("GROUP-OPENID", "MEMBER-OPENID", "m-2", "hi")),
	})

	params := decodeFrames(t, out.String())[0]["params"].(map[string]any)
	if params["chat_id"] != "group:GROUP-OPENID" || params["chat_type"] != ChatTypeGroup {
		t.Fatalf("chat = %v/%v", params["chat_id"], params["chat_type"])
	}
	if params["sender_id"] != "MEMBER-OPENID" {
		t.Fatalf("sender_id = %v", params["sender_id"])
	}
	if meta := params["metadata"].(map[string]any); meta[MetaKeyGroupID] != "GROUP-OPENID" {
		t.Fatalf("metadata.group_id = %v", meta[MetaKeyGroupID])
	}
}

// TestTextEventSendsViaAPI host 的 text 事件必须真的走发送器。
func TestTextEventSendsViaAPI(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "*"})
	api := &fakeMessageAPI{}
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc,
		`{"type":"text","chat_id":"group:GROUP-OPENID","content":"reply text"}`)

	calls := api.Calls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].kind, "group") || calls[0].target != "GROUP-OPENID" || calls[0].content != "reply text" {
		t.Fatalf("calls = %+v", calls)
	}
	if out.Len() != 0 {
		t.Fatalf("text event has no id, must not emit a response frame: %s", out.String())
	}
}

// TestChannelSendRPCRepliesOk 旧路径 channel_send 带 id，发送成功后要回 ok。
func TestChannelSendRPCRepliesOk(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "*"})
	api := &fakeMessageAPI{}
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: api}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc,
		`{"id":"srv-1","method":"channel_send","params":{"chat_id":"USER-OPENID","content":"hi"}}`)

	frames := decodeFrames(t, out.String())
	if len(frames) != 1 || frames[0]["id"] != "srv-1" || frames[0]["result"] != "ok" {
		t.Fatalf("frames = %v, want ok response", frames)
	}
	if calls := api.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0].kind, "c2c") {
		t.Fatalf("calls = %+v", calls)
	}
}

// TestOutboundWithoutRuntimeReturnsError 未连接时必须诚实报错，不能假装成功。
func TestOutboundWithoutRuntimeReturnsError(t *testing.T) {
	a := newTestApp()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc,
		`{"id":"srv-2","method":"channel_send","params":{"chat_id":"U","content":"hi"}}`)

	frames := decodeFrames(t, out.String())
	if len(frames) != 1 || frames[0]["id"] != "srv-2" {
		t.Fatalf("frames = %v", frames)
	}
	errMsg, _ := frames[0]["error"].(string)
	if !strings.Contains(errMsg, "not connected") {
		t.Fatalf("error = %q, want not connected", errMsg)
	}
}

// TestUnknownMethodGetsErrorResponse 未知 method 必须应答（否则 host 等到超时）。
func TestUnknownMethodGetsErrorResponse(t *testing.T) {
	a := newTestApp()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleLine(context.Background(), enc, `{"id":"7","method":"nope"}`)

	frames := decodeFrames(t, out.String())
	if len(frames) != 1 || frames[0]["id"] != "7" || frames[0]["error"] == nil {
		t.Fatalf("frames = %v, want error response", frames)
	}
}

// TestRunSurvivesBadFramesAndStopsOnDeactivate 坏帧不杀协议循环，deactivate 退出。
func TestRunSurvivesBadFramesAndStopsOnDeactivate(t *testing.T) {
	a := newTestApp()
	in := strings.NewReader("not json\n" +
		`{"id":"1","method":"activate"}` + "\n" +
		`{"id":"2","method":"deactivate"}` + "\n")

	var out bytes.Buffer
	if err := a.run(context.Background(), in, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	frames := decodeFrames(t, out.String())
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2 (activate + deactivate)", len(frames))
	}
	if frames[1]["id"] != "2" || frames[1]["result"] != "ok" {
		t.Fatalf("deactivate response = %v", frames[1])
	}
}

// TestGatewayEventParseErrorDoesNotEmit 报文缺 openid 时只记日志，不发 RPC。
func TestGatewayEventParseErrorDoesNotEmit(t *testing.T) {
	cfg := ParseConfig(map[string]string{KeyAllow: "*"})
	a := newTestApp()
	a.setConfig(cfg)
	a.rt = newRuntime(cfg, runtimeDeps{api: &fakeMessageAPI{}}, nil)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	a.handleGatewayEvent(enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		Data: json.RawMessage(`{"id":"m","content":"hi","author":{"id":"INTERNAL"}}`),
	})
	if out.Len() != 0 {
		t.Fatalf("malformed event must not emit send_inbound: %s", out.String())
	}
}
