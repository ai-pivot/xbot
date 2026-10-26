package main

// 哪些错误"重试没用" —— 决定重连循环该退还是该停。
//
// 背景（2026-10-26 真机联调事故）：出口 IP 没进白名单，每次取 gateway URL 都
// 返回 401；插件按"渠道要长期自愈"的默认策略 1s→2s→4s…→1m 无限重连，短时间内
// 打出十几次请求，把平台从 401 打成 100017（接口调用超过频率限制）。
//
// 这是**产品缺陷**，不是"重试得不够努力"：
//   - IP 白名单只能由人去开放平台加，重试一万次也不会变绿；
//   - 限流一旦触发，继续按分钟级重试会延长封禁；
//   - 两条都会烧被动消息配额，用户红线。
//
// 所以：这两类错误 + 凭据错误一律**立即停止重连并给出可执行提示**，
// 由运维/用户修好外部条件后重新加载插件（而不是让进程默默刷接口）。

import (
	"errors"
	"fmt"
	"strings"
)

// TokenHTTPError 是换 access_token 时的非 200 响应（结构化，便于分类）。
type TokenHTTPError struct {
	URL    string
	Status int
	Body   string
}

func (e *TokenHTTPError) Error() string {
	return fmt.Sprintf("getAppAccessToken(%s): HTTP %d: %s", e.URL, e.Status, e.Body)
}

// IsFatal 报告这个 token 错误是否"重试无用"。
//
// 4xx 基本都是配置问题（app_id / app_secret 错、IP 不在白名单、被限流）；
// 5xx 与网络错误留给调用方重试。
func (e *TokenHTTPError) IsFatal() bool {
	if e == nil {
		return false
	}
	return e.Status >= 400 && e.Status < 500
}

// FatalGatewayReason 判断一个网关错误是否"重试无用"。
//
// 返回 (true, hint) 表示调用方必须停止重连；hint 是给运维看的一句话，
// 里面必须写清"谁能解、怎么解"。
func FatalGatewayReason(err error) (bool, string) {
	if err == nil {
		return false, ""
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.IsIPNotAllowed():
			// 2026-10-26 真机定论：11298/40023002 在"应用未发布"场景下也会出现，
			// 且加白名单无效 —— 只有沙箱域名通。所以提示必须先指向沙箱开关。
			return true, "（应用可能在**沙箱态**：HTTP 401 + 11298/40023002 常见于" +
				"应用未在开放平台发布、却访问了正式域名 api.bot.qq.com；" +
				"把 channels.qq.sandbox 打开（base 切到 sandbox.api.sgroup.qq.com）即可连，" +
				"或去 QQ 开放平台发布应用后再关掉这个开关。" +
				"若确实已发布，则去 开发设置 → IP 白名单 加上本机出口 IP。" +
				"本插件已停止重连，改好后 reload 插件即可恢复）"
		case apiErr.IsRateLimited():
			return true, "（接口调用超过频率限制 100017：继续重试只会加重风控；" +
				"本插件已停止重连，等平台限流窗口过去后 reload 插件）"
		}
		// 401/403 但不是白名单/沙箱码：凭据或权限问题，同样重试无用。
		if apiErr.Status == 401 || apiErr.Status == 403 {
			return true, "（鉴权失败：检查 app_id / app_secret 与机器人权限；" +
				"若应用尚未发布，确认 channels.qq.sandbox 已打开（正式域名对未发布应用一律 401）；" +
				"本插件已停止重连）"
		}
		return false, ""
	}

	var tokErr *TokenHTTPError
	if errors.As(err, &tokErr) {
		if tokErr.IsFatal() {
			return true, "（换 access_token 被拒：检查 app_id / app_secret 是否正确、IP 是否在白名单；" +
				"本插件已停止重连）"
		}
		return false, ""
	}

	// 纯文本兜底：历史路径里可能仍有字符串化的错误。
	msg := err.Error()
	switch {
	case strings.Contains(msg, "白名单"):
		return true, "（出口 IP 不在白名单：需要用户去 QQ 开放平台加白名单；本插件已停止重连）"
	case strings.Contains(msg, "频率限制"):
		return true, "（接口调用超过频率限制：继续重试会加重风控；本插件已停止重连）"
	}
	return false, ""
}
