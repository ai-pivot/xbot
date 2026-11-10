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
//	  - roles/RULES.md      # 相对 $XBOT_HOME，也可写绝对路径
//
// 只有声明了 rules 的角色在 spawn 时加载这些文件。没声明的角色
// （tester / explore / 其它项目的子代理）**一个字都不会多读**。

// maxRoleRuleBytes 单个规则文件上限：规则是给人看的纪律，不该塞进整本书。
// 同时防止把大文件（乃至二进制）灌进 system prompt 撑爆上下文。
const maxRoleRuleBytes = 64 * 1024

// RoleSystemPrompt 返回该角色最终应当注入的 system prompt：
// role.SystemPrompt（角色专有能力描述）+ frontmatter `rules:` 声明的每个文件内容。
//
// 这是 rules 注入的**唯一实现**。所有入口都必须走它：
//   - 工具层 spawn/send：tools 包内各处调用 appendRoleRules（本函数的薄包装）；
//   - 重启恢复：agent/interactive_persist.go 的 restoreOneAgent；
//   - 兜底：agent/engine_wire.go 的 prompt 汇聚点用 RolePromptForName。
//
// 任一文件读不到/被拒绝只告警、不阻断 spawn（规则缺失不该让子代理起不来）。
func RoleSystemPrompt(role *SubAgentRole) string {
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
			// 2026-10-22 安全审计：frontmatter 可来自 workspace 里的 role 文件，
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

// appendRoleRules 是 RoleSystemPrompt 的工具层薄包装。
//
// 保留这个"多余"的一层是为了让所有 spawn/send 调用点的源码行都含同一个可 grep 的
// 标识符（tools/agents_dir_loading_test.go 的 TestAllSpawnCallSitesInjectRoleRules
// 靠它做源码级守护：漏注入一处就红）。
func appendRoleRules(_ *ToolContext, role *SubAgentRole) string {
	return RoleSystemPrompt(role)
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

// RolePromptForName 是"只有 role 名字、没有 role 结构"时的 rules 兜底注入。
//
// 用在 system prompt 的唯一汇聚点（agent/engine_wire.go 的 buildSubAgentRunConfig）：
// spawn 交互子代理有三类入口（SubAgent 工具 / CreateChat 两种类型 / 重启恢复），
// 其中任何一处漏调 RoleSystemPrompt，该角色就会"静默不带规矩"——不报错、不红测试。
// 汇聚点补一次兜底，把"漏改"变成一条 warn 日志而不是无声降级。
//
// 幂等：**只有** systemPrompt 与磁盘上的 role.SystemPrompt 完全一致（= 调用方压根没
// 注入过 rules）时才追加；调用方已注入（结果与裸 prompt 不同）则原样返回，绝不重复追加。
func RolePromptForName(roleName, systemPrompt string) string {
	if roleName == "" || len(systemPrompt) == 0 {
		return systemPrompt
	}
	role, ok := GetSubAgentRole(roleName)
	if !ok || role == nil || len(role.Rules) == 0 {
		return systemPrompt
	}
	// 幂等判定不靠文本嗅探（role 正文里可能恰好出现"## 规则文件："之类的字样），
	// 而是直接重算一次：RoleSystemPrompt 是确定性函数，算出来的东西等于入参就说明
	// 调用方已经注入过（或规则一个都读不到，无从注入）。
	if systemPrompt != role.SystemPrompt {
		return systemPrompt // 调用方用了自定义 prompt —— 不碰
	}
	injected := RoleSystemPrompt(role)
	if injected == systemPrompt {
		// 声明了 rules 但一个都没读到（文件缺失/被拒）：RoleSystemPrompt 已各自告警，
		// 这里不能再报"调用方漏注入"——那是误报。
		return systemPrompt
	}
	log.WithFields(log.Fields{"role": role.Name, "rules": len(role.Rules)}).
		Warn("Role rules were not injected by the caller — injecting at prompt assembly fallback (a spawn path is missing RoleSystemPrompt)")
	return injected
}
