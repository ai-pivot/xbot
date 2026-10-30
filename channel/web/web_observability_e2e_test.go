package web

// 本文件是 G-09 / G-17 的「隔离实例」验收夹具：
// 用真实 http.Server（127.0.0.1 随机端口）+ 真实中间件链 + 真实 cookie 鉴权，
// 端到端跑一遍，把 100ms 档日志与 /api/rpc-stats 的原始 JSON 打进 go test 输出，
// 作为证据留档。跑法：
//
//	go test ./channel/web/ -run TestObservabilityIsolatedInstance -count=1 -v

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestObservabilityIsolatedInstance 在隔离实例上验证：
//  1. 10 个快请求不触发 100ms 档，1 个 130ms 请求触发且带节流标记；
//  2. /api/rpc-stats 返回按 method 的 count/p50/p99，且 get_session_usage_stats
//     与 get_pending_ask_user 各自独立成行（不是总耗时冒充）。
func TestObservabilityIsolatedInstance(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)

	// RPC 处理器：两个 method 故意给不同耗时量级，用于证明 method 维度不互相污染。
	rpcDelays := map[string]time.Duration{
		"get_session_usage_stats": 3 * time.Millisecond,
		"get_pending_ask_user":    15 * time.Millisecond,
	}
	wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		d, ok := rpcDelays[method]
		if !ok {
			d = time.Millisecond
		}
		time.Sleep(d)
		return json.RawMessage(`{"ok":true,"method":"` + method + `"}`), nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/register", wc.handleRegister)
	mux.HandleFunc("/api/auth/login", wc.handleLogin)
	mux.HandleFunc("/api/rpc", wc.authMiddleware(wc.handleRPC))
	mux.HandleFunc("/api/rpc-stats", wc.authMiddleware(wc.handleRPCStats))
	// 快接口：立即返回，用于「10 个快请求不得误触发 100ms 档」。
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pong")
	})
	// 慢接口：固定 130ms，落在 100–200ms 档内。
	mux.HandleFunc("/api/slow-130ms", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(130 * time.Millisecond)
		fmt.Fprint(w, "slow")
	})

	// 真实中间件链（与 Start() 里的一致）。
	server := httptest.NewServer(wc.requestTimingMiddleware(wc.securityHeadersMiddleware(mux)))
	defer server.Close()
	t.Logf("ISOLATED_INSTANCE=%s", server.URL)

	cookie := loginTestAdmin(t, server.URL)
	do := func(path string, body string) string {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+path, rdr)
		if err != nil {
			t.Fatalf("new request %s: %v", path, err)
		}
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body=%s", path, resp.StatusCode, b)
		}
		return string(b)
	}

	// ---- 1. 10 个快请求：100ms 档必须保持 0 ----
	// -race 构建会把墙钟放大到数百毫秒（实测 auth/DB 路径 650ms+），此时
	// 「快请求」在计时上不再快，断言无判别力，跳过；默认门禁覆盖该断言。
	for i := 0; i < 10; i++ {
		do("/api/ping", "{}")
	}
	if !raceEnabled {
		if n := wc.reqTiming.over100ms.Load(); n != 0 {
			t.Fatalf("10 fast requests bumped the 100ms bucket: %d", n)
		}
	}
	t.Logf("FAST_BURST over100ms=%d over200ms=%d (race=%v; want 0/0 when !race)",
		wc.reqTiming.over100ms.Load(), wc.reqTiming.over200ms.Load(), raceEnabled)

	// ---- 2. 1 个慢请求（130ms）：100ms 档必须被触发 ----
	do("/api/slow-130ms", "{}")
	if n := wc.reqTiming.over100ms.Load(); n < 1 {
		t.Fatalf("130ms request did not hit the 100ms bucket: %d", n)
	}
	if !raceEnabled {
		// 非 -race 下 130ms 必须留在 100ms 档，不得串到 200ms 档。
		if n := wc.reqTiming.over200ms.Load(); n != 0 {
			t.Fatalf("130ms request leaked into the 200ms bucket: %d", n)
		}
		if n := wc.reqTiming.over100ms.Load(); n != 1 {
			t.Fatalf("over100ms = %d, want 1", n)
		}
	}
	t.Logf("SLOW_CALL over100ms=%d over200ms=%d (want 1/0 when !race)", wc.reqTiming.over100ms.Load(), wc.reqTiming.over200ms.Load())

	// ---- 3. 两个 method 各打若干次，然后抓 /api/rpc-stats ----
	for i := 0; i < 5; i++ {
		do("/api/rpc", `{"method":"get_session_usage_stats","params":{}}`)
		do("/api/rpc", `{"method":"get_pending_ask_user","params":{}}`)
	}
	statsBody := do("/api/rpc-stats", "")
	// 原始 JSON 打进测试输出（证据要求：贴原始 json）。
	t.Logf("RPC_STATS_JSON=%s", statsBody)

	var envelope struct {
		Data struct {
			TotalSamples uint64              `json:"total_samples"`
			Methods      []rpcMethodSnapshot `json:"methods"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(statsBody), &envelope); err != nil {
		t.Fatalf("decode rpc-stats: %v body=%s", err, statsBody)
	}
	payload := envelope.Data
	byMethod := map[string]rpcMethodSnapshot{}
	for _, m := range payload.Methods {
		byMethod[m.Method] = m
	}
	usage, ok := byMethod["get_session_usage_stats"]
	if !ok {
		t.Fatalf("get_session_usage_stats missing from json: %s", statsBody)
	}
	pending, ok := byMethod["get_pending_ask_user"]
	if !ok {
		t.Fatalf("get_pending_ask_user missing from json: %s", statsBody)
	}
	if usage.N != 5 || pending.N != 5 {
		t.Fatalf("per-method n = usage:%d pending:%d, want 5/5", usage.N, pending.N)
	}
	if !raceEnabled {
		// usage(3ms) 与 pending(15ms) 必须能区分开：若退化成总耗时口径，两者会趋同。
		if usage.P99Ms >= 10 {
			t.Fatalf("get_session_usage_stats polluted: p99=%.3f (want <10ms)", usage.P99Ms)
		}
		if pending.P50Ms < 15 {
			t.Fatalf("get_pending_ask_user p50=%.3f (want >=15ms)", pending.P50Ms)
		}
		if pending.P50Ms <= usage.P99Ms*2 {
			t.Fatalf("methods not separable: pending p50=%.3f usage p99=%.3f", pending.P50Ms, usage.P99Ms)
		}
	}
	t.Logf("METHOD_ROWS usage n=%d p50=%.3f p99=%.3f | pending n=%d p50=%.3f p99=%.3f",
		usage.N, usage.P50Ms, usage.P99Ms, pending.N, pending.P50Ms, pending.P99Ms)
}
