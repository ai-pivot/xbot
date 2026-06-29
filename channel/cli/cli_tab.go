package cli

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// handleTabComplete 处理 Tab 补全（§8：/ 命令补全，§8b：@ 文件路径补全）
func (m *cliModel) handleTabComplete() {
	input := m.textarea.Value()

	// 检测 @ 文件引用补全（从输入末尾检测）
	atOk, atPrefix := detectAtPrefix(input)
	if atOk {
		m.handleFileTabComplete(input, atPrefix)
		return
	}

	// / 命令补全
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return
	}

	if len(m.completions) == 0 {
		m.completions = m.getCommandCompletions(trimmed)
		if len(m.completions) == 0 {
			return
		}
		m.compIdx = 0
	} else {
		m.compIdx = (m.compIdx + 1) % len(m.completions)
	}

	m.textarea.SetValue(m.completions[m.compIdx] + " ")
}

// getCommandCompletions returns all registered command names (built-in + plugin)
// matching the given prefix. This is the SINGLE source of truth used by both
// Tab completion (handleTabComplete) and hint display (renderCompletionsHint).
func (m *cliModel) getCommandCompletions(prefix string) []string {
	var matches []string
	for _, cmd := range cliCommands {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
		}
	}
	m.refreshPluginCmdNames()
	for _, cmd := range m.pluginCmdNames {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
		}
	}
	return matches
}

// detectAtPrefix 检测输入文本末尾是否有 @ 触发文件补全。
// ok=true 表示检测到 @（即使后面无字符也应触发 glob）。
// prefix 是 @ 之后到文本末尾的部分。
func detectAtPrefix(input string) (ok bool, prefix string) {
	if len(input) == 0 || input[len(input)-1] == ' ' {
		return false, ""
	}
	i := len(input) - 1
	for i >= 0 && input[i] != ' ' && input[i] != '@' {
		i--
	}
	if i < 0 || input[i] != '@' {
		return false, ""
	}
	if i > 0 && input[i-1] != ' ' {
		return false, ""
	}
	return true, input[i+1:]
}

// populateFileCompletions 根据 prefix 执行 glob 搜索并填充 fileCompletions
func (m *cliModel) populateFileCompletions(prefix string) {
	pattern := prefix
	if !strings.Contains(pattern, "*") {
		if strings.HasSuffix(pattern, "/") {
			pattern += "*"
		} else {
			pattern += "*"
		}
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		m.fileCompletions = nil
		m.fileCompIdx = 0
		return
	}
	// 过滤隐藏文件（以 . 开头）
	matches = slices.DeleteFunc(matches, func(f string) bool {
		base := filepath.Base(f)
		return len(base) > 0 && base[0] == '.'
	})
	sort.Slice(matches, func(i, j int) bool {
		di, dj := isDir(matches[i]), isDir(matches[j])
		if di != dj {
			return di
		}
		return matches[i] < matches[j]
	})
	if len(matches) > 20 {
		matches = matches[:20]
	}
	m.fileCompletions = matches
	m.fileCompIdx = 0
}

// handleFileTabComplete 处理 @ 文件路径 Tab 补全
func (m *cliModel) handleFileTabComplete(input string, prefix string) {
	if !m.fileCompActive || len(m.fileCompletions) == 0 {
		// 首次 Tab 或候选被清空：glob 并进入循环模式
		m.populateFileCompletions(prefix)
		if len(m.fileCompletions) == 0 {
			return
		}
		m.fileCompActive = true
	} else {
		// 循环模式：切换到下一个候选
		m.fileCompIdx = (m.fileCompIdx + 1) % len(m.fileCompletions)
	}

	selected := m.fileCompletions[m.fileCompIdx]
	if isDir(selected) {
		selected += "/"
	}
	atStart := len(input) - len(prefix) - 1
	newInput := input[:atStart] + "@" + selected
	m.textarea.SetValue(newInput)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
