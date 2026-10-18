package agent

import "testing"

// progressSeqFor 必须是 **per-session 持久** 的：同一 key 返回同一计数器，
// 跨 run/resume 继续递增；不同 key 独立。首值以 UnixMilli 播种（>0）。
//
// 反例（修复前）：每个 Run 新建 atomic.Uint64 ⇒ resume 后 seq 从 1 重计 ⇒
// 客户端 per-turn 水位判定为"重放" ⇒ 迭代不再更新。
func TestProgressSeqPerSessionMonotonic(t *testing.T) {
	a := &Agent{}
	s1 := a.progressSeqFor("web:chat_A")
	seed := s1.Load()
	if seed == 0 {
		t.Fatal("first seq must be seeded (>0), got 0")
	}
	first := s1.Add(1)
	second := s1.Add(1)
	if second != first+1 {
		t.Fatalf("seq must increment by 1: first=%d second=%d", first, second)
	}

	// 模拟 resume：同一会话再次取计数器 ⇒ 必须是同一个指针，且继续递增。
	s2 := a.progressSeqFor("web:chat_A")
	if s2 != s1 {
		t.Fatal("same session must reuse the same counter pointer")
	}
	third := s2.Add(1)
	if third <= second {
		t.Fatalf("resume must continue above the previous watermark: second=%d third=%d", second, third)
	}

	// 不同会话独立（互不影响）。
	other := a.progressSeqFor("web:chat_B")
	if other == s1 {
		t.Fatal("different sessions must not share the counter")
	}
	if got := other.Add(1); got <= seed {
		t.Fatalf("independent counter should be seeded similarly: %d (seed %d)", got, seed)
	}
}
