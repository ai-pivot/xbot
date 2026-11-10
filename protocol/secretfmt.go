package protocol

// secretfmt.go — 固定格式凭据识别（跨渠道共用）。
//
// 2026-11-07 用户裁定后的唯一形态：
//   - 只认**有精确长度/字符集的发行方格式**（GitHub PAT、OpenAI/Anthropic key、
//     AWS AK、Slack、Google API key、GitLab PAT、JWT）。
//   - 不做 key=value / --flag / Bearer 上下文推断 —— 那些全是误杀
//     （pwd=*** <path>、signature: <fn>、session_id=<id> 都曾被无辜打码）。
//
// 调用位置有严格要求：**只能用于「工具输出物 → 前端展示」的出口**
// （web SSE 的 ToolProgress.Summary/Detail、飞书卡片的面板正文）。
// 模型自己的文本、模型写的工具参数、落库的历史/记忆，都必须保持原文 ——
// 模型需要真值才能工作。
import "regexp"

var fixedFormSecretRes = []*regexp.Regexp{
	// GitHub PAT (classic): ghp_ + 36 位
	regexp.MustCompile(`(^|[^A-Za-z0-9_])(ghp_[A-Za-z0-9]{36,})`),
	// GitHub fine-grained PAT: github_pat_ + 82 位（官方规格）
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}`),
	// GitHub oauth / user-to-server / server-to-server / refresh: gh[ousr]_ + 36 位
	regexp.MustCompile(`(^|[^A-Za-z0-9_])(gh[ousr]_[A-Za-z0-9]{36,})`),
	// OpenAI / Anthropic: sk- + 至少 32 位（含 sk-proj- / sk-ant-api03-）
	//
	// ⚠️ 左边界不能只用 \b：\b 只挡字母数字前缀，挡不住**连字符 / 斜杠**后面紧跟
	// 的 `sk-`（2026-11-10 CR 第 2 轮 P1 实测误杀：
	//   `task-sk-550e8400e29b41d4a716446655440000abcd` → `task-***`
	//   `/opt/models/sk-abcdefghijklmnopqrstuvwxyz012345/weights.bin` → `/opt/models/***/...`
	// 用户裁定「误杀一次都不能有」。改为索引式左边界：前一个字符必须**不属于**
	// `[A-Za-z0-9_/-]`（字母数字、下划线、连字符、斜杠都算"在标识符/路径内部"）。
	// 用捕获组保留前导字符，替换时回落 $1。
	regexp.MustCompile(`(^|[^A-Za-z0-9_/-])(sk-[A-Za-z0-9_-]{32,})`),
	// AWS access key id（长期 AKIA / 临时 ASIA）+ 16 位
	regexp.MustCompile(`(^|[^0-9A-Z])((?:AKIA|ASIA)[0-9A-Z]{16,})`),
	// Slack: xox[baprs]- + 至少 10 位
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}`),
	// Google API key: AIza + 35 位
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),
	// GitLab PAT: glpat- + 至少 20 位
	regexp.MustCompile(`\bglpat-[0-9A-Za-z_-]{20,}`),
	// JWT: header.payload.signature（base64url，header 段固定 eyJ 开头）
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// RedactFixedFormats 把固定格式凭据替换为 ***；其余文本原样返回。
func RedactFixedFormats(s string) string {
	if s == "" {
		return s
	}
	for _, re := range fixedFormSecretRes {
		// 带捕获组的模式（索引式左边界）：保留 $1（前导字符），只打码凭据本身。
		// 无捕获组的模式 $1 为空串，等价于整体替换。
		s = re.ReplaceAllString(s, "${1}***")
	}
	return s
}
