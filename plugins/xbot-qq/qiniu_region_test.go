package main

import "testing"

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
