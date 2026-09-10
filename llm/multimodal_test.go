package llm

// Multimodal (vision) message building — parseMultimodalContent + degradation
// + budget + Anthropic image blocks.
//
// Design contract (see multimodal.go): vision is a PURELY MANUAL per-model
// switch — no built-in model-name whitelist. Vision off → ALL image references
// (markdown + legacy <image> tags) degrade to text placeholders so non-vision
// models never receive image parts. Budget: only the most RECENT MaxImages
// survive; older ones degrade.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeResolver struct {
	resolved map[string]string // ref → dataURL
	failOn   string            // ref that returns an error
	calls    []string
}

func (f *fakeResolver) ResolveImage(ctx context.Context, ref string) (string, error) {
	f.calls = append(f.calls, ref)
	if ref == f.failOn {
		return "", errors.New("boom")
	}
	if u, ok := f.resolved[ref]; ok {
		return u, nil
	}
	return "data:image/png;base64,RESOLVED", nil
}

func TestParseMultimodalContent_NoImages_FastPath(t *testing.T) {
	parts := parseMultimodalContent(context.TODO(), "just plain text, no images", nil)
	if len(parts) != 1 || parts[0].Type != "text" || parts[0].Text != "just plain text, no images" {
		t.Fatalf("plain text must be a single verbatim part, got %+v", parts)
	}
}

func TestParseMultimodalContent_VisionOff_DegradesToPlaceholder(t *testing.T) {
	content := "before\n![chart](/api/files/download?key=uploads%2Fu1%2Fa.png)\nafter"
	parts := parseMultimodalContent(context.TODO(), content, nil) // nil mc = vision off
	if len(parts) != 1 || parts[0].Type != "text" {
		t.Fatalf("vision off must degrade to text-only, got %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "[图片: chart — 当前模型未开启视觉输入") {
		t.Fatalf("expected vision-off placeholder, got %q", parts[0].Text)
	}
	// The raw reference IS carried on purpose: the model can't see the image,
	// but it can point the user to it (or hand it to a tool) — the old
	// URL-less placeholder left the model with a bare filename (Feishu report:
	// "也没有给我可下载的 file_key/URL").
	if !strings.Contains(parts[0].Text, "；引用: /api/files/download") {
		t.Fatalf("degraded text must carry the raw reference, got %q", parts[0].Text)
	}
	// data: URLs are ALSO gated by the vision switch — non-vision models must
	// never receive image parts (API 400).
	parts = parseMultimodalContent(context.TODO(), "![x](data:image/png;base64,AAA)", nil)
	if len(parts) != 1 || parts[0].Type != "text" {
		t.Fatalf("data: URL with vision off must degrade too, got %+v", parts)
	}
}

func TestParseMultimodalContent_VisionOn_DataURLPassesThrough(t *testing.T) {
	mc := &MultimodalConfig{VisionEnabled: true}
	content := "look ![pic](data:image/png;base64,iVBOR) thanks"
	parts := parseMultimodalContent(context.TODO(), content, mc)
	// [text, caption, image, text] — the caption accompanies the pixels so the
	// model knows WHICH image it is looking at (a data: URL has no path).
	if len(parts) != 4 {
		t.Fatalf("expected [text, caption, image, text], got %+v", parts)
	}
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "look") {
		t.Fatalf("first part: %+v", parts[0])
	}
	if parts[1].Type != "text" || !strings.Contains(parts[1].Text, "图片") {
		t.Fatalf("caption part: %+v", parts[1])
	}
	if parts[2].Type != "image" || parts[2].URL != "data:image/png;base64,iVBOR" {
		t.Fatalf("image part: %+v", parts[2])
	}
	if parts[3].Type != "text" || !strings.Contains(parts[3].Text, "thanks") {
		t.Fatalf("last part: %+v", parts[3])
	}
}

func TestParseMultimodalContent_VisionOn_ResolverReferences(t *testing.T) {
	r := &fakeResolver{}
	mc := &MultimodalConfig{VisionEnabled: true, ImageResolver: r}
	refs := []string{
		"/api/files/download?key=uploads%2Fu1%2Fa.png",
		"https://oss.example.com/signed/x.png",
		"viewimg://abc-123",
	}
	for _, ref := range refs {
		content := fmt.Sprintf("![img](%s)", ref)
		parts := parseMultimodalContent(context.Background(), content, mc)
		if len(parts) != 2 {
			t.Fatalf("ref %s: expected [caption, image], got %+v", ref, parts)
		}
		// The caption MUST keep the reference visible to the model.
		if parts[0].Type != "text" || !strings.Contains(parts[0].Text, ref) {
			t.Fatalf("ref %s: caption must contain the reference, got %+v", ref, parts[0])
		}
		if parts[1].Type != "image" || parts[1].URL != "data:image/png;base64,RESOLVED" {
			t.Fatalf("ref %s: image part = %+v", ref, parts[1])
		}
	}
	if len(r.calls) != 3 {
		t.Fatalf("resolver called %d times, want 3: %v", len(r.calls), r.calls)
	}
}

func TestParseMultimodalContent_ResolverFailure_Degrades(t *testing.T) {
	r := &fakeResolver{failOn: "https://expired.example.com/x.png"}
	mc := &MultimodalConfig{VisionEnabled: true, ImageResolver: r}
	parts := parseMultimodalContent(context.TODO(), "![photo](https://expired.example.com/x.png)", mc)
	if len(parts) != 1 || parts[0].Type != "text" {
		t.Fatalf("resolve failure must degrade, got %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "[图片: photo — 加载失败") {
		t.Fatalf("expected load-failure placeholder, got %q", parts[0].Text)
	}
}

func TestParseMultimodalContent_BudgetKeepsMostRecent(t *testing.T) {
	r := &fakeResolver{}
	mc := &MultimodalConfig{VisionEnabled: true, ImageResolver: r, MaxImages: 3}
	var sb strings.Builder
	sb.WriteString("start ")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sb, "![img%d](viewimg://%d) ", i, i)
	}
	sb.WriteString("end")
	parts := parseMultimodalContent(context.TODO(), sb.String(), mc)
	images := 0
	placeholders := 0
	for _, p := range parts {
		if p.Type == "image" {
			images++
		} else if strings.Contains(p.Text, "已省略") {
			placeholders++
		}
	}
	if images != 3 {
		t.Fatalf("budget 3 must keep exactly 3 most recent images, got %d", images)
	}
	if placeholders != 2 {
		t.Fatalf("oldest 2 images must degrade to placeholders, got %d", placeholders)
	}
	// The over-budget placeholders (img0, img1) appear in the parts sequence
	// (after the leading "start" text) — verify img0 degraded.
	var img0Degraded bool
	for _, p := range parts {
		if p.Type == "text" && strings.Contains(p.Text, "img0") && strings.Contains(p.Text, "已省略") {
			img0Degraded = true
		}
	}
	if !img0Degraded {
		t.Fatalf("img0 (over budget) must degrade to placeholder, parts: %+v", parts)
	}
	// Verify which refs the resolver actually resolved (calls[0..2] = viewimg://2,3,4 — budget keeps the LAST 3).
	if len(r.calls) != 3 || r.calls[0] != "viewimg://2" || r.calls[2] != "viewimg://4" {
		t.Fatalf("budget must resolve only the 3 most recent refs (2,3,4), got calls: %v", r.calls)
	}
}

func TestParseMultimodalContent_LegacyImageTag(t *testing.T) {
	r := &fakeResolver{}
	mc := &MultimodalConfig{VisionEnabled: true, ImageResolver: r}
	content := `<image url="https://oss.example.com/pic.png" name="pic.png" size="12345" />`
	parts := parseMultimodalContent(context.TODO(), content, mc)
	if len(parts) != 2 || parts[1].Type != "image" {
		t.Fatalf("legacy <image> tag must resolve to [caption, image], got %+v", parts)
	}
	if r.calls[0] != "https://oss.example.com/pic.png" {
		t.Fatalf("resolver ref = %q", r.calls[0])
	}
	// Vision off: legacy tag degrades with the tag's name attribute.
	parts = parseMultimodalContent(context.TODO(), content, nil)
	if !strings.Contains(parts[0].Text, "[图片: pic.png — 当前模型未开启视觉输入") {
		t.Fatalf("legacy tag vision-off placeholder: %q", parts[0].Text)
	}
}

func TestParseMultimodalContent_NoResolverNonDataURL_Degrades(t *testing.T) {
	mc := &MultimodalConfig{VisionEnabled: true} // no ImageResolver
	parts := parseMultimodalContent(context.TODO(), "![x](viewimg://abc)", mc)
	if len(parts) != 1 || parts[0].Type != "text" {
		t.Fatalf("expected degradation without resolver, got %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "加载失败") {
		t.Fatalf("placeholder should say 加载失败, got %q", parts[0].Text)
	}
}

func TestToOpenAIMessages_VisionDegradation(t *testing.T) {
	msgs := []ChatMessage{NewUserMessage("analyze ![chart](https://example.com/c.png) please")}

	// Vision OFF: single plain text user message with the placeholder.
	out := toOpenAIMessages(context.TODO(), msgs, "", nil)
	if len(out) != 1 || out[0].OfUser == nil {
		t.Fatalf("vision off: expected 1 user message, got %+v", out)
	}
	if !out[0].OfUser.Content.OfString.Valid() {
		t.Fatalf("vision off: content must be a plain string, got %+v", out[0].OfUser.Content)
	}
	got := out[0].OfUser.Content.OfString.Value
	if !strings.Contains(got, "[图片: chart — 当前模型未开启视觉输入") {
		t.Fatalf("vision off placeholder missing: %q", got)
	}

	// Vision ON with resolver: multi-part message with image_url.
	r := &fakeResolver{}
	out = toOpenAIMessages(context.TODO(), msgs, "", &MultimodalConfig{VisionEnabled: true, ImageResolver: r})
	if len(out) != 1 || out[0].OfUser == nil {
		t.Fatalf("vision on: expected 1 user message, got %+v", out)
	}
	parts := out[0].OfUser.Content.OfArrayOfContentParts
	// 4 parts: [text, caption, image, text] — the caption (added with the
	// image-locator fix) rides alongside every successfully resolved image so
	// the model knows where the file is.
	if parts == nil || len(parts) != 4 {
		t.Fatalf("vision on: expected 4 content parts, got %+v", out[0].OfUser.Content)
	}
	hasImage := false
	for _, p := range parts {
		if p.OfImageURL != nil {
			hasImage = true
			if p.OfImageURL.ImageURL.URL != "data:image/png;base64,RESOLVED" {
				t.Fatalf("image URL = %q", p.OfImageURL.ImageURL.URL)
			}
		}
	}
	if !hasImage {
		t.Fatal("vision on: no image content part found")
	}
}

func TestToAnthropicMessages_ImageBlocks(t *testing.T) {
	msgs := []ChatMessage{NewUserMessage("see ![pic](data:image/jpeg;base64,QUJD) here")}

	// Vision ON: user message becomes content blocks with a base64 image.
	out := toAnthropicMessages(context.TODO(), msgs, false, &MultimodalConfig{VisionEnabled: true})
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	blocks, ok := out[0].Content.([]any)
	if !ok {
		t.Fatalf("expected []any blocks, got %T", out[0].Content)
	}
	var sawImage bool
	for _, b := range blocks {
		img, isImg := b.(anthropicImageBlock)
		if !isImg {
			continue
		}
		sawImage = true
		if img.Type != "image" || img.Source.Type != "base64" {
			t.Fatalf("image block: %+v", img)
		}
		if img.Source.MediaType != "image/jpeg" || img.Source.Data != "QUJD" {
			t.Fatalf("source: %+v", img.Source)
		}
	}
	if !sawImage {
		t.Fatalf("no image block in %+v", blocks)
	}

	// Vision OFF: plain string content with the placeholder.
	out = toAnthropicMessages(context.TODO(), msgs, false, nil)
	if s, ok := out[0].Content.(string); !ok || !strings.Contains(s, "未开启视觉输入") {
		t.Fatalf("vision off anthropic: %v", out[0].Content)
	}
}

func TestSplitDataURL(t *testing.T) {
	mt, data, ok := splitDataURL("data:image/png;base64,AAA")
	if !ok || mt != "image/png" || data != "AAA" {
		t.Fatalf("got %q %q %v", mt, data, ok)
	}
	if _, _, ok := splitDataURL("https://x/y.png"); ok {
		t.Fatal("http URL must not parse as data URL")
	}
	if _, _, ok := splitDataURL("data:image/png,AAA"); ok {
		t.Fatal("non-base64 data URL must be rejected")
	}
	if _, _, ok := splitDataURL("data:;base64,AAA"); ok {
		t.Fatal("empty media type must be rejected (raw split would yield empty)")
	}
}

// localPathResolver implements the optional LocalPath extension.
type localPathResolver struct{ fakeResolver }

func (l *localPathResolver) LocalPath(ref string) (string, bool) {
	if strings.Contains(ref, "viewimg") || strings.Contains(ref, "download") {
		return "/home/smith/.xbot/view_images/abc.png", true
	}
	return "", false
}

// TestParseMultimodalContent_VisionOn_CaptionCarriesLocalPath —— 本次修复的核心：
// 开了 vision 时，模型拿到像素之外还必须拿到**可操作的本地路径**，否则让它
// "ps 这张图"只能演变成全盘 find（用户报告）。
func TestParseMultimodalContent_VisionOn_CaptionCarriesLocalPath(t *testing.T) {
	r := &localPathResolver{}
	mc := &MultimodalConfig{VisionEnabled: true, ImageResolver: r}
	content := "![shot](/api/files/download?key=uploads%2Fu1%2Fabc.png)"
	parts := parseMultimodalContent(context.TODO(), content, mc)
	if len(parts) != 2 {
		t.Fatalf("expected [caption, image], got %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "/home/smith/.xbot/view_images/abc.png") {
		t.Fatalf("caption must expose the actionable local path, got %q", parts[0].Text)
	}
	if !strings.Contains(parts[0].Text, "本地路径") {
		t.Fatalf("caption must label the local path, got %q", parts[0].Text)
	}
	if parts[1].Type != "image" {
		t.Fatalf("image part missing: %+v", parts)
	}
}

// TestParseMultimodalContent_VisionOn_CaptionFallsBackToRef —— 没有本地副本时
// （远端 http / 纯 OSS），caption 至少保留原始引用。
func TestParseMultimodalContent_VisionOn_CaptionFallsBackToRef(t *testing.T) {
	r := &localPathResolver{}
	mc := &MultimodalConfig{VisionEnabled: true, ImageResolver: r}
	content := "![remote](https://example.com/a.png)"
	parts := parseMultimodalContent(context.TODO(), content, mc)
	if len(parts) != 2 {
		t.Fatalf("expected [caption, image], got %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "https://example.com/a.png") {
		t.Fatalf("caption must fall back to the raw reference, got %q", parts[0].Text)
	}
}
