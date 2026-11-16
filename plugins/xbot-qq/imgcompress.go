package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // 注册解码器
	"image/jpeg"
	_ "image/png"
	"strings"
)

// compressToLimit 把图片压到 limit 字节以内。
//
// 用户要求：「超 5M 就压缩」。官方 file_data 直传上限 5M，而 QQ 平台侧
// 没有我们的鉴权（签名 URL / 内网地址下不下来），所以必须本地直传；
// 直传就要求体积达标 —— 这里做等比缩小 + 逐级降 JPEG 质量。
//
// 策略：
//  1. 解码（png/jpeg/gif）
//  2. 若超限，先按比例缩小（每轮 0.75），最多缩到 320px 宽
//  3. 每档尺寸内，JPEG 质量从 88 逐级降到 40
//  4. 全部失败时返回错误（调用方退回 url 方式）
func compressToLimit(data []byte, limit int) ([]byte, string, error) {
	if len(data) <= limit {
		return data, "", nil
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, "", fmt.Errorf("zero-size image")
	}

	cur := img
	for scale := 1.0; scale >= 0.10; scale *= 0.75 {
		tw, th := int(float64(w)*scale), int(float64(h)*scale)
		if tw < 32 || th < 32 {
			break
		}
		cur = resizeNearest(img, tw, th)
		for _, q := range []int{88, 80, 72, 64, 56, 48, 40} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, cur, &jpeg.Options{Quality: q}); err != nil {
				continue
			}
			if buf.Len() <= limit {
				name := "image.jpg"
				if strings.EqualFold(format, "gif") {
					name = "image.jpg" // gif 转静态 jpg
				}
				return buf.Bytes(), name, nil
			}
		}
	}
	return nil, "", fmt.Errorf("cannot compress %d bytes under %d", len(data), limit)
}

// resizeNearest 用最近邻缩放（标准库无 image/draw 的高质量缩放的轻量替代）。
// 对"把大图压到 5M 以内"这个目的足够；避免引入第三方依赖。
func resizeNearest(src image.Image, tw, th int) image.Image {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		sy := sb.Min.Y + y*sh/th
		for x := 0; x < tw; x++ {
			sx := sb.Min.X + x*sw/tw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
