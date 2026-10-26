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
// 注意：配置键只有 app_id / app_secret / allow / admins / enabled ——
// 不含任何 OneBot 字段（反向 WS 地址 / 静态 token）。
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
// 配置项顺序即 UI 面板顺序：先开关与凭据，再准入，最后权限 ——
// 让用户在填白名单之前先看到"留空 = 不处理任何消息"的说明。
//
// 只暴露 QQ 官方开放平台的凭据（app_id + app_secret）。OneBot / NapCat 的
// 反向 WS 与静态 token 已作废，不能再出现在配置面板里（否则用户会以为还要
// 跑一个第三方网关）。
func qqChannelDeclaration() ChannelDeclaration {
	return ChannelDeclaration{
		Name: ChannelName,
		ConfigSchema: []SettingDef{
			{
				Key:          KeyEnabled,
				Label:        "启用 QQ 渠道",
				Description:  "关闭时插件保持加载但不连接 QQ 官方网关。",
				Type:         "toggle",
				DefaultValue: "false",
				Category:     "连接",
			},
			{
				Key:          KeyAppID,
				Label:        "AppID",
				Description:  "QQ 开放平台 → 机器人 → 开发设置 里的 AppID（纯数字）。",
				Type:         "text",
				DefaultValue: "",
				Category:     "连接",
			},
			{
				Key:          KeyAppSecret,
				Label:        "AppSecret",
				Description:  "QQ 开放平台 → 机器人 → 开发设置 里的 AppSecret；只用于换 access_token。",
				Type:         "password",
				DefaultValue: "",
				Category:     "连接",
			},
			{
				Key:   KeySandbox,
				Label: "沙箱环境（应用未发布时必开）",
				Description: "开启时 OpenAPI 走 sandbox.api.sgroup.qq.com，关闭时走正式域名 api.bot.qq.com。" +
					"应用未在开放平台发布时，正式域名的 /gateway 一律 401（11298/40023002），只有沙箱能连。" +
					"默认开启；发布上线后再关掉。",
				Type:         "toggle",
				DefaultValue: "true",
				Category:     "连接",
			},
			{
				Key:   KeyIntents,
				Label: "事件订阅位（intents，十进制，留空=默认）",
				Description: "留空使用默认：1<<25 | 1<<30 = 1107296256。" +
					"QQ 的「群聊」走 GROUP_AND_C2C(1<<25)，「频道」走 PUBLIC_GUILD_MESSAGES(1<<30)，" +
					"两类��景的事件名不同、位也不同。" +
					"填 33554432 只订群聊+私聊；填 1107296256 同时订频道。改完点重载插件即可生效，不必重启服务。",
				Type:         "text",
				DefaultValue: "",
				Category:     "连接",
			},
			{
				Key:   KeyAllow,
				Label: "消息白名单",
				Description: "决定哪些发送者的消息交给 agent。匹配的是 **openid**（不是 QQ 号）；" +
					"每行/逗号分隔，支持精确值与通配模式（* 任意长度，? 一个字符）；" +
					"填 * 表示处理所有消息。留空 = 不处理任何消息。",
				Type:         "textarea",
				DefaultValue: "",
				Category:     "准入",
			},
			{
				Key:   KeyAdmins,
				Label: "管理员名单",
				Description: "拥有管理员权限的发送者 openid。每行/逗号分隔，支持通配。留空 = 无管理员。" +
					"注意：host 侧 agent.admins 才是权限最终权威，两处口径需一致。",
				Type:         "textarea",
				DefaultValue: "",
				Category:     "权限",
			},
		},
	}
}
