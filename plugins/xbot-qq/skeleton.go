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

// SlashGate 是 slash 命令的入口判定（**只做语法判断**）。
//
// 注意：真正的放行策略（管理员判定、拒绝文案、/compact→/compress 改写）在
// slash.go；本类型保留是因为它被单测与潜在调用方当作"是否以命令前缀开头"的
// 轻量判断使用，不代表任何授权决定。
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

// DefaultReconnectPolicy 返回默认策略：2s 起，指数退避到 10min，不限次数。
//
// 为什么不再是"1s 起 60s 封顶"（2026-10-26 联调事故的教训）：
//   - 无限重试在"配置错"（IP 白名单 / 凭据）场景下只是刷平台接口，
//     1 分钟内就能把 401 打成 100017（接口调用超过频率限制），把自己送进风控；
//   - 单次等待上限拉到 10min，让"真的只是网络抖动"的场景也能自愈，但不会高频敲门。
//
// 为什么次数仍然不限：配置错导致的致命错误另有快停路径（见 gateway_fatal.go），
// 根本不进退避循环；剩下的都是可自愈的瞬时故障，给次数上限只会让渠道
// 在长稳场景下悄悄死掉、等人去重启。
func DefaultReconnectPolicy() ReconnectPolicy {
	return ReconnectPolicy{Min: 2 * time.Second, Max: 10 * time.Minute, MaxAttempts: 0}
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
