package main

// 日志限流：把重复噪音压成"×N"，别让真错误被埋掉。
//
// 2026-10-26 联调事故：`drop progress frame` 刷了 23294 行 / 2.1MB，把
// `HTTP 401 接口访问源IP不在白名单` 这种决定性的一行埋进了 2MB 噪音里，
// 排查时一度误判成"从未建连"。
//
// 纪律：debug 级噪音同样要限流 —— 高频路径（progress / stream）每个 key
// 每 interval 最多打一行，并在行尾带"自上次以来压掉了多少行"。

import (
	"sync"
	"time"
)

// dropLogInterval 是同类噪音的最小打印间隔。
const dropLogInterval = 60 * time.Second

// logThrottle 是按 key 的日志限流器（并发安全）。
type logThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	last     map[string]time.Time
	dropped  map[string]int
}

// NewLogThrottle 构造限流器（now 为 nil 用 time.Now）。
func NewLogThrottle(interval time.Duration, now func() time.Time) *logThrottle {
	if now == nil {
		now = time.Now
	}
	return &logThrottle{
		interval: interval,
		now:      now,
		last:     make(map[string]time.Time),
		dropped:  make(map[string]int),
	}
}

// Allow 报告 key 现在是否应当打印；第二个返回值是自上次打印以来被压掉的行数。
func (t *logThrottle) Allow(key string) (bool, int) {
	if t == nil {
		return true, 0
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		t.dropped[key]++
		return false, 0
	}
	dropped := t.dropped[key]
	delete(t.dropped, key)
	t.last[key] = now
	return true, dropped
}
