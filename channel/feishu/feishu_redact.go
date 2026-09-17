package feishu

// feishu_redact.go — 飞书侧**工具脱敏**（用户 2026-09-17：「飞书加一个工具脱敏功能，
// 自动去掉所有敏感内容」）。
//
// 飞书是外部渠道：工具参数/结果里经常带着真实凭据（GH_TOKEN=ghp_… 的 shell 命令、
// FileCreate 写出的含密脚本、curl 的 URL token、Bearer 头…），这些内容会进 CoT 的
// title/args/result 与卡片的工具面板 —— 必须在**出口**统一打码。普通内容（路径、
// 代码、中文、计数）原样保留，脱敏宁多勿漏但不得毁掉可读性。
//
// 作用面（全部经 redactSensitive 出口）：
//   - 原生 CoT：TOOL_CALL_START.title / TOOL_CALL_ARGS.delta / TOOL_CALL_RESULT.code
//   - CardKit 卡片：toolHeaderArg（折叠标题）+ toolDetailFull（面板正文）
//
// 实现 = 三层正则（全部包级编译，绝不每帧重编译）：
//  1. key=value / key:value / "key":"value" / --flag value（键名含敏感词）
//  2. Bearer <token>
//  3. 高置信 token 格式（sk-/ghp_/xox/AKIA/JWT…，无需键名上下文）
// 误伤防护：值短于 4、纯数字（token 计数）、字面量 Bearer 保持原样。

import (
	"regexp"
	"strings"
)

// kvForm 匹配「键名含敏感词」的赋值：JSON（"api_key": "v"）、shell（KEY=v）、
// YAML（key: v）、URL query（?token=v&…）。键名允许常见前后缀（OPENAI_API_KEY、
// DB_PASSWORD、access-key…），值支持单双引号/裸词。
var redactKvRe = regexp.MustCompile(`(?i)("?[A-Za-z0-9_-]*(?:api[_-]?key|apikey|secret|token|passwd|password|pwd|authorization|credential|access[-_]?key(?:id)?|private[-_]?key|client[_-]?secret|session[_-]?key|session[_-]?id|signature|cookie)"?\s*[:=]\s*)("([^"\\]|\\.)*"|'[^']*'|[^\s,;&}\]]+)`)

// flagForm 匹配 CLI 空格分隔形态：--api-key <v> / --password <v>（值以空格分隔，
// 裸键名无法与行文区分，因此要求 --/-- 前缀）。
var redactFlagRe = regexp.MustCompile(`(?i)((?:--?)[A-Za-z0-9_-]*(?:api[_-]?key|apikey|secret|token|passwd|password|pwd|credential|access[-_]?key(?:id)?|private[-_]?key|client[_-]?secret)[A-Za-z0-9_-]*)(\s+)("[^"]*"|'[^']*'|[^\s,;&}\]]+)`)

// redactBearerRe 匹配 Bearer 头。
var redactBearerRe = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{10,}`)

// redactTokenRe 匹配高置信 token 格式（无键名也能认出）。
var redactTokenRe = regexp.MustCompile(`(sk-(?:ant-|proj-)?[A-Za-z0-9_-]{10,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{10,})`)

// redactSensitive 把 s 里的敏感内容打码为 ***（引号风格保留）。
func redactSensitive(s string) string {
	if s == "" {
		return ""
	}
	s = redactKvRe.ReplaceAllStringFunc(s, redactKVMatch)
	s = redactFlagRe.ReplaceAllStringFunc(s, redactFlagMatch)
	s = redactBearerRe.ReplaceAllString(s, "${1}***")
	return redactTokenRe.ReplaceAllString(s, "***")
}

// maskLike 按被掩值的引号风格输出 ***（保住 JSON/圆串的可读性）。
func maskLike(v string) string {
	if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
		return `"***"`
	}
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2 {
		return "'***'"
	}
	return "***"
}

// benignValue 报告「看起来不是秘密」的值：短值、纯数字（token 计数）、字面量
// Bearer —— 这些保留原文，避免把 total_tokens=5 之类的正常输出打成 ***。
func benignValue(v string) bool {
	t := strings.Trim(v, "\"'")
	if len(t) < 4 {
		return true
	}
	if strings.EqualFold(t, "bearer") {
		return true
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func redactKVMatch(m string) string {
	sub := redactKvRe.FindStringSubmatch(m)
	// ⚠️ 组结构：(1)键+分隔符 (2)值（其首分支 "…" 内还有嵌套捕获组）——值是 sub[2]。
	if len(sub) < 3 || benignValue(sub[2]) {
		return m
	}
	return sub[1] + maskLike(sub[2])
}

func redactFlagMatch(m string) string {
	sub := redactFlagRe.FindStringSubmatch(m)
	if len(sub) < 4 || benignValue(sub[3]) {
		return m
	}
	return sub[1] + sub[2] + maskLike(sub[3])
}
