package tools

// view_image tool — the model pulls an image from its environment into its
// own context (multimodal vision).
//
// Why a tool + follow-up user message instead of an image in the tool result:
// OpenAI chat-completions tool messages are TEXT-ONLY — the only role that
// can carry image content parts is "user". The tool copies the image into the
// persistent view_images directory and returns ToolResult.Images; the engine
// (processToolResults) appends a follow-up USER message carrying the stable
// markdown reference. The LLM layer (llm.parseMultimodalContent) resolves the
// reference into base64 parts at request-build time when the model's manual
// vision switch is on; otherwise it degrades to a text placeholder.
//
// The stored reference is a RELATIVE URL (/api/files/viewimg/<uuid>): the web
// frontend renders it directly (cookie-auth <img>), the LLM resolver reads
// the same file from disk — one reference, two consumers, no signed URLs.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
)

// ViewImageTool lets the model view an image from its environment (local
// path or URL) by injecting it into its own context as a follow-up user
// message. Requires the model's manual vision switch (PerModelConfig.Vision).
type ViewImageTool struct{}

// NewViewImageTool creates the view_image tool.
func NewViewImageTool() *ViewImageTool { return &ViewImageTool{} }

func (t *ViewImageTool) Name() string { return "view_image" }
func (t *ViewImageTool) Description() string {
	return "View an image and inject it into your context for visual analysis. " +
		"Requires the current model's vision switch to be enabled (models without vision see a placeholder). " +
		"Use for analyzing charts, screenshots, downloaded images, or generated pictures. " +
		"Path supports workspace-relative paths; url accepts http(s) and /api/files/download references. " +
		"Images are preprocessed (longest edge ≤ 2048px). Multiple images injected in one turn are subject to the per-request image budget (most recent 8 win)."
}

func (t *ViewImageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "Local file path of the image (workspace-relative or absolute within the workspace)", Required: false},
		{Name: "url", Type: "string", Description: "Image URL (http/https or /api/files/download?key=... reference) — mutually exclusive with path", Required: false},
	}
}

// viewImageMaxBytes caps the raw source (before preprocessing — the LLM
// resolver resizes later; this is only a sanity/download cap).
const viewImageMaxBytes = 20 << 20 // 20MB

// viewImagesDir returns ~/.xbot/view_images (created on demand).
func viewImagesDir() (string, error) {
	dir := filepath.Join(config.XbotHome(), "view_images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create view_images dir: %w", err)
	}
	return dir, nil
}

// sniffImageExt detects the image format from magic bytes and returns the
// canonical extension ("" when not a recognized image).
func sniffImageExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case bytes.HasPrefix(data, []byte("BM")):
		return "bmp"
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")):
		return "tiff"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	default:
		return ""
	}
}

func (t *ViewImageTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	}](input)
	if err != nil {
		return nil, err
	}
	if params.Path == "" && params.URL == "" {
		return nil, fmt.Errorf("either path or url is required")
	}
	if params.Path != "" && params.URL != "" {
		return nil, fmt.Errorf("path and url are mutually exclusive — pass one")
	}

	var (
		data     []byte
		srcLabel string
	)
	if params.Path != "" {
		data, err = t.readLocal(ctx, params.Path)
		srcLabel = params.Path
	} else {
		data, err = t.fetchURL(ctx, params.URL)
		srcLabel = params.URL
	}
	if err != nil {
		return nil, err
	}

	ext := sniffImageExt(data)
	if ext == "" {
		return &ToolResult{
			Summary: fmt.Sprintf("Error: %s is not a recognized image (png/jpeg/gif/webp/bmp/tiff). view_image only handles image files — for text files use Read.", filepath.Base(srcLabel)),
			IsError: true,
		}, nil
	}
	if len(data) > viewImageMaxBytes {
		return &ToolResult{
			Summary: fmt.Sprintf("Error: image %s is %dMB — exceeds the 20MB source cap. Compress or downscale it first (e.g. via Shell + ImageMagick).", filepath.Base(srcLabel), len(data)>>20),
			IsError: true,
		}, nil
	}

	// Dimensions for the summary (cheap header decode — no full pixel decode).
	dims := image.Config{}
	if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil {
		dims = cfg
	}

	// Persist into the shared view_images dir (stable reference target —
	// the resolver and the web viewimg endpoint both read from here).
	dir, err := viewImagesDir()
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	stored := filepath.Join(dir, id+"."+ext)
	if err := os.WriteFile(stored, data, 0o644); err != nil {
		return nil, fmt.Errorf("persist image: %w", err)
	}

	name := filepath.Base(srcLabel)
	ref := "/api/files/viewimg/" + id + "." + ext
	sizeKB := (len(data) + 1023) >> 10
	dimPart := ""
	if dims.Width > 0 {
		dimPart = fmt.Sprintf(", %d×%d", dims.Width, dims.Height)
	}
	return &ToolResult{
		Summary: fmt.Sprintf("Image loaded and injected into context: %s (%dKB%s). You can now visually analyze it in the next turn.", name, sizeKB, dimPart),
		Detail:  fmt.Sprintf("source=%s\nstored=%s\nref=%s", srcLabel, stored, ref),
		Images: []ImageInjection{
			{Ref: ref, Label: name},
		},
	}, nil
}

// readLocal reads a local image, honoring the sandbox (remote runners read via
// the sandbox protocol). Any local path is readable — the old whitelist
// (workspace root / working dir / view_images / read-only roots) was removed
// on user request 2026-09-15: it blocked reading e.g. /tmp screenshots the
// agent had just captured.
func (t *ViewImageTool) readLocal(ctx *ToolContext, path string) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil tool context")
	}
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(ctx.WorkingDir, resolved)
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	// 任意路径可读（用户 2026-09-15：「把这个删了，哪里的都允许读」）：
	// 原先的白名单（workspace root / working dir / view_images / ReadOnlyRoots）会把
	// /tmp、别的仓库、别的会话目录下的截图全部挡掉（报 "outside the readable roots"），
	// 导致 agent 无法查看自己刚截的图。view_image 是"看图"工具，读什么由调用方决定。

	if shouldUseSandbox(ctx) && ctx.Sandbox != nil {
		sandboxCtx, cancel := SandboxCtx()
		defer cancel()
		userID := ctx.OriginUserID
		if userID == "" {
			userID = ctx.SenderID
		}
		data, err := ctx.Sandbox.ReadFile(sandboxCtx, abs, userID)
		if err != nil {
			return nil, fmt.Errorf("sandbox read: %w", err)
		}
		return data, nil
	}
	return os.ReadFile(abs)
}

// fetchURL downloads the image over HTTP (http/https absolute URLs; the
// /api/files/download?key= relative reference is rejected here — the model
// should pass files it downloaded with the DownloadFile tool via path instead;
// web uploads arrive as URLs the RESOLVER handles, not this tool).
func (t *ViewImageTool) fetchURL(ctx *ToolContext, rawURL string) ([]byte, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("unsupported url scheme (want http/https): %s", rawURL)
	}
	if _, err := url.Parse(rawURL); err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch image: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, viewImageMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if len(data) > viewImageMaxBytes {
		return nil, fmt.Errorf("image exceeds 20MB download cap")
	}
	log.WithField("url", rawURL).WithField("bytes", len(data)).Debug("view_image downloaded")
	return data, nil
}
