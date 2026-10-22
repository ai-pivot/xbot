package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "xbot/logger"
)

// role_rules.go —— 角色声明的规则文件注入（声明式，非目录式）。
//
// 背景（2026-10-22 用户要求）：组织级规则不能靠「覆盖项目 AGENTS.md」也不能靠
// 「写全局 $XBOT_HOME/AGENTS.md」——前者污染项目上下文（而且
// LoadProjectContextFile 只返回第一个命中，会把真正的项目文件挤掉），后者污染
// 所有会话。正确做法是挂在**角色**上：role frontmatter 里写
//
//	rules:
//	  - court/CHARTER.md      # 相对 $XBOT_HOME，也可写绝对路径
//
// 只有声明了 rules 的角色在 spawn 时加载这些文件。没声明的角色
// （tester / explore / 其它项目的子代理）**一个字都不会多读**。

// maxRoleRuleBytes 单个规则文件上限：规则是给人看的纪律，不该塞进整本书。
// 同时防止把大文件（乃至二进制）灌进 system prompt 撑爆上下文。
const maxRoleRuleBytes = 64 * 1024

// appendRoleRules 把 role.Rules 的内容追加到角色 system prompt 后面。
// 任一文件读不到只告警、不阻断 spawn（规则缺失不该让部门起不来）。
func appendRoleRules(_ *ToolContext, role *SubAgentRole) string {
	if role == nil {
		return ""
	}
	prompt := role.SystemPrompt
	if len(role.Rules) == 0 {
		return prompt
	}

	home := roleRulesHome()

	var b strings.Builder
	b.WriteString(prompt)
	for _, rel := range role.Rules {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		path, perr := resolveRoleRulePath(home, rel)
		if perr != nil {
			// 2026-10-22 兵部审计：frontmatter 可来自 workspace 里的 role 文件，
			// 不校验就能写 rules: ["../../.ssh/id_rsa"] 把私钥读进 system prompt
			// 交给 LLM。这里只允许 $XBOT_HOME 之内的相对路径。
			log.WithFields(log.Fields{"role": role.Name, "rule": rel, "error": perr.Error()}).
				Warn("Refusing to load role rule file (outside XBOT_HOME or absolute) — skipped")
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			log.WithFields(log.Fields{"role": role.Name, "rule": rel, "error": err.Error()}).
				Warn("Failed to read role rule file — sub-agent starts without it")
			continue
		}
		if len(data) > maxRoleRuleBytes {
			log.WithFields(log.Fields{"role": role.Name, "rule": rel, "bytes": len(data)}).
				Warn("Role rule file too large — skipped")
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		b.WriteString("\n\n## 规则文件：" + rel + "\n\n" + content + "\n")
	}
	return b.String()
}

// resolveRoleRulePath 把 frontmatter 里的 rules 条目解析成绝对路径，并做安全校验：
//   - 必须相对 $XBOT_HOME（绝对路径一律拒绝）
//   - 清理后不得逃逸出 $XBOT_HOME（拒绝 ..）
//   - 必须是普通文件（拒绝 symlink / 目录 / 设备）
//
// 返回 error 表示拒绝加载（调用方只告警，不阻断 spawn）。
func resolveRoleRulePath(home, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("empty rule path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute rule path is not allowed")
	}
	if home == "" {
		return "", fmt.Errorf("XBOT_HOME unknown")
	}
	root, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("rule path escapes XBOT_HOME: %s", rel)
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("rule path is a symlink: %s", rel)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("rule path is not a regular file: %s", rel)
	}
	return full, nil
}
