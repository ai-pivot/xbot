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
func TestRequestTiming100msTier(t *testing.T) {
	if raceEnabled {
		t.Skip("-race 构建会把墙钟放大到数百毫秒（实测 auth 路径 650ms+），100–200ms 档不可观测；严格断言由默认门禁覆盖")
	}

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
	for _, want := range []string{"tier=100ms", "path=/api/mid", "throttled=true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("100ms line missing %q:\n%s", want, out)
		}
	}
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
func TestRequestTiming100msTierThrottled(t *testing.T) {
	if raceEnabled {
		t.Skip("-race 构建会把墙钟放大到数百毫秒（实测 auth 路径 650ms+），100–200ms 档不可观测；严格断言由默认门禁覆盖")
	}

	wc, _ := newTestWebChannel(t, nil)
	buf := captureLogs(t)
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
	// 节流窗口过去后，被折叠掉的 2 条必须在下一条日志里补报出来（信息不丢）。
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
	if n := wc.reqTiming.over100ms.Load(); n != 4 {
		t.Fatalf("over100ms = %d, want 4", n)
	}
}

// TestRequestTimingBucketsAreCumulative 验证分档是「包含更高档」的累计口径，
// 且 200ms 档仍保留原有 Warn 日志（不破坏既有的 grep 习惯）。
func TestRequestTimingBucketsAreCumulative(t *testing.T) {
	if raceEnabled {
		t.Skip("-race 构建会把墙钟放大到数百毫秒（实测 auth 路径 650ms+），100–200ms 档不可观测；严格断言由默认门禁覆盖")
	}

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
