package serverapp

import (
	"fmt"
	"strings"

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
//   - provider "aliyun"(阿里云 OSS) / "cos"(腾讯云 COS) / "qiniu" / "s3" → cloud
//     object storage; the bucket is the source of
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

	case "s3", "aliyun", "aliyun-oss", "cos", "tencent", "tencent-cos":
		// AWS S3 and the S3-compatible clouds share the same credential fields
		// (s3_*). Aliyun OSS / Tencent COS additionally **derive their endpoint
		// from the region** so the operator only fills region + bucket + keys:
		//   aliyun → https://oss-<region>.aliyuncs.com   (e.g. oss-cn-hangzhou)
		//   cos    → https://cos.<region>.myqcloud.com   (e.g. cos.ap-guangzhou)
		// An explicit s3_endpoint always wins (MinIO / R2 / self-hosted gateway).
		name := cfg.OSS.Provider
		switch name {
		case "aliyun-oss":
			name = "aliyun"
		case "tencent", "tencent-cos":
			name = "cos"
		}
		endpoint := strings.TrimRight(strings.TrimSpace(cfg.OSS.S3Endpoint), "/")
		if endpoint == "" {
			region := strings.TrimSpace(cfg.OSS.S3Region)
			switch name {
			case "aliyun":
				if region == "" {
					return nil, "", fmt.Errorf("aliyun OSS: set the region (e.g. cn-hangzhou) or an explicit endpoint override")
				}
				endpoint = "https://oss-" + region + ".aliyuncs.com"
			case "cos":
				if region == "" {
					return nil, "", fmt.Errorf("tencent COS: set the region (e.g. ap-guangzhou) or an explicit endpoint override")
				}
				endpoint = "https://cos." + region + ".myqcloud.com"
			}
		}
		p, err := web.NewS3Provider(web.S3Config{
			AccessKey:    cfg.OSS.S3AccessKey,
			SecretKey:    cfg.OSS.S3SecretKey,
			Bucket:       cfg.OSS.S3Bucket,
			Region:       cfg.OSS.S3Region,
			Endpoint:     endpoint,
			UsePathStyle: cfg.OSS.S3UsePathStyle,
			Domain:       cfg.OSS.S3Domain,
		})
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", name, err)
		}
		return p, name, nil

	default:
		return nil, "", fmt.Errorf("unknown storage provider %q (want local/qiniu/s3)", cfg.OSS.Provider)
	}
}
