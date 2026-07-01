package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"xbot/internal/cmdbuilder"
	"xbot/llm"
)

const (
	tipFileCreated   = "文件已创建。建议用 Read 验证内容。"
	tipEditCompleted = "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"
)

// ============================================================================
// FileCreateTool — 创建新文件（2 params: path, content）
// ============================================================================

// FileCreateTool 创建新文件工具
type FileCreateTool struct{}

func (t *FileCreateTool) Name() string {
	return "FileCreate"
}

func (t *FileCreateTool) Description() string {
	return `Create a new file.
Required: path, content
Creates the file (and parent directories if needed). Returns error if file already exists, unless rewrite is set to true.

Examples:
- {"path": "hello.txt", "content": "Hello!"}
- {"path": "src/main.go", "content": "package main\n\nfunc main() {}"}
- {"path": "config.yaml", "content": "key: value", "rewrite": true}`
}

func (t *FileCreateTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path to create (relative to working directory or absolute)", Required: true},
		{Name: "content", Type: "string", Description: "Content to write to the new file", Required: true},
		{Name: "rewrite", Type: "boolean", Description: "Set to true to allow overwriting an existing file. Default is false.", Required: false},
		{Name: "run_as", Type: "string", Description: "OS username to execute as. Requires permission control to be enabled. Only effective in none sandbox mode.", Required: false},
		{Name: "reason", Type: "string", Description: "Optional human-readable reason shown in approval requests when approval is required.", Required: false},
	}
}

type FileCreateParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Rewrite bool   `json:"rewrite"`
	RunAs   string `json:"run_as"`
	Reason  string `json:"reason"`
}

func (t *FileCreateTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[FileCreateParams](input)
	if err != nil {
		return nil, err
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// When permission control is disabled, ignore stale run_as/reason from LLM cache
	if !isPermControlActiveFromCtx(ctx.Ctx) {
		params.RunAs = ""
		params.Reason = ""
	}

	if err := validateRunAsReason(params.RunAs, params.Reason); err != nil {
		return nil, err
	}

	if shouldUseSandbox(ctx) {
		sandboxPath := resolveSandboxPath(ctx, params.Path)
		return t.sandboxCreate(ctx, sandboxPath, params.Content, params.Rewrite)
	}
	return t.executeLocal(ctx, *params)
}

func (t *FileCreateTool) sandboxCreate(ctx *ToolContext, path, content string, rewrite bool) (*ToolResult, error) {
	if !rewrite {
		if err := sandboxWriteNewFile(ctx, path, content); err != nil {
			return nil, err
		}
	} else {
		if err := sandboxWriteFile(ctx, path, content); err != nil {
			return nil, err
		}
	}
	summary := fmt.Sprintf("File created successfully: %s", path)
	return &ToolResult{Summary: summary, Tips: tipFileCreated}, nil
}

func (t *FileCreateTool) executeLocal(ctx *ToolContext, params FileCreateParams) (*ToolResult, error) {
	// Per-tool timeout: 10s for single file I/O
	parentCtx := context.Background()
	if ctx != nil && ctx.Ctx != nil {
		parentCtx = ctx.Ctx
	}
	ioCtx, cancel := context.WithTimeout(parentCtx, EditLocalTimeout)
	defer cancel()

	filePath, err := ResolveWritePath(ctx, params.Path)
	if err != nil {
		return nil, err
	}

	// Check if file already exists
	if info, statErr := os.Stat(filePath); statErr == nil {
		if !params.Rewrite {
			return nil, fmt.Errorf("file already exists: %s (use FileReplace to modify existing files, or set rewrite=true to overwrite)", filePath)
		}
		// File size check for rewrite mode
		if info.Size() > MaxEditFileSize {
			return nil, fmt.Errorf("file too large (>%d bytes): %s", MaxEditFileSize, filePath)
		}
	}

	// Create parent directories if needed
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		select {
		case <-ioCtx.Done():
			return nil, fmt.Errorf("cancelled: %w", ioCtx.Err())
		default:
		}
		if params.RunAs != "" {
			if err := cmdbuilder.MkdirAllAsUser(params.RunAs, dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory as user %q: %w", params.RunAs, err)
			}
		} else if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}

	if err := cmdbuilder.WriteFileAsUser(params.RunAs, filePath, []byte(params.Content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	summary := fmt.Sprintf("File created successfully: %s", filePath)
	diff := computeUnifiedDiff(filePath, "", params.Content)
	result := &ToolResult{Summary: summary, Tips: tipFileCreated}
	if diff != "" {
		result.Metadata = map[string]string{"diff": diff}
	}
	return result, nil
}

// ============================================================================
// FileReplaceTool — 查找替换文件内容（7 params）
// ============================================================================

// FileReplaceTool 文件替换工具
type FileReplaceTool struct{}

func (t *FileReplaceTool) Name() string {
	return "FileReplace"
}

func (t *FileReplaceTool) Description() string {
	return `Find and replace text in a file.
Required: path, old_string, new_string
Optional: replace_all (default false), regex (default false), start_line, end_line

Default behavior: exact string match, replaces first occurrence only.
When regex=true, old_string is treated as RE2 pattern, new_string supports $1/$2 captures.
When replace_all=true, replaces all occurrences.

⚠️ Common mistakes (avoid these!):
- old_string should be unique in the file to avoid replacing the wrong occurrence.
- To restrict replacement to a specific range, use start_line and end_line.
- start_line and end_line restrict the search range. They do NOT select lines for replacement.

Examples:
- {"path": "main.go", "old_string": "foo", "new_string": "bar"}
- {"path": "main.go", "old_string": "oldName", "new_string": "newName", "replace_all": true}
- {"path": "main.go", "old_string": "v\\d+\\.\\d+", "new_string": "v2.0", "regex": true}
- {"path": "main.go", "old_string": "foo", "new_string": "bar", "start_line": 10, "end_line": 20}`
}

func (t *FileReplaceTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path (relative to working directory or absolute)", Required: true},
		{Name: "old_string", Type: "string", Description: "Text to find (exact match by default). When regex=true, treated as RE2 pattern.", Required: true},
		{Name: "new_string", Type: "string", Description: "Replacement text. Supports $1/$2 captures when regex=true.", Required: true},
		{Name: "replace_all", Type: "boolean", Description: "Replace all occurrences (default false, replaces first only)", Required: false},
		{Name: "regex", Type: "boolean", Description: "Use RE2 regex matching (default false, exact match)", Required: false},
		{Name: "start_line", Type: "integer", Description: "Restrict search from this line, 1-based inclusive", Required: false},
		{Name: "end_line", Type: "integer", Description: "Restrict search to this line, 1-based inclusive", Required: false},
		{Name: "run_as", Type: "string", Description: "OS username to execute as. Requires permission control to be enabled. Only effective in none sandbox mode.", Required: false},
		{Name: "reason", Type: "string", Description: "Optional human-readable reason shown in approval requests when approval is required.", Required: false},
	}
}

type FileReplaceParams struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Regex      bool   `json:"regex"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	RunAs      string `json:"run_as"`
	Reason     string `json:"reason"`
}

func (t *FileReplaceTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[FileReplaceParams](input)
	if err != nil {
		return nil, err
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if params.OldString == "" {
		return nil, fmt.Errorf("old_string is required")
	}

	// When permission control is disabled, ignore stale run_as/reason from LLM cache
	if !isPermControlActiveFromCtx(ctx.Ctx) {
		params.RunAs = ""
		params.Reason = ""
	}

	if err := validateRunAsReason(params.RunAs, params.Reason); err != nil {
		return nil, err
	}

	// When only end_line is specified, default start_line to 1
	if params.EndLine > 0 && params.StartLine <= 0 {
		params.StartLine = 1
	}

	if shouldUseSandbox(ctx) {
		sandboxPath := resolveSandboxPath(ctx, params.Path)
		return t.executeInSandbox(ctx, sandboxPath, *params)
	}
	return t.executeLocal(ctx, *params)
}

func (t *FileReplaceTool) executeInSandbox(ctx *ToolContext, path string, params FileReplaceParams) (*ToolResult, error) {
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	newContent, result, err := doReplace(oldContent, params, path)
	if err != nil {
		return nil, err
	}

	if err := sandboxWriteFile(ctx, path, newContent); err != nil {
		return nil, err
	}

	return &ToolResult{Summary: result, Tips: tipEditCompleted}, nil
}

func (t *FileReplaceTool) executeLocal(ctx *ToolContext, params FileReplaceParams) (*ToolResult, error) {
	// Per-tool timeout: 10s for single file I/O
	parentCtx := context.Background()
	if ctx != nil && ctx.Ctx != nil {
		parentCtx = ctx.Ctx
	}
	ioCtx, cancel := context.WithTimeout(parentCtx, EditLocalTimeout)
	defer cancel()

	filePath, err := ResolveWritePath(ctx, params.Path)
	if err != nil {
		return nil, err
	}

	// File size check to prevent OOM on large files
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.Size() > MaxEditFileSize {
		return nil, fmt.Errorf("file too large (>%d bytes): %s — use Shell with sed to edit large files", MaxEditFileSize, filePath)
	}

	// Read file with context-aware cancellation
	type readResult struct {
		content []byte
		err     error
	}
	readCh := make(chan readResult, 1)
	go func() {
		data, err := cmdbuilder.ReadFileAsUser(params.RunAs, filePath)
		readCh <- readResult{data, err}
	}()

	var content []byte
	select {
	case <-ioCtx.Done():
		return nil, fmt.Errorf("read timed out or cancelled: %w", ioCtx.Err())
	case res := <-readCh:
		if res.err != nil {
			return nil, fmt.Errorf("failed to read file: %w", res.err)
		}
		content = res.content
	}

	mode := info.Mode().Perm()

	newContent, result, err := doReplace(string(content), params, filePath)
	if err != nil {
		return nil, err
	}

	if err := cmdbuilder.WriteFileAsUser(params.RunAs, filePath, []byte(newContent), mode); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	diff := computeUnifiedDiff(filePath, string(content), newContent)
	toolResult := &ToolResult{Summary: result, Tips: tipEditCompleted}
	if diff != "" {
		toolResult.Metadata = map[string]string{"diff": diff}
	}
	return toolResult, nil
}

// ============================================================================
// Shared helpers
// ============================================================================

// resolveSandboxPath 将用户输入的路径转换为容器内路径
func resolveSandboxPath(ctx *ToolContext, userPath string) string {
	sandboxBase := sandboxBaseDir(ctx)

	if !strings.HasPrefix(userPath, sandboxBase+"/") && userPath != sandboxBase && !strings.HasPrefix(userPath, "/") {
		if sandboxCWD := resolveSandboxCWD(ctx, sandboxBase); sandboxCWD != "" {
			// Sandbox paths always use forward slashes (Linux container)
			return path.Join(sandboxCWD, filepath.ToSlash(userPath))
		}
		return sandboxBase + "/" + userPath
	} else if strings.HasPrefix(userPath, sandboxBase+"/") || userPath == sandboxBase {
		return userPath
	} else if strings.HasPrefix(userPath, "/") {
		if ctx.WorkspaceRoot != "" {
			if rel, err := filepath.Rel(ctx.WorkspaceRoot, userPath); err == nil && !strings.HasPrefix(rel, "..") {
				return sandboxBase + "/" + filepath.ToSlash(rel)
			}
		}
	}
	return userPath
}

// sandboxReadFile 通过 cat 读取沙箱内文件内容（保留原始内容，不做 TrimSpace）
func sandboxReadFile(ctx *ToolContext, path string) (string, error) {
	cmd := fmt.Sprintf("cat '%s'", strings.ReplaceAll(path, "'", "'\\''"))
	content, err := RunInSandboxRawWithShellTimeout(ctx, cmd, EditLocalTimeout)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %v", path, err)
	}
	return content, nil
}

// sandboxWriteFile 将内容 base64 编码后写入沙箱内文件（彻底避免 shell 转义问题）
func sandboxWriteFile(ctx *ToolContext, path, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	safePath := strings.ReplaceAll(path, "'", "'\\''")
	cmd := fmt.Sprintf("echo '%s' | base64 -d > '%s'", encoded, safePath)
	_, err := RunInSandboxWithShellTimeout(ctx, cmd, EditLocalTimeout)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %v", path, err)
	}
	return nil
}

// sandboxWriteNewFile 创建新文件并写入内容（含 mkdir -p），通过 base64 避免转义
func sandboxWriteNewFile(ctx *ToolContext, path, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	safePath := strings.ReplaceAll(path, "'", "'\\''")
	cmd := fmt.Sprintf("mkdir -p '%s' && echo '%s' | base64 -d > '%s'",
		strings.ReplaceAll(filepath.Dir(path), "'", "'\\''"), encoded, safePath)
	_, err := RunInSandboxWithShellTimeout(ctx, cmd, EditLocalTimeout)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %v", path, err)
	}
	return nil
}

// doReplace 执行文本替换（支持精确匹配和 RE2 正则匹配）
// SECURITY NOTE: Go's regexp package uses RE2 engine which guarantees O(n) time complexity
// for all operations, preventing ReDoS attacks.
func doReplace(content string, params FileReplaceParams, filePath string) (string, string, error) {
	if params.OldString == "" {
		return "", "", fmt.Errorf("old_string is required")
	}
	// Guard against excessively large inputs that could cause memory/CPU issues.
	const maxParamLen = 4 << 20 // 4 MB
	if len(params.OldString) > maxParamLen {
		return "", "", fmt.Errorf("old_string too large (%d bytes, max %d)", len(params.OldString), maxParamLen)
	}
	if len(params.NewString) > maxParamLen {
		return "", "", fmt.Errorf("new_string too large (%d bytes, max %d)", len(params.NewString), maxParamLen)
	}

	// Split content by line range if start_line/end_line specified
	prefix, rangeText, suffix, err := splitContentByLineRange(content, params.StartLine, params.EndLine)
	if err != nil {
		return "", "", err
	}

	if params.Regex {
		return doRegexReplace(prefix, rangeText, suffix, params, filePath, content)
	}

	// Exact string match
	count := strings.Count(rangeText, params.OldString)
	if count == 0 {
		// Fuzzy whitespace fallback: try matching with leading/trailing whitespace stripped per line
		if actualOld, adjustedNew, ok := fuzzyWhitespaceMatch(rangeText, params.OldString, params.NewString); ok {
			actualCount := strings.Count(rangeText, actualOld)
			var newRangeText string
			var replacedCount int
			if params.ReplaceAll {
				newRangeText = strings.ReplaceAll(rangeText, actualOld, adjustedNew)
				replacedCount = actualCount
			} else {
				newRangeText = strings.Replace(rangeText, actualOld, adjustedNew, 1)
				replacedCount = 1
			}
			newContent := prefix + newRangeText + suffix
			if actualCount > 1 && !params.ReplaceAll {
				return newContent, fmt.Sprintf("Replaced 1 of %d occurrences (auto-corrected whitespace) in %s. Use replace_all=true to replace all.", actualCount, filePath), nil
			}
			return newContent, fmt.Sprintf("Successfully replaced %d occurrence(s) (auto-corrected whitespace) in %s", replacedCount, filePath), nil
		}

		hint := suggestMatch(rangeText, params.OldString)
		if params.StartLine > 0 || params.EndLine > 0 {
			effStart := params.StartLine
			if effStart <= 0 {
				effStart = 1
			}
			effEnd := params.EndLine
			if effEnd <= 0 {
				lines, _ := splitLines(content)
				effEnd = len(lines)
			}
			return "", "", fmt.Errorf("text not found in lines %d-%d: %q%s", effStart, effEnd, params.OldString, hint)
		}
		return "", "", fmt.Errorf("text not found: %q%s", params.OldString, hint)
	}

	// Normalize indentation in the replacement text so that new lines added
	// by the LLM follow the same whitespace convention (tab vs space) as the
	// matched region in the file.
	normalizedNew := normalizeReplacementIndent(params.OldString, params.NewString)

	var newRangeText string
	var replacedCount int

	if params.ReplaceAll {
		newRangeText = strings.ReplaceAll(rangeText, params.OldString, normalizedNew)
		replacedCount = count
	} else {
		newRangeText = strings.Replace(rangeText, params.OldString, normalizedNew, 1)
		replacedCount = 1
	}

	newContent := prefix + newRangeText + suffix

	if count > 1 && !params.ReplaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d occurrences. Use replace_all=true to replace all.", count), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d occurrence(s) in %s", replacedCount, filePath), nil
}

// doRegexReplace 执行正则替换（由 doReplace 在 regex=true 时调用）
func doRegexReplace(prefix, rangeText, suffix string, params FileReplaceParams, filePath, fullContent string) (string, string, error) {
	re, err := regexp.Compile(params.OldString)
	if err != nil {
		return "", "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	matches := re.FindAllString(rangeText, -1)
	if len(matches) == 0 {
		if params.StartLine > 0 || params.EndLine > 0 {
			effStart := params.StartLine
			if effStart <= 0 {
				effStart = 1
			}
			effEnd := params.EndLine
			if effEnd <= 0 {
				lines, _ := splitLines(fullContent)
				effEnd = len(lines)
			}
			return "", "", fmt.Errorf("no match found for pattern in lines %d-%d: %s", effStart, effEnd, params.OldString)
		}
		return "", "", fmt.Errorf("no match found for pattern: %s", params.OldString)
	}

	var newRangeText string
	var replacedCount int

	if params.ReplaceAll {
		newRangeText = re.ReplaceAllString(rangeText, params.NewString)
		replacedCount = len(matches)
	} else {
		newRangeText = re.ReplaceAllStringFunc(rangeText, func(m string) string {
			if replacedCount == 0 {
				replacedCount++
				return re.ReplaceAllString(m, params.NewString)
			}
			return m
		})
	}

	newContent := prefix + newRangeText + suffix

	if len(matches) > 1 && !params.ReplaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d matches. Use replace_all=true to replace all.", len(matches)), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d match(es) in %s", replacedCount, filePath), nil
}

// splitLines splits content into lines, correctly handling trailing newline.
func splitLines(content string) ([]string, bool) {
	lines := strings.Split(content, "\n")
	hasTrailingNL := len(lines) > 1 && lines[len(lines)-1] == ""
	if hasTrailingNL {
		lines = lines[:len(lines)-1]
	}
	return lines, hasTrailingNL
}

// splitContentByLineRange splits content by line range for replace operations.
// Returns prefix (before range), rangeText (within range), suffix (after range), and error.
// When start=0 and end=0, returns the entire content as rangeText (no line restriction).
func splitContentByLineRange(content string, start, end int) (string, string, string, error) {
	lines, hasTrailingNL := splitLines(content)
	totalLines := len(lines)

	if start <= 0 && end <= 0 {
		return "", content, "", nil
	}

	if end > totalLines {
		return "", "", "", fmt.Errorf("end_line %d exceeds total lines %d", end, totalLines)
	}
	if start > end {
		return "", "", "", fmt.Errorf("start_line %d is greater than end_line %d", start, end)
	}

	startIdx := start - 1
	endIdx := end

	prefix := ""
	if startIdx > 0 {
		prefix = strings.Join(lines[:startIdx], "\n") + "\n"
	}

	rangeText := strings.Join(lines[startIdx:endIdx], "\n")

	suffix := ""
	if endIdx < totalLines {
		suffix = "\n" + strings.Join(lines[endIdx:], "\n")
	}
	if hasTrailingNL {
		suffix += "\n"
	}

	return prefix, rangeText, suffix, nil
}

// leadingWhitespace returns the leading whitespace prefix of s.
func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// adjustIndentation adjusts newStr's indentation based on the difference
// between oldLines (what the LLM sent) and actualLines (what the file has).
//
// oldLines and actualLines are guaranteed line-by-line aligned (same count, same
// trimmed content). We compute the indent delta per line pair and apply a
// consistent mapping to every line of newStr.
//
// Strategy:
//  1. From all non-empty line pairs, derive the base indent (minimum old indent)
//     and the corresponding actual base indent.
//  2. For each newStr line, compute how many characters deeper than the base
//     its indent is, then produce: actualBase + scaled_relative + rest.
//     The scaling ratio is derived from the line pairs so tab↔space works.
func adjustIndentation(oldLines, actualLines []string, newStr string) string {
	// Collect indent pairs from non-empty lines.
	type indentPair struct{ old, actual string }
	var pairs []indentPair
	for i, line := range oldLines {
		if strings.TrimSpace(line) == "" || i >= len(actualLines) {
			continue
		}
		pairs = append(pairs, indentPair{
			old:    leadingWhitespace(line),
			actual: leadingWhitespace(actualLines[i]),
		})
	}
	if len(pairs) == 0 {
		return newStr
	}

	// Find minimum indents (the common base level).
	minOld := pairs[0].old
	minActual := pairs[0].actual
	for _, p := range pairs[1:] {
		if len(p.old) < len(minOld) {
			minOld = p.old
		}
		if len(p.actual) < len(minActual) {
			minActual = p.actual
		}
	}

	// Nothing to adjust if ALL pairs are truly identical (every old indent
	// matches its corresponding actual indent).  Only checking minOld ==
	// minActual is insufficient: deeper lines may still have mismatched
	// whitespace (e.g. spaces vs tabs) even when the base level agrees.
	allIdentical := true
	for _, p := range pairs {
		if p.old != p.actual {
			allIdentical = false
			break
		}
	}
	if allIdentical {
		return newStr
	}

	// Compute average scale: how many actual-chars per old-char for extra depth.
	// Use weighted average across all pairs with extra depth.
	var totalOldExtra, totalActualExtra float64
	for _, p := range pairs {
		extraOld := len(p.old) - len(minOld)
		extraActual := len(p.actual) - len(minActual)
		if extraOld > 0 {
			totalOldExtra += float64(extraOld)
			totalActualExtra += float64(extraActual)
		}
	}
	scale := 1.0
	if totalOldExtra > 0 {
		scale = totalActualExtra / totalOldExtra
	}

	// Determine the character used for actual indentation (tab or space).
	actualPadChar := byte(' ')
	if strings.ContainsRune(minActual, '\t') {
		actualPadChar = '\t'
	}

	// Apply to each line of newStr.
	newLines := strings.Split(newStr, "\n")
	for i, line := range newLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingWhitespace(line)
		rest := line[len(indent):]

		relativeLen := len(indent) - len(minOld)
		if relativeLen <= 0 {
			// At or below base level: use actual base indent.
			newLines[i] = minActual + rest
		} else {
			// Scale the relative portion.
			scaledLen := int(math.Round(float64(relativeLen) * scale))
			if scaledLen < 0 {
				scaledLen = 0
			}
			newLines[i] = minActual + strings.Repeat(string(actualPadChar), scaledLen) + rest
		}
	}
	return strings.Join(newLines, "\n")
}

// normalizeReplacementIndent ensures newStr's indentation follows the same
// whitespace convention (tab vs space) as matchedOld, which is the exact
// text from the file. This handles the case where the LLM correctly matches
// old_string (with proper tabs/spaces) but writes new lines with a different
// whitespace style.
//
// Strategy:
//  1. Detect the file's indent style from matchedOld (tab or space).
//  2. Build a map of trimmed content → file indent for lines in matchedOld.
//  3. For each line in newStr:
//     - If it exists in matchedOld (same trimmed content), use the file's exact indent.
//     - If it's a genuinely new line, inherit the indent of the nearest
//     old-matching line (peer-level heuristic).
//     - Blank lines: strip whitespace for consistency.
func normalizeReplacementIndent(matchedOld, newStr string) string {
	oldLines := strings.Split(matchedOld, "\n")

	// Detect file indent style from matchedOld.
	fileUsesTabs := false
	for _, line := range oldLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.ContainsRune(leadingWhitespace(line), '\t') {
			fileUsesTabs = true
			break
		}
	}
	if !fileUsesTabs {
		return newStr // file uses spaces — less common case, skip for now
	}

	// Build trimmed content → file indent map.
	// Use the first occurrence to avoid ambiguity from duplicate lines.
	fileIndent := make(map[string]string)
	for _, line := range oldLines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if _, exists := fileIndent[trimmed]; !exists {
				fileIndent[trimmed] = leadingWhitespace(line)
			}
		}
	}

	// Check if newStr actually has space-indented lines that need correction.
	needsFix := false
	for _, line := range strings.Split(newStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// Blank line with whitespace: needs cleanup.
			if line != "" {
				needsFix = true
				break
			}
			continue
		}
		indent := leadingWhitespace(line)
		if indent != "" && !strings.ContainsRune(indent, '\t') {
			// This line has space-only indentation and file uses tabs.
			// But skip if this line exists in old with the SAME indent (no issue).
			if fileInd, ok := fileIndent[trimmed]; !ok || fileInd != indent {
				needsFix = true
				break
			}
		}
	}
	if !needsFix {
		return newStr
	}

	// Normalize each line.
	newLines := strings.Split(newStr, "\n")

	// Detect tab width from the file. Look at existing lines that use tabs
	// and find the common indent delta (e.g. \t vs \t\t = 1 tab per level).
	// Default to 4 if we can't determine.
	tabWidth := 4
	for _, line := range oldLines {
		indent := leadingWhitespace(line)
		if strings.ContainsRune(indent, '\t') {
			// Count tabs
			tabCount := strings.Count(indent, "\t")
			if tabCount > 0 {
				// The visual width of this indent (assuming tabWidth=4)
				// vs the tab count tells us the ratio
				break
			}
		}
	}

	// Build a reference: first old line's indent depth in tab equivalents.
	// This helps us convert new lines' space-indents to correct tab depth.
	refFileTabDepth := 0
	refNewSpaceDepth := 0
	for _, line := range newLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if fIndent, ok := fileIndent[trimmed]; ok {
			refFileTabDepth = strings.Count(fIndent, "\t")
			// Use visual width, not byte length — a \t is tabWidth columns,
			// not 1. Otherwise "    " (4 spaces) vs "\t" (1 byte) gives
			// spaceDelta=3 and produces an extra tab level.
			indent := leadingWhitespace(line)
			visualW := 0
			for _, r := range indent {
				if r == '\t' {
					visualW += tabWidth - (visualW % tabWidth)
				} else {
					visualW++
				}
			}
			refNewSpaceDepth = visualW
			break
		}
	}

	// lastNormalizedIndent tracks the indent of the most recent line that
	// was successfully normalized. Used as fallback for lines with no indent.
	lastNormalizedIndent := ""

	for i, line := range newLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// Blank line: strip whitespace for cleanliness.
			newLines[i] = ""
			continue
		}

		if fIndent, ok := fileIndent[trimmed]; ok {
			// Line exists in old — use file's exact indent.
			newLines[i] = fIndent + trimmed
			lastNormalizedIndent = fIndent
		} else {
			// Genuinely new line.
			currIndent := leadingWhitespace(line)
			if strings.ContainsRune(currIndent, '\t') {
				// Already uses tabs, keep as-is.
				newLines[i] = line
				lastNormalizedIndent = currIndent
			} else if currIndent != "" && refNewSpaceDepth > 0 {
				// Has spaces where file uses tabs.
				// Convert space depth to tab depth using reference ratio.
				spaceDelta := len(currIndent) - refNewSpaceDepth
				newTabDepth := refFileTabDepth
				if spaceDelta > 0 {
					newTabDepth += (spaceDelta + tabWidth - 1) / tabWidth
				} else if spaceDelta < 0 {
					newTabDepth -= (-spaceDelta) / tabWidth
					if newTabDepth < 0 {
						newTabDepth = 0
					}
				}
				indent := strings.Repeat("\t", newTabDepth)
				newLines[i] = indent + trimmed
				lastNormalizedIndent = indent
			} else if currIndent == "" && lastNormalizedIndent != "" {
				// New line has NO indent but previous line had tabs.
				// LLMs often omit indentation for body lines.
				// Inherit the previous line's indent as the best guess.
				newLines[i] = lastNormalizedIndent + trimmed
			}
			// If no reference at all, leave as-is.
		}
	}

	return strings.Join(newLines, "\n")
}

// fuzzyWhitespaceMatch attempts whitespace-tolerant matching when exact match fails.
// It strips leading+trailing whitespace per line and uses a sliding window to find
// a unique match. Returns (actualOldString, adjustedNewString, true) on success.
// Requires exactly 1 match; returns false on 0 or 2+ matches (ambiguous).
func fuzzyWhitespaceMatch(content, oldStr, newStr string) (string, string, bool) {
	oldLines := strings.Split(oldStr, "\n")
	contentLines := strings.Split(content, "\n")

	// Strip for comparison
	strippedOld := make([]string, len(oldLines))
	hasContent := false
	for i, line := range oldLines {
		strippedOld[i] = strings.TrimSpace(line)
		if strippedOld[i] != "" {
			hasContent = true
		}
	}
	if !hasContent {
		return "", "", false
	}

	windowSize := len(strippedOld)
	if windowSize > len(contentLines) {
		return "", "", false
	}

	// Sliding window search
	var matchIdx int
	matchCount := 0
	for i := 0; i <= len(contentLines)-windowSize; i++ {
		match := true
		for j, stripped := range strippedOld {
			if strings.TrimSpace(contentLines[i+j]) != stripped {
				match = false
				break
			}
		}
		if match {
			matchCount++
			matchIdx = i
			if matchCount > 1 {
				return "", "", false
			}
		}
	}

	if matchCount != 1 {
		return "", "", false
	}

	actualLines := contentLines[matchIdx : matchIdx+windowSize]
	actualOld := strings.Join(actualLines, "\n")

	adjustedNew := adjustIndentation(oldLines, actualLines, newStr)

	return actualOld, adjustedNew, true
}

// suggestMatch tries to find similar text when exact match fails.
// Helps the LLM identify whitespace/indentation mismatches.
func suggestMatch(content, searchStr string) string {
	var firstLine string
	for _, l := range strings.Split(searchStr, "\n") {
		if t := strings.TrimSpace(l); t != "" && len(t) >= 3 {
			firstLine = t
			break
		}
	}
	if firstLine == "" {
		return ""
	}
	lines, _ := splitLines(content)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, firstLine) {
			return fmt.Sprintf("\nHint: line %d has similar text (possible whitespace mismatch): %q", i+1, Truncate(trimmed, 100))
		}
	}
	return ""
}

// Truncate 截断字符串（公共函数，供多处使用）
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen < 4 {
		return "..."
	}
	return string(runes[:maxLen-3]) + "..."
}

// computeUnifiedDiff generates a unified diff between old and new content.
// Uses the system `diff` command. Returns empty string on error or if no diff.
// The diff is capped at 200 lines to avoid excessive metadata.
func computeUnifiedDiff(label string, oldContent, newContent string) string {
	oldFile, err := os.CreateTemp("", "xbot-diff-old-*")
	if err != nil {
		return ""
	}
	defer os.Remove(oldFile.Name())
	oldFile.WriteString(oldContent)
	oldFile.Close()

	newFile, err := os.CreateTemp("", "xbot-diff-new-*")
	if err != nil {
		return ""
	}
	defer os.Remove(newFile.Name())
	newFile.WriteString(newContent)
	newFile.Close()

	// For absolute paths, strip leading / to avoid a//home/... double slash
	diffLabel := strings.TrimPrefix(label, "/")

	cmd := exec.Command("diff", "-u",
		"--label", "a/"+diffLabel,
		"--label", "b/"+diffLabel,
		oldFile.Name(), newFile.Name())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	// diff returns exit code 1 when there are differences — ignore that error.
	_ = cmd.Run()

	result := out.String()
	if result == "" {
		return ""
	}

	// Cap at 200 lines to avoid bloating metadata
	lines := strings.Split(result, "\n")
	if len(lines) > 200 {
		lines = lines[:200]
		lines = append(lines, fmt.Sprintf("... (%d more lines truncated)", len(lines)-200))
	}
	return strings.Join(lines, "\n")
}
