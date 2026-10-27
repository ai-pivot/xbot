package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestQiniuUploadLive 用真实凭据上传一张小图，验证 2026-10-27 的区域故障
// 已经被自动纠正逻辑修好。
//
// 生产现象：cdn upload failed ... incorrect region, please use up-as0.qiniup.com,
// bucket is: xbot-1 → 图片一条都发不出去，还被静默降级成 media-per-message。
//
// 没有凭据时跳过（CI / 别人机器上不该失败）。
func TestQiniuUploadLive(t *testing.T) {
	secrets := os.Getenv("QINIU_SECRETS")
	if secrets == "" {
		secrets = "/home/smith/.xbot/skills/qiniu/secrets.env"
	}
	if _, err := os.Stat(secrets); err != nil {
		t.Skipf("no qiniu credentials at %s", secrets)
	}

	var logs []string
	u := newQiniuFromEnvFile(secrets, func(f string, a ...any) {
		logs = append(logs, f)
		t.Logf("plugin log: "+f, a...)
	})
	if u == nil {
		t.Skip("qiniu credentials incomplete")
	}

	// 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url, err := u.Upload(ctx, png, "region-fix-probe.png")
	if err != nil {
		t.Fatalf("upload failed — the region auto-correction did not work: %v\nlogs: %v", err, logs)
	}
	if url == "" {
		t.Fatal("upload returned an empty URL")
	}
	t.Logf("uploaded: %s", url)

	// 上传入口应已被自动纠正（不再是全局域名）。
	if h := u.uploadHostOf(); h == defaultQiniuUploadHost {
		t.Logf("note: still using the global upload host %q (worked anyway)", h)
	} else {
		t.Logf("upload host auto-corrected to %q", h)
	}
}

// TestQiniuUpload_ReturnsSignedURL 验证插件 Upload 返回的是**签名 URL**，
// 而不是私有空间里任何人（含 QQ 平台）都取不到的裸 URL。
//
// 2026-10-27：裸 URL 一律 401，QQ 平台抓图失败 → 850027 富媒体文件上传超时
// → 用户侧表现为"图根本没发出来"。
func TestQiniuUpload_ReturnsSignedURL(t *testing.T) {
	secrets := "/home/smith/.xbot/skills/qiniu/secrets.env"
	if _, err := os.Stat(secrets); err != nil {
		t.Skip("no qiniu credentials")
	}
	u := newQiniuFromEnvFile(secrets, func(f string, a ...any) { t.Logf(f, a...) })
	if u == nil {
		t.Skip("credentials incomplete")
	}
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url, err := u.Upload(ctx, png, "signed-url-probe.png")
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	t.Logf("uploaded: %s", url)
	if !strings.Contains(url, "&token=") {
		t.Fatalf("Upload returned an UNSIGNED url — private bucket will 401: %s", url)
	}
	// 真机拉一次，确认 200
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("fetch signed url: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	t.Logf("fetch: HTTP %d len=%d", resp.StatusCode, len(body))
	if resp.StatusCode != 200 {
		t.Fatalf("signed url returned HTTP %d — 签名仍然无效", resp.StatusCode)
	}
}
