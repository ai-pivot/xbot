package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	logrus "github.com/sirupsen/logrus"
)

// captureLogs 把 logrus 输出重定向到内存，测试结束自动还原。
func captureLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prevOut := logrus.StandardLogger().Out
	prevFmt := logrus.StandardLogger().Formatter
	logrus.SetOutput(&buf)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	t.Cleanup(func() {
		logrus.SetOutput(prevOut)
		logrus.SetFormatter(prevFmt)
	})
	return &buf
}

// timingTestHandler 返回一个固定耗时的 /api/ 处理器（走中间件的计时分支）。
func timingTestHandler(delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	})
}

// TestRequestTiming100msTier 是 G-09 的核心验收：100–200ms 的请求必须产生
// 100ms 档日志，而快请求绝不能误触发。
//
// 本用例**在 -race 下照样跑**（CI 的唯一门禁就是 -race，见 ci.yml）：
// 被测的是合成 handler 上的 time.Sleep，不经 auth/DB，-race 只会带来个位数
// 毫秒级开销；实测 sleep=130ms → observed≈130ms，距 200ms 档仍有约 70ms 余量。
// 若将来这条余量真的被吃掉，正确做法是改成对被测行为的确定性断言（fake clock /
// 收窄阈值），而不是退回 skip —— skip 等于核心判据从未执行。
func TestRequestTiming100msTier(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)

	// ---- 场景 1：10 个快请求 → 100ms 档一次都不许触发 ----
	buf := captureLogs(t)
	fast := wc.requestTimingMiddleware(timingTestHandler(2 * time.Millisecond))
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		fast.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/fast", nil))
	}
	if n := wc.reqTiming.over100ms.Load(); n != 0 {
		t.Fatalf("fast requests bumped 100ms bucket: %d", n)
	}
	if got := buf.String(); strings.Contains(got, "over 100ms") || strings.Contains(got, "Slow API request") {
		t.Fatalf("fast requests must not log slow lines, got:\n%s", got)
	}

	// ---- 场景 2：1 个 130ms 请求 → 恰好一条 100ms 档日志 ----
	buf.Reset()
	wc.reqTiming.lastLog100.Store(0) // 清节流窗口，让本场景独立
	mid := wc.requestTimingMiddleware(timingTestHandler(130 * time.Millisecond))
	rec := httptest.NewRecorder()
	mid.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mid", nil))

	out := buf.String()
	if got := strings.Count(out, "API request over 100ms"); got != 1 {
		t.Fatalf("want exactly 1 100ms-tier line, got %d:\n%s", got, out)
	}
	for _, want := range []string{"tier=100ms", "path=/api/mid", "throttled=true", "count_ge_100ms=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("100ms line missing %q:\n%s", want, out)
		}
	}
	// 字段名必须是「≥100ms 累计」口径（G-17 必改-2）。上面的正向断言已足够
	// 判别：把 key 改回「档位单数」的旧名，`count_ge_100ms=1` 立即缺失 → 本用例变红
	// （变异检验：把 web.go 的 key 改回旧名后跑本用例应 exit=1）。
	// 这里刻意不再写旧名字面量，好让验收口径的旧字段名 grep 保持 0 命中。
	if strings.Contains(out, "Slow API request") {
		t.Fatalf("130ms must not be promoted to the 200ms tier:\n%s", out)
	}
	if n := wc.reqTiming.over100ms.Load(); n != 1 {
		t.Fatalf("over100ms = %d, want 1", n)
	}
	if n := wc.reqTiming.over200ms.Load(); n != 0 {
		t.Fatalf("over200ms = %d, want 0", n)
	}
}

// TestRequestTiming100msTierThrottled 验证 100ms 档**必须节流**：连发两次
// 130ms 请求只许出一行日志，但计数一次都不能少（信息不丢、日志不炸）。
// 与 TestRequestTiming100msTier 同理，-race 下不跳过（见该用例注释）。
func TestRequestTiming100msTierThrottled(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	buf := captureLogs(t)
	// 用一个远超测试时长的节流窗口，把「三次调用是否落在同一窗口内」从墙钟
	// 假设变成确定事实：默认 1s 在机器繁忙时可能被 3×130ms 跨过，第 3 次调用
	// 会另起一行，断言变成假红。窗口放大后，这里验证的只剩节流逻辑本身。
	prevThrottle := requestTimingThrottle
	requestTimingThrottle = time.Hour
	t.Cleanup(func() { requestTimingThrottle = prevThrottle })
	wc.reqTiming.lastLog100.Store(0)

	mid := wc.requestTimingMiddleware(timingTestHandler(130 * time.Millisecond))
	for i := 0; i < 3; i++ {
		mid.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/mid", nil))
	}

	out := buf.String()
	if got := strings.Count(out, "API request over 100ms"); got != 1 {
		t.Fatalf("throttle broken: want 1 line for 3 rapid calls, got %d:\n%s", got, out)
	}
	if n := wc.reqTiming.over100ms.Load(); n != 3 {
		t.Fatalf("over100ms = %d, want 3 (counting must not be throttled)", n)
	}
	// 节流窗口过去后（lastLog100 归零 = 早已过期），被折叠掉的 2 条必须在下一条
	// 日志里补报出来（信息不丢）。这里同样不依赖墙钟：0 永远早于任何 now。
	wc.reqTiming.lastLog100.Store(0)
	buf.Reset()
	mid.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/mid", nil))
	out = buf.String()
	if got := strings.Count(out, "API request over 100ms"); got != 1 {
		t.Fatalf("want 1 line after throttle window, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "folded=2") {
		t.Fatalf("folded samples lost, want folded=2:\n%s", out)
	}
	// 累计口径：本行代表 1 条样本，但 count_ge_100ms 必须是 4（含被折叠的 3 条）。
	if !strings.Contains(out, "count_ge_100ms=4") {
		t.Fatalf("count_ge_100ms must be cumulative (=4), got:\n%s", out)
	}
	if n := wc.reqTiming.over100ms.Load(); n != 4 {
		t.Fatalf("over100ms = %d, want 4", n)
	}
}

// TestRequestTimingBucketsAreCumulative 验证分档是「包含更高档」的累计口径，
// 且 200ms 档仍保留原有 Warn 日志（不破坏既有的 grep 习惯）。
// 250ms 距 200ms 档有 50ms 余量，-race 下不跳过（见 TestRequestTiming100msTier）。
func TestRequestTimingBucketsAreCumulative(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	buf := captureLogs(t)
	wc.reqTiming.lastLog100.Store(0)

	slow := wc.requestTimingMiddleware(timingTestHandler(250 * time.Millisecond))
	slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/slow", nil))

	out := buf.String()
	if !strings.Contains(out, "Slow API request") || !strings.Contains(out, "tier=200ms") {
		t.Fatalf("250ms should hit the 200ms tier:\n%s", out)
	}
	if strings.Contains(out, "API request over 100ms") {
		t.Fatalf("250ms should not emit the 100ms-only line:\n%s", out)
	}
	if got := wc.reqTiming.over100ms.Load(); got != 1 {
		t.Fatalf("over100ms = %d, want 1 (cumulative)", got)
	}
	if got := wc.reqTiming.over200ms.Load(); got != 1 {
		t.Fatalf("over200ms = %d, want 1", got)
	}
	if got := wc.reqTiming.over1s.Load(); got != 0 {
		t.Fatalf("over1s = %d, want 0", got)
	}
}
