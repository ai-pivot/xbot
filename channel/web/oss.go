// xbot OSS Provider — abstract file storage backend (local / qiniu)

package web

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	log "xbot/logger"

	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/storage"
)

// OSSProvider abstracts file storage operations.
type OSSProvider interface {
	// Upload uploads file data to the given key.
	Upload(key string, data []byte) error
	// GetDownloadURL returns a publicly accessible URL for the given object key.
	GetDownloadURL(key string) (url string, err error)
	// GetViewURL returns an INLINE URL (no Content-Disposition: attachment) for
	// browser-side rendering (<img> src in the composer). Differs from
	// GetDownloadURL which forces attachment download via attname — images
	// must render inline in the editor, everything else downloads.
	GetViewURL(key string) (url string, err error)
	// Name returns the provider name.
	Name() string
	// Domain returns the CDN domain URL for this provider (empty if not applicable).
	Domain() string
}

// ---------------------------------------------------------------------------
// LocalProvider — wraps existing local disk storage
// ---------------------------------------------------------------------------

// LocalProvider stores files on local disk (existing behavior).
type LocalProvider struct {
	uploadDir string // base upload directory
}

// NewLocalProvider creates a local storage provider.
func NewLocalProvider(uploadDir string) *LocalProvider {
	return &LocalProvider{uploadDir: uploadDir}
}

func (p *LocalProvider) Name() string   { return "local" }
func (p *LocalProvider) Domain() string { return "" }

func (p *LocalProvider) Upload(key string, data []byte) error {
	// Local provider should not be used for cloud upload — files are handled directly.
	return fmt.Errorf("local provider does not support Upload; files are saved directly by handleFileUpload")
}

func (p *LocalProvider) GetDownloadURL(key string) (string, error) {
	// Local mode: return the file path (relative to uploadDir/web/)
	// This is only used as fallback; normally local files are embedded or copied directly.
	return "", fmt.Errorf("local provider does not support GetDownloadURL; files are accessed directly")
}

func (p *LocalProvider) GetViewURL(key string) (string, error) {
	return "", fmt.Errorf("local provider does not support GetViewURL; files are accessed directly")
}

// ---------------------------------------------------------------------------
// QiniuProvider — Qiniu Cloud (七牛云) Object Storage
// ---------------------------------------------------------------------------

// qiniuZoneMap maps region IDs to Qiniu storage.Zone values.
func getQiniuZone(region string) *storage.Zone {
	switch region {
	case "z0":
		return &storage.ZoneHuadong
	case "z1":
		return &storage.ZoneHuabei
	case "z2":
		return &storage.ZoneHuanan
	case "na0":
		return &storage.ZoneBeimei
	case "as0":
		return &storage.ZoneXinjiapo
	case "cn-east-2":
		return &storage.ZoneHuadong
	default:
		log.WithField("region", region).Warn("Unknown Qiniu region, falling back to z0")
		return &storage.ZoneHuadong
	}
}

// QiniuProvider stores files on Qiniu Cloud Object Storage.
type QiniuProvider struct {
	accessKey string
	secretKey string
	bucket    string
	domain    string // CDN domain, e.g. "https://cdn.example.com"
	region    string // region ID, e.g. "z0"
	mac       *auth.Credentials
}

// NewQiniuProvider creates a Qiniu Cloud storage provider.
func NewQiniuProvider(accessKey, secretKey, bucket, domain, region string) (*QiniuProvider, error) {
	if accessKey == "" || secretKey == "" || bucket == "" || domain == "" {
		return nil, fmt.Errorf("qiniu: access_key, secret_key, bucket, and domain are required")
	}
	if region == "" {
		region = "z0"
	}
	domain = strings.TrimSpace(strings.TrimRight(domain, "/"))
	// MakePrivateURL uses url.Parse on "domain/key"; without a scheme the result is not a valid
	// absolute URL. curl and other clients then default to http:// on port 80, which often
	// returns 401 in front of HTTPS-only CDN (e.g. openresty).
	if !strings.HasPrefix(domain, "http://") && !strings.HasPrefix(domain, "https://") {
		domain = "https://" + domain
	}
	return &QiniuProvider{
		accessKey: accessKey,
		secretKey: secretKey,
		bucket:    bucket,
		domain:    domain,
		region:    region,
		mac:       auth.New(accessKey, secretKey),
	}, nil
}

func (p *QiniuProvider) Name() string { return "qiniu" }

func (p *QiniuProvider) Upload(key string, data []byte) error {
	// Generate upload token with 1-hour expiry
	putPolicy := storage.PutPolicy{
		Scope:   fmt.Sprintf("%s:%s", p.bucket, key),
		Expires: uint64(time.Now().Add(time.Hour).Unix()),
	}
	upToken := putPolicy.UploadToken(p.mac)

	// Create form uploader with correct region zone
	cfg := storage.Config{
		UseHTTPS:      true,
		UseCdnDomains: false,
		Zone:          getQiniuZone(p.region),
	}
	formUploader := storage.NewFormUploader(&cfg)
	ret := storage.PutRet{}

	// Bound the upload: qiniu SDK has no default timeout and context.TODO() made
	// uploads hang for 50–128s (log: "Slow API request elapsed=2m8s"). The
	// gateway's proxy_read_timeout fires first and returns 502 to the browser,
	// while the upload keeps running in the background — the user sees failure
	// for a request that eventually succeeds. A 30s cap turns that into a
	// clean error the caller can surface ("上传超时，请重试") instead of a 502.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := formUploader.Put(ctx, &ret, upToken, key, bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		return fmt.Errorf("qiniu upload failed: %w", err)
	}

	log.WithFields(log.Fields{
		"key":  key,
		"hash": ret.Hash,
	}).Debug("File uploaded to Qiniu")
	return nil
}

func (p *QiniuProvider) GetDownloadURL(key string) (string, error) {
	deadline := time.Now().Add(time.Hour).Unix()
	signedURL := storage.MakePrivateURL(p.mac, p.domain, key, deadline)
	// Force Content-Disposition: attachment on download (CR security note, PR #345):
	// uploads are type-unrestricted by design, so OSS-served .html/.svg files
	// would otherwise render INLINE in the browser when the download link is
	// clicked. The qiniu `attname` query param forces a file download instead.
	// This is a serving-side change only — it does NOT gate any upload.
	sep := "?"
	if strings.Contains(signedURL, "?") {
		sep = "&"
	}
	signedURL += sep + "attname=" + url.QueryEscape(path.Base(key))
	log.WithField("key", key).Debug("Generated Qiniu download URL")
	return signedURL, nil
}

// GetViewURL returns the INLINE variant (no attname) — for <img> rendering in the
// composer (pasted images render inline via ![name](/api/files/download?inline=1)).
// Content-Disposition: attachment would make browsers download instead of render.
func (p *QiniuProvider) GetViewURL(key string) (string, error) {
	deadline := time.Now().Add(time.Hour).Unix()
	signedURL := storage.MakePrivateURL(p.mac, p.domain, key, deadline)
	log.WithField("key", key).Debug("Generated Qiniu view URL")
	return signedURL, nil
}
func (p *QiniuProvider) Domain() string { return p.domain }

// ---------------------------------------------------------------------------
// NewOSSProvider — factory function
// ---------------------------------------------------------------------------

// NewOSSProvider creates the appropriate OSS provider based on config.
// provider must be "local", "qiniu", or "s3".
func NewOSSProvider(provider, uploadDir string, cfg ...QiniuConfig) (OSSProvider, error) {
	switch provider {
	case "local":
		return NewLocalProvider(uploadDir), nil
	case "qiniu":
		if len(cfg) == 0 {
			return nil, fmt.Errorf("qiniu config is required for qiniu provider")
		}
		c := cfg[0]
		return NewQiniuProvider(c.AccessKey, c.SecretKey, c.Bucket, c.Domain, c.Region)
	case "s3":
		// S3 provider is constructed directly in server.go via NewS3Provider(S3Config{...})
		return nil, fmt.Errorf("s3 provider must be constructed via NewS3Provider")
	default:
		return nil, fmt.Errorf("unknown OSS provider: %s", provider)
	}
}

// QiniuConfig holds Qiniu-specific configuration.
type QiniuConfig struct {
	AccessKey string
	SecretKey string
	Bucket    string
	Domain    string
	Region    string
}
