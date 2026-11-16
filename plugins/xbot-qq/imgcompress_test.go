package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// makeNoisyPNG 生成一张体积较大的 PNG（噪声不可压缩，模拟真实照片）。
func makeNoisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(2463534242)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			img.Set(x, y, color.RGBA{uint8(seed), uint8(seed >> 8), uint8(seed >> 16), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// TestCompressToLimit 超 5M 的图片必须被压到上限以内，且仍是可解码的 JPEG。
//
// 用户要求：「超 5M 就压缩」。因为 QQ 的 file_data 直传上限是 5M，而平台侧
// 没有我们的鉴权（签名 URL 下不下来），所以必须本地压到 5M 内再直传。
func TestCompressToLimit(t *testing.T) {
	raw := makeNoisyPNG(t, 2400, 2400)
	t.Logf("原始 PNG 大小 = %d bytes", len(raw))
	if len(raw) <= uploadBinaryLimit {
		t.Skipf("生成的样本不够大（%d），跳过", len(raw))
	}

	out, name, err := compressToLimit(raw, uploadBinaryLimit)
	if err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	if len(out) > uploadBinaryLimit {
		t.Fatalf("压缩后 %d > 上限 %d", len(out), uploadBinaryLimit)
	}
	if name != "image.jpg" {
		t.Fatalf("压缩后文件名 = %q, want image.jpg", name)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("压缩结果不是有效 JPEG: %v", err)
	}
	t.Logf("压缩后大小 = %d bytes", len(out))
}

// TestCompressSkipsSmall 小于上限的图片原样返回（不做无谓重编码）。
func TestCompressSkipsSmall(t *testing.T) {
	small := makeNoisyPNG(t, 32, 32)
	out, name, err := compressToLimit(small, uploadBinaryLimit)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !bytes.Equal(out, small) || name != "" {
		t.Fatalf("小图不应被重编码（out=%d bytes, name=%q）", len(out), name)
	}
}
