package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var nonSafeSegment = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// SanitizeWorkspaceKey 清理用户维度标识，避免路径注入。
func SanitizeWorkspaceKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "anonymous"
	}
	// 限制最大长度，防止超长输入导致路径过长或 hash DoS
	const maxKeyLength = 256
	if len(trimmed) > maxKeyLength {
		trimmed = trimmed[:maxKeyLength]
	}
	sanitized := nonSafeSegment.ReplaceAllString(trimmed, "_")
	sanitized = strings.Trim(sanitized, "._-")
	if sanitized == "" {
		return "anonymous"
	}
	return sanitized
}

// UserRoot 返回用户根目录：{workDir}/.xbot/users/{sender}
//
// 幂等：workDir 本身已经是该用户的 user root 或 workspace 时直接复用，不再前缀。
// 见 joinPathIdempotent 的注释（2026-11-02 生产日志：.../workspace 被拼了两次）。
func UserRoot(workDir, senderID string) string {
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	key := SanitizeWorkspaceKey(senderID)
	clean := filepath.Clean(workDir)
	if root, ok := existingUserRoot(clean, key); ok {
		return root
	}
	return joinPathIdempotent(clean, filepath.Join(".xbot", "users", key))
}

// joinPathIdempotent 把 rel 拼到 base 下，但对绝对路径幂等：rel 已是绝对路径时
// 直接 filepath.Clean(rel)，绝不再加一次 base 前缀。
//
// 背景（2026-11-02 生产日志，每日 6~7 条 "Failed to load user agent roles"）：
//
//	read agents dir /home/smith/.xbot/users/web-4/workspace/.xbot/users/web-4/workspace/agents
//
// 子代理的 ToolContext 把 per-user workspace 直接当 WorkingDir 传下来
// （agent/interactive.go buildParentToolContext: WorkingDir = workspaceRoot），
// 旧实现无条件 filepath.Join(workDir, ".xbot", "users", sender) 就把
// {workspace}/.xbot/users/{sender}/workspace 又拼了一遍。
func joinPathIdempotent(base, rel string) string {
	if rel == "" {
		return filepath.Clean(base)
	}
	if base == "" {
		return filepath.Clean(rel)
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(base, rel))
}

// existingUserRoot 判断 path 是否已经是 {…}/.xbot/users/{key} 或其 workspace 子目录，
// 是则返回该 user root（幂等拼接的落点）。
func existingUserRoot(path, key string) (string, bool) {
	if filepath.Base(path) == "workspace" {
		if root := filepath.Dir(path); isUserRootDir(root, key) {
			return root, true
		}
	}
	if isUserRootDir(path, key) {
		return path, true
	}
	return "", false
}

// isUserRootDir 判断 path 是否为 {…}/.xbot/users/{key}（UserRoot 的产物形态）。
func isUserRootDir(path, key string) bool {
	return filepath.Base(path) == key &&
		filepath.Base(filepath.Dir(path)) == "users" &&
		filepath.Base(filepath.Dir(filepath.Dir(path))) == ".xbot"
}

// UserWorkspaceRoot 返回用户工作区目录：{workDir}/.xbot/users/{sender}/workspace
func UserWorkspaceRoot(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "workspace")
}

// UserSkillsRoot 返回用户私有 skill 目录：{workDir}/.xbot/users/{sender}/workspace/skills
func UserSkillsRoot(workDir, senderID string) string {
	return filepath.Join(UserWorkspaceRoot(workDir, senderID), "skills")
}

// UserMCPConfigPath 返回用户 MCP 配置路径：{workDir}/.xbot/users/{sender}/mcp.json
func UserMCPConfigPath(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "mcp.json")
}

// UserPreferencesPath returns the per-user UI preferences JSON path:
// {workDir}/.xbot/users/{sender}/preferences.json
func UserPreferencesPath(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "preferences.json")
}

// UserAgentsRoot returns the user-private agents directory.
func UserAgentsRoot(workDir, senderID string) string {
	return filepath.Join(UserWorkspaceRoot(workDir, senderID), "agents")
}

func cleanAbsPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func isWithinRoot(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	if runtime.GOOS == "windows" {
		relLower := strings.ToLower(rel)
		if strings.HasPrefix(relLower, "..") {
			return false
		}
	}
	return true
}
