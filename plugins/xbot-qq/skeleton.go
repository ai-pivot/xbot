package main

// 本文件是阶段 3 的落地位置（阶段 1/2 已把形状与前半段接线做完）。
//
// 已完成（不在本文件）：
//
//	渠道注册声明 → declaration.go（activate 返回 channel_provider）
//	配置模型     → config.go
//	白名单/权限  → patterns.go / policy.go
//	身份注入     → inbound.go（Identity / InboundCandidate / Params）
//	渠道提示词   → prompt.go（go:embed prompt/channel_prompt.md）
//
// 本文件里剩下的都是阶段 3：
//
//	SlashGate        → slash 门禁（命令白名单 + admin 判定 + 拒绝文案）
//	ReconnectPolicy  → 断线重连退避（接到 OneBot WS 读循环）
//
// 它们现在只有纯函数级别的实现（语法判断 / 算术），不含定时器、不含连接、
// 不启动任何 goroutine —— 因此接入也不会改变现有行为。

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// slash 门禁（阶段 3）
// ---------------------------------------------------------------------------

// SlashGate 是 slash 命令的入口判定。
//
// 阶段 3 的规则（尚未实现）：以 Prefix 开头的消息不当作普通对话送给 agent，
// 而是先过命令白名单 + 管理员判定，未授权直接回一条拒绝文案。
//
// 现在只有"这是不是一条 slash 命令"这一个无策略的语法判断 —— 它不决定放行。
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

// TODO(阶段3-gate)：Split(cmd, args) + 命令白名单 + admin 判定 + 拒绝文案。

// ---------------------------------------------------------------------------
// 断线重连（阶段 3）
// ---------------------------------------------------------------------------

// ReconnectPolicy 描述 OneBot 连接断开后的重连退避策略。
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

// TODO(阶段3-reconnect)：把 Backoff 接到 OneBot WS 读循环，配合 SlashGate
// 之后统一处理"连接丧失期间的入站积压"与"重连后会话续接"。
