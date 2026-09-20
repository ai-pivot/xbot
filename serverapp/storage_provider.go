package serverapp

import (
	"fmt"

	"xbot/channel/web"
	"xbot/config"
)

// buildStorageProvider constructs the OSS provider for a config. It is the
// SINGLE construction path — used both at startup (server.go) and when the Web
// settings panel changes the storage backend at runtime (storage_config.go), so
// the two can never drift.
//
//   - provider ""          → local static (the zero-config default)
//   - provider "local"     → local static (explicit)
//   - provider "qiniu"/"s3"→ cloud object storage; the bucket is the source of
//     truth and a local spill copy keeps a real path for the model.
//
// Returns the provider plus the canonical provider name ("local"/"qiniu"/"s3").
// An unknown provider or a broken cloud config returns an error — callers decide
// whether to fall back to local (startup does; the settings RPC reports it).
func buildStorageProvider(cfg *config.Config) (web.OSSProvider, string, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	local := func() (web.OSSProvider, string, error) {
		p, err := web.NewOSSProvider("local", web.LocalUploadRoot(config.XbotHome()))
		if err != nil {
			return nil, "", err
		}
		return p, "local", nil
	}

	switch cfg.OSS.Provider {
	case "", "local":
		return local()

	case "qiniu":
		p, err := web.NewOSSProvider(
			"qiniu",
			"",
			web.QiniuConfig{
				AccessKey: cfg.OSS.QiniuAccessKey,
				SecretKey: cfg.OSS.QiniuSecretKey,
				Bucket:    cfg.OSS.QiniuBucket,
				Domain:    cfg.OSS.QiniuDomain,
				Region:    cfg.OSS.QiniuRegion,
			},
		)
		if err != nil {
			return nil, "", fmt.Errorf("qiniu: %w", err)
		}
		return p, "qiniu", nil

	case "s3":
		p, err := web.NewS3Provider(web.S3Config{
			AccessKey:    cfg.OSS.S3AccessKey,
			SecretKey:    cfg.OSS.S3SecretKey,
			Bucket:       cfg.OSS.S3Bucket,
			Region:       cfg.OSS.S3Region,
			Endpoint:     cfg.OSS.S3Endpoint,
			UsePathStyle: cfg.OSS.S3UsePathStyle,
			Domain:       cfg.OSS.S3Domain,
		})
		if err != nil {
			return nil, "", fmt.Errorf("s3: %w", err)
		}
		return p, "s3", nil

	default:
		return nil, "", fmt.Errorf("unknown storage provider %q (want local/qiniu/s3)", cfg.OSS.Provider)
	}
}
