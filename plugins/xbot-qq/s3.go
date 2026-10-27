package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// s3Uploader 是 S3 兼容对象存储的上传实现（纯标准库，SigV4 签名手写）。
//
// 覆盖：AWS S3、阿里云 OSS、腾讯云 COS、MinIO、Cloudflare R2、自建网关 ——
// 它们都实现 S3 协议；endpoint 由 NewHoster 依 provider/region 推导或显式给出。
// 与 xbot 主模块的 web.NewS3Provider 保持同样的字段语义，但**不引入 SDK**
// （插件必须自包含，不能依赖主模块或第三方包）。
type s3Uploader struct {
	name       string // s3 / aliyun / cos（仅用于日志）
	ak, sk     string
	bucket     string
	region     string
	endpoint   string // 形如 https://oss-cn-hangzhou.aliyuncs.com
	publicBase string // 可选：公共访问前缀（默认同 endpoint）
	logf       func(string, ...any)
}

// Name 实现 Hoster。
func (u *s3Uploader) Name() string { return u.name }

// Upload 用 SigV4 签名 PUT 到 <endpoint>/<bucket>/<key>，返回公网 URL。
func (u *s3Uploader) Upload(ctx context.Context, data []byte, name string) (string, error) {
	key := fmt.Sprintf("qq/%s-%s", time.Now().UTC().Format("20060102-150405"), name)
	host := strings.TrimPrefix(strings.TrimPrefix(u.endpoint, "https://"), "http://")
	uri := "/" + u.bucket + "/" + key

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// 1) canonical request
	payloadHash := sha256Hex(data)
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		http.MethodPut, uri, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	// 2) string to sign
	scope := dateStamp + "/" + u.region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// 3) signing key + signature
	kDate := hmacSHA256([]byte("AWS4"+u.sk), dateStamp)
	kRegion := hmacSHA256(kDate, u.region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		u.ak, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.endpoint+uri, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Host", host)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", contentTypeByName(name))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("s3 PUT %s: HTTP %d %.200s", uri, resp.StatusCode, body)
	}

	base := u.publicBase
	if base == "" {
		base = u.endpoint
	}
	// 阿里云 OSS 的公开访问域名形如 bucket.endpoint（路径风格也可，但虚拟主机更常见）。
	if u.name == "aliyun" || u.name == "cos" {
		if i := strings.Index(base, "://"); i > 0 {
			base = base[:i+3] + u.bucket + "." + base[i+3:]
		}
	}
	return strings.TrimRight(base, "/") + "/" + key, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// contentTypeByName 依扩展名给 Content-Type（缺省用二进制流）。
func contentTypeByName(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".png"):
		return "image/png"
	case strings.HasSuffix(l, ".jpg"), strings.HasSuffix(l, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(l, ".gif"):
		return "image/gif"
	case strings.HasSuffix(l, ".webp"):
		return "image/webp"
	case strings.HasSuffix(l, ".pdf"):
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

var _ = sort.Strings // 保留 sort 依赖以便后续扩展查询串签名
