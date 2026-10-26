package main

// 本文件实现**渠道注册**的声明部分：activate 时回给 host 的
// channel_provider（名字 + 配置 schema）。
//
// host 侧接线路径（不依赖本文件的注释，靠可复现验证）：
//
//	插件 activate 返回 channel_provider
//	  → plugin.stdioPlugin.ChannelProvider（plugin/runtime.go 的 GetChannelProviderDecl）
//	  → serverapp.NewStdioChannelPluginProvider(decl, ...)（ChannelProviderFactory）
//	  → plugin.WireChannelProviders(pm) → ChannelProviderRegistrar
//	  → serverapp.ChannelProviderRegistry.Register(provider)  ← 名字 "qq" 从这里可查
//	  → registerChannels / registerCLIPluginChannels 用 registry.List() 建实例
//
// 验证方式见 scripts/qq-channel-verify（跑的是上面这条真实链路）。

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
type ChannelDeclaration struct {
	Name         string       `json:"name"`
	ConfigSchema []SettingDef `json:"config_schema"`
}

// ChannelName 是本插件注册的渠道名（= config.json 里 channels.<name> 的键）。
// 不能与内置渠道 feishu / web / cli 重名（host 的 ChannelProviderRegistry
// 同名会注册失败）。
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
