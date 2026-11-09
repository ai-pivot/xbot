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
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{36}`),
	// GitHub fine-grained PAT: github_pat_ + 82 位（官方规格）
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}`),
	// GitHub oauth / user-to-server / server-to-server / refresh: gh[ousr]_ + 36 位
	regexp.MustCompile(`\bgh[ousr]_[A-Za-z0-9]{36}`),
	// OpenAI / Anthropic: sk- + 至少 32 位（含 sk-proj- / sk-ant-api03-）
	// ⚠️ 左边界必须保留：没有 \b 时 `task-<32charId>` / `disk-<uuid>` / `mask-...`
	// 里的 `sk-` 子串会把普通文件名/变量名当 OpenAI key 打码（2026-11-07 实锤：
	// `$(cat /tmp/task-...)` 被替换成 ***）。
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{32,}`),
	// AWS access key id（长期 AKIA / 临时 ASIA）+ 16 位
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}`),
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
		s = re.ReplaceAllString(s, "***")
	}
	return s
}
