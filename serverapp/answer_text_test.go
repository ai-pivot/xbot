package serverapp

// answer_text_test.go — 交互回执的**渲染判据**（取代已删除的 answerText）。
//
// 为什么删 answerText：它是「裸词」的产地 —— `cancelled → "已取消"`、空答案 →
// "已回答"、单问题连问题正文都不带。2026-11-25 用户实测：发起 agent 收到一条
// 内容为「已取消」的消息，完全不知道是哪张卡、问的是什么。
//
// 现在唯一实现是 userNoticeResolutionText（+ answerSelections）：
//   - option 一律翻译成 label（老数据没有 spec 才退回 id，不能变空）；
//   - 问题正文必须出现（单问题也要，否则「能看到，选项有文字」这种句子读不出在答什么）；
//   - 取消只说「用户取消」，不附加行为指导。

import (
	"strings"
	"testing"

	"xbot/protocol"
	"xbot/storage/common"
)

func TestAnswerSelectionsUsesOptionLabels(t *testing.T) {
	specs := []askQuestionSpec{
		{
			ID:       "smoke",
			Question: "选项文字能看到吗？",
			Options: map[string]string{
				"yes":     "能看到，选项有文字",
				"no":      "看不到，还是空白",
				"partial": "部分空白 / 只有复选框",
			},
		},
	}
	got := answerSelections([]protocol.StructuredAnswer{
		{QuestionID: "smoke", OptionIDs: []string{"yes"}},
	}, specs)
	if !strings.Contains(got, "能看到，选项有文字") {
		t.Fatalf("answerSelections = %q, 必须用 label，不能落到内部 id", got)
	}
	if !strings.Contains(got, "选项文字能看到吗") {
		t.Fatalf("answerSelections = %q, 必须带问题正文（单问题也要）", got)
	}
	if strings.Contains(got, "yes") {
		t.Fatalf("answerSelections = %q, 不得泄露内部 option id", got)
	}
}

func TestAnswerSelectionsMultiSelectAndCustomText(t *testing.T) {
	specs := []askQuestionSpec{
		{ID: "q1", Question: "选哪些？", Options: map[string]string{"a": "选项 A", "b": "选项 B"}},
	}
	got := answerSelections([]protocol.StructuredAnswer{
		{QuestionID: "q1", OptionIDs: []string{"a", "b"}, Text: "补充说明"},
	}, specs)
	if !strings.Contains(got, "选项 A、选项 B") {
		t.Fatalf("answerSelections = %q, want both labels joined", got)
	}
	if !strings.Contains(got, "补充说明") {
		t.Fatalf("answerSelections = %q, want the custom text", got)
	}
}

// 老数据兼容：specs 缺失（或 option 没有 label 映射）时退回 id，不能变空。
func TestAnswerSelectionsFallsBackToIDsWhenSpecsMissing(t *testing.T) {
	got := answerSelections([]protocol.StructuredAnswer{
		{QuestionID: "smoke", OptionIDs: []string{"yes"}},
	}, nil)
	if got != "yes" {
		t.Fatalf("answerSelections without specs = %q, want %q", got, "yes")
	}
}

// 「取消」回执：必须带卡片上下文，且不得变成裸词。
func TestResolutionTextCancelledIsNeverBareWord(t *testing.T) {
	card := &common.StructuredMessage{
		ID:      7,
		Kind:    "ask_user",
		Content: "需要你拍板目标主机",
		StructuredJSON: `{"payload":{"questions":[{` +
			`"id":"q1","question":"目标主机选哪台？","options":[{"id":"a","label":"1101"}]}]}}`,
	}
	got := userNoticeResolutionText(card, card.ID, card.Kind, "cancelled", "", nil, nil)
	if strings.TrimSpace(got) == "已取消" || strings.TrimSpace(got) == "取消" {
		t.Fatalf("cancel receipt is a bare word: %q", got)
	}
	for _, want := range []string{"卡片 #7", "需要你拍板目标主机", "目标主机选哪台"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cancel receipt = %q, missing %q", got, want)
		}
	}
	// 用户口径：取消只说「用户取消」，不加行为指导。
	for _, banned := range []string{"不要重复", "请继续", "按现有信息"} {
		if strings.Contains(got, banned) {
			t.Fatalf("cancel receipt = %q must not carry extra instructions (%q)", got, banned)
		}
	}
}
