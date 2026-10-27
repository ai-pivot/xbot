package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// TestImageDimensions_PNG 读取真实 PNG 尺寸。
func TestImageDimensions_PNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1419, 995))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	w, h := imageDimensions(buf.Bytes())
	if w != 1419 || h != 995 {
		t.Fatalf("png dimensions = %dx%d, want 1419x995", w, h)
	}
}

// TestImageDimensions_JPEG 读取真实 JPEG 尺寸（走 SOF 段解析）。
func TestImageDimensions_JPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{0, uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	w, h := imageDimensions(buf.Bytes())
	if w != 640 || h != 480 {
		t.Fatalf("jpeg dimensions = %dx%d, want 640x480", w, h)
	}
}

// TestImageDimensions_GIF 读取 GIF 逻辑屏尺寸。
func TestImageDimensions_GIF(t *testing.T) {
	// 最小 GIF 头：GIF89a + 宽 32 高 16（小端）
	buf := []byte{'G', 'I', 'F', '8', '9', 'a'}
	var d [4]byte
	binary.LittleEndian.PutUint16(d[0:2], 32)
	binary.LittleEndian.PutUint16(d[2:4], 16)
	buf = append(buf, d[:]...)
	buf = append(buf, make([]byte, 16)...) // padding，长度需 >24 走不到 PNG 分支也无所谓
	w, h := imageDimensions(buf)
	if w != 32 || h != 16 {
		t.Fatalf("gif dimensions = %dx%d, want 32x16", w, h)
	}
}

// TestImageDimensions_Unknown 无法识别时返回 0,0（由 markdownImageTag 兜底）。
func TestImageDimensions_Unknown(t *testing.T) {
	if w, h := imageDimensions([]byte("not an image at all, just text")); w != 0 || h != 0 {
		t.Fatalf("want 0,0 for unknown data, got %dx%d", w, h)
	}
	if w, h := imageDimensions(nil); w != 0 || h != 0 {
		t.Fatalf("want 0,0 for nil, got %dx%d", w, h)
	}
}

// TestMarkdownImageTag 钉住「两个尺寸都必须给」。
//
// 2026-10-27 实测：`![图片 #300px](url)`（只有宽）客户端**不渲染**，
// 表现为「消息发出去了但没图」，而平台侧完全无错；写成
// `![图片 #300px #210px](url)` 就正常显示。
func TestMarkdownImageTag(t *testing.T) {
	tag := markdownImageTag("qqbot-media://image/v1/abc", 300, 210)
	want := "![图片 #300px #210px](qqbot-media://image/v1/abc)"
	if tag != want {
		t.Fatalf("tag = %q, want %q", tag, want)
	}
	// 必须同时含宽和高
	if !strings.Contains(tag, "#300px #210px") {
		t.Fatalf("tag must contain BOTH dimensions: %q", tag)
	}
}

// TestMarkdownImageTag_FallbackAndClamp 覆盖尺寸缺失与超大图。
func TestMarkdownImageTag_FallbackAndClamp(t *testing.T) {
	// 读不出尺寸 → 用保守默认值，且仍然两个尺寸都给
	tag := markdownImageTag("ref", 0, 0)
	if !strings.Contains(tag, "px #") || strings.Count(tag, "px") != 2 {
		t.Fatalf("fallback tag must still carry two dimensions: %q", tag)
	}
	// 超大图按 600px 宽等比缩放
	tag = markdownImageTag("ref", 1200, 800)
	if !strings.Contains(tag, "#600px #400px") {
		t.Fatalf("oversize image not clamped proportionally: %q", tag)
	}
	// 极端竖图不能出现 0 高度
	tag = markdownImageTag("ref", 100000, 1)
	if strings.Contains(tag, "#0px") {
		t.Fatalf("height collapsed to 0: %q", tag)
	}
}

// TestImageReplacementKeepsPosition 钉住「图片必须留在原位」。
//
// 2026-10-27 用户实测：旧实现先 extractMarkdownImages 把图从正文删掉，
// 再统一拼到消息末尾，于是「不管模型把图放在哪一段，图永远显示在最下方」。
// 改成 ReplaceAllStringFunc 原地替换后，图文相对顺序才和模型写的一致。
func TestImageReplacementKeepsPosition(t *testing.T) {
	md := "开头说明\n\n![甲 #100px #100px](/tmp/a.png)\n\n中间段落\n\n![乙 #200px #200px](/tmp/b.png)\n\n结尾"
	out := imageMarkdownRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := imageMarkdownRe.FindStringSubmatch(m)
		// 模拟"上传后拿到平台引用"
		return markdownImageTag("qqbot-media://image/v1"+sub[1], 100, 100)
	})

	// 顺序断言：开头 < 甲图 < 中间段落 < 乙图 < 结尾
	order := []string{"开头说明", "qqbot-media://image/v1/tmp/a.png", "中间段落", "qqbot-media://image/v1/tmp/b.png", "结尾"}
	last := -1
	for _, part := range order {
		i := strings.Index(out, part)
		if i < 0 {
			t.Fatalf("missing %q in output:\n%s", part, out)
		}
		if i < last {
			t.Fatalf("%q appeared out of order — images must stay where the model put them:\n%s", part, out)
		}
		last = i
	}
	// 旧实现的症状：两张图都被挪到最末尾
	if strings.LastIndex(out, "![") > strings.Index(out, "结尾") {
		t.Fatalf("an image was moved after the trailing text:\n%s", out)
	}
}
