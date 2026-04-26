package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"xbot/llm"
)

// globToFindArgs translates a glob pattern to find command arguments.
// Return: (find search subdirectory, find filter parameter fragment)
//
// Translation rules:
//   - *.go            → ("", "-maxdepth 1 -name '*.go'")
//   - **/*.go         → ("", "-name '*.go'")               // recursive
//   - src/*.go        → ("src", "-maxdepth 1 -name '*.go'")
//   - src/**/*.go     → ("src", "-name '*.go'")            // recursive
//   - **/test/*.go    → ("", "-path '*/test/*.go'")        // recursive
//   - src/**/test/*.go→ ("src", "-path '*/test/*.go'")     // recursive
func globToFindArgs(pattern string) (searchBase string, args string) {
	// filepath.ToSlash converts Windows backslashes \ to forward slashes /,
	// Ensure cross-platform glob patterns work correctly in the Linux sandbox.
	// For example, "src\*.go" is normalized to "src/*.go".
	pattern = strings.Trim(pattern, "/")
	pattern = filepath.ToSlash(pattern)
	if pattern == "" {
		return "", ""
	}

	segments := strings.Split(pattern, "/")

	// locate the first ** position
	doubleStarIdx := -1
	for i, seg := range segments {
		if seg == "**" {
			doubleStarIdx = i
			break
		}
	}

	if doubleStarIdx == -1 {
		// No **: simple match, -maxdepth 1 limits to non-recursive
		if len(segments) == 1 {
			return "", fmt.Sprintf("-maxdepth 1 -name '%s'", shellEscape(segments[0]))
		}
		base := strings.Join(segments[:len(segments)-1], "/")
		name := segments[len(segments)-1]
		return base, fmt.Sprintf("-maxdepth 1 -name '%s'", shellEscape(name))
	}

	// Has **:
	prefix := strings.Join(segments[:doubleStarIdx], "/")
	suffixSegments := segments[doubleStarIdx+1:]

	if len(suffixSegments) == 0 {
		return prefix, ""
	}

	if len(suffixSegments) == 1 {
		return prefix, fmt.Sprintf("-name '%s'", shellEscape(suffixSegments[0]))
	}

	// multiple suffix segments: use -path
	pathPattern := "*/" + strings.Join(suffixSegments, "/")
	return prefix, fmt.Sprintf("-path '%s'", shellEscape(pathPattern))
}

// GlobTool file pattern matching search tool
type GlobTool struct{}

func (t *GlobTool) Name() string {
	return "Glob"
}

func (t *GlobTool) Description() string {
	return `Search for files matching a glob pattern.
Supports standard glob patterns including ** for recursive directory matching.
Parameters (JSON):
  - pattern: string, the glob pattern to match (e.g., "**/*.go", "src/**/*.ts", "*.txt")
  - path: string, optional, the base directory to search in (defaults to current working directory)
Example: {"pattern": "**/*.go", "path": "/project"}`
}

func (t *GlobTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "pattern", Type: "string", Description: "The glob pattern to match files against (supports ** for recursive matching)", Required: true},
		{Name: "path", Type: "string", Description: "The base directory to search in (defaults to current working directory)", Required: false},
	}
}

func (t *GlobTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Sandbox mode: execute find command inside the container
	if shouldUseSandbox(ctx) {
		return t.executeInSandbox(ctx, params.Pattern, params.Path)
	}

	// Non-sandbox mode: local file search
	return t.executeLocal(ctx, params.Pattern, params.Path)
}

// executeInSandbox executes find in the sandbox
func (t *GlobTool) executeInSandbox(ctx *ToolContext, pattern, path string) (*ToolResult, error) {
	sandboxBase := sandboxBaseDir(ctx)

	// Translate glob pattern → find parameters
	searchBase, findArgs := globToFindArgs(pattern)

	// Determine the find search directory
	searchDir := sandboxBase
	if path != "" {
		if path == sandboxBase || strings.HasPrefix(path, sandboxBase+"/") {
			searchDir = path
		} else {
			searchDir = sandboxBase + "/" + path
		}
	} else if sandboxCWD := resolveSandboxCWD(ctx, sandboxBase); sandboxCWD != "" {
		searchDir = sandboxCWD
	}

	// merge globToFindArgs subdirectory prefix
	if searchBase != "" {
		searchDir = searchDir + "/" + searchBase
	}

	// Build the find command (shellEscape searchDir to prevent injection)
	escapedDir := shellEscape(searchDir)
	findCmd := fmt.Sprintf(
		"find '%s' -type f %s -not -path '*/.*' -not -path '*/node_modules/*' 2>/dev/null | head -200",
		escapedDir, findArgs)
	output, err := RunInSandboxWithShell(ctx, findCmd)
	if err != nil {
		// If it's a "no matching files" case, return empty results
		if output == "" {
			return NewResultWithTips("No files matched the pattern.", "检查 glob 模式语法，或尝试更宽泛的匹配（如 **/*.go）。"), nil
		}
		return nil, fmt.Errorf("sandbox glob failed: %v, output: %s", err, output)
	}

	if output == "" {
		return NewResultWithTips("No files matched the pattern.", "检查 glob 模式语法，或尝试更宽泛的匹配（如 **/*.go）。"), nil
	}

	// Output is already a container-internal path, return directly
	lines := strings.Split(output, "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matching file(s):\n", len(lines))
	for _, line := range lines {
		if line != "" {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	return NewResultWithTips(sb.String(), "使用 Read 查看感兴趣的文件内容。"), nil
}

// executeLocal executes file search locally (non-sandbox mode)
func (t *GlobTool) executeLocal(ctx *ToolContext, pattern, path string) (*ToolResult, error) {
	// Determine base directory
	baseDir := path
	if baseDir == "" {
		// prefer CurrentDir (PWD tool optimization)
		if ctx != nil && ctx.CurrentDir != "" {
			baseDir = ctx.CurrentDir
		} else if ctx != nil && ctx.WorkspaceRoot != "" {
			baseDir = ctx.WorkspaceRoot
		} else if ctx != nil && ctx.WorkingDir != "" {
			baseDir = ctx.WorkingDir
		} else {
			var err error
			baseDir, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get working directory: %w", err)
			}
		}
	}

	baseDir, err := ResolveReadPath(ctx, baseDir)
	if err != nil {
		return nil, err
	}

	// Verify base directory exists
	info, err := os.Stat(baseDir)
	if err != nil {
		return nil, fmt.Errorf("base directory does not exist: %s", baseDir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", baseDir)
	}

	var matches []string

	// Expand brace patterns (e.g., "*.{go,ts}" → ["*.go", "*.ts"]) before matching,
	// since neither filepath.Glob nor matchDoublestar support brace expansion.
	bracePatterns := expandBracePattern(pattern)
	seen := make(map[string]bool)

	for _, bp := range bracePatterns {
		var bpMatches []string
		if strings.Contains(bp, "**") {
			bpMatches, err = globWithDoublestar(baseDir, bp)
			if err != nil {
				return nil, fmt.Errorf("glob search failed: %w", err)
			}
		} else {
			fullPattern := filepath.Join(baseDir, bp)
			bpMatches, err = filepath.Glob(fullPattern)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern: %w", err)
			}
		}
		for _, m := range bpMatches {
			if !seen[m] {
				seen[m] = true
				matches = append(matches, m)
			}
		}
	}

	sort.Strings(matches)

	if len(matches) == 0 {
		return NewResultWithTips("No files matched the pattern.", "检查 glob 模式语法，或尝试更宽泛的匹配（如 **/*.go）。"), nil
	}

	// Limit results to avoid excessive output
	const maxResults = 200
	truncated := false
	if len(matches) > maxResults {
		matches = matches[:maxResults]
		truncated = true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matching file(s):\n", len(matches))
	for _, match := range matches {
		sb.WriteString(match)
		sb.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "\n(Results truncated. Showing first %d matches.)\n", maxResults)
	}

	return NewResultWithTips(sb.String(), "使用 Read 查看感兴趣的文件内容。"), nil
}

// globWithDoublestar handles glob patterns containing ** for recursive directory matching.
// It splits the pattern at ** boundaries, walks the directory tree, and matches each
// path segment against the corresponding pattern part.
func globWithDoublestar(baseDir, pattern string) ([]string, error) {
	var matches []string

	// Normalize the pattern separators
	pattern = filepath.FromSlash(pattern)

	err := filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip files/dirs we can't access
		}

		// Get the path relative to baseDir for matching
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return nil
		}

		// Skip the base directory itself
		if relPath == "." {
			return nil
		}

		// Skip hidden directories (starting with .)
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}

		// Skip node_modules
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
		}

		// Match the relative path against the pattern
		if matchDoublestar(pattern, relPath) {
			matches = append(matches, path)
		}

		return nil
	})

	return matches, err
}

// matchDoublestar checks if a path matches a pattern that may contain ** wildcards.
// ** matches zero or more directory levels.
func matchDoublestar(pattern, path string) bool {
	// Split pattern and path into segments
	patternParts := splitPath(pattern)
	pathParts := splitPath(path)

	return matchParts(patternParts, pathParts)
}

// splitPath splits a file path into its component parts.
func splitPath(path string) []string {
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	// Filter out empty parts
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// matchParts recursively matches pattern parts against path parts.
// Supports ** (matches zero or more directories) and standard glob wildcards (* and ?).
func matchParts(patternParts, pathParts []string) bool {
	for len(patternParts) > 0 {
		part := patternParts[0]

		if part == "**" {
			// Remove the ** from pattern
			patternParts = patternParts[1:]

			// If ** is the last element, it matches everything remaining
			if len(patternParts) == 0 {
				return true
			}

			// Try matching ** against zero or more path segments
			for i := 0; i <= len(pathParts); i++ {
				if matchParts(patternParts, pathParts[i:]) {
					return true
				}
			}
			return false
		}

		// No more path parts but still have pattern parts
		if len(pathParts) == 0 {
			return false
		}

		// Match current parts using filepath.Match
		matched, err := filepath.Match(part, pathParts[0])
		if err != nil || !matched {
			return false
		}

		patternParts = patternParts[1:]
		pathParts = pathParts[1:]
	}

	// Pattern exhausted, path must also be exhausted
	return len(pathParts) == 0
}
