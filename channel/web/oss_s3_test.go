package web

import (
	"testing"
)

func TestNewS3Provider_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  S3Config
	}{
		{"empty access key", S3Config{SecretKey: "s", Bucket: "b"}},
		{"empty secret key", S3Config{AccessKey: "a", Bucket: "b"}},
		{"empty bucket", S3Config{AccessKey: "a", SecretKey: "s"}},
		{"all empty", S3Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewS3Provider(tt.cfg)
			if err == nil {
				t.Fatal("expected error for missing fields, got nil")
			}
		})
	}
}

func TestNewS3Provider_DefaultRegion(t *testing.T) {
	p, err := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "b",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.region != "us-east-1" {
		t.Fatalf("expected default region 'us-east-1', got %q", p.region)
	}
}

func TestS3Provider_Name(t *testing.T) {
	p, err := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "b",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "s3" {
		t.Fatalf("expected name 's3', got %q", p.Name())
	}
}

func TestS3Provider_Domain(t *testing.T) {
	// With domain (already has scheme)
	p, _ := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "b",
		Domain:    "https://cdn.example.com",
	})
	if p.Domain() != "https://cdn.example.com" {
		t.Fatalf("expected 'https://cdn.example.com', got %q", p.Domain())
	}

	// Without domain
	p2, _ := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "b",
	})
	if p2.Domain() != "" {
		t.Fatalf("expected empty domain, got %q", p2.Domain())
	}
}

func TestS3Provider_DomainAutoHTTPS(t *testing.T) {
	p, _ := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "b",
		Domain:    "cdn.example.com",
	})
	if p.Domain() != "https://cdn.example.com" {
		t.Fatalf("expected 'https://cdn.example.com', got %q", p.Domain())
	}
}

func TestNewS3Provider_WithEndpoint(t *testing.T) {
	p, err := NewS3Provider(S3Config{
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test",
		Region:       "us-east-1",
		Endpoint:     "http://localhost:9000",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.endpoint != "http://localhost:9000" {
		t.Fatalf("expected endpoint 'http://localhost:9000', got %q", p.endpoint)
	}
}

func TestS3Provider_GetDownloadURL_WithDomain(t *testing.T) {
	p, _ := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "b",
		Domain:    "https://cdn.example.com",
	})
	url, err := p.GetDownloadURL("uploads/test/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "https://cdn.example.com/uploads/test/file.txt"
	if url != expected {
		t.Fatalf("expected %q, got %q", expected, url)
	}
}

func TestS3Provider_GetDownloadURL_Presigned(t *testing.T) {
	p, _ := NewS3Provider(S3Config{
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test",
		Region:       "us-east-1",
		Endpoint:     "http://localhost:9000",
		UsePathStyle: true,
	})
	url, err := p.GetDownloadURL("uploads/abc/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Presigned URL should contain the endpoint and SigV4 query params
	if !contains(url, "http://localhost:9000") {
		t.Fatalf("expected URL to contain endpoint, got %q", url)
	}
	if !contains(url, "X-Amz-Algorithm=AWS4-HMAC-SHA256") {
		t.Fatalf("expected URL to contain X-Amz-Algorithm, got %q", url)
	}
	if !contains(url, "X-Amz-Signature=") {
		t.Fatalf("expected URL to contain X-Amz-Signature, got %q", url)
	}
	if !contains(url, "X-Amz-Expires=3600") {
		t.Fatalf("expected URL to contain X-Amz-Expires=3600, got %q", url)
	}
}

func TestS3Provider_ObjectURL_PathStyle(t *testing.T) {
	p, _ := NewS3Provider(S3Config{
		AccessKey:    "a",
		SecretKey:    "s",
		Bucket:       "mybucket",
		Region:       "us-east-1",
		Endpoint:     "http://localhost:9000",
		UsePathStyle: true,
	})
	u := p.objectURL("uploads/test/file.txt")
	expected := "http://localhost:9000/mybucket/uploads/test/file.txt"
	if u.String() != expected {
		t.Fatalf("expected %q, got %q", expected, u.String())
	}
}

func TestS3Provider_ObjectURL_VirtualHosted(t *testing.T) {
	p, _ := NewS3Provider(S3Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "mybucket",
		Region:    "us-east-1",
	})
	u := p.objectURL("uploads/test/file.txt")
	expected := "https://mybucket.s3.us-east-1.amazonaws.com/uploads/test/file.txt"
	if u.String() != expected {
		t.Fatalf("expected %q, got %q", expected, u.String())
	}
}

func TestS3Provider_SigV4SigningKey(t *testing.T) {
	// Verify the signing key derivation chain produces a 32-byte key
	p, _ := NewS3Provider(S3Config{
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Bucket:    "b",
		Region:    "us-east-1",
	})
	key := p.getSigningKey("20130524")
	if len(key) != 32 {
		t.Fatalf("expected 32-byte signing key, got %d bytes", len(key))
	}
}

func TestSha256Hex(t *testing.T) {
	// Known SHA-256 of empty string
	got := sha256Hex(nil)
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestAwsURIEncode(t *testing.T) {
	tests := []struct {
		input       string
		encodeSlash bool
		expected    string
	}{
		{"simple", true, "simple"},
		{"a/b", false, "a/b"},
		{"a/b", true, "a%2Fb"},
		{"a b", true, "a%20b"},
		{"a+b", true, "a%2Bb"},
		{"a~b", true, "a~b"},
		{"a-b_c.d", true, "a-b_c.d"},
		// Non-ASCII (UTF-8) must be encoded per-byte, not per-rune
		{"中文", true, "%E4%B8%AD%E6%96%87"},
		{"文件.txt", false, "%E6%96%87%E4%BB%B6.txt"},
	}
	for _, tt := range tests {
		got := awsURIEncode(tt.input, tt.encodeSlash)
		if got != tt.expected {
			t.Errorf("awsURIEncode(%q, %v) = %q, want %q", tt.input, tt.encodeSlash, got, tt.expected)
		}
	}
}

// contains is a simple substring check.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
