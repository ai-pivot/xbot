package main

import (
	"testing"
	"time"
)

// TestBackoff 覆盖重连退避：1s 起指数增长、封顶 60s、可配放弃点。
func TestBackoff(t *testing.T) {
	r := DefaultReconnectPolicy()
	want := map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 32 * time.Second,
		7: time.Minute, // 64s 封顶到 60s
		8: time.Minute,
	}
	for attempt, exp := range want {
		if got := r.Backoff(attempt); got != exp {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, exp)
		}
	}
	if got := r.Backoff(0); got != 0 {
		t.Errorf("Backoff(0) = %v, want 0 (invalid attempt)", got)
	}

	capped := ReconnectPolicy{Min: time.Second, Max: 10 * time.Second}
	if got := capped.Backoff(9); got != 10*time.Second {
		t.Errorf("capped Backoff(9) = %v, want 10s (Max)", got)
	}

	limited := ReconnectPolicy{Min: time.Second, Max: 10 * time.Second, MaxAttempts: 2}
	if got := limited.Backoff(2); got != 2*time.Second {
		t.Errorf("limited Backoff(2) = %v, want 2s", got)
	}
	if got := limited.Backoff(3); got != 0 {
		t.Errorf("limited Backoff(3) = %v, want 0 (past MaxAttempts)", got)
	}
}

// TestSlashGateIsSlash 只做语法判断，不含任何放行策略。
func TestSlashGateIsSlash(t *testing.T) {
	g := DefaultSlashGate()
	for _, s := range []string{"/help", "  /model gpt", "/"} {
		if !g.IsSlash(s) {
			t.Errorf("IsSlash(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "help", "a/b", "(/help)", "／全角斜杠"} {
		if g.IsSlash(s) {
			t.Errorf("IsSlash(%q) = true, want false", s)
		}
	}
}
