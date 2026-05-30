package channel

import (
	"xbot/bus"
)

// ChannelProvider 由插件实现，用于注册自定义 Channel。
// 插件在 Activate 阶段通过 PluginContext.RegisterChannelProvider() 注册，
// serverapp 会在 registerChannels 和动态启停路径中查找并调用。
type ChannelProvider interface {
	// Name 返回唯一 channel 标识符（如 "telegram"）。
	// 不能与内置 channel（feishu/qq/napcat/web）重名。
	Name() string

	// CreateChannel 根据配置创建 Channel 实例。
	// cfg 来自 config.json 的 channels.<name> 段（map[string]string）。
	// msgBus 用于发送 Inbound 消息到 Agent。
	CreateChannel(cfg map[string]string, msgBus *bus.MessageBus) (Channel, error)

	// ConfigSchema 返回此 channel 的配置字段定义。
	// TUI settings 面板使用此 schema 自动渲染配置 UI。
	// 返回空 slice 表示该 channel 无可配置项。
	ConfigSchema() []SettingDefinition

	// IsEnabled 检查配置是否启用此 channel。
	// cfg 来自 config.json 的 channels.<name> 段。
	IsEnabled(cfg map[string]string) bool
}
