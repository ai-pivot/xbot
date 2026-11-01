// Package redact 提供**写入侧**的凭据脱敏：把高置信秘密替换为 ***，其余文本逐字保留。
//
// 规则来源：channel/feishu/feishu_redact.go 已验证的三层正则（key=value / --flag value /
// Bearer / 高置信 token 格式）。本包按兵部 ORDER（
// court/evidence/bingbu/redact-write-20261101/ORDER.md §1）与工部施工单
// （court/evidence/gongbu/restart-20261101/41-redact-spec.md §1.1）的差异点收紧三处：
//
//  1. 高置信 token 形态的长度阈值 10 → **20**（feishu 是 10；本包用 20，避免把
//     `sk-learn` 这类普通词当凭据）。
//  2. 增加**左边界**判定（ORDER §4.1 的假阳性：`task-frontend-cancel-reason` 的子串
//     `sk-frontend-…` 不是 key）。Go 的 regexp 是 RE2，**不支持 lookbehind**
//     （`(?<![A-Za-z0-9_-])` 会 panic: invalid or unsupported Perl syntax），因此左边界
//     只能用「索引式」实现：FindAllStringIndex 后判断 match 前一个字节是否属于
//     [A-Za-z0-9_-]，再用 strings.Builder 拼回。
//  3. benignValue 在 feishu 的「短值 / 纯数字 / 字面量 Bearer」之外，再加 **JSON 裸标量**
//     （true/false/null/数字）。原因：本包会被用在 iteration_history.tools 这类 JSON 列上，
//     把 `{"secret":true}` 的值打成 *** 会让整列 JSON 解析失败 —— 写脏数据比漏打码更糟。
//     （`total_tokens=5` 这类正常输出也靠这条保住。）
//
// 幂等：替换产物 *** 不匹配任何规则，因此 Secrets(Secrets(x)) == Secrets(x)。
package redact

import (
	"regexp"
	"strings"
)

// mask 是统一的替换产物（与 feishu 侧一致）。
const mask = "***"

// kvRe 匹配「键名含敏感词」的赋值：JSON（"api_key": "v"）、shell（KEY=v）、
// YAML（key: v）、URL query（?token=v&…）。键名允许常见前后缀（OPENAI_API_KEY、
// DB_PASSWORD、access-key…），值支持单双引号/裸词。
//
// 键名部分与 feishu_redact.go 的 redactKvRe 逐字一致（来源：channel/feishu/feishu_redact.go）。
//
// ⚠️ 裸值分支 `[^\s,;&}\]"'\\]+` 是本包相对 feishu 的**必要收紧**：feishu 只做展示层
// 打码，吃掉一个引号/反斜杠不致命；本包写在**持久层**（session_messages.tool_arguments
// 是 JSON 列），若裸值把结尾的 `'"` 一起吞掉，落库的就是 `{"cmd":"curl -H 'api_key=***}`
// ——非法 JSON，前端/回放直接解析失败。排除 `"` `'` `\\` 后，被吃掉的只是凭据本身，
// JSON 结构与转义保持配平。
var kvRe = regexp.MustCompile(`(?i)("?[A-Za-z0-9_-]*(?:api[_-]?key|apikey|secret|token|passwd|password|pwd|authorization|credential|access[-_]?key(?:id)?|private[-_]?key|client[_-]?secret|session[_-]?key|session[_-]?id|signature|cookie)"?\s*[:=]\s*)("([^"\\]|\\.)*"|'[^']*'|[^\s,;&}\]"'\\]+)`)

// flagRe 匹配 CLI 空格分隔形态：--api-key <v> / --password <v>（裸键名无法与行文
// 区分，因此要求 -/-- 前缀）。与 feishu_redact.go 的 redactFlagRe 一致。
var flagRe = regexp.MustCompile(`(?i)((?:--?)[A-Za-z0-9_-]*(?:api[_-]?key|apikey|secret|token|passwd|password|pwd|credential|access[-_]?key(?:id)?|private[-_]?key|client[_-]?secret)[A-Za-z0-9_-]*)(\s+)("[^"]*"|'[^']*'|[^\s,;&}\]"'\\]+)`)

// bearerRe 匹配 Bearer 头。阈值按本单规格取 20（feishu 是 10）。
var bearerRe = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{20,}`)

// tokenRe 匹配高置信 token 格式（无键名上下文也能认出）。
//
// 与 feishu_redact.go 的 redactTokenRe 同构，差异只有长度阈值：
// sk-/gh*_/github_pat_/xox 由 10 提到 20；AKIA（AWS access key id）固定 16 位、
// JWT（eyJ…）各段长度沿用 feishu 的已验证值。
//
// 左边界**不在正则里**（RE2 不支持 lookbehind），由 maskTokens 逐 match 判定。
var tokenRe = regexp.MustCompile(`(sk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{10,})`)

// Secrets 把高置信凭据替换为 ***，其余文本原样保留；幂等。
//
// 顺序与 feishu 侧一致：先按键名上下文（键值对 / CLI flag / Bearer），再按 token 形态。
// 先键名后形态的好处是 `api_key=sk-…` 只会被第一条规则改一次（值整体 → ***）。
func Secrets(s string) string {
	if s == "" {
		return ""
	}
	s = kvRe.ReplaceAllStringFunc(s, maskKV)
	s = flagRe.ReplaceAllStringFunc(s, maskFlag)
	s = bearerRe.ReplaceAllString(s, "${1}"+mask)
	return maskTokens(s)
}

// maskTokens 按 token 形态打码，并施加左边界判据。
func maskTokens(s string) string {
	locs := tokenRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	changed := false
	for _, loc := range locs {
		if !hasTokenLeftBoundary(s, loc[0]) {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(mask)
		last = loc[1]
		changed = true
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// hasTokenLeftBoundary 报告 s[start:] 处的 token 是否**独立成词**：前一个字节
// 不属于 [A-Za-z0-9_-]（多字节 UTF-8 的首字节 >0x7f 天然满足）。
//
// 反例（必须是 false）：task-frontend-cancel-reason 里的 sk-frontend-cancel-reason…
// 正例（必须是 true）：x=sk-… / 空格后的 sk-… / 行首 sk-…
func hasTokenLeftBoundary(s string, start int) bool {
	if start <= 0 {
		return true
	}
	switch c := s[start-1]; {
	case c >= 'a' && c <= 'z':
		return false
	case c >= 'A' && c <= 'Z':
		return false
	case c >= '0' && c <= '9':
		return false
	case c == '_' || c == '-':
		return false
	default:
		return true
	}
}

// maskLike 按被掩值的引号风格输出 ***（保住 JSON/圆串的可读性）。
func maskLike(v string) string {
	if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
		return `"` + mask + `"`
	}
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2 {
		return "'" + mask + "'"
	}
	return mask
}

// benignValue 报告「看起来不是秘密」的值：短值、字面量 Bearer、JSON 裸标量
// （true/false/null/数字）—— 这些保留原文，避免把 total_tokens=5 之类的正常输出
// 打成 ***（也避免把 iteration_history.tools 这类 JSON 列打成非法 JSON）。
func benignValue(v string) bool {
	t := strings.Trim(v, "\"'")
	if len(t) < 4 {
		return true
	}
	if strings.EqualFold(t, "bearer") {
		return true
	}
	return isBareScalar(t)
}

// isBareScalar 报告 t 是否是 JSON 里**不带引号**的标量（true/false/null/数字）。
func isBareScalar(t string) bool {
	switch t {
	case "true", "false", "null":
		return true
	}
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
		case r == '-' || r == '+' || r == '.' || r == 'e' || r == 'E':
		default:
			return false
		}
	}
	return true
}

func maskKV(m string) string {
	sub := kvRe.FindStringSubmatch(m)
	// 组结构：(1)键+分隔符 (2)值（其首分支 "…" 内还有嵌套捕获组）——值是 sub[2]。
	if len(sub) < 3 || benignValue(sub[2]) {
		return m
	}
	return sub[1] + maskLike(sub[2])
}

func maskFlag(m string) string {
	sub := flagRe.FindStringSubmatch(m)
	if len(sub) < 4 || benignValue(sub[3]) {
		return m
	}
	return sub[1] + sub[2] + maskLike(sub[3])
}
