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
				Key:   KeyMarkdown,
				Label: "Markdown 渲染（推荐开启）",
				Description: "开启时用 msg_type=2 发送回复，QQ 客户端会按 Markdown 渲染" +
					"（标题、列表、代码块、粗体等）。QQ 只支持部分 Markdown 子集。" +
					"关闭则退化为纯文本（msg_type=0）。默认开启。",
				Type:         "toggle",
				DefaultValue: "true",
				Category:     "发送",
			},
			{
				Key:   KeyQiniuEnv,
				Label: "CDN 凭据文件（可选，用于图文混排）",
				Description: "填一个 k=v 文件路径，含 QINIU_AK / QINIU_SK / QINIU_BUCKET / QINIU_DOMAIN。" +
					"配置后：图片会先传到 CDN 拿公网 URL，再以 Markdown **图文同一条消息**发出" +
					"（QQ 的富媒体消息 msg_type=7 与正文 content 互斥，无法混排；" +
					"而 markdown 支持图片，接口里 force_verify_image_resource 即『图片转存』）。" +
					"不配置则退回『图片单独一条 + 文字单独一条』。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:   KeyCDNProvider,
				Label: "图床 provider（图文混排用，留空=关闭）",
				Description: "留空/local = 关闭：图片单独一条、文字单独一条。" +
					"填 qiniu / s3 / aliyun / cos 则先把图传到你的对象存储拿公网 URL，" +
					"再用 Markdown 把**图和文合成一条**发出" +
					"（QQ 的富媒体消息 msg_type=7 与正文 content 互斥，只有 markdown 能混排）。" +
					"字段与 xbot 的 Storage Provider 一致：qiniu 用 qiniu_ak/sk/bucket/domain，" +
					"s3 系用 s3_ak/sk/bucket/region（aliyun/cos 可只填 region，endpoint 自动推导）。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:   KeyCDNPublicBase,
				Label: "CDN 公共访问前缀（可选）",
				Description: "覆盖生成 URL 的前缀，例如 https://cdn.example.com。" +
					"留空时：qiniu 用 qiniu_domain；s3 用 endpoint；aliyun/cos 用 bucket.endpoint。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyQiniuAK,
				Label:        "七牛 AccessKey",
				Description:  "provider=qiniu 时必填。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyQiniuSK,
				Label:        "七牛 SecretKey",
				Description:  "provider=qiniu 时必填。建议用 0600 文件注入，不要明文提交仓库。",
				Type:         "password",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyQiniuBucket,
				Label:        "七牛 Bucket",
				Description:  "provider=qiniu 时必填。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyQiniuDomain,
				Label:        "七牛访问域名",
				Description:  "provider=qiniu 时必填，如 https://cdn.example.com。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyQiniuRegion,
				Label:        "七牛 Region（可选）",
				Description:  "七牛区域标识（z0/cn-east-1 等）。用自定义域名时可留空。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyS3AK,
				Label:        "S3 AccessKey",
				Description:  "provider=s3/aliyun/cos 时必填。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyS3SK,
				Label:        "S3 SecretKey",
				Description:  "provider=s3/aliyun/cos 时必填。",
				Type:         "password",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:          KeyS3Bucket,
				Label:        "S3 Bucket",
				Description:  "provider=s3/aliyun/cos 时必填。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:   KeyS3Region,
				Label: "S3 Region",
				Description: "如 cn-hangzhou（aliyun）/ ap-guangzhou（cos）/ us-east-1（AWS）。" +
					"provider=aliyun/cos 时用它推导 endpoint。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
			},
			{
				Key:   KeyS3Endpoint,
				Label: "S3 Endpoint（可选，优先级最高）",
				Description: "显式 endpoint，用于 MinIO / Cloudflare R2 / 自建网关。" +
					"填了就忽略 region 推导。",
				Type:         "text",
				DefaultValue: "",
				Category:     "发送",
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
