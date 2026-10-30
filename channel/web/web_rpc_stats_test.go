package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	logrus "github.com/sirupsen/logrus"
)

// benchRPCMethods 是工部压测脚本 (bench.py RPC_MIX) 里的 method 集合。
// 打点必须对每个都给出 n/p50/p95/p99 —— 少一个就是可观测性缺口。
var benchRPCMethods = []string{
	"get_pending_ask_user",
	"web_plugin_list",
	"get_tools_settings",
	"list_web_users",
	"get_system_info",
	"get_agent_session_dump",
}

// TestRPCStatsPercentileFormula 校验分位数与 bench.py 的 pct() 同口径
// （线性插值，索引 (n-1)*p/100）。
func TestRPCStatsPercentileFormula(t *testing.T) {
	vals := make([]int64, 0, 100)
	for i := int64(1); i <= 100; i++ {
		vals = append(vals, i)
	}
	cases := []struct {
		p    float64
		want float64 // 微秒（线性插值，与 bench.py 同口径）
	}{
		{50, 50.5},  // k=49.5 → 50 + 0.5*(51-50)
		{95, 95.05}, // k=94.05 → 95 + 0.05*(96-95)
		{99, 99.01}, // k=98.01 → 99 + 0.01*(100-99)
	}
	for _, c := range cases {
		if got := percentileMicros(vals, c.p); got != c.want {
			t.Fatalf("p%.0f = %v, want %v", c.p, got, c.want)
		}
	}
	if got := percentileMicros(nil, 50); got != 0 {
		t.Fatalf("empty percentile = %v, want 0", got)
	}
	if got := percentileMicros([]int64{42}, 99); got != 42 {
		t.Fatalf("single-sample percentile = %v, want 42", got)
	}
}

// TestRPCStatsObserveAndSnapshot 覆盖 n / errors / p50 / p95 / p99 / max。
func TestRPCStatsObserveAndSnapshot(t *testing.T) {
	var s rpcStats
	for i := 0; i < 100; i++ {
		s.Observe("slow_method", time.Duration(i+1)*time.Microsecond, http.StatusOK)
	}
	s.Observe("broken_method", 5*time.Millisecond, http.StatusInternalServerError)

	snaps, total := s.snapshotAll(false)
	if total != 101 {
		t.Fatalf("total = %d, want 101", total)
	}
	if len(snaps) != 2 {
		t.Fatalf("methods = %d, want 2", len(snaps))
	}
	// snapshotAll 按 method 名排序。
	if snaps[0].Method != "broken_method" || snaps[1].Method != "slow_method" {
		t.Fatalf("unexpected order: %s,%s", snaps[0].Method, snaps[1].Method)
	}
	slow := snaps[1]
	if slow.N != 100 || slow.Samples != 100 {
		t.Fatalf("n/samples = %d/%d, want 100/100", slow.N, slow.Samples)
	}
	if slow.P50Ms != 0.0505 || slow.P95Ms != 0.09505 || slow.P99Ms != 0.09901 || slow.MaxMs != 0.1 {
		t.Fatalf("percentiles = p50=%.5f p95=%.5f p99=%.5f max=%.5f, want 0.0505/0.09505/0.09901/0.1",
			slow.P50Ms, slow.P95Ms, slow.P99Ms, slow.MaxMs)
	}
	broken := snaps[0]
	if broken.Errors != 1 {
		t.Fatalf("errors = %d, want 1", broken.Errors)
	}
}

// TestRPCStatsRingWraps 校验环形窗口只保留最近 rpcStatRingSize 个样本，
// 但累计 n 不丢 —— 否则长时间运行后 n 失真。
func TestRPCStatsRingWraps(t *testing.T) {
	var s rpcStats
	// 先灌满窗口：全是 10ms。
	for i := 0; i < rpcStatRingSize; i++ {
		s.Observe("m", 10*time.Millisecond, http.StatusOK)
	}
	// 再灌 rpcStatRingSize 个 1ms，旧样本应被全部挤出。
	for i := 0; i < rpcStatRingSize; i++ {
		s.Observe("m", 1*time.Millisecond, http.StatusOK)
	}
	snaps, total := s.snapshotAll(false)
	if total != 2*rpcStatRingSize {
		t.Fatalf("total = %d, want %d", total, 2*rpcStatRingSize)
	}
	got := snaps[0]
	if got.N != uint64(2*rpcStatRingSize) {
		t.Fatalf("N = %d, want %d", got.N, 2*rpcStatRingSize)
	}
	if got.Samples != rpcStatRingSize {
		t.Fatalf("samples = %d, want %d (ring cap)", got.Samples, rpcStatRingSize)
	}
	if got.P50Ms != 1 {
		t.Fatalf("p50 = %.3f ms, want 1.000 (old samples evicted)", got.P50Ms)
	}
}

// TestRPCStatsConcurrentObserve 保证打点路径并发安全（-race 下跑）。
func TestRPCStatsConcurrentObserve(t *testing.T) {
	var s rpcStats
	var wg sync.WaitGroup
	const workers, perWorker = 16, 200
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				s.Observe(benchRPCMethods[(w+i)%len(benchRPCMethods)], time.Duration(i+1)*time.Microsecond, http.StatusOK)
			}
		}(w)
	}
	wg.Wait()
	snaps, total := s.snapshotAll(false)
	if int(total) != workers*perWorker {
		t.Fatalf("total = %d, want %d", total, workers*perWorker)
	}
	if len(snaps) != len(benchRPCMethods) {
		t.Fatalf("methods = %d, want %d", len(snaps), len(benchRPCMethods))
	}
	for _, snap := range snaps {
		if snap.N == 0 || snap.Samples == 0 {
			t.Fatalf("method %s has no samples", snap.Method)
		}
	}
}

// TestHandleRPCEmitsPerMethodStats 是核心验收测试：走真实 handleRPC 入口，
// 7 类 method 各自产生一条独立的分位数统计（证明打点在唯一咽喉点上）。
func TestHandleRPCEmitsPerMethodStats(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)

	delays := map[string]time.Duration{}
	for i, m := range benchRPCMethods {
		delays[m] = time.Duration(i+1) * time.Millisecond
	}
	wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		time.Sleep(delays[method])
		return json.RawMessage(`{"ok":true}`), nil
	}

	for _, m := range benchRPCMethods {
		body := []byte(fmt.Sprintf(`{"method":%q,"params":{}}`, m))
		rec := httptest.NewRecorder()
		wc.handleRPC(rec, authedAPIRequestFor(http.MethodPost, "/api/rpc", body, "web-2", 2))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body=%s", m, rec.Code, rec.Body.String())
		}
	}

	snaps, _ := wc.rpcObs.stats.snapshotAll(false)
	if len(snaps) != len(benchRPCMethods) {
		t.Fatalf("methods = %d, want %d: %+v", len(snaps), len(benchRPCMethods), snaps)
	}
	for _, snap := range snaps {
		if snap.N != 1 {
			t.Fatalf("%s: n = %d, want 1", snap.Method, snap.N)
		}
		want := delays[snap.Method].Seconds() * 1000
		if snap.P99Ms < want {
			t.Fatalf("%s: p99 = %.3fms, want >= %.3fms", snap.Method, snap.P99Ms, want)
		}
	}
}

// TestHandleRPCStatsEndpoint 校验对外接口形态：每个 method 都有
// n / p50_ms / p95_ms / p99_ms 字段，且非法请求归入 "(invalid)" 而不是丢失。
func TestHandleRPCStatsEndpoint(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}

	for _, m := range benchRPCMethods {
		body := []byte(fmt.Sprintf(`{"method":%q,"params":{}}`, m))
		wc.handleRPC(httptest.NewRecorder(), authedAPIRequestFor(http.MethodPost, "/api/rpc", body, "web-2", 2))
	}
	// 非法请求体（缺 method）也必须被计入，不能静默丢弃。
	wc.handleRPC(httptest.NewRecorder(), authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{}`), "web-2", 2))

	rec := httptest.NewRecorder()
	wc.handleRPCStats(rec, authedAPIRequestFor(http.MethodGet, "/api/rpc-stats", nil, "web-2", 2))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var data struct {
		TotalSamples uint64              `json:"total_samples"`
		Methods      []rpcMethodSnapshot `json:"methods"`
	}
	decodeAPIData(t, rec.Body, &data)
	if data.TotalSamples != uint64(len(benchRPCMethods)+1) {
		t.Fatalf("total_samples = %d, want %d", data.TotalSamples, len(benchRPCMethods)+1)
	}
	if len(data.Methods) != len(benchRPCMethods)+1 {
		t.Fatalf("methods = %d, want %d", len(data.Methods), len(benchRPCMethods)+1)
	}
	foundInvalid := false
	for _, m := range data.Methods {
		if m.Method == "(invalid)" {
			foundInvalid = true
			continue
		}
		if m.N != 1 {
			t.Fatalf("%s: n = %d, want 1", m.Method, m.N)
		}
		if m.P50Ms <= 0 || m.P99Ms <= 0 {
			t.Fatalf("%s: percentiles not populated: %+v", m.Method, m)
		}
	}
	if !foundInvalid {
		t.Fatal("malformed request was not recorded as (invalid)")
	}
}

// TestRPCStatsReportLogsEachMethod 校验日志路径：一次 Report 为每个有样本的
// method 打一行，且行内带 method 字段（验收要求「贴一条含 method 的真实日志行」）。
func TestRPCStatsReportLogsEachMethod(t *testing.T) {
	var s rpcStats
	for i, m := range benchRPCMethods {
		s.Observe(m, time.Duration(i+1)*time.Millisecond, http.StatusOK)
	}

	var buf strings.Builder
	prevOut := logrus.StandardLogger().Out
	prevFmt := logrus.StandardLogger().Formatter
	logrus.SetOutput(&buf)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	t.Cleanup(func() {
		logrus.SetOutput(prevOut)
		logrus.SetFormatter(prevFmt)
	})

	reported := s.Report("test")
	if reported != len(benchRPCMethods) {
		t.Fatalf("reported = %d, want %d", reported, len(benchRPCMethods))
	}
	out := buf.String()
	for _, m := range benchRPCMethods {
		if !strings.Contains(out, "method="+m) {
			t.Fatalf("log missing method %q; got:\n%s", m, out)
		}
	}
	if strings.Count(out, "rpc latency by method") != len(benchRPCMethods) {
		t.Fatalf("want %d log lines, got:\n%s", len(benchRPCMethods), out)
	}
	// 第二次 Report：没有新样本 → 不打印（避免空闲刷屏）。
	buf.Reset()
	if got := s.Report("test"); got != 0 {
		t.Fatalf("second report = %d, want 0", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("second report should be silent, got: %s", buf.String())
	}
}

// TestRPCStatsMethodIsolation 是「method 维度不是总耗时冒充」的判据：
// 两个耗时量级不同的 method 各打 20 次，快 method 的分位数不得被慢 method
// 污染（若是「总耗时冒充」，fast 的 p99 会被 25ms 的样本拉起来）。
func TestRPCStatsMethodIsolation(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)

	delays := map[string]time.Duration{
		"fast_method": time.Millisecond,
		"slow_method": 25 * time.Millisecond,
	}
	wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		time.Sleep(delays[method])
		return json.RawMessage(`{"ok":true}`), nil
	}

	// 交错发送，确保两条时间线在同一次压测里互相穿插。
	for i := 0; i < 20; i++ {
		for _, m := range []string{"fast_method", "slow_method"} {
			body := []byte(fmt.Sprintf(`{"method":%q,"params":{}}`, m))
			rec := httptest.NewRecorder()
			wc.handleRPC(rec, authedAPIRequestFor(http.MethodPost, "/api/rpc", body, "web-2", 2))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", m, rec.Code)
			}
		}
	}

	snaps, total := wc.rpcObs.stats.snapshotAll(false)
	if total != 40 {
		t.Fatalf("total = %d, want 40", total)
	}
	byMethod := map[string]rpcMethodSnapshot{}
	for _, s := range snaps {
		byMethod[s.Method] = s
	}
	fast, ok := byMethod["fast_method"]
	if !ok {
		t.Fatalf("fast_method missing: %+v", snaps)
	}
	slow, ok := byMethod["slow_method"]
	if !ok {
		t.Fatalf("slow_method missing: %+v", snaps)
	}
	if fast.N != 20 || slow.N != 20 {
		t.Fatalf("n = fast:%d slow:%d, want 20/20 (counts must be per-method)", fast.N, slow.N)
	}
	// 快 method 的每一条样本都不该看到慢 method 的耗时。
	if fast.P99Ms >= 10 || fast.MaxMs >= 10 {
		t.Fatalf("fast_method polluted by slow samples: p99=%.3f max=%.3f", fast.P99Ms, fast.MaxMs)
	}
	if slow.P50Ms < 25 || slow.P99Ms < 25 {
		t.Fatalf("slow_method percentiles too low: p50=%.3f p99=%.3f", slow.P50Ms, slow.P99Ms)
	}
	if slow.P50Ms < fast.P99Ms*2 {
		t.Fatalf("methods are not separable: slow p50=%.3f vs fast p99=%.3f", slow.P50Ms, fast.P99Ms)
	}
}

// TestRPCStatsMethodCardinalityBounded 验证无界增长防护：method 名是客户端
// 可控字符串（handleRPC 在 dispatch 前就打点），基数必须封顶，超出折叠到
// rpcStatOverflowMethod，窗口数（内存）不得随请求内容增长。
func TestRPCStatsMethodCardinalityBounded(t *testing.T) {
	var s rpcStats
	const attempts = rpcStatMaxMethods + 44
	for i := 0; i < attempts; i++ {
		s.Observe(fmt.Sprintf("rand_method_%d", i), time.Millisecond, http.StatusOK)
	}

	snaps, total := s.snapshotAll(false)
	if total != attempts {
		t.Fatalf("total = %d, want %d (counting must not drop samples)", total, attempts)
	}
	if len(snaps) > rpcStatMaxMethods+1 {
		t.Fatalf("windows = %d, want <= %d (cardinality must be bounded)", len(snaps), rpcStatMaxMethods+1)
	}
	var overflow *rpcMethodSnapshot
	for i := range snaps {
		if snaps[i].Method == rpcStatOverflowMethod {
			overflow = &snaps[i]
		}
	}
	if overflow == nil {
		t.Fatalf("no overflow bucket in %d windows", len(snaps))
	}
	wantOverflow := uint64(attempts - rpcStatMaxMethods)
	if overflow.N != wantOverflow {
		t.Fatalf("overflow n = %d, want %d", overflow.N, wantOverflow)
	}
	// 超出上限之后的新 method 不得再新建窗口。
	s.methods.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), "rand_method_") {
			return true
		}
		if k.(string) != rpcStatOverflowMethod {
			t.Fatalf("unexpected window %q", k.(string))
		}
		return true
	})
}

// TestRPCStatsReportResetsPendingSeen 验证提前上报阈值只做一次原子读，
// 且 Report 会重置未上报计数（否则会每请求触发一次上报）。
func TestRPCStatsReportResetsPendingSeen(t *testing.T) {
	var s rpcStats
	for i := 0; i < rpcStatLogEverySamples; i++ {
		s.Observe("m", time.Millisecond, http.StatusOK)
	}
	if got := s.pendingSeen(); got != rpcStatLogEverySamples {
		t.Fatalf("pendingSeen = %d, want %d", got, rpcStatLogEverySamples)
	}
	var buf strings.Builder
	prevOut := logrus.StandardLogger().Out
	logrus.SetOutput(&buf)
	t.Cleanup(func() { logrus.SetOutput(prevOut) })
	if n := s.Report("threshold"); n != rpcStatLogEverySamples {
		t.Fatalf("reported = %d, want %d", n, rpcStatLogEverySamples)
	}
	if got := s.pendingSeen(); got != 0 {
		t.Fatalf("pendingSeen after Report = %d, want 0", got)
	}
}
