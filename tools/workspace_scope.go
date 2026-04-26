package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var nonSafeSegment = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// SanitizeWorkspaceKey sanitizes a user-scoped identifier to prevent path injection.
func SanitizeWorkspaceKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "anonymous"
	}
	// Limit maximum length to prevent overly long input from causing excessively long paths or hash DoS
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

// UserRoot returns the user root directory：{workDir}/.xbot/users/{sender}
func UserRoot(workDir, senderID string) string {
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	return filepath.Join(workDir, ".xbot", "users", SanitizeWorkspaceKey(senderID))
}

// UserWorkspaceRoot returns the user's workspace directory：{workDir}/.xbot/users/{sender}/workspace
func UserWorkspaceRoot(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "workspace")
}

// UserSkillsRoot returns the user's private skill directory：{workDir}/.xbot/users/{sender}/workspace/skills
func UserSkillsRoot(workDir, senderID string) string {
	return filepath.Join(UserWorkspaceRoot(workDir, senderID), "skills")
}

// UserMCPConfigPath returns the user MCP config path：{workDir}/.xbot/users/{sender}/mcp.json
func UserMCPConfigPath(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "mcp.json")
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
