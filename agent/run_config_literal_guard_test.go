package agent

// run_config_literal_guard_test —— 「构造点穷举」守卫：
// **每一个 RunConfig 复合字面量都必须显式声明 reminder 策略**（`DisableSystemReminder:`），
// 否则测试红，并打印是哪个 file:line 漏了。
//
// 为什么需要它（2026-10-28 事故）：
//   首轮真注入落地后，压缩摘要（内部一次性 Run）的请求尾部多出一对
//   assistant(tool_call)+tool，破坏了「最后一条消息 = 压缩指令（user）」的契约，
//   5 条 TestCompactMessages_* 同时变红。修法是给 compressCfg 加显式
//   `DisableSystemReminder: true`。但只要有人**以后再加一个内部 Run 而忘记写这个字段**，
//   同样的破坏会再来一次 —— 而且可能以更难定位的方式出现。
//
// 为什么是「穷举构造点」而不是 `MaxIterations > 0` / `AgentID == "compressor"` 推断：
//   隐式推断在新增内部 Run 时**静默生效或静默失效**，没人会红；显式字段漏写则本用例立刻红。
//   MaxIterations（轮次上限）与 DisableSystemReminder（reminder 策略）是正交的两件事，
//   用前者推后者等于把两个契约焊死。
//
// 变异自证（写进证据 guard-mutation.txt）：
//   删掉 agent/compress.go 里 `DisableSystemReminder: true` → 本用例必须 FAIL 并指出该行；
//   还原后 sha256 一致、用例重新 PASS。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// maskGoSource 把字符串字面量与注释替换成等长空格，用于在「纯代码」上做括号匹配与关键字查找。
// 这样注释里出现的 `RunConfig{` 或字符串里的 `DisableSystemReminder:` 都不会误判。
func maskGoSource(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	const (
		stCode = iota
		stLineComment
		stBlockComment
		stString
		stRawString
		stRune
	)
	state := stCode
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch state {
		case stCode:
			switch {
			case c == '/' && i+1 < len(out) && out[i+1] == '/':
				state = stLineComment
				out[i] = ' '
			case c == '/' && i+1 < len(out) && out[i+1] == '*':
				state = stBlockComment
				out[i] = ' '
			case c == '"':
				state = stString
				out[i] = ' '
			case c == '`':
				state = stRawString
				out[i] = ' '
			case c == '\'':
				state = stRune
				out[i] = ' '
			}
		case stLineComment:
			if c == '\n' {
				state = stCode
			} else {
				out[i] = ' '
			}
		case stBlockComment:
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = stCode
			} else {
				out[i] = ' '
			}
		case stString:
			if c == '\\' && i+1 < len(out) {
				out[i], out[i+1] = ' ', ' '
				i++
			} else if c == '"' {
				state = stCode
				out[i] = ' '
			} else {
				out[i] = ' '
			}
		case stRawString:
			if c == '`' {
				state = stCode
				out[i] = ' '
			} else {
				out[i] = ' '
			}
		case stRune:
			if c == '\\' && i+1 < len(out) {
				out[i], out[i+1] = ' ', ' '
				i++
			} else if c == '\'' {
				state = stCode
				out[i] = ' '
			} else {
				out[i] = ' '
			}
		}
	}
	return out
}

// matchBrace 返回 open（指向 '{'）对应的 '}' 下标；找不到返回 -1。
func matchBrace(masked []byte, open int) int {
	depth := 0
	for i := open; i < len(masked); i++ {
		switch masked[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func lineOf(src []byte, off int) int {
	return strings.Count(string(src[:off]), "\n") + 1
}

// TestRunConfigLiteralsDeclareReminderPolicy 穷举 agent 包内所有 RunConfig 复合字面量，
// 要求每个**非空**字面量都显式写出 `DisableSystemReminder:`（true 或 false 均可）。
func TestRunConfigLiteralsDeclareReminderPolicy(t *testing.T) {
	needle := "Run" + "Config{"
	policyField := "Disable" + "SystemReminder:"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(files)

	type site struct {
		file string
		line int
	}
	var checked []site
	var missing []site

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue // 测试内的构造点由各自用例负责，不参与交付代码的策略穷举
		}
		if f == "run_config_literal_guard_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		masked := maskGoSource(src)
		for off := 0; ; {
			idx := strings.Index(string(masked[off:]), needle)
			if idx < 0 {
				break
			}
			start := off + idx
			open := start + len(needle) - 1
			end := matchBrace(masked, open)
			if end < 0 {
				t.Fatalf("%s:%d: RunConfig 字面量括号不匹配（测试自身实现有 bug）", f, lineOf(src, start))
			}
			body := string(masked[open+1 : end])
			if strings.TrimSpace(body) != "" { // 空字面量（如 `return RunConfig{}` 错误返回）不承载策略
				checked = append(checked, site{f, lineOf(src, start)})
				if !strings.Contains(body, policyField) {
					missing = append(missing, site{f, lineOf(src, start)})
				}
			}
			off = end
		}
	}

	if len(checked) == 0 {
		t.Fatal("没扫到任何 RunConfig 构造点 —— 守卫失效（文件集或匹配逻辑错了）")
	}
	if len(missing) > 0 {
		var sb strings.Builder
		for _, m := range missing {
			fmt.Fprintf(&sb, "\n  - %s:%d", m.file, m.line)
		}
		t.Fatalf("有 %d 个 RunConfig 构造点未显式声明 reminder 策略（缺少 %s）：%s\n"+
			"内部一次性 Run（如上下文压缩）必须写 %s true —— 否则 system_reminder 会拼进请求尾部，\n"+
			"破坏「最后一条消息 = 该 Run 的指令」的契约；对话轮请显式写 false。",
			len(missing), policyField, sb.String(), policyField)
	}
	t.Logf("RunConfig 构造点穷举通过：%d 个非空字面量均显式声明 %s", len(checked), policyField)
	for _, c := range checked {
		t.Logf("  - %s:%d", c.file, c.line)
	}
}
