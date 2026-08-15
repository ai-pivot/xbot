package tools

import (
	"os"
	"testing"
)

func TestReadTool_ImageDetection(t *testing.T) {
	// Create a minimal PNG file (1x1 pixel)
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, // IHDR length
		0x49, 0x48, 0x44, 0x52, // "IHDR"
		0x00, 0x00, 0x00, 0x01, // width=1
		0x00, 0x00, 0x00, 0x01, // height=1
		0x08, 0x06, 0x00, 0x00, 0x00, // bit depth=8, color type=6
		0x1F, 0x15, 0xC4, 0x89, // CRC
		0x00, 0x00, 0x00, 0x0A, // IDAT length
		0x49, 0x44, 0x41, 0x54, // "IDAT"
		0x78, 0x9C, 0x62, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0D, 0x0A, 0x2D, 0xB4, // CRC
		0x00, 0x00, 0x00, 0x00, // IEND length
		0x49, 0x45, 0x4E, 0x44, // "IEND"
		0xAE, 0x42, 0x60, 0x82, // CRC
	}
	tmpFile, err := os.CreateTemp("", "test_image*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(pngData)
	tmpFile.Close()

	info, _ := os.Stat(tmpFile.Name())
	mime := detectImageMIME(tmpFile.Name(), info)
	if mime != "image/png" {
		t.Errorf("expected image/png, got %s", mime)
	}

	// Test non-image file
	txtFile, _ := os.CreateTemp("", "test_text*.txt")
	defer os.Remove(txtFile.Name())
	txtFile.WriteString("hello world")
	txtFile.Close()
	info2, _ := os.Stat(txtFile.Name())
	mime2 := detectImageMIME(txtFile.Name(), info2)
	if mime2 != "" {
		t.Errorf("expected empty mime for txt file, got %s", mime2)
	}
}
