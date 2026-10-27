package main

import (
	"context"
	"fmt"
	"strings"
)

// Hoster 是可插拔的图片托管抽象（纯插件实现，不依赖主模块）。
//
// 为什么需要：QQ 的富媒体消息(msg_type=7)与正文字段互斥（官方：「msg_type 决定
// 哪个内容字段生效」；markdown 填写后 content 必须为空），因此「图 + 文同一条」
// 只能用 msg_type=2 的 Markdown，而 markdown 里的图片必须是一个**平台能访问的
// 公网 URL**（接口参数 force_verify_image_resource 即"图片转存"）。
// 所以需要一个"把本地图变成公网 URL"的能力。
//
// 设计对齐 xbot 的 Storage Provider（serverapp/storage_provider.go）：
//
//	provider = "" | local | qiniu | s3 | aliyun | cos
//	qiniu  → qiniu_ak / qiniu_sk / qiniu_bucket / qiniu_domain [/ qiniu_region]
//	s3 系  → s3_ak / s3_sk / s3_bucket / s3_region [/ s3_endpoint]
//	         aliyun 由 region 推 https://oss-<region>.aliyuncs.com
//	         cos    由 region 推 https://cos.<region>.myqcloud.com
//	         显式 s3_endpoint 总是优先（MinIO / R2 / 自建网关）
//
// 未配置或 local → NewHoster 返回 nil，调用方退回"图片单独一条 + 文字单独一条"。
type Hoster interface {
	// Name 返回 provider 名（用于日志）。
	Name() string
	// Upload 上传一份数据并返回可公开访问的 URL。
	Upload(ctx context.Context, data []byte, name string) (string, error)
}

// hosterConfig 是插件的 CDN/图床配置（全部来自 channels.qq.* 配置项）。
type hosterConfig struct {
	Provider string

	QiniuAK     string
	QiniuSK     string
	QiniuBucket string
	QiniuDomain string
	QiniuRegion string

	S3AK       string
	S3SK       string
	S3Bucket   string
	S3Region   string
	S3Endpoint string

	// 公共 URL 前缀覆盖（如 CDN 自定义域名）。
	PublicBase string
}

// NewHoster 按 provider 构造 Hoster；返回 nil 表示"不做托管"。
func NewHoster(cfg hosterConfig, logf func(string, ...any)) (Hoster, error) {
	p := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch p {
	case "", "local", "none", "off":
		return nil, nil

	case "qiniu":
		if cfg.QiniuAK == "" || cfg.QiniuSK == "" || cfg.QiniuBucket == "" || cfg.QiniuDomain == "" {
			return nil, fmt.Errorf("qiniu: qiniu_ak/qiniu_sk/qiniu_bucket/qiniu_domain 都必填")
		}
		return &qiniuUploader{
			ak: cfg.QiniuAK, sk: cfg.QiniuSK, bucket: cfg.QiniuBucket,
			domain: strings.TrimRight(cfg.QiniuDomain, "/"), logf: logf,
		}, nil

	case "s3", "aliyun", "aliyun-oss", "cos", "tencent", "tencent-cos":
		name := p
		switch name {
		case "aliyun-oss":
			name = "aliyun"
		case "tencent", "tencent-cos":
			name = "cos"
		}
		endpoint := strings.TrimRight(strings.TrimSpace(cfg.S3Endpoint), "/")
		if endpoint == "" {
			region := strings.TrimSpace(cfg.S3Region)
			switch name {
			case "aliyun":
				if region == "" {
					return nil, fmt.Errorf("aliyun: 需要 s3_region（如 cn-hangzhou）或显式 s3_endpoint")
				}
				endpoint = "https://oss-" + region + ".aliyuncs.com"
			case "cos":
				if region == "" {
					return nil, fmt.Errorf("cos: 需要 s3_region（如 ap-guangzhou）或显式 s3_endpoint")
				}
				endpoint = "https://cos." + region + ".myqcloud.com"
			default:
				if region == "" {
					return nil, fmt.Errorf("s3: 需要 s3_region 或显式 s3_endpoint")
				}
				endpoint = "https://s3." + region + ".amazonaws.com"
			}
		}
		if cfg.S3AK == "" || cfg.S3SK == "" || cfg.S3Bucket == "" {
			return nil, fmt.Errorf("%s: s3_ak/s3_sk/s3_bucket 必填", name)
		}
		return &s3Uploader{
			name: name, ak: cfg.S3AK, sk: cfg.S3SK, bucket: cfg.S3Bucket,
			region: cfg.S3Region, endpoint: endpoint,
			publicBase: strings.TrimRight(cfg.PublicBase, "/"), logf: logf,
		}, nil

	default:
		return nil, fmt.Errorf("unknown cdn provider %q (支持 qiniu / s3 / aliyun / cos / local)", cfg.Provider)
	}
}

// State 返回 provider 的规范化值（排障日志用）。
func (c hosterConfig) State() string { return strings.ToLower(strings.TrimSpace(c.Provider)) }

// countNonEmpty 统计非空字符串个数（排障日志用）。
func countNonEmpty(vals ...string) int {
	n := 0
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	return n
}
