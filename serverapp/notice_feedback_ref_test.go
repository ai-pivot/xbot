package serverapp

import (
	"strings"
	"testing"

	"xbot/storage/common"
)

// 用户要求（2026-11-15）：卡片被「取消 / 忽略 / 回答」时，回流给发起 agent 的
// 消息必须**带上卡片原内容作为引用**，否则模型只能凭记忆猜是哪张卡 → 幻觉。
func TestNoticeFeedbackEmbedsCardBody(t *testing.T) {
	card := &common.StructuredMessage{
		ID:      31748,
		Kind:    "ask_user",
		Content: "部署目标冲突：ssh b300-target 实际连到 18.61.208.121（跑 rb54），你指定的是 184.96。",
		StructuredJSON: `{"text":"部署目标冲突：ssh b300-target 实际连到 18.61.208.121（跑 rb54），你指定的是 184.96。",
			"payload":{"questions":[{"id":"deploy_host","question":"本次唯一允许修改和重启的目标是哪台？",
			"options":[{"id":"alias208","label":"18.61.208.121（别名当前目标、rb54）"},{"id":"explicit184","label":"18.61.184.96"}]}]}}`,
	}
	for _, status := range []string{"cancelled", "dismissed", "answered", "approved"} {
		got := userNoticeResolutionText(card, card.ID, card.Kind, status, "")
		if !strings.Contains(got, "卡片原文引用") {
			t.Fatalf("%s: feedback lost the card-reference block:\n%s", status, got)
		}
		if !strings.Contains(got, "部署目标冲突") {
			t.Fatalf("%s: feedback lost the card body (model would have to guess):\n%s", status, got)
		}
		if !strings.Contains(got, "本次唯一允许修改和重启的目标是哪台？") {
			t.Fatalf("%s: feedback lost the question:\n%s", status, got)
		}
		if !strings.Contains(got, "18.61.208.121（别名当前目标、rb54）") {
			t.Fatalf("%s: feedback lost the options:\n%s", status, got)
		}
		if !strings.Contains(got, "#31748") {
			t.Fatalf("%s: feedback lost the card id:\n%s", status, got)
		}
	}
}

// 卡片行缺失时不能崩，也不能编造内容：只保留 id/kind/status 这一行。
func TestNoticeFeedbackWithoutCardRowStaysMinimal(t *testing.T) {
	got := userNoticeResolutionText(nil, 42, "report", "cancelled", "")
	if !strings.Contains(got, "#42") || strings.Contains(got, "卡片原文引用") {
		t.Fatalf("nil card should yield the minimal line only, got: %s", got)
	}
}

// 超长卡片正文按 rune 截断，且必须标注截断（CJK 不能被切成半个字）。
func TestNoticeFeedbackTruncatesLongBodyByRune(t *testing.T) {
	body := strings.Repeat("测", 1200)
	got := userNoticeResolutionText(&common.StructuredMessage{ID: 1, Kind: "report", Content: body}, 1, "report", "dismissed", "")
	if !strings.Contains(got, "截断") {
		t.Fatalf("expected a truncation marker")
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("truncation broke a multi-byte rune")
	}
}
