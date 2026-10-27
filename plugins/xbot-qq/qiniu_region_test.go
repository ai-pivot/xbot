package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestRegionHostFromError 覆盖 2026-10-27 图片发不出去的真实响应体。
//
// 生产实测：
//
//	qiniu upload HTTP 400: {"error":"incorrect region, please use
//	up-as0.qiniup.com, bucket is: xbot-1"}
//
// 代码原来硬编码 https://upload.qiniup.com，对这个 bucket 永久 400，
// 图片一条都发不出去，还被静默降级成 media-per-message。
func TestRegionHostFromError(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{
			name: "真实生产响应",
			body: `{"error":"incorrect region, please use up-as0.qiniup.com, bucket is: xbot-1"}`,
			want: "up-as0.qiniup.com",
		},
		{
			name: "带引号的变体",
			body: `{"error":"incorrect region, please use \"up-z1.qiniup.com\", bucket is: b"}`,
			want: "up-z1.qiniup.com",
		},
		{
			name: "没有该提示",
			body: `{"error":"invalid token"}`,
			want: "",
		},
		{
			name: "空响应",
			body: ``,
			want: "",
		},
		{
			name: "提示后没有域名（防误取）",
			body: `{"error":"please use something"}`,
			want: "",
		},
	}
	for _, c := range cases {
		if got := regionHostFromError([]byte(c.body)); got != c.want {
			t.Errorf("%s: regionHostFromError = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestUploadHostOf_ConfigAndCorrection 验证入口优先级与自动纠正的落地。
func TestUploadHostOf_ConfigAndCorrection(t *testing.T) {
	var logs []string
	u := &qiniuUploader{ak: "a", sk: "s", bucket: "b", domain: "d",
		logf: func(f string, a ...any) { logs = append(logs, f) }}

	// 未配置 → 全局默认。
	if got := u.uploadHostOf(); got != defaultQiniuUploadHost {
		t.Errorf("unconfigured host = %q, want %q", got, defaultQiniuUploadHost)
	}

	// 自动纠正后要记住，后续上传不再走错区域。
	u.setUploadHost("up-as0.qiniup.com")
	if got := u.uploadHostOf(); got != "up-as0.qiniup.com" {
		t.Errorf("after correction host = %q", got)
	}

	// 显式配置优先。
	u2 := &qiniuUploader{ak: "a", sk: "s", bucket: "b", domain: "d",
		host: "up-z0.qiniup.com", logf: func(string, ...any) {}}
	if got := u2.uploadHostOf(); got != "up-z0.qiniup.com" {
		t.Errorf("configured host = %q", got)
	}
}

// TestSignedURL_MatchesRepoImplementation 钉住七牛私有空间签名算法。
//
// 2026-10-27 踩坑：to_sign 漏掉域名前缀（写成 "/<key>?e=..."），签名恒 401，
// 而且 CDN 报的是 "origin auth return status: 401"，看起来像 CDN 配置问题，
// 极具误导性。正确做法照仓库 storage provider / qiniu skill 的实现：
//
//	to_sign = "<domain>/<key>?e=<deadline>"   ← 含域名
//	token   = urlsafe_base64(hmac_sha1(sk, to_sign))  ← 保留 = 填充
func TestSignedURL_MatchesRepoImplementation(t *testing.T) {
	u := &qiniuUploader{ak: "AK", sk: "SK", bucket: "b", domain: "https://cdn.example",
		logf: func(string, ...any) {}}
	got := u.signedURL("qq/a.png", 7*24*time.Hour)

	if !strings.HasPrefix(got, "https://cdn.example/qq/a.png?e=") {
		t.Fatalf("signed url must start with domain/key?e= : %s", got)
	}
	if !strings.Contains(got, "&token=AK:") {
		t.Fatalf("signed url must carry &token=AK:<sign> : %s", got)
	}
	// 签名必须保留 base64 的 '=' 填充（七牛要求；去掉会 401 BadToken）
	tok := got[strings.Index(got, "&token=AK:")+len("&token=AK:"):]
	if !strings.HasSuffix(tok, "=") {
		t.Fatalf("token lost its base64 padding: %q", tok)
	}

	// 关键回归：待签名串必须含域名。手工复算一次并比对。
	deadline := strings.Split(strings.Split(got, "?e=")[1], "&")[0]
	toSign := fmt.Sprintf("%s/qq/a.png?e=%s", u.domain, deadline)
	mac := hmac.New(sha1.New, []byte(u.sk))
	mac.Write([]byte(toSign))
	want := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	if tok != want {
		t.Fatalf("token = %q, want %q (computed over %q)", tok, want, toSign)
	}
}
