package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// qiniuUploader 把本地/远程图片传到七牛对象存储，返回公网 URL。
//
// 为什么需要它：QQ 的富媒体消息(msg_type=7)与 content **互斥**（官方：
// 「msg_type 决定哪个内容字段生效」；markdown 填写后 content 必须为空），
// 所以「图 + 文字在同一条消息里」只有一条路：
//
//	msg_type=2 的 Markdown + 一个平台能访问的**公网图片 URL**
//
// （接口里 force_verify_image_resource 参数即"图片转存"，说明 markdown 支持图片）。
// 而 QQ 的 /files 上传只返回 file_info（内部标识，不是 URL），本地图拿不到公网地址，
// 因此需要先把图落到我们自己的 CDN 上，再嵌进 markdown。
// defaultQiniuUploadHost 是七牛的全局上传入口。区域不对时服务端会在 400 里
// 明确给出正确域名，uploadRaw 会自动纠正并重试。
const defaultQiniuUploadHost = "upload.qiniup.com"

type qiniuUploader struct {
	ak, sk, bucket, domain string
	// host 是上传入口；空 = 用 defaultQiniuUploadHost。配置项 QINIU_UPLOAD_HOST
	// 可显式指定，400 incorrect region 时也会被自动纠正并记住。
	host string
	mu   sync.Mutex
	logf func(string, ...any)
}

// newQiniuFromEnvFile 从 k=v 文件读取七牛配置（默认复用 qiniu skill 的 secrets.env）。
// 任一项缺失就返回 nil，调用方自动退回"富媒体分条发送"。
func newQiniuFromEnvFile(path string, logf func(string, ...any)) *qiniuUploader {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
			kv[k] = v
		}
	}
	u := &qiniuUploader{
		ak:     kv["QINIU_AK"],
		sk:     kv["QINIU_SK"],
		bucket: kv["QINIU_BUCKET"],
		domain: strings.TrimRight(kv["QINIU_DOMAIN"], "/"),
		// 上传入口默认走全局域名；某些 bucket 落在特定区域（xbot-1 在 up-as0），
		// 全局域名会返回 400 incorrect region。允许配置覆盖，另外 400 时也会
		// 自动纠正（见 uploadRaw），两条路都能自愈。
		host: strings.TrimSpace(kv["QINIU_UPLOAD_HOST"]),
		logf: logf,
	}
	if u.ak == "" || u.sk == "" || u.bucket == "" || u.domain == "" {
		return nil
	}
	return u
}

// Name 实现 Hoster。
func (u *qiniuUploader) Name() string { return "qiniu" }

// upload 上传一份数据，返回公网 URL。
// Upload 实现 Hoster 接口。
func (u *qiniuUploader) Upload(ctx context.Context, data []byte, name string) (string, error) {
	return u.uploadRaw(ctx, data, name)
}

func (u *qiniuUploader) uploadRaw(ctx context.Context, data []byte, name string) (string, error) {
	key := fmt.Sprintf("qq/%s-%s", time.Now().UTC().Format("20060102-150405"), name)
	policy := map[string]any{
		"scope":      u.bucket + ":" + key,
		"deadline":   time.Now().Unix() + 3600,
		"insertOnly": 1,
	}
	pj, _ := json.Marshal(policy)
	encPolicy := base64.URLEncoding.EncodeToString(pj)
	mac := hmac.New(sha1.New, []byte(u.sk))
	mac.Write([]byte(encPolicy))
	token := u.ak + ":" + base64.URLEncoding.EncodeToString(mac.Sum(nil)) + ":" + encPolicy

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("token", token)
	_ = mw.WriteField("key", key)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	mw.Close()

	payload := buf.Bytes()
	contentType := mw.FormDataContentType()

	// 第一次用配置的 host（或全局默认）。
	host := u.uploadHostOf()
	status, body, err := u.postUpload(ctx, host, contentType, payload)
	if err != nil {
		return "", err
	}
	// 区域不对时七牛在 400 里直接告诉我们该用哪个域名：
	//   {"error":"incorrect region, please use up-as0.qiniup.com, bucket is: xbot-1"}
	// 官方 SDK 的标准做法就是解析它并重试一次，而不是让人手改配置。这条路径
	// 2026-10-27 实测踩过：硬编码 upload.qiniup.com 对 xbot-1 永久 400，
	// 图片一条都发不出去，还被静默降级成 media-per-message。
	if status/100 != 2 {
		if corrected := regionHostFromError(body); corrected != "" && corrected != host {
			u.logf("qiniu: region mismatch, retrying upload against %s", corrected)
			u.setUploadHost(corrected)
			if st2, b2, err2 := u.postUpload(ctx, corrected, contentType, payload); err2 == nil {
				status, body = st2, b2
			} else {
				u.logf("qiniu: retry against %s failed: %v", corrected, err2)
			}
		}
	}
	if status/100 != 2 {
		return "", fmt.Errorf("qiniu upload HTTP %d: %.200s", status, body)
	}

	// 私有空间：裸 URL 一律 401（even 带 query 也可能被 CDN 回源鉴权拦掉）。
	// 必须按七牛下载凭证算法现算签名 URL：
	//   to_sign = "<domain>/<key>?e=<deadline>"    ← **必须含域名**
	//   token   = urlsafe_base64(hmac_sha1(sk, to_sign))   ← **保留 = 填充**
	//   url     = "<to_sign>&token=<ak>:<token>"
	// 2026-10-27 踩坑：漏掉 to_sign 里的域名前缀，导致签名恒 401，且 CDN 报的是
	// "origin auth return status: 401"（看起来像 CDN 配置问题，极具误导性）。
	return u.signedURL(key, signedURLTTL), nil
}

// signedURLTTL 签名有效期。QQ 平台拿到 URL 后会在几秒内下载转存，
// 但同一条 markdown 可能在客户端被重新拉取，所以给足余量。
const signedURLTTL = 7 * 24 * time.Hour

// signedURL 生成七牛私有空间的下载凭证 URL。
func (u *qiniuUploader) signedURL(key string, ttl time.Duration) string {
	deadline := time.Now().Add(ttl).Unix()
	toSign := fmt.Sprintf("%s/%s?e=%d", u.domain, key, deadline)
	mac := hmac.New(sha1.New, []byte(u.sk))
	mac.Write([]byte(toSign))
	token := base64.URLEncoding.EncodeToString(mac.Sum(nil)) // 保留 = 填充
	return fmt.Sprintf("%s&token=%s:%s", toSign, u.ak, token)
}

// postUpload 发一次 multipart 上传，返回状态码与响应体。
func (u *qiniuUploader) postUpload(ctx context.Context, host, contentType string, payload []byte) (int, []byte, error) {
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, nil
}

// regionHostFromError 从七牛的 400 响应里取出「please use <host>」。
// 找不到返回 ""。
func regionHostFromError(body []byte) string {
	const marker = "please use "
	s := string(body)
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[i+len(marker):])
	// 有些响应带引号：please use "up-z1.qiniup.com", bucket is: ...
	// 先把前导引号剥掉，否则下面第一个字符就是引号，直接截成空串。
	// 响应体是原始 JSON 文本，引号在字节层是反斜杠+引号（\"），两者都要剥。
	rest = strings.TrimLeft(rest, "\\\"'")
	end := strings.IndexAny(rest, ",}\r\n \t\"'\\")
	if end < 0 {
		end = len(rest)
	}
	host := strings.TrimSpace(rest[:end])
	if !strings.Contains(host, ".") {
		return ""
	}
	return host
}

// uploadHostOf 返回当前生效的上传入口（配置优先，否则七牛全局域名）。
func (u *qiniuUploader) uploadHostOf() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.host != "" {
		return u.host
	}
	return defaultQiniuUploadHost
}

// setUploadHost 记录自动纠正后的入口，本次进程内后续上传直接复用。
func (u *qiniuUploader) setUploadHost(h string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.host = h
}

// uploadFile 读取本地路径或 http(s) 源，上传后返回公网 URL。
func (u *qiniuUploader) uploadFile(ctx context.Context, src string) (string, error) {
	data, name, err := fetchImageBytes(ctx, src, 64<<20)
	if err != nil {
		return "", err
	}
	base := filepath.Base(name)
	if base == "." || base == "/" || base == "" {
		base = "img.png"
	}
	return u.uploadRaw(ctx, data, base)
}
