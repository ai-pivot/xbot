package llm

// Multimodal (vision) image input support.
//
// Design: message content carries STABLE image references (markdown image
// syntax / legacy <image url=...> tags, ~100 bytes); the data: URL (base64)
// form is materialized ONLY when the LLM request is built — every time,
// idempotently, through the injected ImageResolver. DB / compression / SSE /
// frontend pipelines never see base64 payloads.
//
// Vision is a PURELY MANUAL per-model switch (PerModelConfig.Vision, set in
// the model editor) — there is NO built-in model-name whitelist. When vision
// is disabled, every image reference degrades to a text placeholder so
// non-vision models never receive image content parts (API 400).

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// ImageResolver turns an image reference into a data: URL (base64 inline).
// The host app (serverapp) implements it: relative /api/files/download URLs
// resolve through the OSS/local provider, absolute http(s) URLs are fetched,
// viewimg:// references read the persistent view_images directory.
// The llm package stays dependency-free — implementations live outside.
type ImageResolver interface {
	// ResolveImage returns the inline data: URL for the given reference.
	// Supported refs (implementation-defined, see serverapp.imageResolver):
	//   - "data:..."                       → returned as-is (already inline)
	//   - "/api/files/download?key=..."    → OSS/local file → data: URL
	//   - "viewimg://<uuid>"               → view_images dir → data: URL
	//   - "http(s)://..."                  → fetch (with preprocessing)
	// A non-nil error degrades the image to a text placeholder — the request
	// NEVER fails because of an unresolvable image.
	ResolveImage(ctx context.Context, ref string) (dataURL string, err error)
}

// localPathProvider is an OPTIONAL extension of ImageResolver: when the
// resolver can map a reference to a real file on this machine, the model is
// told that path. Without it the model receives pixels and nothing else — asked
// to e.g. `ps` a pasted screenshot it has no filename to work with and ends up
// scanning the whole filesystem.
type localPathProvider interface {
	LocalPath(ref string) (string, bool)
}

// imageRefText renders the caption that accompanies an image part. It keeps the
// reference visible to the model and prefers an actionable LOCAL PATH when the
// resolver can provide one.
const maxImageAltRunes = 120

// sanitizeImageAlt bounds and cleans user-controlled alt text so it cannot
// impersonate the caption's trusted fields. The alt comes from `![alt](url)`,
// which a user fully controls; without this it could inject text such as
// "…；本地路径: /etc/shadow" and have the model treat a forged path as one the
// system supplied.
func sanitizeImageAlt(alt string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, alt)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "未命名"
	}
	runes := []rune(cleaned)
	if len(runes) > maxImageAltRunes {
		return string(runes[:maxImageAltRunes]) + "…"
	}
	return cleaned
}

// imageRefText renders the caption that accompanies an image part.
//
// The format is deliberately attributed (alt=/ref=/local=) rather than prose so
// the trusted fields — in particular the local path — are unambiguous and
// cannot be counterfeited by the user-controlled alt text.
func imageRefText(alt, ref string, mc *MultimodalConfig) string {
	name := sanitizeImageAlt(alt)
	if ref == "" || strings.HasPrefix(ref, "data:") {
		return fmt.Sprintf("[image alt=%q]", name)
	}
	if mc.ImageResolver != nil {
		if p, ok := mc.ImageResolver.(localPathProvider); ok {
			if local, ok2 := p.LocalPath(ref); ok2 && local != "" {
				return fmt.Sprintf("[image alt=%q local=%q ref=%q]", name, local, ref)
			}
		}
	}
	return fmt.Sprintf("[image alt=%q ref=%q]", name, ref)
}

// MultimodalConfig carries the per-request vision settings for message
// building. Attached to OpenAI/Anthropic clients from the per-model
// subscription config (PerModelConfig.Vision / VisionDetail) at
// createClientFromSub time — the single source of truth is the manual
// per-model switch in the model editor, NOT a model-name whitelist.
type MultimodalConfig struct {
	// ImageResolver materializes references into data: URLs. nil = only
	// already-inline data: URLs resolve (unit-test / legacy behavior).
	ImageResolver ImageResolver
	// VisionEnabled gates ALL image parts. false (zero value) degrades every
	// image reference to a text placeholder — non-vision models must never
	// receive image content parts (API 400).
	VisionEnabled bool
	// VisionDetail is the OpenAI image_url.detail hint: "low" | "high" | ""
	// ("" = auto). Anthropic ignores it.
	VisionDetail string
	// MaxImages is the per-request image budget. The MOST RECENT images win;
	// older ones degrade to placeholders. 0 = defaultMaxImages.
	MaxImages int
}

// defaultMaxImages is the per-request image budget (user-confirmed default 8):
// "超出部分降级为文本占位（最近的优先）".
const defaultMaxImages = 8

var (
	// markdownImageRe matches ![alt](url) image syntax with ANY url kind
	// (data:, viewimg:, /relative, http(s)://).
	markdownImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	// legacyImageTagRe matches the <image url="..." name="..." size="..." />
	// tags expandUploadKeys used to append (pre-multimodal format, kept for
	// historical messages replayed from the DB).
	legacyImageTagRe = regexp.MustCompile(`<image\s+url="([^"]+)"(?:\s+name="([^"]*)")?(?:\s+size="(\d+)")?\s*/>`)
)

// imagePlaceholder builds the degrade text for an image reference that will
// NOT become a content part (vision off / resolve failed / over budget).
// The raw reference is appended when available: the model cannot see the
// image, but it can tell the user where it lives (or hand it to a tool),
// instead of being left with a bare filename — the Feishu report "也没有给我
// 可下载的 file_key/URL" was exactly this information loss.
func imagePlaceholder(alt, reason, ref string) string {
	name := strings.TrimSpace(alt)
	if name == "" {
		name = "未命名"
	}
	if ref != "" && !strings.HasPrefix(ref, "data:") {
		return "[图片: " + name + " — " + reason + "；引用: " + ref + "]"
	}
	return "[图片: " + name + " — " + reason + "]"
}

// resolveMultimodalConfig normalizes a possibly-nil config (nil = zero value:
// vision off, default budget — used by direct function-call tests).
func resolveMultimodalConfig(mc *MultimodalConfig) *MultimodalConfig {
	if mc == nil {
		mc = &MultimodalConfig{}
	}
	if mc.MaxImages <= 0 {
		mc.MaxImages = defaultMaxImages
	}
	return mc
}

// parseMultimodalContent splits content into text/image parts.
//
// Image references (markdown images + legacy <image> tags) are resolved via
// mc.ImageResolver when vision is enabled. Degrade paths (never fail):
//   - VisionEnabled=false → all images → "[图片: alt — 当前模型未开启视觉输入]"
//   - resolver nil and non-data: URL → "[图片: alt — 加载失败]"
//   - resolver error → "[图片: alt — 加载失败]"
//   - over budget (only the MOST RECENT MaxImages survive) → "[图片: alt — 已省略]"
//
// The original text spacing is otherwise preserved verbatim (text parts carry
// the surrounding content) so the no-image path is a byte-identical fast path.
func parseMultimodalContent(ctx context.Context, content string, mc *MultimodalConfig) []imageContentPart {
	mc = resolveMultimodalConfig(mc)

	type imageRef struct {
		start, end int    // span in content
		url        string // reference (data:, viewimg:, /relative, http)
		alt        string // alt text (fallback name in placeholders)
	}

	var refs []imageRef
	for _, m := range markdownImageRe.FindAllStringSubmatchIndex(content, -1) {
		refs = append(refs, imageRef{start: m[0], end: m[1], url: content[m[4]:m[5]], alt: content[m[2]:m[3]]})
	}
	for _, m := range legacyImageTagRe.FindAllStringSubmatchIndex(content, -1) {
		alt := ""
		if m[4] >= 0 {
			alt = content[m[4]:m[5]]
		}
		refs = append(refs, imageRef{start: m[0], end: m[1], url: content[m[2]:m[3]], alt: alt})
	}
	if len(refs) == 0 {
		return []imageContentPart{{Type: "text", Text: content}}
	}
	// Sort by position so text/image interleaving is exact (regexes scan
	// separately; legacy tags may precede markdown images in a message).
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && refs[j].start < refs[j-1].start; j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}

	// Budget: only the MOST RECENT MaxImages refs become image parts. Images
	// are ordered by position; keep the LAST MaxImages (older context degrades
	// first — "最近上下文优先" matches how long conversations forget detail).
	overBudget := map[int]bool{} // index into refs
	if len(refs) > mc.MaxImages {
		for i := 0; i < len(refs)-mc.MaxImages; i++ {
			overBudget[i] = true
		}
	}

	var parts []imageContentPart
	lastIdx := 0
	for i, r := range refs {
		if r.start > lastIdx {
			if text := strings.TrimSpace(content[lastIdx:r.start]); text != "" {
				parts = append(parts, imageContentPart{Type: "text", Text: text})
			}
		}
		lastIdx = r.end

		switch {
		case overBudget[i]:
			parts = append(parts, imageContentPart{Type: "text", Text: imagePlaceholder(r.alt, "已省略，超出单次请求图片预算", r.url)})
		case !mc.VisionEnabled:
			parts = append(parts, imageContentPart{Type: "text", Text: imagePlaceholder(r.alt, "当前模型未开启视觉输入", r.url)})
		default:
			dataURL, err := resolveImageRef(ctx, r.url, mc)
			if err != nil {
				parts = append(parts, imageContentPart{Type: "text", Text: imagePlaceholder(r.alt, "加载失败", r.url)})
				continue
			}
			// Caption FIRST, then the pixels: the image part carries no path,
			// so without this the model knows what the picture looks like but
			// not where it lives. Degraded paths (vision off / failure / over
			// budget) already keep the reference via imagePlaceholder — this
			// closes the gap for the SUCCESS path, which is the common case.
			parts = append(parts, imageContentPart{Type: "text", Text: imageRefText(r.alt, r.url, mc)})
			parts = append(parts, imageContentPart{Type: "image", URL: dataURL, Detail: mc.VisionDetail})
		}
	}
	if lastIdx < len(content) {
		if text := strings.TrimSpace(content[lastIdx:]); text != "" {
			parts = append(parts, imageContentPart{Type: "text", Text: text})
		}
	}
	if len(parts) == 0 {
		return []imageContentPart{{Type: "text", Text: content}}
	}
	// Collapse all-text results into a single part: when every image degraded
	// (vision off / resolve failure / over budget) the multi-part output is
	// equivalent to plain text — a single text part keeps the API message a
	// simple string (no wasteful content-parts array) and the "degraded"
	// content is byte-stable across requests.
	if len(parts) > 1 {
		hasImage := false
		for _, p := range parts {
			if p.Type == "image" {
				hasImage = true
				break
			}
		}
		if !hasImage {
			var sb strings.Builder
			for _, p := range parts {
				if p.Text == "" {
					continue
				}
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(p.Text)
			}
			return []imageContentPart{{Type: "text", Text: sb.String()}}
		}
	}
	return parts
}

// resolveImageRef materializes one reference into a data: URL.
// data: URLs pass through (already inline); everything else needs the resolver.
func resolveImageRef(ctx context.Context, ref string, mc *MultimodalConfig) (string, error) {
	if strings.HasPrefix(ref, "data:") {
		return ref, nil
	}
	if mc.ImageResolver == nil {
		return "", errNoImageResolver
	}
	return mc.ImageResolver.ResolveImage(ctx, ref)
}

// splitDataURL parses "data:image/png;base64,<payload>" into (mediaType, base64).
// Returns ok=false for non-data: URLs or malformed values (no ";base64," marker).
func splitDataURL(dataURL string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return "", "", false
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return "", "", false
	}
	meta := dataURL[len(prefix):comma] // e.g. "image/png;base64"
	if !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	mediaType = strings.TrimSuffix(meta, ";base64")
	if mediaType == "" {
		// An empty media type (e.g. "data:;base64,AAA") is malformed — reject
		// instead of guessing (Anthropic requires a concrete media_type and
		// OpenAI needs the format hint for image processing).
		return "", "", false
	}
	return mediaType, dataURL[comma+1:], true
}

// errNoImageResolver degrades non-inline image references when no resolver is
// wired (unit tests / legacy embedding). Never fails the request.
var errNoImageResolver = &imageResolveError{"no image resolver wired (only data: URLs are inline-resolvable)"}

type imageResolveError struct{ msg string }

func (e *imageResolveError) Error() string { return e.msg }

// hasMultimodalImages reports whether the content carries at least one image
// reference (markdown or legacy tag). Used by callers to skip the full parse
// on the common no-image path.
func hasMultimodalImages(content string) bool {
	return strings.Contains(content, "![") || strings.Contains(content, "<image ")
}
