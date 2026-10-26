package main

// 本文件是阶段 2 / 阶段 3 的落地位置。
//
// 阶段 1 只定义**形状**（类型、字段、构造函数），不接线、不启动任何连接：
//
//	阶段 2（渠道注册 + 身份注入 + 渠道提示词落盘）
//	  - ChannelDeclaration  → 已由 activate 返回（渠道注册的声明部分已完成）
//	  - Identity / InboundCandidate → 入站消息的身份注入挂载点
//	  - 渠道提示词（channel_prompt）→ 见 TODO(阶段2-prompt)
//	阶段 3（slash 门禁 + 断线重连）
//	  - SlashGate     → slash 门禁挂载点
//	  - ReconnectPolicy → 断线重连策略
//
// 判据：阶段 1 结束时这些类型必须能编译、能被单测断言，但没有任何一条
// 真实网络连接或消息链路依赖它们。

import (
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 渠道注册（阶段 2：与 host 的 channel registry 对接，声明本身已就绪）
// ---------------------------------------------------------------------------

// SettingOption 是 select 类配置项的一个选项。
type SettingOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// SettingDef 对应 host 侧 channel.SettingDefinition 的 JSON 形状。
// host 的类型转换在 serverapp/channel_plugin.go 的 ConfigSchema() 里完成，
// 插件侧只负责按协议字段名输出。
type SettingDef struct {
	Key          string          `json:"key"`
	Label        string          `json:"label"`
	Description  string          `json:"description,omitempty"`
	Type         string          `json:"type"`
	DefaultValue string          `json:"default_value,omitempty"`
	Category     string          `json:"category,omitempty"`
	Options      []SettingOption `json:"options,omitempty"`
}

// ChannelDeclaration 是 activate 时回给 host 的 channel_provider 声明。
// 渠道名 "qq" 不能与内置渠道 feishu / web / cli 冲突。
type ChannelDeclaration struct {
	Name         string       `json:"name"`
	ConfigSchema []SettingDef `json:"config_schema"`
}

// ChannelName 是本插件注册的渠道名（= config.json 里 channels.<name> 的键）。
const ChannelName = "qq"

// qqChannelDeclaration 返回 QQ 渠道的完整声明。
//
// 配置项顺序即 UI 面板顺序：先开关与连接，再准入，最后权限 ——
// 让用户在填白名单之前先看到"留空 = 不处理任何消息"的说明。
func qqChannelDeclaration() ChannelDeclaration {
	return ChannelDeclaration{
		Name: ChannelName,
		ConfigSchema: []SettingDef{
			{
				Key:          KeyEnabled,
				Label:        "启用 QQ 渠道",
				Description:  "关闭时插件保持加载但不连接 OneBot。",
				Type:         "toggle",
				DefaultValue: "false",
				Category:     "连接",
			},
			{
				Key:          KeyWSURL,
				Label:        "OneBot WS 地址",
				Description:  "OneBot v11 的正向 WebSocket 地址，例如 ws://127.0.0.1:3001。",
				Type:         "text",
				DefaultValue: "",
				Category:     "连接",
			},
			{
				Key:          KeyAccessToken,
				Label:        "Access Token",
				Description:  "OneBot 的 access_token；未设置则留空。",
				Type:         "password",
				DefaultValue: "",
				Category:     "连接",
			},
			{
				Key:   KeyAllow,
				Label: "消息白名单",
				Description: "决定哪些发送者的消息交给 agent。每行/逗号分隔；" +
					"支持精确值（QQ 号、group:<群号>）与通配模式（* 任意长度，? 一个字符）；" +
					"填 * 表示处理所有消息。留空 = 不处理任何消息。",
				Type:         "textarea",
				DefaultValue: "",
				Category:     "准入",
			},
			{
				Key:   KeyAdmins,
				Label: "管理员名单",
				Description: "拥有管理员权限的发送者（QQ 号）。每行/逗号分隔，支持通配。留空 = 无管理员。" +
					"注意：host 侧 agent.admins 才是权限最终权威，两处口径需一致。",
				Type:         "textarea",
				DefaultValue: "",
				Category:     "权限",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 身份注入（阶段 2）
// ---------------------------------------------------------------------------

// Identity 是一条入站 QQ 消息的发送者身份。
//
// 阶段 2 会把它填进 send_inbound 的 sender_id / sender_name / chat_id / chat_type，
// 并在渠道提示词里告诉 agent "这是 QQ 渠道的消息"。
type Identity struct {
	// SenderID 是 QQ 号（稳定标识，用于白名单与 admin 判定）。
	SenderID string
	// SenderName 是群名片或昵称（仅用于展示，不参与任何权限判断）。
	SenderName string
	// ChatID 是会话标识：私聊为 "<QQ号>"，群聊为 "group:<群号>"。
	ChatID string
	// ChatType 是 "p2p"（私聊）或 "group"（群聊）。
	ChatType string
}

// Valid 报告身份是否齐全到可以送进 agent。
func (i Identity) Valid() bool {
	return i.SenderID != "" && i.ChatID != ""
}

// IsGroup 报告这是不是群聊消息。
func (i Identity) IsGroup() bool { return i.ChatType == "group" }

// Describe 返回日志用的单行描述。
func (i Identity) Describe() string {
	if i.SenderName != "" {
		return fmt.Sprintf("%s(%s) in %s", i.SenderName, i.SenderID, i.ChatID)
	}
	return fmt.Sprintf("%s in %s", i.SenderID, i.ChatID)
}

// InboundCandidate 是一条待提交给 host 的入站消息。
//
// 阶段 2 的接线是：Identity + 文本 → （Policy 放行）→ InboundCandidate → send_inbound RPC。
// 阶段 1 只提供形状与 host 参数名映射，不发送。
type InboundCandidate struct {
	Identity Identity
	Content  string
	// Admin 由 Policy 判定后填入，供 host 侧与日志使用（host 仍会用自己的
	// agent.admins 再判一次，两处口径必须一致）。
	Admin bool
	// RequestID 用于去重/对齐乐观消息（host 侧 send_inbound 的 request_id）。
	RequestID string
}

// Params 把候选消息映射成 host send_inbound 的参数名（阶段 2 使用）。
func (c InboundCandidate) Params() map[string]any {
	return map[string]any{
		"channel":     ChannelName,
		"chat_id":     c.Identity.ChatID,
		"content":     c.Content,
		"sender_id":   c.Identity.SenderID,
		"sender_name": c.Identity.SenderName,
		"chat_type":   c.Identity.ChatType,
		"request_id":  c.RequestID,
		"metadata":    map[string]string{"admin": fmt.Sprintf("%t", c.Admin)},
	}
}

// TODO(阶段2-prompt)：渠道提示词落盘 —— 收到 channel_config 后回一条
//
//	{"type":"channel_prompt","system_parts":{"05_channel_qq":"..."}}
//
// 声明 QQ 渠道的回复规范（长度上限、@ 语法、群聊上下文说明）。

// ---------------------------------------------------------------------------
// slash 门禁（阶段 3）
// ---------------------------------------------------------------------------

// SlashGate 是 slash 命令的入口判定。
//
// 阶段 3 的规则（尚未实现）：以 Prefix 开头的消息不当作普通对话送给 agent，
// 而是先过命令白名单 + 管理员判定，未授权直接回一条拒绝文案。
//
// 阶段 1 只提供"这是不是一条 slash 命令"这一个无策略的语法判断 ——
// 它不决定放行，因此现在接入也不会改变任何行为。
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
//
// 阶段 1 只做纯算术（Backoff），不含计时器、不含连接、不启动任何 goroutine。
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
