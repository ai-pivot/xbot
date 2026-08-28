package web

import "testing"

// TestS3Provider_GetDownloadURL_WithDomain_PathStyle reproduces the 404 bug:
// when UsePathStyle=true (MinIO), the domain-mode download URL must include
// the bucket in the path. Without it, MinIO interprets the first path segment
// as the bucket and the remaining as the key — which doesn't match the stored
// object key, causing a 404 NoSuchKey error.
func TestS3Provider_GetDownloadURL_WithDomain_PathStyle(t *testing.T) {
	p, _ := NewS3Provider(S3Config{
		AccessKey:    "xbot",
		SecretKey:    "xbotpassword",
		Bucket:       "uploads",
		Region:       "us-east-1",
		Endpoint:     "http://localhost:9000",
		UsePathStyle: true,
		Domain:       "http://localhost:9000",
	})
	// Key from web_file.go: "uploads/<userID>/<uuid><ext>"
	url, err := p.GetDownloadURL("uploads/1/abc-123.h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Path-style: domain/bucket/key — the bucket must appear in the URL path
	expected := "http://localhost:9000/uploads/uploads/1/abc-123.h"
	if url != expected {
		t.Fatalf("domain path-style URL mismatch:\n  want %q\n  got  %q", expected, url)
	}
}
