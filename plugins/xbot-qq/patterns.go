package main

import "strings"

// Patterns 是一组访问控制模式 —— 白名单（allow）与管理员名单（admins）
// 共用同一套匹配语义。
//
// 两种形态：
//   - 精确项：全串相等才算命中，例如 "10001"、"group:123456"；
//   - 通配项：含 '*'（任意长度的任意字符序列，含空）或 '?'（恰好一个字符），
//     例如 "*"、"group:*"、"1000?"。
//
// 空 Patterns **不匹配任何输入**（fail-closed）：漏配白名单的后果是"渠道不工作"，
// 而不是"渠道对所有人开放"。要显式全开放必须写 "*"。
type Patterns struct {
	items []string
}

// NewPatterns 由已切分好的条目构造（空白条目被丢弃）。
func NewPatterns(items ...string) Patterns {
	cleaned := make([]string, 0, len(items))
	for _, it := range items {
		if v := strings.TrimSpace(it); v != "" {
			cleaned = append(cleaned, v)
		}
	}
	return Patterns{items: cleaned}
}

// patternSeparators 是配置里可用的分隔符：用户从文档里复制列表时常见
// 逗号 / 中文逗号 / 分号 / 空格 / 制表符 / 换行混用，全部接受。
const patternSeparators = ",，;； \t\r\n"

// ParsePatterns 把一段配置文本切分成 Patterns。
//
// 例：`ParsePatterns("10001, group:123*，*（测试）")` → ["10001","group:123*","*（测试）"]
//
// QQ 号 / 群号 / 群名片里都不会出现空白，所以按空白切分是安全的。
func ParsePatterns(text string) Patterns {
	return NewPatterns(strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune(patternSeparators, r)
	})...)
}

// Len 报告模式条目数。
func (p Patterns) Len() int { return len(p.items) }

// Empty 报告是否没有任何模式（= 谁都不匹配）。
func (p Patterns) Empty() bool { return len(p.items) == 0 }

// List 返回条目副本（调用方修改不会影响 Patterns）。
func (p Patterns) List() []string {
	out := make([]string, len(p.items))
	copy(out, p.items)
	return out
}

// AllowsAll 报告是否包含全通配 "*"（即"所有消息都处理"）。
func (p Patterns) AllowsAll() bool {
	for _, it := range p.items {
		if it == "*" {
			return true
		}
	}
	return false
}

// Match 报告 subject 是否命中任一条目。
//
// 空 Patterns 返回 false；含 "*" 时任何非空输入都返回 true。
// subject 为空时一律返回 false —— 没有身份的消息不做准入判断（无法审计）。
func (p Patterns) Match(subject string) bool {
	if subject == "" {
		return false
	}
	for _, it := range p.items {
		if it == subject || globMatch(it, subject) {
			return true
		}
	}
	return false
}

// String 返回便于日志输出的形式，例如 `[10001 group:*]`。
func (p Patterns) String() string {
	if len(p.items) == 0 {
		return "[]"
	}
	return "[" + strings.Join(p.items, " ") + "]"
}

// globMatch 用 '*'（任意长度，含空）与 '?'（恰好一个字符）匹配整串。
//
// 双指针回溯实现：遇到 '*' 时记住位置，失败就退回让 '*' 多吃一个字符，
// 因此最坏 O(len(pattern)*len(subject))，无正则编译开销、无递归。
// 其余字符（含 '[' ']' '.' '+'）一律按字面量比较 —— 和白名单里写 QQ 号一样直觉。
func globMatch(pattern, subject string) bool {
	pat := []rune(pattern)
	sub := []rune(subject)

	pi, si := 0, 0
	star, mark := -1, 0

	for si < len(sub) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == sub[si]):
			pi++
			si++
		case pi < len(pat) && pat[pi] == '*':
			star = pi
			mark = si
			pi++
		case star >= 0:
			// 回溯：让上一个 '*' 多吃一个字符，重新尝试。
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}
