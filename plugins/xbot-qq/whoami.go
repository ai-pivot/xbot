package main

// !whoami —— 让用户自己查到自己的 openid。
//
// 为什么必须存在（用户原话：「你他妈让我填写 openid，我他妈怎么知道什么是
// open id」）：openid 由 QQ 官方生成，客户端里任何界面都看不到。要求用户
// "把 openid 填进白名单"而没有任何自查手段，等于把产品做死。
//
// 三条硬规则：
//  1. **必须在白名单判定之前处理** —— 被拒的人如果连自己 id 都问不到，就永远
//     进不来（先有鸡还是先有蛋）。
//  2. **绝不允许触发 LLM** —— 这条命令只查本地状态，不产生 send_inbound。
//  3. **必须限流** —— 回复同样消耗 QQ 被动回复配额（单聊 60 分钟 4 次 /
//     群聊 5 分钟 5 次），高频触发会把配额烧光。同一 openid 每 60s 最多 1 次。
//
// 命令形式是 **bang**（!whoami），不是 slash —— QQ 官方渠道不认 "/" 命令前缀。

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// WhoamiCommand 是查询自身 openid 的命令字面量（bang，不是 slash）。
	WhoamiCommand = "!whoami"
	// noticeCooldown 是同一 openid 的自动应答最小间隔（限流，省配额）。
	noticeCooldown = 60 * time.Second
)

// IsWhoamiCommand 报告一条消息正文是否就是 !whoami（大小写不敏感，允许首尾空白）。
func IsWhoamiCommand(content string) bool {
	return strings.EqualFold(strings.TrimSpace(content), WhoamiCommand)
}

// whoamiText 生成 !whoami 的回复正文。
//
// allowConfigured=false 表示 allow 压根没配（所有人都会被拒）；true 且
// allowed=false 表示配了但不含本人。两种情形的下一步动作不同，文案要分开说。
func whoamiText(openid, name string, allowed, allowConfigured bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "你的 openid = %s\n昵称 = %s\n", openid, name)
	switch {
	case allowed:
		b.WriteString("白名单状态 = 已在白名单内，可以直接对话。")
	case !allowConfigured:
		b.WriteString("白名单状态 = allow 还没配置（当前谁的消息都不会交给模型）。\n")
		b.WriteString("把上面这行 openid 填进插件配置的 allow（填 * 表示接收所有消息）即可开始对话。")
	default:
		b.WriteString("白名单状态 = 不在 allow 里（当前你的消息不会交给模型）。\n")
		b.WriteString("把上面这行 openid 填进插件配置的 allow（填 * 表示接收所有消息）即可开始对话。")
	}
	return b.String()
}

// allowEmptyText 是 allow 未配置时对任意入站消息的自动应答（零 LLM）。
//
// 理由：用户第一次发消息时如果毫无反应，他不知道下一步该干嘛 —— 产品等于死的。
// 但 allow **已配置**却不含本人时必须保持静默（联调步骤 6 的判据）。
func allowEmptyText(openid, name string) string {
	return fmt.Sprintf("你的 openid = %s\n昵称 = %s\n"+
		"当前 allow 还没配置，所以这条消息不会交给模型。\n"+
		"把上面的 openid 填进插件配置的 allow（填 * 表示接收所有消息）即可开始对话。",
		openid, name)
}

// noticeLimiter 是自动应答的限流器：同一 key 在 cooldown 内只放行一次。
type noticeLimiter struct {
	mu       sync.Mutex
	cooldown time.Duration
	now      func() time.Time
	last     map[string]time.Time
}

// NewNoticeLimiter 构造限流器（now 为 nil 用 time.Now）。
func NewNoticeLimiter(cooldown time.Duration, now func() time.Time) *noticeLimiter {
	if now == nil {
		now = time.Now
	}
	return &noticeLimiter{cooldown: cooldown, now: now, last: make(map[string]time.Time)}
}

// Allow 报告 key 现在是否允许应答；允许时顺手记下时间。
func (l *noticeLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.last[key]; ok && now.Sub(t) < l.cooldown {
		return false
	}
	l.last[key] = now
	// 防 map 无界增长：超过 512 条顺手清掉已过期的。
	if len(l.last) > 512 {
		for k, t := range l.last {
			if now.Sub(t) >= l.cooldown {
				delete(l.last, k)
			}
		}
	}
	return true
}
