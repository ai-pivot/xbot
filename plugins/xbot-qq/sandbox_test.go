package main

// 沙箱环境改造的验收用例（2026-10-26 真机定论）。
//
// 现场事实（同一 access_token、同一台机器、同一出口 IP 202.163.9.244）：
//
//	POST bots.qq.com/app/getAppAccessToken     → 200            凭据正确
//	GET  api.bot.qq.com/gateway             → 401 11298/40023002
//	GET  api.bot.qq.com/gateway                → 401 同上
//	GET  sandbox.api.sgroup.qq.com/gateway     → 200 {"url":"wss://sandbox..."}
//
// 所以：应用处于**沙箱态**，正式 OpenAPI 全拒。三个用例分别锁死
//   ① sandbox=true（含缺键默认）→ 沙箱域名
//   ② sandbox=false            → 正式域名
//   ③ 401 + 11298/40023002     → 致命，只请求一次，不重试、不 dial

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSandboxBaseURLFromConfig ① ②：配置 → 基地址/环境名/host 的一一对应。
func TestSandboxBaseURLFromConfig(t *testing.T) {
	cases := []struct {
		name     string
		raw      map[string]string
		wantEnv  string
		wantBase string
		wantHost string
	}{
		{
			name:     "缺键默认沙箱",
			raw:      nil,
			wantEnv:  "sandbox",
			wantBase: "https://sandbox.api.sgroup.qq.com",
			wantHost: "sandbox.api.sgroup.qq.com",
		},
		{
			name:     "显式开沙箱",
			raw:      map[string]string{KeySandbox: "true"},
			wantEnv:  "sandbox",
			wantBase: "https://sandbox.api.sgroup.qq.com",
			wantHost: "sandbox.api.sgroup.qq.com",
		},
		{
			name:     "显式关沙箱走正式",
			raw:      map[string]string{KeySandbox: "false"},
			wantEnv:  "prod",
			wantBase: "https://api.bot.qq.com",
			wantHost: "api.bot.qq.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ParseConfig(tc.raw)
			if got := EnvName(cfg.Sandbox); got != tc.wantEnv {
				t.Fatalf("EnvName(%v) = %q, want %q", cfg.Sandbox, got, tc.wantEnv)
			}
			if got := APIBase(cfg.Sandbox); got != tc.wantBase {
				t.Fatalf("APIBase(%v) = %q, want %q", cfg.Sandbox, got, tc.wantBase)
			}
			if got := APIBaseHost(cfg.Sandbox); got != tc.wantHost {
				t.Fatalf("APIBaseHost(%v) = %q, want %q", cfg.Sandbox, got, tc.wantHost)
			}
		})
	}
}

// TestSandboxWiresIntoAPIClient 光有 APIBase 不够：客户端与真实运行时必须接上。
// 这条专门防"函数算对了、接线没改"的假绿。
func TestSandboxWiresIntoAPIClient(t *testing.T) {
	for _, sandbox := range []bool{true, false} {
		api := NewAPIClientEnv(&stubTokens{}, sandbox)
		if api.baseURL != APIBase(sandbox) {
			t.Fatalf("NewAPIClientEnv(sandbox=%v).baseURL = %q, want %q",
				sandbox, api.baseURL, APIBase(sandbox))
		}
		if api.Env() != EnvName(sandbox) {
			t.Fatalf("Env() = %q, want %q", api.Env(), EnvName(sandbox))
		}
	}

	// 默认构造 = 沙箱（应用未发布时只有沙箱能连）。
	if api := NewAPIClient(&stubTokens{}); api.baseURL != apiBaseSandbox {
		t.Fatalf("NewAPIClient().baseURL = %q, want sandbox %q", api.baseURL, apiBaseSandbox)
	}

	// 生产运行时（newDefaultRuntime）必须按 cfg.Sandbox 选域名。
	for _, sandbox := range []bool{true, false} {
		rt := newDefaultRuntime(Config{Sandbox: sandbox}, nil)
		api, ok := rt.sender.api.(*APIClient)
		if !ok {
			t.Fatalf("newDefaultRuntime 的 sender.api 不是 *APIClient：%T", rt.sender.api)
		}
		if api.baseURL != APIBase(sandbox) {
			t.Fatalf("newDefaultRuntime(sandbox=%v) baseURL = %q, want %q",
				sandbox, api.baseURL, APIBase(sandbox))
		}
	}
}

// TestStartupLogPrintsEnv 启动日志必须打环境与 base：
// 上线忘切环境（沙箱→正式）时，日志是唯一能立刻看出来的地方。
func TestStartupLogPrintsEnv(t *testing.T) {
	cases := []struct {
		sandbox  string
		wantPart string
	}{
		{"true", "qq channel started: env=sandbox base=sandbox.api.sgroup.qq.com app_id=102030405"},
		{"false", "qq channel started: env=prod base=api.bot.qq.com app_id=102030405"},
	}

	for _, tc := range cases {
		t.Run("sandbox="+tc.sandbox, func(t *testing.T) {
			var logs bytes.Buffer
			a := newApp(&logs)
			a.newRuntime = func(cfg Config, logf func(string, ...any)) *channelRuntime {
				return newRuntime(cfg, runtimeDeps{
					tokens: &recordingTokens{},
					gatewayURL: func(context.Context) (string, error) {
						// 让网关立刻失败退出，测试不碰网络。
						return "", context.Canceled
					},
					dialer: &countingDialer{make: func(int) (WSConn, error) { return nil, context.Canceled }},
					api:    &fakeMessageAPI{},
					sleep:  func(context.Context, time.Duration) bool { return false },
				}, logf)
			}

			var out bytes.Buffer
			enc := json.NewEncoder(&out)
			inner := `{"enabled":"true","app_id":"102030405","app_secret":"s","allow":"*","sandbox":` +
				strconv.Quote(tc.sandbox) + `}`
			a.handleLine(context.Background(), enc,
				`{"type":"channel_config","metadata":{"config":`+strconv.Quote(inner)+`}}`)
			a.stopRuntime()

			if !strings.Contains(logs.String(), tc.wantPart) {
				t.Fatalf("启动日志缺环境信息，want contains %q, got:\n%s", tc.wantPart, logs.String())
			}
		})
	}
}

// TestGatewayStopsOnSandbox401 ③ 401 + 11298/40023002（沙箱态）必须致命：
// 只请求一次 /gateway，不 dial，不进重试循环。
//
// 对照实验：把 gateway_fatal.go 里 IsIPNotAllowed 那个 case 删掉，
// Reconnect.MaxAttempts=3 会让 /gateway 被调 4 次，本用例立刻变红。
func TestGatewayStopsOnSandbox401(t *testing.T) {
	const body = `{"message":"接口访问源IP不在白名单","code":11298,"err_code":40023002,"trace_id":"t-1"}`

	var gatewayCalls int
	sleeps := 0
	dialer := &countingDialer{make: func(int) (WSConn, error) {
		t.Error("致命错误路径不应尝试 dial")
		return nil, context.Canceled
	}}

	gw := NewGateway(&recordingTokens{}, func(context.Context) (string, error) {
		gatewayCalls++
		return "", NewAPIError("gateway", 401, body)
	}, dialer, nil, t.Logf)
	gw.cfg = GatewayConfig{
		// MaxAttempts 故意设成有限值：万一致命快停被删掉，测试是"红"而不是"挂死"。
		Reconnect: ReconnectPolicy{Min: time.Millisecond, Max: time.Millisecond, MaxAttempts: 3},
		Sleep: func(context.Context, time.Duration) bool {
			sleeps++
			return true
		},
	}

	err := gw.Run(context.Background())
	if err == nil {
		t.Fatal("Run = nil, want fatal error")
	}
	if gatewayCalls != 1 {
		t.Fatalf("/gateway 被调用 %d 次，want 1（致命错误必须停止重试）", gatewayCalls)
	}
	if sleeps != 0 {
		t.Fatalf("致命错误不应进入退避等待，实际 sleep %d 次", sleeps)
	}
	if dialer.Calls() != 0 {
		t.Fatalf("dial 次数 = %d, want 0", dialer.Calls())
	}
	// 提示必须指向沙箱（而不是让人去加 IP 白名单，那是解决不了这个错的）。
	if !strings.Contains(err.Error(), "沙箱") {
		t.Fatalf("错误提示未指向沙箱态：%v", err)
	}
	if !strings.Contains(err.Error(), "sandbox.api.sgroup.qq.com") {
		t.Fatalf("错误提示未给出沙箱域名：%v", err)
	}
}

// TestFatalGatewayReasonSandboxHint 纯函数层：11298/40023002 → (致命, 含沙箱提示)。
func TestFatalGatewayReasonSandboxHint(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		fatal  bool
		substr string
	}{
		{
			name:   "11298/40023002 → 沙箱提示",
			err:    NewAPIError("gateway", 401, `{"message":"接口访问源IP不在白名单","code":11298,"err_code":40023002}`),
			fatal:  true,
			substr: "沙箱",
		},
		{
			// 2026-10-27 修正：限流是**临时**错误，不再判致命 —— 交给外层退避重连
			// （限流最小重连间隔 60s，见 rateLimitReconnectDelay）。
			// 若再判 fatal，一次 100017 就会永久停机、必须人工 reload。
			name:   "限流 100017 → 非致命（退避重连）",
			err:    NewAPIError("gateway", 400, `{"message":"接口调用超过频率限制","code":100017,"err_code":40023001}`),
			fatal:  false,
			substr: "",
		},
		{
			name:   "普通 500 → 可重试",
			err:    NewAPIError("gateway", 500, `{"message":"internal error"}`),
			fatal:  false,
			substr: "",
		},
		{
			name:   "网络错误 → 可重试",
			err:    context.DeadlineExceeded,
			fatal:  false,
			substr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fatal, hint := FatalGatewayReason(tc.err)
			if fatal != tc.fatal {
				t.Fatalf("fatal = %v, want %v (hint=%q)", fatal, tc.fatal, hint)
			}
			if tc.substr != "" && !strings.Contains(hint, tc.substr) {
				t.Fatalf("hint = %q, want contains %q", hint, tc.substr)
			}
		})
	}
}
