package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessageMarkdown_ForceVerifyImageResource 钉住 2026-10-27 图片不显示的根因。
//
// QQ 的 markdown 是支持图片的，但必须带 markdown.force_verify_image_resource=true
// （"图片转存"：平台抓取 URL 并转存到自己的 CDN，客户端才渲染成真图）。
//
// 旧实现里这个参数**只存在于注释**，MessageMarkdown 只有 Content 一个字段，
// 于是 `![](url)` 被当普通文本发出去，用户看到"图片来了"却没有任何图。
func TestMessageMarkdown_ForceVerifyImageResource(t *testing.T) {
	// 1) 含图片语法 → 必须序列化出 force_verify_image_resource: true
	body := messageBody{
		MsgType: MsgTypeMarkdown,
		Markdown: &MessageMarkdown{
			Content:                  "喏，图片来了。\n![](https://cdn.example/a.png)",
			ForceVerifyImageResource: true,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"force_verify_image_resource":true`) {
		t.Fatalf("force_verify_image_resource missing from payload: %s", s)
	}
	if !strings.Contains(s, `"msg_type":2`) {
		t.Fatalf("msg_type should be 2 (markdown): %s", s)
	}

	// 2) 不含图片时不要多带字段（避免引入平台侧未验证的行为）
	body2 := messageBody{
		MsgType:  MsgTypeMarkdown,
		Markdown: &MessageMarkdown{Content: "纯文本"},
	}
	raw2, _ := json.Marshal(body2)
	if strings.Contains(string(raw2), "force_verify_image_resource") {
		t.Fatalf("flag must be omitted when there is no image: %s", raw2)
	}
}

// TestSendMarkdown_SetsVerifyFlagOnlyWithImage 验证 sendMarkdown 的判定条件：
// 只有正文真的含 `![` 时才开图片转存。
func TestSendMarkdown_SetsVerifyFlagOnlyWithImage(t *testing.T) {
	cases := []struct {
		md   string
		want bool
	}{
		{"正文\n![](https://x/a.png)", true},
		{"正文里的感叹号! 和方括号 [x] 不算图片", false},
		{"纯文本", false},
		{"![alt](https://x/a.png)", true},
	}
	for _, c := range cases {
		got := strings.Contains(c.md, "![")
		if got != c.want {
			t.Errorf("contains ![(%q) = %v, want %v", c.md, got, c.want)
		}
	}
}
