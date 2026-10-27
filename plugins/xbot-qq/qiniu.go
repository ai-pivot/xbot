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
type qiniuUploader struct {
	ak, sk, bucket, domain string
	logf                   func(string, ...any)
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
		logf:   logf,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upload.qiniup.com", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("qiniu upload HTTP %d: %.200s", resp.StatusCode, body)
	}
	return u.domain + "/" + key, nil
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
