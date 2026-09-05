// xbot Web Channel - File upload tests

package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xbot/bus"
)

// fakeOSSProvider accepts any upload (in-memory) — mirrors OSSProvider for tests.
type fakeOSSProvider struct {
	uploaded map[string][]byte
}

func (f *fakeOSSProvider) Name() string { return "fake" }

func (f *fakeOSSProvider) Domain() string { return "https://fake.example" }

func (f *fakeOSSProvider) Upload(key string, data []byte) error {
	f.uploaded[key] = data
	return nil
}

func (f *fakeOSSProvider) GetDownloadURL(key string) (string, error) {
	return "https://fake.example/" + key, nil
}

func newUploadTestChannel() (*WebChannel, *fakeOSSProvider) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	oss := &fakeOSSProvider{uploaded: map[string][]byte{}}
	wc.ossProvider = oss
	return wc, oss
}

func multipartUploadRequest(filename, contentType string, body []byte) *http.Request {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", filename)
	_, _ = part.Write(body)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/files/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	_ = contentType
	return req
}

// Regression: uploads must have NO file-type restrictions — .html, .exe, .sh
// and extensionless files are all valid inputs for the agent. The old
// isAllowedExtension whitelist + isBlockedMIME blacklist rejected them.
func TestFileUploadAcceptsAnyFileType(t *testing.T) {
	wc, oss := newUploadTestChannel()

	cases := []struct {
		name     string
		filename string
		body     []byte
	}{
		{"html previously blocked by MIME sniffing", "page.html", []byte("<html><body>hi</body></html>")},
		{"exe previously blocked by extension whitelist", "tool.exe", []byte{0x4d, 0x5a, 0x00, 0x01}},
		{"php previously blocked by MIME sniffing", "index.php", []byte("<?php echo 1;")},
		{"shell script previously allowed", "run.sh", []byte("#!/bin/sh\ntrue\n")},
		{"svg (image but xml-ish)", "logo.svg", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")},
		{"no extension at all", "Makefile", []byte("all:\n\ttrue\n")},
		{"binary archive", "data.zip", []byte{0x50, 0x4b, 0x03, 0x04}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			wc.handleFileUpload(rec, multipartUploadRequest(tc.filename, "", tc.body))

			if rec.Code != http.StatusOK {
				t.Fatalf("upload of %q rejected: status=%d body=%s — type restrictions must not exist", tc.filename, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "upload_key") {
				t.Fatalf("upload of %q missing upload_key: %s", tc.filename, rec.Body.String())
			}
			if len(oss.uploaded) != 1 {
				t.Fatalf("upload of %q did not reach OSS provider (stored=%d)", tc.filename, len(oss.uploaded))
			}
			for _, data := range oss.uploaded {
				if !bytes.Equal(data, tc.body) {
					t.Fatalf("stored payload differs from uploaded body for %q", tc.filename)
				}
			}
			// reset between cases
			oss.uploaded = map[string][]byte{}
		})
	}
}

// The 10MB size cap stays (size is not a type restriction).
func TestFileUploadSizeLimitStillEnforced(t *testing.T) {
	wc, _ := newUploadTestChannel()

	big := make([]byte, 10<<20+2048)
	rec := httptest.NewRecorder()
	wc.handleFileUpload(rec, multipartUploadRequest("big.bin", "", big))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload should stay 413, got %d body=%s", rec.Code, rec.Body.String())
	}
}
