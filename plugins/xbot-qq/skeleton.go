package main

// 本文件保留两个与"连接策略"相关的纯函数组件：
//
//	SlashGate        → slash 命令的语法判定（当前仅保留能力，不参与放行）
//	ReconnectPolicy  → 断线重连退避（由 gateway.go 的 Run 使用）
//
// 它们都不启动 goroutine、不含定时器、不做网络 I/O，因此可以独立单测。
// 真实的连接生命周期在 gateway.go，鉴权在 token.go。

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// slash 门禁
// ---------------------------------------------------------------------------

// SlashGate 是 slash 命令的入口判定。
//
// 当前只做"这是不是一条 slash 命令"的语法判断，不决定放行 —— 放行策略
// （命令白名单 + admin 判定）尚未实现。保留它是为了后续在进 agent 之前
// 拦下 /xxx 命令并回拒绝文案。
type SlashGate struct {
	// Prefix 是命令前缀，默认 "/"。
	Prefix string
}

// DefaultSlashGate 返回默认门禁（前缀 "/"）。
func DefaultSlashGate() SlashGate { return SlashGate{Prefix: "/"} }

// IsSlash 报告消息是否以命令前缀开头。
func (g SlashGate) IsSlash(content string) bool {
	prefix := g.Prefix
	if prefix == "" {
		prefix = "/"
	}
	return strings.HasPrefix(strings.TrimSpace(content), prefix)
}

// ---------------------------------------------------------------------------
// 断线重连
// ---------------------------------------------------------------------------

// ReconnectPolicy 描述网关连接断开后的重连退避策略。
type ReconnectPolicy struct {
	// Min 是首次重试等待。
	Min time.Duration
	// Max 是单次等待上限。
	Max time.Duration
	// MaxAttempts 是最大连续重试次数；0 表示不限次数。
	MaxAttempts int
}

// DefaultReconnectPolicy 返回默认策略：1s 起，指数退避到 60s，无限重试
// （渠道插件应当长期自愈，而不是自己放弃后需要人去重启服务）。
func DefaultReconnectPolicy() ReconnectPolicy {
	return ReconnectPolicy{Min: time.Second, Max: time.Minute, MaxAttempts: 0}
}

// Backoff 返回第 attempt 次重试（attempt 从 1 开始）应等待的时长。
//
// 返回 0 表示按策略应当放弃：attempt < 1、或 MaxAttempts > 0 且 attempt 超过它。
func (r ReconnectPolicy) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	if r.MaxAttempts > 0 && attempt > r.MaxAttempts {
		return 0
	}
	min := r.Min
	if min <= 0 {
		min = time.Second
	}
	delay := min
	for i := 1; i < attempt; i++ {
		delay *= 2
		if r.Max > 0 && delay >= r.Max {
			return r.Max
		}
	}
	if r.Max > 0 && delay > r.Max {
		return r.Max
	}
	return delay
}
