package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// FileSharer publishes a local file as a web-accessible URL using the
// configured storage provider (local disk / Qiniu / S3). Web-only: only
// registered when the web channel is active and an OSS provider is
// configured. The agent calls ShareFile with a local path and gets back a
// URL it can embed in messages as markdown (![image](url) or [file](url)).
type FileSharer interface {
	ShareFile(localPath string, displayName string) (url string, err error)
}

// ShareFileTool is a web-specific tool that publishes a local file as a
// web-accessible URL. Only registered when the web channel is active.
type ShareFileTool struct {
	sharer FileSharer
}

func NewShareFileTool(sharer FileSharer) *ShareFileTool {
	return &ShareFileTool{sharer: sharer}
}

func (t *ShareFileTool) Name() string { return "share_file" }

func (t *ShareFileTool) Description() string {
	return `Publish a local file as a web-accessible URL that can be embedded in messages.

The file is uploaded to the configured storage provider (local disk / Qiniu / S3).
Returns a URL that renders in the web UI:
- Images: renders inline as markdown image syntax → ![name](url)
- Other files: downloadable link → [name](url)

**Web-only tool**: only available when the web channel is active.

Usage:
  share_file(path="/tmp/chart.png")
  → returns: /api/files/download?key=agent%2F<uuid>%2Fchart.png&inline=1
  → embed in reply: ![chart](/api/files/download?key=agent%2F...)

The URL is stable (never expires for local storage; TTL-based for cloud).`
}

func (t *ShareFileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "Local file path to publish (must be readable).", Required: true},
		{Name: "name", Type: "string", Description: "Display name for the file (defaults to the filename).", Required: false},
	}
}

func (t *ShareFileTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Resolve to absolute path (relative to working dir).
	p := params.Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(ctx.WorkingDir, p)
	}

	// Path guard: the agent can only share files it can read. The sandbox
	// already enforces read access; here we just check the file exists.
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("file not accessible: %w", err)
	}

	url, err := t.sharer.ShareFile(p, params.Name)
	if err != nil {
		return nil, fmt.Errorf("share file: %w", err)
	}

	displayName := params.Name
	if displayName == "" {
		displayName = filepath.Base(p)
	}
	ext := strings.ToLower(filepath.Ext(p))
	isImg := isImageExt(ext)

	var md string
	if isImg {
		md = fmt.Sprintf("![%s](%s)", displayName, url)
	} else {
		md = fmt.Sprintf("[%s](%s)", displayName, url)
	}

	return &ToolResult{
		Summary: fmt.Sprintf("Published %s → %s", displayName, url),
		Detail:  fmt.Sprintf("File published successfully.\n\nURL: %s\n\nEmbed in your reply:\n%s", url, md),
		Tips:    fmt.Sprintf("Use this markdown in your reply to show the file to the user:\n%s", md),
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico":
		return true
	}
	return false
}
