package channel

import "testing"

// TestBoundHistoryIterations_TailOnly — 用户 2026-09-15：「加载时间这么久，能优化吗？
// 是不是如果 busy turn 的 iter 数量非常多就会卡非常久啊」。实测单 turn 可达 1,661 个迭代
// (~3.6MB content+reasoning)，而历史响应原本**没有任何上限** ⇒ 加载时间随迭代数线性增长。
// 本用例锁定：只回传尾部 N 个迭代，且**不静默丢数据**（丢弃数量必须上报）。
func TestBoundHistoryIterations_TailOnly(t *testing.T) {
	const total = 1000
	iters := make([]HistoryIteration, total)
	for i := range iters {
		iters[i] = HistoryIteration{Iteration: i + 1}
	}
	got := BoundHistoryIterations([]HistoryMessage{{TurnID: 1, Iterations: iters}})
	first := got[0]
	if len(first.Iterations) != maxHistoryIterationsPerTurn {
		t.Fatalf("kept %d iterations, want %d", len(first.Iterations), maxHistoryIterationsPerTurn)
	}
	if first.IterationsTruncated != total-maxHistoryIterationsPerTurn {
		t.Fatalf("IterationsTruncated = %d, want %d", first.IterationsTruncated, total-maxHistoryIterationsPerTurn)
	}
	// 保留的必须是**尾部**（最近 N 个），否则渲染最新迭代会缺块。
	if want := total - maxHistoryIterationsPerTurn + 1; first.Iterations[0].Iteration != want {
		t.Fatalf("tail start = %d, want %d", first.Iterations[0].Iteration, want)
	}
	if last := first.Iterations[len(first.Iterations)-1].Iteration; last != total {
		t.Fatalf("tail end = %d, want %d", last, total)
	}
	// 未超限：原样返回、不报截断。
	small := BoundHistoryIterations([]HistoryMessage{{TurnID: 2, Iterations: []HistoryIteration{{Iteration: 1}}}})
	if len(small[0].Iterations) != 1 || small[0].IterationsTruncated != 0 {
		t.Fatalf("small turn must pass through unchanged, got %d/%d", len(small[0].Iterations), small[0].IterationsTruncated)
	}
}
