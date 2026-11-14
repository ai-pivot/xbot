package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// ChannelPromptKey 是本渠道提示词在 system prompt 里的片段键。
//
// 前缀 "05_" 是 host 的排序约定（见 agent/channel_prompt.go）：
// 排在 "00_base" 之后、"10_skills" 之前 —— 渠道约束先于技能目录进入上下文，
// 才不会被后面的大段技能说明冲淡。
const ChannelPromptKey = "05_channel_qq"

// channelPromptMarkdown 是提示词的唯一事实源，来自仓库内的
// plugins/xbot-qq/prompt/channel_prompt.md。
//
// 用 go:embed 而不是运行时读文件：插件被安装到 ~/.xbot/plugins 后 cwd 是
// 安装目录，`prompt/` 未必被拷贝；embed 保证"仓库里审阅的那份"和"运行时
// 注入的那份"逐字节一致（差别只能是重新编译），也方便用 sha256 对照。
//
//go:embed prompt/channel_prompt.md
var channelPromptMarkdown string

// ChannelPromptText 返回提示词全文（= 仓库文件原文）。
func ChannelPromptText() string { return channelPromptMarkdown }

// ChannelPromptSHA256 返回提示词全文的 sha256（hex）。
// 用于证明"运行时注入的内容"与"仓库里那份文件"一致。
func ChannelPromptSHA256() string { return channelPromptSHA }

var channelPromptSHA = func() string {
	sum := sha256.Sum256([]byte(channelPromptMarkdown))
	return hex.EncodeToString(sum[:])
}()

// channelSystemParts 构造 host channel_prompt 帧的 system_parts。
// host 侧 handleChannelPrompt 会把它整段收下并注册给 ChannelPromptMiddleware。
func channelSystemParts() map[string]string {
	return map[string]string{ChannelPromptKey: channelPromptMarkdown}
}

// channelPromptFrame 是 host → plugin 协议里的 channel_prompt 消息
// （方向实际是 plugin → host，字段名沿用 host 的解析结构）。
type channelPromptFrame struct {
	Type        string            `json:"type"`
	SystemParts map[string]string `json:"system_parts"`
}

// newChannelPromptFrame 构造一帧 channel_prompt 声明。
//
// 每收到一次 channel_config 就发一次：host 的语义是 hot-update（后发的整段
// 替换先前内容），所以升级插件后无需重启 host 也能换掉提示词。
func newChannelPromptFrame() channelPromptFrame {
	return channelPromptFrame{
		Type:        "channel_prompt",
		SystemParts: channelSystemParts(),
	}
}
