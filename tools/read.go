package tools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"xbot/llm"
)

// DefaultMaxReadLines: no default truncation — offload handles large results.
// Only applies when the user explicitly passes max_lines > 0.
const DefaultMaxReadLines = 0

// ReadTool file reading tool
type ReadTool struct{}

func (t *ReadTool) Name() string {
	return "Read"
}

func (t *ReadTool) Description() string {
	return `Read a file and return its content.
Each output line is prefixed with its line number (1-based), useful for Edit tool's line mode.
Parameters (JSON):
  - path: string, the file path to read (relative to working directory or absolute)
  - max_lines: number, maximum lines to return (0 or omit = no limit)
  - offset: number, start reading from this line number (1-based, 0 or omit = start from beginning)
Example: {"path": "hello.txt"}
Example: {"path": "hello.txt", "offset": 100, "max_lines": 50}`
}

func (t *ReadTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "The file path to read", Required: true},
		{Name: "max_lines", Type: "integer", Description: "Maximum lines to return (0 or omit = no limit)"},
		{Name: "offset", Type: "integer", Description: "Start reading from this line number (1-based, 0 or omit = start from beginning)"},
	}
}

func (t *ReadTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path     string `json:"path"`
		MaxLines int    `json:"max_lines"`
		Offset   int    `json:"offset"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Sandbox mode: execute cat command inside the container
	if shouldUseSandbox(ctx) {
		result, err := t.executeInSandbox(ctx, params.Path)
		if err != nil {
			return nil, err
		}
		return applyLineLimit(result, params.MaxLines, params.Offset), nil
	}

	// Non-sandbox mode: local read
	result, err := t.executeLocal(ctx, params.Path)
	if err != nil {
		return nil, err
	}
	return applyLineLimit(result, params.MaxLines, params.Offset), nil
}

// applyLineLimit applies offset and maxLines to the tool result.
// offset is 1-based: offset=10 means skip the first 9 lines, start from line 10.
// Only applies when the respective parameter is > 0 (explicitly requested by user).
// Large results without explicit truncation are handled by the offload system.
func applyLineLimit(result *ToolResult, maxLines, offset int) *ToolResult {
	if result == nil {
		return result
	}
	if result.Summary == "" {
		return result
	}

	lines := strings.Split(result.Summary, "\n")
	totalLines := len(lines)

	// Determine the starting line number (1-based) before slicing
	startLineNum := 1

	// Apply offset (1-based): offset=N means skip first N-1 lines
	if offset > 0 {
		startLineNum = offset
		// Convert to 0-based: if offset=10, we want lines[9:]
		startIdx := offset - 1
		if startIdx < 0 {
			startIdx = 0
		}
		if startIdx >= totalLines {
			// offset beyond file end — return empty with a hint
			result.Summary = fmt.Sprintf("(offset %d exceeds file length %d — file has no content from this line)", offset, totalLines)
			return result
		}
		lines = lines[startIdx:]
	}

	// Apply maxLines truncation
	var truncatedMsg string
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		truncatedMsg = fmt.Sprintf("\n\n... [truncated: showing %d of %d lines, use max_lines parameter to see more]", maxLines, totalLines)
	}

	// Add line numbers to each line
	maxLineNum := startLineNum + len(lines) - 1
	width := len(strconv.Itoa(maxLineNum))
	numbered := make([]string, len(lines))
	for i, line := range lines {
		numbered[i] = fmt.Sprintf("%*d\t%s", width, startLineNum+i, line)
	}

	result.Summary = strings.Join(numbered, "\n") + truncatedMsg
	return result
}

// executeInSandbox executes cat in the sandbox
func (t *ReadTool) executeInSandbox(ctx *ToolContext, filePath string) (*ToolResult, error) {
	sandboxBase := sandboxBaseDir(ctx)

	// converts a user-input path to a container-internal path
	sandboxPath := filePath
	if !strings.HasPrefix(filePath, sandboxBase+"/") && filePath != sandboxBase && !strings.HasPrefix(filePath, "/") {
		// Relative path: prefer CurrentDir (sandbox path after Cd), otherwise sandboxBase
		sandboxCWD := resolveSandboxCWD(ctx, sandboxBase)
		if sandboxCWD != "" {
			sandboxPath = path.Join(sandboxCWD, filePath)
		} else {
			sandboxPath = sandboxBase + "/" + filePath
		}
	} else if strings.HasPrefix(filePath, sandboxBase+"/") || filePath == sandboxBase {
		sandboxPath = filePath
	} else if strings.HasPrefix(filePath, "/") {
		if ctx.WorkspaceRoot != "" {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, filePath)
			if err == nil && !strings.HasPrefix(rel, "..") {
				sandboxPath = sandboxBase + "/" + rel
			}
		}
	}

	// Execute cat inside the container
	cmd := fmt.Sprintf("cat '%s'", shellEscape(sandboxPath))
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file in sandbox: %v, output: %s", err, output)
	}

	return NewResultWithTips(output, "如需修改此文件，优先使用 Edit 工具。"), nil
}

// executeLocal reads file locally
func (t *ReadTool) executeLocal(ctx *ToolContext, filePath string) (*ToolResult, error) {
	// ResolveReadPath internally already supports CurrentDir-priority resolution.
	// If the file doesn't exist under CurrentDir, fall through to WorkspaceRoot resolution —
	// This allows the agent to read files under workspace root even after cd'ing into a subdirectory.
	resolvedPath, err := ResolveReadPath(ctx, filePath)
	if err == nil {
		if _, statErr := os.Stat(resolvedPath); statErr != nil && ctx != nil && ctx.CurrentDir != "" && !filepath.IsAbs(filePath) {
			// Not found under CurrentDir, try resolving from workspace root
			root, rootErr := resolveScopedBase(ctx)
			if rootErr == nil {
				rootPath := filepath.Join(root, filePath)
				if fallback, fbErr := ResolveReadPath(ctx, rootPath); fbErr == nil {
					if _, fbStatErr := os.Stat(fallback); fbStatErr == nil {
						resolvedPath = fallback
						err = nil
					}
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return NewResultWithTips(string(content), "如需修改此文件，优先使用 Edit 工具。"), nil
}
