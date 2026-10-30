package web

import (
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// /api/rpc per-method latency observability
//
// 背景：`/api/rpc` 是 web 端插件/设置/会话接口的单一入口（handleRPC），历史上
// 只有「整个 path 一条 p99」的粗口径 —— 慢的时候无法回答「哪个 method 拖尾」，
// 优化只能靠猜。这里在 handleRPC 这个唯一咽喉点按 method 打点，输出
// n / p50 / p95 / p99，供日志与 /api/rpc-stats 接口消费。
//
// 设计约束（性能任务自己不许变成性能问题）：
//  1. 打点路径只做「一次 map 查找 + 一次数组写入 + 两个计数」，O(1)、无排序、
//     无分配。分位数计算全部推迟到 reporter goroutine / 查询接口里做。
//  2. 采样用固定长度环形缓冲（每个 method 独立），内存上界 = 方法数 × 环形大小，
//     长时间运行不会无限增长。N 是累计总数（不受环形窗口覆盖影响）。
//  3. 环形窗口是「最近 N 个样本」——分位数反映近期状态，而不是把一周前的
//     慢请求混进来。
// ---------------------------------------------------------------------------

const (
	// rpcStatRingSize 是每个 method 保留的最近样本数。8192 个 int64 = 64KiB/method，
	// 6~10 个 method 合计 < 700KiB；p99 需要 ≥100 样本才稳定，8192 远超。
	rpcStatRingSize = 8192

	// rpcStatMaxMethods 是 method 基数的硬上限，超出后统一归入 rpcStatOverflowMethod。
	//
	// 为什么需要上限：**下发的 method 集合是有限集**（serverapp/rpc_table.go 里
	// BuildRPCTable 注册 143 个 key，未注册的走 Dispatch 报错），但**到达
	// web_rpc_stats.go 的 method 字符串是客户端可控的**——handleRPC 在 dispatch
	// 之前就把 request.Method 拿去打点（见 web_rest.go handleRPC 的 rpcMethod 赋值），
	// 因此一个发随机 method 名的客户端可以撑爆 sync.Map，每个条目还带 64KiB 环形
	// 缓冲。这里按 143+余量取 256 封顶，超出部分折叠到 rpcStatOverflowMethod，
	// 保证内存上界 = 257 × 64KiB ≈ 16MiB。
	rpcStatMaxMethods = 256

	// rpcStatOverflowMethod 是基数额度耗尽后的兜底桶名（带括号，不会与真实 method 撞名）。
	rpcStatOverflowMethod = "(overflow)"

	// rpcStatReportInterval 是日志上报周期。只有「自上次上报后确实有新样本」才打印，
	// 空闲时零日志。
	rpcStatReportInterval = 30 * time.Second

	// rpcStatLogEverySamples 是「不足一个上报周期但样本已经攒够」时的提前上报阈值。
	// 压测能在 30 秒内打满数千请求，不提前上报会拿不到窗口内的分位表。
	rpcStatLogEverySamples = 1000
)

// rpcMethodWindow 是单个 method 的环形采样窗口。
// 零值不可用，必须经 rpcStats.window() 创建。
type rpcMethodWindow struct {
	mu   sync.Mutex
	buf  []int64
	idx  int
	n    uint64 // 累计调用次数（可超过 ring 容量）
	errs uint64 // 累计 status != 200 的次数
	last int64  // 最近一次耗时，微秒
	seen uint64 // 自上次上报以来的新样本数（只在锁内读写）
}

func newRPCMethodWindow() *rpcMethodWindow {
	return &rpcMethodWindow{buf: make([]int64, rpcStatRingSize)}
}

// observe 记录一次样本。O(1)，无分配。
func (w *rpcMethodWindow) observe(micros int64, status int) {
	w.mu.Lock()
	w.buf[w.idx] = micros
	w.idx++
	if w.idx == len(w.buf) {
		w.idx = 0
	}
	w.n++
	w.seen++
	if status != http.StatusOK {
		w.errs++
	}
	w.last = micros
	w.mu.Unlock()
}

// snapshot 复制当前窗口并计算分位数。只在上报/查询路径调用。
// clearSeen=true 时同时清零上报游标（供日志 reporter 使用，保证每个样本只上报一次）。
func (w *rpcMethodWindow) snapshot(method string, clearSeen bool) rpcMethodSnapshot {
	w.mu.Lock()
	total := w.n
	errs := w.errs
	last := w.last
	seen := w.seen
	if clearSeen {
		w.seen = 0
	}
	filled := int(total)
	if filled > len(w.buf) {
		filled = len(w.buf)
	}
	vals := make([]int64, filled)
	// 从最老到最新复制（顺序不影响分位数，但便于人工排查时序）。
	start := w.idx - filled
	if start < 0 {
		start += len(w.buf)
	}
	for i := 0; i < filled; i++ {
		vals[i] = w.buf[(start+i)%len(w.buf)]
	}
	w.mu.Unlock()

	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })

	snap := rpcMethodSnapshot{
		Method:  method,
		N:       total,
		Samples: uint64(filled),
		Errors:  errs,
		LastMs:  microsToMs(float64(last)),
		seen:    seen,
	}
	if filled > 0 {
		snap.MaxMs = microsToMs(float64(vals[filled-1]))
		snap.P50Ms = microsToMs(percentileMicros(vals, 50))
		snap.P95Ms = microsToMs(percentileMicros(vals, 95))
		snap.P99Ms = microsToMs(percentileMicros(vals, 99))
	}
	return snap
}

// rpcMethodSnapshot 是单个 method 的分位数快照（对外 JSON / 日志共用）。
type rpcMethodSnapshot struct {
	Method  string  `json:"method"`
	N       uint64  `json:"n"`       // 累计调用次数
	Samples uint64  `json:"samples"` // 窗口内实际样本数（≤ rpcStatRingSize）
	Errors  uint64  `json:"errors"`
	P50Ms   float64 `json:"p50_ms"`
	P95Ms   float64 `json:"p95_ms"`
	P99Ms   float64 `json:"p99_ms"`
	MaxMs   float64 `json:"max_ms"`
	LastMs  float64 `json:"last_ms"`

	// seen 是上报游标，只给日志 reporter 用，不对外序列化。
	seen uint64
}

// rpcObservability 是挂在 WebChannel 上的按 method 观测组件。
// 零值可用（内部 sync.Map + atomic），因此不需要改 WebChannel 构造函数。
type rpcObservability struct {
	stats rpcStats

	// reportCh 是「攒够样本、请 reporter 立刻上报」的合并信号（容量 1）。
	// 用信号而不是热路径直接 go + wg.Add：① 避免把分位数计算塞进被测量的
	// 请求；② 避免在请求 goroutine 里 wg.Add 与 Stop() 的 wg.Wait 并发
	// （Go 明确禁止 Add 与 Wait 竞争，会 panic "WaitGroup misuse"）。
	// 由 Start() 在启动 reporter 前创建。
	reportCh chan struct{}
}

// rpcStats 是全量 method 的注册表。
type rpcStats struct {
	methods sync.Map // method(string) → *rpcMethodWindow
	total   atomic.Uint64

	// unreported 是「自上次 Report 以来新采集的样本数」。热路径只读这一个
	// 原子量来决定是否触发提前上报 —— 早期实现要遍历所有 method 逐个加锁
	// 求 seen 之和（每请求 O(methods) 次互斥），现在退化成一次 load。
	unreported atomic.Uint64

	// mu 只在 method 首次注册（慢路径）时持有，用于实施 rpcStatMaxMethods 上限。
	mu    sync.Mutex
	count int // 已注册的窗口数（含 overflow 桶），受 mu 保护
}

// window 返回（必要时创建）method 对应的环形窗口。
// 热路径命中 sync.Map 后直接返回；未命中才进 mutex 做基数上限判定，因此
// 上限检查只在 method 首次出现时付出一次锁代价。
// 基数超过 rpcStatMaxMethods 后，所有新 method 折叠进 rpcStatOverflowMethod，
// 保证窗口数（进而内存）有硬上界。
func (s *rpcStats) window(method string) *rpcMethodWindow {
	if v, ok := s.methods.Load(method); ok {
		return v.(*rpcMethodWindow)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 双检：可能已有并发调用者注册过。
	if v, ok := s.methods.Load(method); ok {
		return v.(*rpcMethodWindow)
	}
	if s.count >= rpcStatMaxMethods {
		if v, ok := s.methods.Load(rpcStatOverflowMethod); ok {
			return v.(*rpcMethodWindow)
		}
		v, _ := s.methods.LoadOrStore(rpcStatOverflowMethod, newRPCMethodWindow())
		s.count++
		return v.(*rpcMethodWindow)
	}
	v, _ := s.methods.LoadOrStore(method, newRPCMethodWindow())
	s.count++
	return v.(*rpcMethodWindow)
}

// Observe 记录一次 /api/rpc 调用。method 为空（请求体非法）时归入 "(invalid)"，
// 这样非法请求既不会被静默丢弃，也不会污染真实 method 的分位数。
func (s *rpcStats) Observe(method string, d time.Duration, status int) {
	if method == "" {
		method = "(invalid)"
	}
	s.window(method).observe(d.Microseconds(), status)
	s.total.Add(1)
	s.unreported.Add(1)
}

// snapshotAll 返回按 method 名字排序的分位数快照（只含有过调用的 method）。
func (s *rpcStats) snapshotAll(clearSeen bool) ([]rpcMethodSnapshot, uint64) {
	var out []rpcMethodSnapshot
	s.methods.Range(func(k, v any) bool {
		out = append(out, v.(*rpcMethodWindow).snapshot(k.(string), clearSeen))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out, s.total.Load()
}

// pendingSeen 返回「自上次上报以来采集的新样本数」。
// 只做一次原子读，不在热路径上遍历方法表。
func (s *rpcStats) pendingSeen() uint64 {
	return s.unreported.Load()
}

// Report 把每个 method 的分位表打成一行日志（method 作为字段，便于 grep/聚合）。
// 返回本次上报的样本数，0 表示无新样本、未打印。
func (s *rpcStats) Report(reason string) int {
	// 先清未上报计数：Report 期间新到的样本留给下一轮，宁可多打一次也不漏。
	s.unreported.Store(0)
	snaps, total := s.snapshotAll(true)
	var reported uint64
	for _, snap := range snaps {
		reported += snap.seen
	}
	if reported == 0 {
		return 0
	}
	for _, snap := range snaps {
		// 只打印本次窗口内有新样本的 method，避免空闲 method 刷屏。
		if snap.seen == 0 {
			continue
		}
		log.WithFields(log.Fields{
			"method":  snap.Method,
			"n":       snap.N,
			"samples": snap.Samples,
			"p50_ms":  snap.P50Ms,
			"p95_ms":  snap.P95Ms,
			"p99_ms":  snap.P99Ms,
			"max_ms":  snap.MaxMs,
			"errors":  snap.Errors,
			"total":   total,
			"reason":  reason,
		}).Info("rpc latency by method")
	}
	return int(reported)
}

// ---------------------------------------------------------------------------
// HTTP 接口
// ---------------------------------------------------------------------------

// handleRPCStats 暴露按 method 的分位数快照，供外部（压测/巡检）抓取。
// 只读，且走 authMiddleware（与其它 /api/ 接口一致）。查询不清零上报游标。
func (wc *WebChannel) handleRPCStats(w http.ResponseWriter, r *http.Request) {
	snaps, total := wc.rpcObs.stats.snapshotAll(false)
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"ring_size":     rpcStatRingSize,
		"total_samples": total,
		"methods":       snaps,
	})
}

// rpcStatsReporter 周期上报，随 channel Stop 一起退出。
// reportCh 上的阈值信号由 maybeReportRPCStats 投递（容量 1，自动合并）。
func (wc *WebChannel) rpcStatsReporter() {
	defer wc.wg.Done()
	t := time.NewTicker(rpcStatReportInterval)
	defer t.Stop()
	for {
		select {
		case <-wc.stopCh:
			return
		case <-t.C:
			wc.rpcObs.stats.Report("interval")
		case <-wc.rpcObs.reportCh:
			wc.rpcObs.stats.Report("threshold")
		}
	}
}

// maybeReportRPCStats 在打点热路径上做「攒够阈值就提前上报」的判定。
// 只做一次原子读 + 一次非阻塞发送，真正的排序/打印由 reporter goroutine
// 完成，不会把分位数计算塞进被测量的请求本身（否则自己制造 p99 尖刺）。
// 这里**不再** wg.Add：请求 goroutine 与 Stop() 的 wg.Wait 并发时 Add 会 panic。
func (wc *WebChannel) maybeReportRPCStats() {
	if wc.rpcObs.stats.pendingSeen() < rpcStatLogEverySamples {
		return
	}
	ch := wc.rpcObs.reportCh
	if ch == nil { // 未 Start（单测直接调 handleRPC）
		return
	}
	select {
	case ch <- struct{}{}:
	default: // 已有一次待处理的触发，合并
	}
}

// ---------------------------------------------------------------------------
// 分位数计算（与 bench.py 的 pct() 同一口径，便于逐位对齐）
// ---------------------------------------------------------------------------

// percentileMicros 用线性插值计算第 p 百分位（输入必须已升序）。
// 返回浮点微秒，与 bench.py 的 pct() 完全同口径（不提前取整，避免
// 0.5µs 级别的系统性偏差在 p50 上被放大成整毫秒差别）。
func percentileMicros(sorted []int64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return float64(sorted[0])
	}
	k := float64(n-1) * p / 100
	lo := int(k)
	if lo >= n-1 {
		return float64(sorted[n-1])
	}
	return float64(sorted[lo]) + float64(sorted[lo+1]-sorted[lo])*(k-float64(lo))
}

func microsToMs(us float64) float64 {
	return us / 1000.0
}
