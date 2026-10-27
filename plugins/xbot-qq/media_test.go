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
