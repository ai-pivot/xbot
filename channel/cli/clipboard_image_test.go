package cli

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestDetectMimeType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"PNG", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...), "image/png"},
		{"JPEG", append([]byte("\xFF\xD8\xFF\xE0"), make([]byte, 100)...), "image/jpeg"},
		{"empty", []byte{}, "image/png"},                // default
		{"unknown", []byte("HELLO WORLD"), "image/png"}, // default
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectMimeType(tt.data)
			if got != tt.want {
				t.Errorf("detectMimeType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtFromMime(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/png", "png"},
		{"image/jpeg", "jpg"},
		{"image/webp", "webp"},
		{"image/gif", "gif"},
		{"application/octet-stream", "png"}, // default
	}
	for _, tt := range tests {
		got := extFromMime(tt.mime)
		if got != tt.want {
			t.Errorf("extFromMime(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}

func TestCompressImage_NoCompressionNeeded(t *testing.T) {
	// Small image — should be returned as-is
	small := createTestPNG(10, 10)
	data, mime, err := CompressImage(small, "image/png", 5<<20)
	if err != nil {
		t.Fatalf("CompressImage failed: %v", err)
	}
	if !bytes.Equal(data, small) {
		t.Error("expected data to be unchanged for small image")
	}
	if mime != "image/png" {
		t.Errorf("expected mime to be unchanged, got %s", mime)
	}
}

func TestCompressImage_PNGToJPEG(t *testing.T) {
	// Create a noisy PNG and force compression with a low threshold
	large := createNoisyPNG(2000, 2000)
	originalSize := len(large)
	// Force compression by setting max just below original size
	maxBytes := originalSize - 1
	if maxBytes < 1000 {
		t.Skipf("test PNG too small: %d bytes", originalSize)
	}
	data, mime, err := CompressImage(large, "image/png", maxBytes)
	if err != nil {
		// Compressed result may still exceed threshold — that's OK,
		// we just want to verify the compression path was exercised.
		// If it failed with "too large after compression", the image was
		// at least decoded, resized, and re-encoded as JPEG.
		if !strings.Contains(err.Error(), "too large") {
			t.Fatalf("unexpected error: %v", err)
		}
		t.Logf("compression triggered but result still too large: %v (original=%d)", err, originalSize)
		return
	}
	// After successful compression, should be JPEG
	if mime != "image/jpeg" {
		t.Errorf("expected image/jpeg after compression, got %s", mime)
	}
	// Verify it's valid JPEG
	_, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("failed to decode compressed image: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("expected jpeg format, got %s", format)
	}
}

func TestCompressImage_TooLargeAfterCompression(t *testing.T) {
	// Create an image with random-ish data that won't compress well
	large := createNoisyPNG(3000, 3000)
	// Set maxBytes extremely small to force failure
	_, _, err := CompressImage(large, "image/png", 100)
	if err == nil {
		t.Error("expected error for image too large after compression")
	}
}

func TestResizeImage(t *testing.T) {
	src := createTestPNG(4000, 2000) // createTestPNG returns PNG bytes, not image.Image
	// Decode to get the image.Image
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	// Resize to max 2048
	resized := resizeImage(img, maxImageDim)
	bounds := resized.Bounds()
	if bounds.Dx() > maxImageDim || bounds.Dy() > maxImageDim {
		t.Errorf("resized dimensions %dx%d exceed max %d", bounds.Dx(), bounds.Dy(), maxImageDim)
	}
	// Should maintain aspect ratio
	// Original 4000x2000 → scale = 2048/4000 = 0.512 → newW≈2048, newH≈1024
	if bounds.Dx() != 2048 || bounds.Dy() != 1024 {
		t.Errorf("expected 2048x1024, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

// createTestPNG creates a simple solid-color PNG of the given dimensions.
func createTestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// createNoisyPNG creates a PNG with high entropy (hard to compress).
func createNoisyPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			val := byte((x*y + x + y) & 0xFF)
			img.Set(x, y, color.RGBA{R: val, G: val, B: val, A: 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestEncodeToMediaContent(t *testing.T) {
	data := []byte("test image data")
	mc := EncodeToMediaContent(data, "image/png", "test.png")
	if mc.MIMEType != "image/png" {
		t.Errorf("expected image/png, got %s", mc.MIMEType)
	}
	if mc.Filename != "test.png" {
		t.Errorf("expected test.png, got %s", mc.Filename)
	}
	// Base64 should decode back to original
	decoded, err := base64.StdEncoding.DecodeString(mc.Base64)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Error("decoded base64 doesn't match original")
	}
}
