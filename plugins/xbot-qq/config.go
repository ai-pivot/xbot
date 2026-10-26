package main

import (
	"fmt"
	"strings"
)

// channels.qq.* 的配置键。host 侧把它们作为 map[string]string 推给插件
// （config.json → channels.qq，见 config.Channels）。
//
// 这里只保留 QQ 官方开放平台（bot.q.qq.com）的凭据：app_id + app_secret。
// OneBot / NapCat 那套"用户自建反向 WS + 静态 token"的模型已作废 ——
// 官方是按 app_id 换 token、再按 openid 收发消息，没有第三方网关这一层。
const (
	KeyEnabled   = "enabled"
	KeyAppID     = "app_id"
	KeyAppSecret = "app_secret"
	KeyAllow     = "allow"
	KeyAdmins    = "admins"
)

// Config 是 QQ 渠道的运行时配置。
//
// 三个字段组：
//   - 凭据：AppID / AppSecret（QQ 开放平台机器人 → 开发设置）；
//   - 准入：Allow（白名单，匹配 openid，支持精确项与通配项，
//     空 = 谁都不处理，"*" = 全部处理）；
//   - 权限：Admins（管理员名单，同样匹配 openid、同样的通配语义）。
type Config struct {
	Enabled   bool
	AppID     string
	AppSecret string
	Allow     Patterns
	Admins    Patterns
}

// ParseConfig 把 host 推来的字符串键值解析成 Config。
//
// 解析是**宽松**的：未知键忽略，格式问题不报错（Enabled 用宽松布尔，
// 空白项丢弃）。这样配置写错一个键不会让渠道起不来 —— 只有 Validate 才做
// 严格检查，由调用方决定"报错还是带病运行"。
func ParseConfig(raw map[string]string) Config {
	get := func(key string) string { return strings.TrimSpace(raw[key]) }
	return Config{
		Enabled:   parseBool(get(KeyEnabled)),
		AppID:     get(KeyAppID),
		AppSecret: get(KeyAppSecret),
		Allow:     ParsePatterns(get(KeyAllow)),
		Admins:    ParsePatterns(get(KeyAdmins)),
	}
}

// parseBool 接受 toggle 组件可能写出的各种"真"。
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "y", "t":
		return true
	default:
		return false
	}
}

// Policy 由配置里的白名单与管理员名单构造访问策略。
func (c Config) Policy() Policy {
	return NewPolicy(c.Allow, c.Admins)
}

// CredentialsReady 报告换 token 所需的两项凭据是否都在。
func (c Config) CredentialsReady() bool {
	return c.AppID != "" && c.AppSecret != "" && looksLikeAppID(c.AppID)
}

// Validate 做严格检查：返回非 nil 表示这份配置不能安全启用。
//
// 只有"启用时"才要求凭据齐全 —— 装好插件但暂时关着是合法状态，
// 不该报错（否则每次启动都刷一条无效告警）。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.AppID == "" {
		return fmt.Errorf("channels.qq.%s is required when %s=true", KeyAppID, KeyEnabled)
	}
	if !looksLikeAppID(c.AppID) {
		return fmt.Errorf("channels.qq.%s %q is not a numeric app id "+
			"(copy it from QQ 开放平台 → 开发设置)", KeyAppID, c.AppID)
	}
	if c.AppSecret == "" {
		return fmt.Errorf("channels.qq.%s is required when %s=true", KeyAppSecret, KeyEnabled)
	}
	return nil
}

// looksLikeAppID 检查 app_id 是否为纯数字。QQ 开放平台的 AppID 是数字串，
// 这一步专门拦"把 clientSecret 粘到 app_id 里"这类错位（否则换 token 会
// 收到一个语焉不详的 400，排查成本远高于这里报错）。
func looksLikeAppID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Redacted 返回可安全写进日志的配置快照（secret 只留长度，避免泄露）。
func (c Config) Redacted() map[string]string {
	return map[string]string{
		KeyEnabled:   fmt.Sprintf("%t", c.Enabled),
		KeyAppID:     c.AppID,
		KeyAppSecret: redactedSecret(c.AppSecret),
		KeyAllow:     c.Allow.String(),
		KeyAdmins:    c.Admins.String(),
	}
}

// Summary 返回一行日志摘要（绝不含 secret 原文）。
func (c Config) Summary() string {
	return fmt.Sprintf("enabled=%t app_id=%s secret=%s allow=%s(%d) admins=%s(%d)",
		c.Enabled, c.AppID, redactedSecret(c.AppSecret),
		c.Allow, c.Allow.Len(), c.Admins, c.Admins.Len())
}

func redactedSecret(s string) string {
	if s == "" {
		return "<empty>"
	}
	return fmt.Sprintf("<%d chars>", len(s))
}
