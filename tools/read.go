package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"xbot/llm"
)

// DefaultMaxReadLines: no default truncation — offload handles large results.
// Only applies when the user explicitly passes max_lines > 0.
const DefaultMaxReadLines = 0

// MaxImageReadSize: images are base64-encoded (~4/3x expansion), so limit to 5MB raw.
const MaxImageReadSize = 5 * 1024 * 1024

// imageExts maps file extensions to MIME types.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// detectImageMIME returns the MIME type if the file is a supported image, else "".
// Checks magic bytes first (reliable), falls back to extension.
func detectImageMIME(filePath string, info os.FileInfo) string {
	// Quick check by extension first (avoids reading non-image files)
	ext := strings.ToLower(filepath.Ext(filePath))
	mime, isImage := imageExts[ext]
	if !isImage {
		return ""
	}
	// Verify with magic bytes — read first 16 bytes
	f, err := os.Open(filePath)
	if err != nil {
		return mime // fall back to extension
	}
	defer f.Close()
	header := make([]byte, 16)
	n, _ := f.Read(header)
	header = header[:n]
	switch {
	case len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(header) >= 3 && header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF:
		return "image/jpeg"
	case len(header) >= 6 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a"):
		return "image/gif"
	case len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "image/webp"
	}
	return mime // fall back to extension if magic bytes don't match
}

// ReadTool 读取文件工具
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
		return nil, err
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// 沙箱模式：在容器内执行 cat 命令
	if shouldUseSandbox(ctx) {
		result, err := t.executeInSandbox(ctx, params.Path)
		if err != nil {
			return nil, err
		}
		if len(result.Images) > 0 {
			return result, nil // skip line numbering for images
		}
		return applyLineLimit(result, params.MaxLines, params.Offset), nil
	}

	// 非沙箱模式：本地读取
	result, err := t.executeLocal(ctx, params.Path)
	if err != nil {
		return nil, err
	}
	if len(result.Images) > 0 {
		return result, nil // skip line numbering for images
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
	width := len(fmt.Sprintf("%d", maxLineNum))
	numbered := make([]string, len(lines))
	for i, line := range lines {
		numbered[i] = fmt.Sprintf("%*d\t%s", width, startLineNum+i, line)
	}

	result.Summary = strings.Join(numbered, "\n") + truncatedMsg
	return result
}

// executeInSandbox 在沙箱容器内执行 cat 命令
func (t *ReadTool) executeInSandbox(ctx *ToolContext, filePath string) (*ToolResult, error) {
	sandboxBase := sandboxBaseDir(ctx)

	// 将用户输入的路径转换为容器内路径
	sandboxPath := filePath
	if !strings.HasPrefix(filePath, sandboxBase+"/") && filePath != sandboxBase && !strings.HasPrefix(filePath, "/") {
		// 相对路径：优先基于 CurrentDir（Cd 后的沙箱路径），否则 sandboxBase
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

	// Image file detection in sandbox: check extension
	ext := strings.ToLower(filepath.Ext(sandboxPath))
	if mimeType, isImage := imageExts[ext]; isImage {
		// Use base64 command instead of cat for image files
		cmd := fmt.Sprintf("base64 '%s'", shellEscape(sandboxPath))
		output, err := RunInSandboxWithShellTimeout(ctx, cmd, ReadLocalTimeout)
		if err != nil {
			return nil, fmt.Errorf("failed to read image in sandbox: %v, output: %s", err, output)
		}
		// base64 command output may contain newlines; strip them
		encoded := strings.TrimSpace(strings.ReplaceAll(output, "\n", ""))
		// Get file size via stat for the summary
		sizeCmd := fmt.Sprintf("stat -c %%s '%s' 2>/dev/null || wc -c < '%s'", shellEscape(sandboxPath), shellEscape(sandboxPath))
		sizeOutput, _ := RunInSandboxWithShellTimeout(ctx, sizeCmd, ReadLocalTimeout)
		sizeStr := strings.TrimSpace(sizeOutput)
		return &ToolResult{
			Summary: fmt.Sprintf("(Image file: %s, %s bytes, %s)", filepath.Base(sandboxPath), sizeStr, mimeType),
			Tips:    "Image content is provided as a native image block to the model.",
			Images:  []llm.ImageContent{{MediaType: mimeType, Data: encoded}},
		}, nil
	}

	// 在容器内执行 cat
	cmd := fmt.Sprintf("cat '%s'", shellEscape(sandboxPath))
	output, err := RunInSandboxWithShellTimeout(ctx, cmd, ReadLocalTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to read file in sandbox: %v, output: %s", err, output)
	}

	return NewResultWithTips(output, "如需修改此文件，优先使用 Edit 工具。"), nil
}

// executeLocal 在本地读取文件
func (t *ReadTool) executeLocal(ctx *ToolContext, filePath string) (*ToolResult, error) {
	// Per-tool timeout: 10s for single file I/O
	parentCtx := context.Background()
	if ctx != nil && ctx.Ctx != nil {
		parentCtx = ctx.Ctx
	}
	readCtx, cancel := context.WithTimeout(parentCtx, ReadLocalTimeout)
	defer cancel()

	// ResolveReadPath 内部已支持 CurrentDir 优先解析。
	// 若 CurrentDir 下文件不存在，fallthrough 到 WorkspaceRoot 解析——
	// 这使得 agent cd 到子目录后仍能读取 workspace root 下的文件。
	resolvedPath, err := ResolveReadPath(ctx, filePath)
	if err == nil {
		if _, statErr := os.Stat(resolvedPath); statErr != nil && ctx != nil && ctx.CurrentDir != "" && !filepath.IsAbs(filePath) {
			// CurrentDir 下找不到，尝试从 workspace root 解析
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

	// File size check to prevent OOM on large files
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.Size() > MaxReadFileSize {
		return nil, fmt.Errorf("file too large (>%d bytes): %s — use Shell with head/tail to read portions", MaxReadFileSize, resolvedPath)
	}

	// Image file detection: if the file is a supported image, base64-encode it
	// and return via ToolResult.Images instead of raw text.
	if mimeType := detectImageMIME(resolvedPath, info); mimeType != "" {
		if info.Size() > MaxImageReadSize {
			return nil, fmt.Errorf("image file too large (>%d bytes): %s", MaxImageReadSize, resolvedPath)
		}
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read image file: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		return &ToolResult{
			Summary: fmt.Sprintf("(Image file: %s, %d bytes, %s)", filepath.Base(resolvedPath), info.Size(), mimeType),
			Tips:    "Image content is provided as a native image block to the model.",
			Images:  []llm.ImageContent{{MediaType: mimeType, Data: encoded}},
		}, nil
	}

	// Read file with context-aware cancellation.
	// os.Open + io.ReadAll allows closing the fd on context cancel,
	// which interrupts the blocking read (unlike os.ReadFile).
	type readResult struct {
		data []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		f, err := os.Open(resolvedPath)
		if err != nil {
			ch <- readResult{nil, err}
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		ch <- readResult{data, err}
	}()

	// Check context BEFORE reading. This ensures a pre-cancelled context
	// (e.g. test with cancel() before Execute) always returns an error,
	// even if the file read would complete instantly. Without this check,
	// the select below randomly picks between readCtx.Done() and ch when
	// both are ready — causing a flaky test on fast CI runners.
	if readCtx.Err() != nil {
		return nil, fmt.Errorf("read timed out or cancelled: %w", readCtx.Err())
	}

	select {
	case <-readCtx.Done():
		// Context cancelled — the goroutine will leak if I/O is truly stuck
		// (NFS hang), but this is rare. The fd is closed by defer when the
		// goroutine eventually returns.
		return nil, fmt.Errorf("read timed out or cancelled: %w", readCtx.Err())
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("failed to read file: %w", res.err)
		}
		return NewResultWithTips(string(res.data), "如需修改此文件，优先使用 Edit 工具。"), nil
	}
}
