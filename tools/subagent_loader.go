package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode"

	log "xbot/logger"
)

// LoadAgentRoles 从目录加载所有 agent 定义文件（*.md）。
//
// 目录结构决定 role 的可见范围与名字空间（2026-10-22 用户要求：
// "role 不应该全局可用"）：
//
//	agents/tester.md            → role "tester"        （顶层，保持向后兼容）
//	agents/team/backend.md      → role "team.backend"
//	agents/proj/xbot/dev.md     → role "proj.xbot.dev"
//
// 子目录用 "." 连接成名字空间。**刻意不用 "/"**：interactiveKey 形如
// `<channel>:<chatID>/<role>:<instance>`，role 里若含 "/" 会让
// parseInteractiveKeyTail 的 LastIndex("/") 解析错误。
// skippedRoleDefs 统计"被跳过的 role 定义文件/子目录"数（解析失败、目录不可读）。
//
// 只做**计数**不做二次扫描：加载热路径（GetSubAgentRole 每次调用都重新加载，
// 支持热更新）上多扫一遍代价不可接受。InitAgentRoles 在启动时清零后读一次，
// 打一条汇总 warn —— 否则"少了一个 role"只表现为调用方的 unknown role。
var skippedRoleDefs atomic.Int64

// isHiddenAgentDir 判断目录项是否应被当成"隐藏/内部目录"跳过。
//
// agents 目录本身通常就是个 git 仓库（$XBOT_HOME/agents/.git 真实存在）：
// 递归进去虽然今天没有 *.md 所以不产生假 role，但只要有人在里面放一个带
// frontmatter 的 .md（docs/README.md、.git/xxx.md），就会凭空多出一个 role。
// 与其靠"里面碰巧没有 md"这种运气，不如显式跳过 . 开头的目录。
func isHiddenAgentDir(name string) bool {
	return strings.HasPrefix(name, ".")
}

func LoadAgentRoles(dir string) ([]SubAgentRole, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read agents dir %s: %w", dir, err)
	}

	var roles []SubAgentRole
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if isHiddenAgentDir(entry.Name()) {
				continue // .git 等内部目录不参与命名空间（见 isHiddenAgentDir）
			}
			// 子目录 = 命名空间。单个子目录解析失败不影响其它目录（告警跳过）。
			sub, err := loadAgentRolesNamespaced(path, entry.Name())
			if err != nil {
				skippedRoleDefs.Add(1)
				log.WithFields(log.Fields{"dir": path, "error": err.Error()}).
					Warn("Failed to load namespaced agent roles, skipping directory")
				continue
			}
			roles = append(roles, sub...)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		role, err := ParseAgentFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", path, err)
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// loadAgentRolesNamespaced 递归加载一个命名空间目录，role 名 = prefix[.sub...].name。
func loadAgentRolesNamespaced(dir, prefix string) ([]SubAgentRole, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var roles []SubAgentRole
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if isHiddenAgentDir(entry.Name()) {
				continue // 同顶层：隐藏目录不是命名空间
			}
			sub, err := loadAgentRolesNamespaced(path, prefix+"."+entry.Name())
			if err != nil {
				skippedRoleDefs.Add(1)
				log.WithFields(log.Fields{"dir": path, "error": err.Error()}).
					Warn("Failed to load namespaced agent roles, skipping directory")
				continue
			}
			roles = append(roles, sub...)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		role, err := ParseAgentFile(path)
		if err != nil {
			skippedRoleDefs.Add(1)
			log.WithFields(log.Fields{"file": path, "error": err.Error()}).
				Warn("Failed to parse agent file, skipping")
			continue
		}
		role.Name = prefix + "." + role.Name
		roles = append(roles, role)
	}
	return roles, nil
}

// LoadAgentRolesSandbox loads agent roles from a directory using Sandbox for file access.
//
// 与 LoadAgentRoles 保持**同一套**命名空间语义（子目录 = role 名前缀，用 "." 拼接）。
// 此前这里 `entry.IsDir` 直接 continue：本地用户能看到 agents/team/*.md 解析出的
// team.backend，而远程/容器 sandbox 用户一个都看不到 —— 两类用户的 role 可见集不同，
// 既影响 agent/agents.go 的 catalog（system prompt 里的 <available_agents>），
// 也影响 spawn 时的 role 解析（同一份 agents 目录，行为却随 sandbox 变化）。
func LoadAgentRolesSandbox(ctx context.Context, dir string, sb Sandbox, userID string) ([]SubAgentRole, error) {
	return loadAgentRolesSandboxDir(ctx, dir, "", sb, userID)
}

// loadAgentRolesSandboxDir 递归加载一个命名空间目录（sandbox 版）。
// prefix 形如 "" / "team." / "proj.xbot."（含结尾点），叶子角色名 = prefix + name。
func loadAgentRolesSandboxDir(ctx context.Context, dir, prefix string, sb Sandbox, userID string) ([]SubAgentRole, error) {
	entries, err := sb.ReadDir(ctx, dir, userID)
	if err != nil {
		return nil, fmt.Errorf("read agents dir %s: %w", dir, err)
	}
	var roles []SubAgentRole
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name)
		if entry.IsDir {
			if isHiddenAgentDir(entry.Name) {
				continue // 与本地版一致：隐藏目录不是命名空间
			}
			// 单个子目录失败不拖垮其它目录（与本地版 LoadAgentRoles 一致）。
			sub, err := loadAgentRolesSandboxDir(ctx, path, prefix+entry.Name+".", sb, userID)
			if err != nil {
				log.WithFields(log.Fields{"dir": path, "error": err.Error()}).
					Warn("Failed to load namespaced agent roles from sandbox, skipping directory")
				continue
			}
			roles = append(roles, sub...)
			continue
		}
		if !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		data, err := sb.ReadFile(ctx, path, userID)
		if err != nil {
			return nil, fmt.Errorf("read agent file %s: %w", path, err)
		}
		fallbackName := strings.TrimSuffix(entry.Name, ".md")
		role, err := ParseAgentFileContent(data, fallbackName)
		if err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", path, err)
		}
		role.Name = prefix + role.Name
		roles = append(roles, role)
	}
	return roles, nil
}

// ParseAgentFile 解析单个 agent 定义文件
// 格式：YAML frontmatter（--- 之间）+ Markdown 正文作为 SystemPrompt
func ParseAgentFile(path string) (SubAgentRole, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SubAgentRole{}, err
	}

	content := string(data)

	// 分离 frontmatter 和正文
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("invalid frontmatter: %w", err)
	}

	// 解析 frontmatter 字段
	name, description, model, allowedTools, rules, caps, err := parseFrontmatter(frontmatter)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	if name == "" {
		// 用文件名（去掉 .md）作为 fallback
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	// 默认允许 spawn_agent，除非 frontmatter 中显式设置 spawn_agent: false
	// （已在 parseFrontmatter 中处理）
	return SubAgentRole{
		Name:         name,
		Description:  description,
		Model:        model,
		SystemPrompt: strings.TrimSpace(body),
		AllowedTools: allowedTools,
		Rules:        rules,
		Capabilities: caps,
	}, nil
}

// ParseAgentFileContent parses agent definition from content bytes.
// fallbackName is used when frontmatter has no name field (e.g., filename without .md).
func ParseAgentFileContent(data []byte, fallbackName string) (SubAgentRole, error) {
	content := string(data)
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("invalid frontmatter: %w", err)
	}
	name, description, model, allowedTools, rules, caps, err := parseFrontmatter(frontmatter)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	if name == "" {
		name = fallbackName
	}
	return SubAgentRole{
		Name:         name,
		Description:  description,
		Model:        model,
		SystemPrompt: strings.TrimSpace(body),
		AllowedTools: allowedTools,
		Rules:        rules,
		Capabilities: caps,
	}, nil
}

// splitFrontmatter 分离 YAML frontmatter 和正文
// 期望格式：以 "---\n" 开头，第二个 "---\n" 结束 frontmatter
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	// 去掉可能的 BOM
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")

	if !strings.HasPrefix(content, "---") {
		return "", "", fmt.Errorf("file does not start with ---")
	}

	// 找第二个 ---
	rest := content[3:] // 跳过第一个 ---
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", fmt.Errorf("closing --- not found")
	}

	frontmatter = rest[:idx]
	body = rest[idx+4:] // 跳过 "\n---"
	// 跳过 --- 后面的换行
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")

	return frontmatter, body, nil
}

// parseFrontmatter 手动解析简单 YAML frontmatter
// 支持 name, description（字符串）、tools/rules（列表）、model（字符串）和 capabilities（子字段）
// 默认 spawn_agent=true，除非显式设置 spawn_agent: false
func parseFrontmatter(fm string) (name, description, model string, tools, rules []string, caps SubAgentCapabilities, err error) {
	caps = SubAgentCapabilities{
		SpawnAgent: true, // 默认允许 spawn 子 agent
	}
	lines := strings.Split(fm, "\n")
	var currentField string

	for _, line := range lines {
		// 去掉 \r
		line = strings.TrimRight(line, "\r")

		// 跳过空行和注释
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// 列表项：以 "  - " 或 "- " 开头（属于当前字段）
		if strings.HasPrefix(trimmed, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if item == "" {
				continue
			}
			switch currentField {
			case "tools":
				tools = append(tools, item)
			case "rules":
				rules = append(rules, item)
			}
			continue
		}

		// 缩进的键值对（capabilities 子字段）
		if (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) && currentField == "capabilities" {
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx < 0 {
				continue
			}
			key := strings.TrimSpace(trimmed[:colonIdx])
			value := strings.TrimSpace(trimmed[colonIdx+1:])
			value = stripQuotes(value)
			switch key {
			case "memory":
				caps.Memory = isTruthy(value)
			case "send_message":
				caps.SendMessage = isTruthy(value)
			case "spawn_agent":
				caps.SpawnAgent = isTruthy(value)
			}
			continue
		}

		// 键值对：key: value
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// 去掉引号包裹
		value = stripQuotes(value)

		switch key {
		case "name":
			name = value
			currentField = "name"
		case "description":
			description = value
			currentField = "description"
		case "model":
			model = value
			currentField = "model"
		case "tools":
			currentField = "tools"
		case "rules":
			currentField = "rules"
			// tools 的值可能在同一行（如 tools: [a, b]）或后续行（列表格式）
			// 我们只支持列表格式，忽略同行值
		case "capabilities":
			currentField = "capabilities"
		default:
			currentField = ""
		}
	}

	// 校验 name 格式：允许 Unicode 字母（含中文）、数字、连字符、下划线
	if name != "" {
		for _, c := range name {
			if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-' && c != '_' {
				return "", "", "", nil, nil, SubAgentCapabilities{}, fmt.Errorf("invalid agent name %q: only letters (including CJK), digits, hyphens and underscores are allowed", name)
			}
		}
	}

	// 校验 tools 列表格式
	for _, t := range tools {
		if t == "" {
			return "", "", "", nil, nil, SubAgentCapabilities{}, fmt.Errorf("empty tool name in tools list")
		}
		for _, c := range t {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
				return "", "", "", nil, nil, SubAgentCapabilities{}, fmt.Errorf("invalid tool name %q: only letters, digits, hyphens, underscores and dots are allowed", t)
			}
		}
	}

	return name, description, model, tools, rules, caps, nil
}

// isTruthy 判断字符串是否表示 true
func isTruthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1"
}

// stripQuotes 去掉字符串两端的引号（单引号或双引号）
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
