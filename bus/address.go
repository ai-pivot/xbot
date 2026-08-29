package bus

import (
	"strings"
)

// Address 统一寻址标识。
//
// 格式: scheme://domain/id
//
// 示例:
//
//	im://feishu/ou_xxx       — 飞书用户
//	im://feishu/oc_xxx       — 飞书群聊
//	im://qq/12345            — QQ 用户
//	agent://main             — 主 Agent
//	agent://main/code-reviewer — SubAgent
type Address struct {
	Scheme string // "im", "agent"
	Domain string // "feishu", "qq", "main"
	ID     string // 实体标识（可为空，如 agent://main）
}

// Common address schemes.
const (
	SchemeIM    = "im"
	SchemeAgent = "agent"
)

// String 返回规范化的地址字符串。
func (a Address) String() string {
	if a.ID == "" {
		return a.Scheme + "://" + a.Domain
	}
	return a.Scheme + "://" + a.Domain + "/" + a.ID
}

// IsAgent 判断是否为 Agent 地址。
func (a Address) IsAgent() bool {
	return a.Scheme == SchemeAgent
}

// --- 便捷构造函数 ---

// NewIMAddress 创建 IM 渠道地址。
//
//	NewIMAddress("feishu", "ou_xxx") → im://feishu/ou_xxx
func NewIMAddress(channel, id string) Address {
	return Address{Scheme: SchemeIM, Domain: channel, ID: id}
}

// NewAgentAddress 创建 Agent 地址。
//
//	NewAgentAddress("main")    → agent://main
//	NewAgentAddress("main/cr") → agent://main/cr
func NewAgentAddress(path string) Address {
	slash := strings.IndexByte(path, '/')
	if slash < 0 {
		return Address{Scheme: SchemeAgent, Domain: path}
	}
	return Address{Scheme: SchemeAgent, Domain: path[:slash], ID: path[slash+1:]}
}
