package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// LoadAgentRoles loads all agent definition files (*.md) from a directory
// Each file contains YAML frontmatter (name, description, tools) and SystemPrompt body
func LoadAgentRoles(dir string) ([]SubAgentRole, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read agents dir %s: %w", dir, err)
	}

	var roles []SubAgentRole
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		role, err := ParseAgentFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", path, err)
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// LoadAgentRolesSandbox loads agent roles from a directory using Sandbox for file access.
func LoadAgentRolesSandbox(ctx context.Context, dir string, sb Sandbox, userID string) ([]SubAgentRole, error) {
	entries, err := sb.ReadDir(ctx, dir, userID)
	if err != nil {
		return nil, fmt.Errorf("read agents dir %s: %w", dir, err)
	}
	var roles []SubAgentRole
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name)
		data, err := sb.ReadFile(ctx, path, userID)
		if err != nil {
			return nil, fmt.Errorf("read agent file %s: %w", path, err)
		}
		fallbackName := strings.TrimSuffix(entry.Name, ".md")
		role, err := ParseAgentFileContent(data, fallbackName)
		if err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", path, err)
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// ParseAgentFile parses a single agent definition file
// Format: YAML frontmatter (between ---) + Markdown body as SystemPrompt
func ParseAgentFile(path string) (SubAgentRole, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SubAgentRole{}, err
	}

	content := string(data)

	// separate frontmatter from body
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("invalid frontmatter: %w", err)
	}

	// Parse frontmatter fields
	name, description, model, allowedTools, caps, err := parseFrontmatter(frontmatter)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	if name == "" {
		// Use filename (without .md) as fallback
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	// spawn_agent allowed by default unless frontmatter explicitly sets spawn_agent: false
	// (already handled in parseFrontmatter)
	return SubAgentRole{
		Name:         name,
		Description:  description,
		Model:        model,
		SystemPrompt: strings.TrimSpace(body),
		AllowedTools: allowedTools,
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
	name, description, model, allowedTools, caps, err := parseFrontmatter(frontmatter)
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
		Capabilities: caps,
	}, nil
}

// splitFrontmatter separates YAML frontmatter from body
// Expected format: starts with "---\n", second "---\n" ends frontmatter
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	// strip possible BOM
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")

	if !strings.HasPrefix(content, "---") {
		return "", "", fmt.Errorf("file does not start with ---")
	}

	// find the second ---
	rest := content[3:] // 跳过第一个 ---
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", fmt.Errorf("closing --- not found")
	}

	frontmatter = rest[:idx]
	body = rest[idx+4:] // 跳过 "\n---"
	// Skip the newline after ---
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")

	return frontmatter, body, nil
}

// parseFrontmatter manually parses simple YAML frontmatter
// supports name, description (string), tools (list), model (string), and capabilities (sub-field)
// spawn_agent allowed by default unless frontmatter explicitly sets spawn_agent: false
func parseFrontmatter(fm string) (name, description, model string, tools []string, caps SubAgentCapabilities, err error) {
	caps = SubAgentCapabilities{
		SpawnAgent: true, // 默认允许 spawn 子 agent
	}
	lines := strings.Split(fm, "\n")
	var currentField string

	for _, line := range lines {
		// strip \r
		line = strings.TrimRight(line, "\r")

		// Skip blank lines and comments
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// list items: starting with "  - " or "- " (belonging to current field)
		if strings.HasPrefix(trimmed, "- ") {
			if currentField == "tools" {
				item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
				if item != "" {
					tools = append(tools, item)
				}
			}
			continue
		}

		// Indented key-value pairs (capabilities sub-fields)
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

		// Key-value pairs: key: value
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// strip quote wrapping
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
			// The tools value may be on the same line (e.g. tools: [a, b]) or subsequent lines (list format)
			// we only support list format, ignore same-line values
		case "capabilities":
			currentField = "capabilities"
		default:
			currentField = ""
		}
	}

	// Validate name format: allows Unicode letters (including Chinese), digits, hyphens, underscores
	if name != "" {
		for _, c := range name {
			if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-' && c != '_' {
				return "", "", "", nil, SubAgentCapabilities{}, fmt.Errorf("invalid agent name %q: only letters (including CJK), digits, hyphens and underscores are allowed", name)
			}
		}
	}

	// Validate tools list format
	for _, t := range tools {
		if t == "" {
			return "", "", "", nil, SubAgentCapabilities{}, fmt.Errorf("empty tool name in tools list")
		}
		for _, c := range t {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
				return "", "", "", nil, SubAgentCapabilities{}, fmt.Errorf("invalid tool name %q: only letters, digits, hyphens, underscores and dots are allowed", t)
			}
		}
	}

	return name, description, model, tools, caps, nil
}

// isTruthy checks if a string represents true
func isTruthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1"
}

// stripQuotes strips surrounding quotes (single or double) from a string
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
