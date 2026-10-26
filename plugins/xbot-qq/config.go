package main

import (
	"fmt"
	"net/url"
	"strings"
)

// channels.qq.* 的配置键。host 侧把它们作为 map[string]string 推给插件
// （config.json → channels.qq，见 config.Channels）。
const (
	KeyEnabled     = "enabled"
	KeyWSURL       = "ws_url"
	KeyAccessToken = "access_token"
	KeyAllow       = "allow"
	KeyAdmins      = "admins"
)

// Config 是 QQ 渠道的运行时配置。
//
// 三个字段组：
//   - 连接：WSURL / AccessToken（OneBot v11 的 WS 地址与 token）；
//   - 准入：Allow（白名单，支持精确项与通配项，空 = 谁都不处理，"*" = 全部处理）；
//   - 权限：Admins（管理员名单，支持同样的通配语义）。
type Config struct {
	Enabled     bool
	WSURL       string
	AccessToken string
	Allow       Patterns
	Admins      Patterns
}

// ParseConfig 把 host 推来的字符串键值解析成 Config。
//
// 解析是**宽松**的：未知键忽略，格式问题不报错（Enabled 用宽松布尔，
// 空白项丢弃）。这样配置写错一个键不会让渠道起不来 —— 只有 Validate 才做
// 严格检查，由调用方决定"报错还是带病运行"。
func ParseConfig(raw map[string]string) Config {
	get := func(key string) string { return strings.TrimSpace(raw[key]) }
	return Config{
		Enabled:     parseBool(get(KeyEnabled)),
		WSURL:       get(KeyWSURL),
		AccessToken: get(KeyAccessToken),
		Allow:       ParsePatterns(get(KeyAllow)),
		Admins:      ParsePatterns(get(KeyAdmins)),
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

// Validate 做严格检查：返回非 nil 表示这份配置不能安全启用。
//
// 只有"启用时"才要求连接参数齐全 —— 装好插件但暂时关着是合法状态，
// 不该报错（否则每次启动都刷一条无效告警）。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.WSURL == "" {
		return fmt.Errorf("channels.qq.%s is required when %s=true", KeyWSURL, KeyEnabled)
	}
	u, err := url.Parse(c.WSURL)
	if err != nil {
		return fmt.Errorf("channels.qq.%s %q is not a valid URL: %w", KeyWSURL, c.WSURL, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss", "http", "https":
	default:
		return fmt.Errorf("channels.qq.%s %q has unsupported scheme %q (want ws/wss/http/https)",
			KeyWSURL, c.WSURL, u.Scheme)
	}
	return nil
}

// Redacted 返回可安全写进日志的配置快照（token 只留长度，避免泄露）。
func (c Config) Redacted() map[string]string {
	token := ""
	if c.AccessToken != "" {
		token = fmt.Sprintf("<%d chars>", len(c.AccessToken))
	}
	return map[string]string{
		KeyEnabled:     fmt.Sprintf("%t", c.Enabled),
		KeyWSURL:       c.WSURL,
		KeyAccessToken: token,
		KeyAllow:       c.Allow.String(),
		KeyAdmins:      c.Admins.String(),
	}
}

// Summary 返回一行日志摘要。
func (c Config) Summary() string {
	return fmt.Sprintf("enabled=%t ws=%q allow=%s(%d) admins=%s(%d)",
		c.Enabled, c.WSURL, c.Allow, c.Allow.Len(), c.Admins, c.Admins.Len())
}
