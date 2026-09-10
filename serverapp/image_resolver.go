package serverapp

// webImageResolver — the llm.ImageResolver implementation for the server.
//
// Materializes stable image references into data: URLs (base64 inline) at
// LLM-request-build time. Reference kinds:
//
//   - "data:..."                        → returned as-is (already inline)
//   - "viewimg://<uuid>"                → ~/.xbot/view_images/<uuid> (view_image tool)
//   - "/api/files/download?key=..."     → OSS provider (GetViewURL + HTTP GET)
//   - "http(s)://..."                   → direct HTTP GET (signed OSS URLs etc.)
//
// Every non-data image goes through processImage before inlining:
//   - max edge 2048px (Lanczos-ish scale-down; detail beyond that is wasted
//     token cost per OpenAI/Anthropic guidance)
//   - max 4MB after processing (jpeg q85 → q70 progressive squeeze)
//   - bmp/tiff → png; png with alpha stays png (transparency preserved);
//     png without alpha may re-encode to jpeg when over budget
//   - gif passes through untouched (animated, APIs support it)
//   - unknown formats (heic/avif …) pass through as-is — decode is unsupported,
//     the API either accepts or the request degrades via the caller's
//     placeholder path.
//
// Results are cached in an LRU (32 entries / 128MB) — the same reference is
// re-resolved on EVERY LLM request (agent loop iterations re-send history),
// so the cache absorbs the repeat downloads.

import (
	"bytes"
	"container/list"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"

	"xbot/channel/web"
	"xbot/llm"
	log "xbot/logger"
)

const (
	// maxImageEdgePx caps the longest image side before sending to the API.
	// Beyond this, vision token cost grows quadratically while detail gain
	// is negligible (both OpenAI and Anthropic operate around 1568-2048px).
	maxImageEdgePx = 2048
	// maxImageBytes caps the processed inline size (base64 inflates by 4/3 —
	// 4MB raw ≈ 5.3MB data URL; OpenAI hard limit 20MB, Anthropic 5MB).
	maxImageBytes = 4 << 20
	// HTTP fetch limits for reference resolution.
	resolveHTTPTimeout = 30 * time.Second
	resolveMaxBody     = 10 << 20 // 10MB download cap (pre-processing)
	// LRU budget.
	imageCacheMaxEntries = 32
	imageCacheMaxBytes   = 128 << 20 // 128MB
)

// viewimgIDRe constrains viewimg ids to filesystem-safe characters. The id is
// joined into a filesystem path, so traversal ("../", separators) must be
// structurally impossible. The OPTIONAL trailing extension is required in
// practice: the view_image tool and the Feishu inbound path store files as
// <uuid>.<ext> and reference them as /api/files/viewimg/<uuid>.<ext>. (The web
// endpoint's viewimgIDPattern carries the same extension; the two MUST agree —
// a dot-rejecting regex here silently broke every image reference.)
var viewimgIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}(\.[a-zA-Z0-9]{1,8})?$`)

// webImageResolver implements llm.ImageResolver.
type webImageResolver struct {
	provider web.OSSProvider // may be nil (no OSS configured — key refs fail)
	viewDir  string          // ~/.xbot/view_images
	// uploadDir is <xbotHome>/uploads — where handleCloudUpload spills a local
	// copy of every web upload so the model can be told a REAL path.
	uploadDir string
	// workspaceRoots whitelist file:// references (absolute local paths, e.g.
	// CLI messages embedding workspace screenshots). Empty = file:// always
	// fails to resolve (degrades to a placeholder, never blocks the request).
	workspaceRoots []string
	http           *http.Client
	cache          *imageLRU
}

// NewImageResolver builds the shared resolver. provider may be nil (web
// uploads without OSS are rejected anyway — key refs degrade with a clear
// error); viewimg files live under <xbotHome>/view_images; workspaceRoot
// whitelists file:// references (local-mode CLI messages may embed absolute
// image paths from the agent's own workspace).
func NewImageResolver(provider web.OSSProvider, xbotHome string, workspaceRoots ...string) *webImageResolver {
	dir := filepath.Join(xbotHome, "view_images")
	uploadDir := filepath.Join(xbotHome, "uploads")
	roots := make([]string, 0, len(workspaceRoots)+1)
	for _, r := range workspaceRoots {
		if r != "" {
			if abs, err := filepath.Abs(r); err == nil {
				roots = append(roots, abs)
			}
		}
	}
	return &webImageResolver{
		provider:       provider,
		uploadDir:      uploadDir,
		viewDir:        dir,
		workspaceRoots: roots,
		http: &http.Client{
			Timeout: resolveHTTPTimeout,
		},
		cache: newImageLRU(imageCacheMaxEntries, imageCacheMaxBytes),
	}
}

// ViewImagesDir returns the directory view_image tool results are stored in.
// Exported for the view_image tool (Phase 5) to write into.
func ViewImagesDir(xbotHome string) string {
	return filepath.Join(xbotHome, "view_images")
}

// imageResolverSingleton is set once at server boot (registerChannels) and
// consumed by the LLM factory when building clients (Phase 6 wiring). Read
// via GetImageResolver().
var (
	imageResolverMu        sync.RWMutex
	imageResolverSingleton llm.ImageResolver
)

// SetImageResolver registers the process-wide image resolver. Called once the
// OSS provider is created (web channel init). nil clears it.
func SetImageResolver(r llm.ImageResolver) {
	imageResolverMu.Lock()
	imageResolverSingleton = r
	imageResolverMu.Unlock()
}

// GetImageResolver returns the process-wide resolver (nil when the server has
// none wired — local/test runs; callers degrade images to placeholders).
func GetImageResolver() llm.ImageResolver {
	imageResolverMu.RLock()
	defer imageResolverMu.RUnlock()
	return imageResolverSingleton
}

// ResolveImage implements llm.ImageResolver. Errors degrade to text
// placeholders in the LLM layer — never fail the request here.
// LocalPath maps a reference to a REAL file on this machine so the model can
// act on it directly (open / ps / read) instead of guessing where the picture
// lives. Returns ("", false) when the ref has no local counterpart — a remote
// http URL, or an upload that never got spilled to disk.
//
// This is the actionable half of the fix for "asked to `ps` a pasted image, the
// model scans the filesystem": the image part carries pixels only, so the
// caption needs a path it can use.
func (r *webImageResolver) LocalPath(ref string) (string, bool) {
	switch {
	case strings.HasPrefix(ref, "viewimg://"):
		id := strings.TrimPrefix(ref, "viewimg://")
		if !viewimgIDRe.MatchString(id) {
			return "", false
		}
		return filepath.Join(r.viewDir, id), true
	case strings.HasPrefix(ref, "/api/files/viewimg/"):
		id := strings.TrimPrefix(ref, "/api/files/viewimg/")
		if !viewimgIDRe.MatchString(id) {
			return "", false
		}
		return filepath.Join(r.viewDir, id), true
	case strings.HasPrefix(ref, "/api/files/download"):
		u, err := url.Parse(ref)
		if err != nil {
			return "", false
		}
		key := u.Query().Get("key")
		if key == "" || !strings.HasPrefix(key, "uploads/") || strings.Contains(key, "..") {
			return "", false
		}
		// Mirror of the spill-to-disk copy written by handleCloudUpload.
		if r.uploadDir == "" {
			return "", false
		}
		path := filepath.Join(r.uploadDir, key)
		if _, err := os.Stat(path); err != nil {
			return "", false
		}
		return path, true
	case strings.HasPrefix(ref, "file://"):
		path := strings.TrimPrefix(ref, "file://")
		if isUnderAnyRoot(path, r.workspaceRoots...) {
			return path, true
		}
		return "", false
	}
	return "", false
}

func (r *webImageResolver) ResolveImage(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty image reference")
	}
	// Already inline — no cache, no processing (caller materialized it).
	if strings.HasPrefix(ref, "data:") {
		return ref, nil
	}
	if cached, ok := r.cache.get(ref); ok {
		return cached, nil
	}
	dataURL, err := r.fetch(ctx, ref)
	if err != nil {
		return "", err
	}
	r.cache.put(ref, dataURL)
	return dataURL, nil
}

// fetch resolves one reference into a (possibly processed) data: URL.
func (r *webImageResolver) fetch(ctx context.Context, ref string) (string, error) {
	raw, mime, err := r.load(ctx, ref)
	if err != nil {
		return "", err
	}
	processed, mime, changed, perr := processImage(raw, mime)
	if perr != nil {
		log.Ctx(ctx).WithError(perr).WithField("ref", truncRef(ref)).Warn("image preprocess failed — passing original through")
		processed, changed = raw, false
	}
	if len(processed) > maxImageBytes {
		return "", fmt.Errorf("image too large after preprocessing (%d bytes, budget %d)", len(processed), maxImageBytes)
	}
	if changed {
		log.Ctx(ctx).WithFields(log.Fields{
			"ref":         truncRef(ref),
			"orig_bytes":  len(raw),
			"final_bytes": len(processed),
			"mime":        mime,
		}).Debug("image preprocessed for vision request")
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(processed)), nil
}

// load fetches the raw bytes for a reference (no processing).
func (r *webImageResolver) load(ctx context.Context, ref string) ([]byte, string, error) {
	// viewimg://<uuid>.<ext> — local view_images dir (view_image tool writes here).
	// Accepts the bare id form (uuid or uuid.ext — both match the id charset).
	if strings.HasPrefix(ref, "viewimg://") {
		id := strings.TrimPrefix(ref, "viewimg://")
		if !viewimgIDRe.MatchString(id) {
			return nil, "", fmt.Errorf("invalid viewimg id")
		}
		path := filepath.Join(r.viewDir, id)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("viewimg read: %w", err)
		}
		return data, detectImageMIME(data), nil
	}

	// /api/files/viewimg/<uuid>.<ext> — the canonical relative reference the
	// view_image tool injects (browser-renderable URL + resolver-readable in
	// one form). Same file, same validation as viewimg://.
	if strings.HasPrefix(ref, "/api/files/viewimg/") {
		id := strings.TrimPrefix(ref, "/api/files/viewimg/")
		if !viewimgIDRe.MatchString(id) {
			return nil, "", fmt.Errorf("invalid viewimg id")
		}
		path := filepath.Join(r.viewDir, id)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("viewimg read: %w", err)
		}
		return data, detectImageMIME(data), nil
	}

	// /api/files/download?key=<encoded> — the canonical web upload reference.
	if strings.HasPrefix(ref, "/api/files/download") {
		u, err := url.Parse(ref)
		if err != nil {
			return nil, "", fmt.Errorf("parse download ref: %w", err)
		}
		key := u.Query().Get("key")
		if key == "" {
			return nil, "", fmt.Errorf("download ref has no key")
		}
		// Same key-validation as the HTTP handler (web_file.go) — only
		// upload-issued keys are addressable, no ".." probing.
		if !strings.HasPrefix(key, "uploads/") || strings.Contains(key, "..") {
			return nil, "", fmt.Errorf("invalid upload key")
		}
		if r.provider == nil {
			return nil, "", fmt.Errorf("no OSS provider configured for image key refs")
		}
		viewURL, err := r.provider.GetViewURL(key)
		if err != nil {
			return nil, "", fmt.Errorf("oss view url: %w", err)
		}
		return r.httpGet(ctx, viewURL)
	}

	// file:///path/to/img.png — absolute local path (CLI/local-mode messages
	// may embed workspace images). Whitelisted to the resolver's workspace
	// roots + view_images dir; anything else fails (→ placeholder degrade).
	if strings.HasPrefix(ref, "file://") {
		path := strings.TrimPrefix(ref, "file://")
		// file://localhost/... and file:///abs/path both appear; normalize.
		// ⚠️ Windows paths look like `C:\dir\img.png` — a caller concatenating
		// "file://localhost"+path yields `file://localhostC:\dir\...` (no slash
		// after localhost). Match the bare prefix so both forms normalize.
		path = strings.TrimPrefix(path, "localhost")
		// URLs use forward slashes; convert to the platform separator
		// (FromSlash is a no-op on Unix).
		path = filepath.FromSlash(path)
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, "", fmt.Errorf("resolve file:// path: %w", err)
		}
		if !isUnderAnyRoot(abs, r.workspaceRoots...) && !isUnderAnyRoot(abs, r.viewDir) {
			return nil, "", fmt.Errorf("file:// path outside whitelisted roots: %s", path)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, "", fmt.Errorf("read file:// image: %w", err)
		}
		return data, detectImageMIME(data), nil
	}

	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return r.httpGet(ctx, ref)
	}

	return nil, "", fmt.Errorf("unsupported image reference kind: %s", truncRef(ref))
}

// isUnderAnyRoot reports whether path is equal to or under one of the roots.
func isUnderAnyRoot(path string, roots ...string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

func (r *webImageResolver) httpGet(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetch image: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, resolveMaxBody+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	if len(data) > resolveMaxBody {
		return nil, "", fmt.Errorf("image exceeds %d bytes download cap", resolveMaxBody)
	}
	mime := resp.Header.Get("Content-Type")
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	if mime == "" || mime == "application/octet-stream" {
		mime = detectImageMIME(data)
	}
	return data, mime, nil
}

// processImage resizes / re-encodes when the input exceeds the vision budget.
// Returns (bytes, mime, changed, err). Best-effort semantics:
//   - decode-able images over 2048px on the longest edge scale down
//   - still over maxImageBytes → jpeg re-encode (q85 → q70)
//   - bmp/tiff decode → png (API-safe formats)
//   - gif and unknown formats pass through untouched
func processImage(raw []byte, mime string) ([]byte, string, bool, error) {
	// gif (animated) passes through — the APIs accept it, and re-encoding
	// would collapse it to a still frame.
	if strings.HasPrefix(mime, "image/gif") {
		return raw, mime, false, nil
	}
	img, format, err := decodeImage(raw)
	if err != nil {
		// Unknown/undecodable format (heic/avif/...) — pass through as-is;
		// the API either accepts it or the caller's degrade path explains.
		return raw, mime, false, nil
	}
	_ = format

	changed := false
	b := img.Bounds()
	longest := b.Dx()
	if b.Dy() > longest {
		longest = b.Dy()
	}
	if longest > maxImageEdgePx {
		img = scaleDown(img, maxImageEdgePx)
		changed = true
	}

	// Encode: target format by transparency + source format.
	hasAlpha := hasAlphaChannel(img)
	outMime := encodeMIME(img, hasAlpha, mime)

	var buf bytes.Buffer
	if outMime == "image/jpeg" {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&buf, img)
		outMime = "image/png"
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("encode: %w", err)
	}
	encoded := buf.Bytes()

	// Still over budget with jpeg? One progressive squeeze at q70.
	if len(encoded) > maxImageBytes && outMime == "image/jpeg" {
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err == nil {
			if q70 := buf.Bytes(); len(q70) < len(encoded) {
				encoded = q70
			}
		}
	}

	// If the ORIGINAL raw bytes are smaller than what we produced AND the
	// original already fits budget AND passes the size rule, keep the raw.
	if !changed && len(raw) <= maxImageBytes && len(raw) <= len(encoded) {
		return raw, mime, false, nil
	}
	if len(encoded) >= len(raw) && len(raw) <= maxImageBytes && !changed {
		return raw, mime, false, nil
	}
	if len(encoded) > maxImageBytes && len(raw) <= maxImageBytes && !changed {
		return raw, mime, false, nil
	}
	return encoded, outMime, true, nil
}

// decodeImage sniffs and decodes the supported formats. webp needs
// x/image/webp; bmp/tiff likewise x/image. gif is NOT decoded (animated —
// passes through untouched); unknown formats return an error (callers pass
// the original bytes through).
func decodeImage(raw []byte) (image.Image, string, error) {
	switch sniffImageFormat(raw) {
	case "png":
		img, err := png.Decode(bytes.NewReader(raw))
		return img, "png", err
	case "jpeg":
		img, err := jpeg.Decode(bytes.NewReader(raw))
		return img, "jpeg", err
	case "webp":
		img, err := webp.Decode(bytes.NewReader(raw))
		return img, "webp", err
	case "bmp":
		img, err := bmp.Decode(bytes.NewReader(raw))
		return img, "bmp", err
	case "tiff":
		img, err := tiff.Decode(bytes.NewReader(raw))
		return img, "tiff", err
	default:
		return nil, "", fmt.Errorf("unsupported image format")
	}
}

func sniffImageFormat(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "jpeg"
	case len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "webp"
	case len(raw) >= 2 && raw[0] == 'B' && raw[1] == 'M':
		return "bmp"
	case len(raw) >= 4 && (bytes.Equal(raw[:4], []byte("II*\x00")) || bytes.Equal(raw[:4], []byte("MM\x00*"))):
		return "tiff"
	default:
		return ""
	}
}

// detectImageMIME sniffs the Content-Type from magic bytes when the HTTP
// response omits it (or sends application/octet-stream).
func detectImageMIME(raw []byte) string {
	switch sniffImageFormat(raw) {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "tiff":
		return "image/tiff"
	case "gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

// scaleDown rescales img so the longest edge becomes edge, preserving aspect.
func scaleDown(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= edge && h <= edge {
		return img
	}
	var nw, nh int
	if w >= h {
		nh = h * edge / w
		nw = edge
	} else {
		nw = w * edge / h
		nh = edge
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

func hasAlphaChannel(img image.Image) bool {
	// NRGBA/RGBA always carry alpha. Gray/CMYK have none. Paletted depends
	// on the palette. Conservatively check concrete types (matches the formats
	// the preprocess pipeline actually decodes: png→NRGBA/RGBA/Gray/Paletted,
	// jpeg→YCbCr(never alpha), webp→NRGBA/YCbCr, bmp→RGBA/Gray, tiff→varies).
	switch img := img.(type) {
	case *image.NRGBA, *image.RGBA, *image.NRGBA64, *image.RGBA64:
		return true
	case *image.Gray, *image.Gray16, *image.YCbCr:
		return false
	case *image.Paletted:
		for _, c := range img.Palette {
			_, _, _, a := c.RGBA()
			if a != 0xffff {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// encodeMIME picks the output format: bmp/tiff → png (lossless, API-safe);
// sources with alpha → png (preserve transparency — UI screenshots etc.);
// opaque sources → jpeg (compact).
func encodeMIME(img image.Image, hasAlpha bool, srcMIME string) string {
	switch {
	case strings.HasPrefix(srcMIME, "image/bmp"), strings.HasPrefix(srcMIME, "image/tiff"):
		return "image/png"
	case hasAlpha:
		return "image/png"
	default:
		return "image/jpeg"
	}
}

func truncRef(ref string) string {
	if len(ref) > 80 {
		return ref[:80] + "…"
	}
	return ref
}

// ─── LRU cache (entries + bytes budget) ─────────────────────────────────────

type imageLRUEntry struct {
	key     string
	dataURL string
	size    int
}

// imageLRU caches ref → dataURL with a dual budget (entries ≤ maxEntries,
// total bytes ≤ maxBytes). Eviction takes the least-recently-used entry
// first. The same reference is resolved on EVERY LLM request (the agent loop
// re-sends full history per iteration), so the cache absorbs repeat
// downloads/preprocessing.
type imageLRU struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	totalBytes int
	entries    map[string]*list.Element
	order      *list.List
}

func newImageLRU(maxEntries, maxBytes int) *imageLRU {
	return &imageLRU{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

func (c *imageLRU) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*imageLRUEntry).dataURL, true
	}
	return "", false
}

func (c *imageLRU) put(key, dataURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		entry := el.Value.(*imageLRUEntry)
		c.totalBytes += len(dataURL) - entry.size
		entry.dataURL = dataURL
		entry.size = len(dataURL)
		c.order.MoveToFront(el)
		c.evictLocked()
		return
	}
	entry := &imageLRUEntry{key: key, dataURL: dataURL, size: len(dataURL)}
	c.entries[key] = c.order.PushFront(entry)
	c.totalBytes += entry.size
	c.evictLocked()
}

func (c *imageLRU) evictLocked() {
	for c.order.Len() > 0 {
		overEntries := c.order.Len() > c.maxEntries
		overBytes := c.totalBytes > c.maxBytes
		if !overEntries && !overBytes {
			return
		}
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		entry := c.order.Remove(oldest).(*imageLRUEntry)
		delete(c.entries, entry.key)
		c.totalBytes -= entry.size
	}
}

// compile-time interface check: the resolver satisfies llm.ImageResolver.
var _ llm.ImageResolver = (*webImageResolver)(nil)
