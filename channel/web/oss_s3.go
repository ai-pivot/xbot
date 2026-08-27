// xbot Web Channel — S3-compatible OSS provider (pure stdlib, no SDK)

package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	log "xbot/logger"
)

// S3Config holds S3-compatible storage configuration.
type S3Config struct {
	AccessKey    string
	SecretKey    string
	Bucket       string
	Region       string
	Endpoint     string // e.g. "http://localhost:9000" (empty = AWS S3)
	UsePathStyle bool   // true for MinIO/SeaweedFS, false for AWS virtual-hosted
	Domain       string // custom CDN domain; empty = use presigned URLs
}

// S3Provider stores files on S3-compatible storage (MinIO, SeaweedFS, AWS S3, etc.)
// using only the Go standard library — no AWS SDK required.
type S3Provider struct {
	accessKey    string
	secretKey    string
	bucket       string
	region       string
	endpoint     string // base URL without trailing slash; empty = AWS S3
	usePathStyle bool
	domain       string // custom domain for public URL construction
}

// NewS3Provider creates an S3-compatible storage provider.
func NewS3Provider(cfg S3Config) (*S3Provider, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: access_key, secret_key, and bucket are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	cfg.Domain = strings.TrimSpace(strings.TrimRight(cfg.Domain, "/"))
	if cfg.Domain != "" && !strings.HasPrefix(cfg.Domain, "http://") && !strings.HasPrefix(cfg.Domain, "https://") {
		cfg.Domain = "https://" + cfg.Domain
	}
	return &S3Provider{
		accessKey:    cfg.AccessKey,
		secretKey:    cfg.SecretKey,
		bucket:       cfg.Bucket,
		region:       cfg.Region,
		endpoint:     cfg.Endpoint,
		usePathStyle: cfg.UsePathStyle,
		domain:       cfg.Domain,
	}, nil
}

func (p *S3Provider) Name() string { return "s3" }

func (p *S3Provider) Domain() string { return p.domain }

// Upload puts data to S3 at the given key using a PUT request with SigV4.
func (p *S3Provider) Upload(key string, data []byte) error {
	req, err := p.buildSignedRequest(http.MethodPut, key, data)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("s3 upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 upload failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	log.WithFields(log.Fields{
		"key":    key,
		"size":   len(data),
		"status": resp.StatusCode,
	}).Debug("File uploaded to S3")
	return nil
}

// GetDownloadURL returns a URL for downloading the file.
// If a custom domain is set, returns a direct public URL.
// Otherwise, generates a presigned URL valid for 1 hour.
func (p *S3Provider) GetDownloadURL(key string) (string, error) {
	if p.domain != "" {
		return fmt.Sprintf("%s/%s", p.domain, key), nil
	}

	// Generate presigned URL (valid for 1 hour)
	url, err := p.presignURL(http.MethodGet, key, time.Hour)
	if err != nil {
		return "", fmt.Errorf("s3 presign failed: %w", err)
	}
	return url, nil
}

// ---------------------------------------------------------------------------
// Internal: SigV4 signing (pure stdlib)
// ---------------------------------------------------------------------------

// buildSignedRequest constructs an HTTP request with AWS SigV4 authorization.
func (p *S3Provider) buildSignedRequest(method, key string, body []byte) (*http.Request, error) {
	u := p.objectURL(key)
	req, err := http.NewRequest(method, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}

	p.signRequest(req, body)
	return req, nil
}

// presignURL generates a presigned S3 URL valid for the given duration.
func (p *S3Provider) presignURL(method, key string, expiry time.Duration) (string, error) {
	u := p.objectURL(key)
	now := time.Now().UTC()
	date := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// Add SigV4 query parameters
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", fmt.Sprintf("%s/%s/%s/s3/aws4_request", p.accessKey, dateStamp, p.region))
	q.Set("X-Amz-Date", date)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(expiry.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()

	// Build canonical request
	canonicalURI := p.canonicalURI(u)
	canonicalQueryString := p.canonicalQueryString(u)
	host := p.requestHost(u)
	canonicalHeaders := "host:" + host + "\n"
	signedHeaders := "host"

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		"UNSIGNED-PAYLOAD")

	// Build string to sign
	hash := sha256Hex([]byte(canonicalRequest))
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s/%s/s3/aws4_request\n%s",
		date, dateStamp, dateStamp, p.region, hash)

	// Calculate signature
	signingKey := p.getSigningKey(dateStamp)
	signature := hmacHex(signingKey, []byte(stringToSign))

	// Append signature to query
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// signRequest signs an HTTP request in-place using AWS SigV4.
func (p *S3Provider) signRequest(req *http.Request, body []byte) {
	now := time.Now().UTC()
	date := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// Set required headers
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", date)
	if body != nil {
		req.Header.Set("X-Amz-Content-Sha256", sha256Hex(body))
	} else {
		req.Header.Set("X-Amz-Content-Sha256", sha256Hex(nil))
	}

	// Build canonical headers (sorted)
	headers := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256"),
		"x-amz-date":           date,
	}
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	// Sort header keys
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var canonicalHeaders strings.Builder
	for _, k := range keys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers[k]))
		canonicalHeaders.WriteByte('\n')
	}

	// Build canonical request
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQueryString := "" // no query params for PUT

	payloadHash := sha256Hex(body)
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash)

	// Build string to sign
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, p.region)
	hashedCanonical := sha256Hex([]byte(canonicalRequest))
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		date, scope, hashedCanonical)

	// Calculate signature
	signingKey := p.getSigningKey(dateStamp)
	signature := hmacHex(signingKey, []byte(stringToSign))

	// Set Authorization header
	credential := fmt.Sprintf("Credential=%s/%s", p.accessKey, scope)
	signatureHeader := fmt.Sprintf("SignedHeaders=%s", signedHeaders)
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 %s, %s, Signature=%s",
		credential, signatureHeader, signature)
	req.Header.Set("Authorization", authHeader)
}

// objectURL builds the full URL for an S3 object.
func (p *S3Provider) objectURL(key string) *url.URL {
	var u url.URL
	if p.endpoint != "" {
		u.Scheme, u.Host = splitSchemeHost(p.endpoint)
	} else {
		u.Scheme = "https"
		u.Host = "s3." + p.region + ".amazonaws.com"
	}

	if p.usePathStyle || p.endpoint != "" {
		// Path-style: endpoint/bucket/key
		u.Path = "/" + p.bucket + "/" + key
	} else {
		// Virtual-hosted-style: bucket.s3.region.amazonaws.com/key
		u.Host = p.bucket + "." + u.Host
		u.Path = "/" + key
	}
	return &u
}

// canonicalURI returns the URI path for canonical request.
func (p *S3Provider) canonicalURI(u *url.URL) string {
	// S3 canonical URI should be the encoded path
	uri := u.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	return uri
}

// canonicalQueryString returns the canonical query string for presigned URLs.
func (p *S3Provider) canonicalQueryString(u *url.URL) string {
	q := u.Query()
	// Sort query parameters
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		for _, v := range q[k] {
			pairs = append(pairs, fmt.Sprintf("%s=%s", awsURIEncode(k, true), awsURIEncode(v, true)))
		}
	}
	return strings.Join(pairs, "&")
}

// requestHost extracts the host from a URL.
func (p *S3Provider) requestHost(u *url.URL) string {
	return u.Host
}

// getSigningKey derives the SigV4 signing key for the given date.
func (p *S3Provider) getSigningKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+p.secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(p.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// sha256Hex returns the hex-encoded SHA-256 hash of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// hmacSHA256 returns HMAC-SHA256(key, data).
func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// hmacHex returns hex-encoded HMAC-SHA256(key, data).
func hmacHex(key, data []byte) string {
	return hex.EncodeToString(hmacSHA256(key, data))
}

// splitSchemeHost splits a URL like "http://localhost:9000" into scheme and host.
func splitSchemeHost(rawURL string) (string, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "https", rawURL
	}
	return u.Scheme, u.Host
}

// awsURIEncode encodes a string per AWS SigV4 rules.
// encodeSlash=true encodes "/" as %2F (for query parameter values).
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteRune(r)
		} else if r == '/' && !encodeSlash {
			b.WriteRune('/')
		} else {
			h := fmt.Sprintf("%%%02X", r)
			b.WriteString(h)
		}
	}
	return b.String()
}
