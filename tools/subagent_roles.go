package tools

import (
	"context"
	"fmt"
	"os"

	log "xbot/logger"
)

// SubAgentRole is a predefined SubAgent role
type SubAgentRole struct {
	Name         string
	Description  string
	SystemPrompt string
	AllowedTools []string
	Model        string // 可选：指定使用的 LLM 模型或模型等级（vanguard/balance/swift）。为空时继承主 Agent 模型。

	Capabilities SubAgentCapabilities
}

// SubAgentCapabilities SubAgent capability declaration
type SubAgentCapabilities struct {
	Memory      bool // 可访问 Letta memory（core/archival/recall）
	SendMessage bool // 可直接向 IM 渠道发送消息
	SpawnAgent  bool // 可创建子 Agent（需注意递归深度限制）
}

// ToMap converts to map[string]bool for cross-package passing (avoids circular dependency).
// 始终包含所有三个 key，确保显式设置的 false 值不会在反序列化时被default value覆盖。
func (c SubAgentCapabilities) ToMap() map[string]bool {
	m := make(map[string]bool)
	m["memory"] = c.Memory
	m["send_message"] = c.SendMessage
	m["spawn_agent"] = c.SpawnAgent
	return m
}

// CapabilitiesFromMap constructs SubAgentCapabilities from map[string]bool.
// Default SpawnAgent=true: all agents can spawn sub-agents unless explicitly set spawn_agent=false.
func CapabilitiesFromMap(m map[string]bool) SubAgentCapabilities {
	caps := SubAgentCapabilities{
		SpawnAgent: true, // 默认允许 spawn 子 agent
	}
	if m != nil {
		caps.Memory = m["memory"]
		caps.SendMessage = m["send_message"]
		if _, ok := m["spawn_agent"]; ok {
			caps.SpawnAgent = m["spawn_agent"]
		}
	}
	return caps
}

var agentsDir string

// InitAgentRoles sets the global agents directory (called once at startup).
// Actual loading happens on-demand in each GetSubAgentRole call.
func InitAgentRoles(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.WithField("dir", dir).Info("Agents directory not found, no predefined roles")
		return nil
	}
	agentsDir = dir
	// 验证目录可读
	roles, err := LoadAgentRoles(dir)
	if err != nil {
		return fmt.Errorf("validate agent roles in %s: %w", dir, err)
	}
	log.WithField("count", len(roles)).Info("Agent roles directory configured")
	return nil
}

// GetSubAgentRole finds a role by name (loaded from file each time, supports hot reload)
// search user's private directory first, then global (user roles take priority)
func GetSubAgentRole(name string, userAgentDirs ...string) (*SubAgentRole, bool) {
	// search user's private directory first
	for _, dir := range userAgentDirs {
		if dir == "" {
			continue
		}
		roles, err := LoadAgentRoles(dir)
		if err != nil {
			log.WithField("dir", dir).WithError(err).Warn("Failed to load user agent roles, skipping directory")
			continue
		}
		for i := range roles {
			if roles[i].Name == name {
				return &roles[i], true
			}
		}
	}

	// then search global directory
	if agentsDir == "" {
		// global directory not found, try embed fallback
		return getEmbeddedAgentRole(name)
	}
	roles, err := LoadAgentRoles(agentsDir)
	if err != nil {
		log.WithError(err).Warn("Failed to load agent roles")
		return getEmbeddedAgentRole(name)
	}
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i], true
		}
	}
	// Not found in global dir, try embed fallback
	return getEmbeddedAgentRole(name)
}

// getEmbeddedAgentRole tries to load a role from embedded agent definitions.
func getEmbeddedAgentRole(name string) (*SubAgentRole, bool) {
	data, err := ReadEmbeddedAgentFile(name)
	if err != nil {
		return nil, false
	}
	role, err := ParseAgentFileContent(data, name)
	if err != nil || role.Name == "" {
		return nil, false
	}
	return &role, true
}

// GetSubAgentRoleSandbox is the sandbox-aware version of GetSubAgentRole.
// User agent directories are accessed via Sandbox when sb is non-nil.
func GetSubAgentRoleSandbox(ctx context.Context, name string, sb Sandbox, userID string, userAgentDirs ...string) (*SubAgentRole, bool) {
	// Search user private directories
	for _, dir := range userAgentDirs {
		if dir == "" {
			continue
		}
		var roles []SubAgentRole
		var err error
		if sb != nil {
			roles, err = LoadAgentRolesSandbox(ctx, dir, sb, userID)
		} else {
			roles, err = LoadAgentRoles(dir)
		}
		if err != nil {
			log.WithField("dir", dir).WithError(err).Warn("Failed to load user agent roles, skipping directory")
			continue
		}
		for i := range roles {
			if roles[i].Name == name {
				return &roles[i], true
			}
		}
	}

	// Search global directory (always os.*)
	if agentsDir == "" {
		// global directory not found, try embed fallback
		return getEmbeddedAgentRole(name)
	}
	roles, err := LoadAgentRoles(agentsDir)
	if err != nil {
		log.WithError(err).Warn("Failed to load agent roles")
		return getEmbeddedAgentRole(name)
	}
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i], true
		}
	}
	// Not found in global dir, try embed fallback
	return getEmbeddedAgentRole(name)
}
