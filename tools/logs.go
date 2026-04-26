package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"xbot/llm"
)

// LogsSubDir is the subdirectory relative to DataDir where logs are stored.
// Can be overridden via config if needed.
// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
const LogsSubDir = ".xbot/logs"

// LogsTool: tool for reading xbot log files (admin only)
type LogsTool struct {
	adminChatID string
}

// NewLogsTool creates a new LogsTool instance
func NewLogsTool(adminChatID string) *LogsTool {
	return &LogsTool{
		adminChatID: adminChatID,
	}
}

func (t *LogsTool) Name() string {
	return "Logs"
}

func (t *LogsTool) Description() string {
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	return `Read xbot log files from .xbot/logs directory.
Parameters (JSON):
  - action: string, "list" (list log files) or "read" (read log content)
  - file: string, log filename (for read action, optional, defaults to latest)
  - lines: number, number of lines to read from end (for read action, optional, default 100)
  - level: string, filter by log level: debug, info, warn, error (optional)
  - grep: string, filter lines containing this text (optional)
Examples:
  {"action": "list"}
  {"action": "read", "lines": 200}
  {"action": "read", "file": "xbot-2026-03-20.log", "level": "error"}
  {"action": "read", "grep": "request_id"}`
}

func (t *LogsTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: list or read", Required: true},
		{Name: "file", Type: "string", Description: "Log filename (for read action, optional, defaults to latest)", Required: false},
		{Name: "lines", Type: "integer", Description: "Number of lines to read from end (default 100)", Required: false},
		{Name: "level", Type: "string", Description: "Filter by log level: debug, info, warn, error (optional)", Required: false},
		{Name: "grep", Type: "string", Description: "Filter lines containing this text (optional)", Required: false},
	}
}

func (t *LogsTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// Permission check: admin only
	if ctx.ChatID != t.adminChatID {
		return nil, fmt.Errorf("permission denied: Logs tool is only available to admin")
	}

	// Parse parameters
	params, err := parseToolArgs[struct {
		Action string `json:"action"`
		File   string `json:"file"`
		Lines  int    `json:"lines"`
		Level  string `json:"level"`
		Grep   string `json:"grep"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Action == "" {
		return nil, fmt.Errorf("action is required")
	}

	// Determine log directory
	// uses DataDir instead of WorkspaceRoot since logs are global, not user-isolated
	logDir := filepath.Join(ctx.DataDir, LogsSubDir)

	switch params.Action {
	case "list":
		return t.listLogs(logDir)
	case "read":
		return t.readLogs(logDir, params.File, params.Lines, params.Level, params.Grep)
	default:
		return nil, fmt.Errorf("unknown action: %s (supported: list, read)", params.Action)
	}
}

// listLogs lists all log files
func (t *LogsTool) listLogs(logDir string) (*ToolResult, error) {
	files, err := t.getLogFiles(logDir)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return NewResult(fmt.Sprintf("No log files found in %s directory.", LogsSubDir)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d log file(s) in %s:\n\n", len(files), LogsSubDir)
	for i, f := range files {
		fmt.Fprintf(&sb, "%d. %s (%s)\n", i+1, f.Name, f.Size)
	}

	return NewResult(sb.String()), nil
}

// logFileInfo: log file information
type logFileInfo struct {
	Name string
	Size string
	Path string
}

// getLogFiles returns log files sorted by date (newest first)
func (t *LogsTool) getLogFiles(logDir string) ([]logFileInfo, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read log directory: %w", err)
	}

	var files []logFileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, logFileInfo{
			Name: entry.Name(),
			Size: formatFileSize(info.Size()),
			Path: filepath.Join(logDir, entry.Name()),
		})
	}

	// Sort by filename (date) in descending order
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name > files[j].Name
	})

	return files, nil
}

// readLogs: read log content
func (t *LogsTool) readLogs(logDir, filename string, lines int, level, grep string) (*ToolResult, error) {
	if lines <= 0 {
		lines = 100
	}

	// Determine log file path
	var logPath string
	if filename != "" {
		logPath = filepath.Join(logDir, filename)
		// Security: prevent path traversal
		absLogPath, err := filepath.Abs(logPath)
		if err != nil {
			return nil, fmt.Errorf("invalid log path: %w", err)
		}
		absLogDir, err := filepath.Abs(logDir)
		if err != nil {
			return nil, fmt.Errorf("invalid log directory: %w", err)
		}
		if !strings.HasPrefix(absLogPath, absLogDir+string(filepath.Separator)) && absLogPath != absLogDir {
			return nil, fmt.Errorf("path traversal not allowed")
		}
	} else {
		// default: read the latest log file
		files, err := t.getLogFiles(logDir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("no log files found")
		}
		logPath = files[0].Path
		filename = files[0].Name
	}

	// Read the last N lines
	contents, err := t.readLastLines(logPath, lines)
	if err != nil {
		return nil, fmt.Errorf("failed to read log file: %w", err)
	}

	// Apply filters
	var filtered []string
	for _, line := range contents {
		// Level filter
		if level != "" && !t.matchLevel(line, level) {
			continue
		}
		// grep filter
		if grep != "" && !strings.Contains(line, grep) {
			continue
		}
		filtered = append(filtered, line)
	}

	if len(filtered) == 0 {
		return NewResult(fmt.Sprintf("No matching lines found in %s (level=%s, grep=%s)", filename, level, grep)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📄 **%s** (showing %d of %d lines", filename, len(filtered), len(contents))
	if level != "" {
		fmt.Fprintf(&sb, ", level=%s", level)
	}
	if grep != "" {
		fmt.Fprintf(&sb, ", grep=%s", grep)
	}
	sb.WriteString(")\n\n")

	for _, line := range filtered {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return NewResult(sb.String()), nil
}

// readLastLines: reads the last N lines of a file
func (t *LogsTool) readLastLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// uses a fixed-size ring buffer keeping only the last N lines to avoid OOM on large files
	type ringBuffer struct {
		data  []string
		pos   int
		count int
		size  int
	}
	buf := make([]string, n)
	rb := &ringBuffer{data: buf, size: n}

	scanner := bufio.NewScanner(f)
	scanBuf := make([]byte, 0, 64*1024)
	scanner.Buffer(scanBuf, maxScanLineSize) // 支持 1MB 的行

	for scanner.Scan() {
		rb.data[rb.pos] = scanner.Text()
		rb.pos = (rb.pos + 1) % rb.size
		if rb.count < rb.size {
			rb.count++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// extract lines from ring buffer (in original order)
	result := make([]string, rb.count)
	if rb.count < rb.size {
		copy(result, rb.data[:rb.count])
	} else {
		copy(result, rb.data[rb.pos:])
		copy(result[rb.size-rb.pos:], rb.data[:rb.pos])
	}
	return result, nil
}

// matchLevel: checks if a log line matches the specified level
func (t *LogsTool) matchLevel(line, level string) bool {
	level = strings.ToLower(level)

	// Try parsing JSON-formatted logs (logrus JSONFormatter)
	var jsonLog map[string]any
	if err := json.Unmarshal([]byte(line), &jsonLog); err == nil {
		if logLevel, ok := jsonLog["level"].(string); ok {
			return strings.ToLower(logLevel) == level
		}
	}

	// fall back to text matching (TextFormatter)
	// Log format is typically: time="..." level=xxx msg="..."
	levelPrefix := fmt.Sprintf("level=%s", level)
	if strings.Contains(strings.ToLower(line), levelPrefix) {
		return true
	}

	// another common format: WARN: xxx, ERROR: xxx
	upperLevel := strings.ToUpper(level)
	switch upperLevel {
	case "DEBUG":
		return strings.Contains(line, "DEBUG") || strings.Contains(line, "level=debug")
	case "INFO":
		return strings.Contains(line, "INFO") || strings.Contains(line, "level=info")
	case "WARN":
		return strings.Contains(line, "WARN") || strings.Contains(line, "level=warn") || strings.Contains(line, "WARNING")
	case "ERROR":
		return strings.Contains(line, "ERROR") || strings.Contains(line, "level=error") || strings.Contains(line, "ERRO")
	default:
		return false
	}
}

// formatFileSize formats a file size
func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// Used for type checking
var _ Tool = (*LogsTool)(nil)

// record time at init (for debugging)
func init() {
	_ = time.Now() // 确保 time 包被导入
}
