package serverapp

// answer_text_test.go — 回答落库文本必须是**人话**。
//
// 背景（2026-11-11 用户实测反馈）：旧实现把答案拼成 `questionId:optionId`，
// 用户在会话历史里看到的是 `smoke:yes` 这种内部代号，完全读不懂。
// 现在用 askQuestionSpec.Options（optionId → label）翻译成用户点击时看到的文字。

import (
	"testing"

	"xbot/protocol"
)

func TestAnswerTextUsesOptionLabels(t *testing.T) {
	specs := []askQuestionSpec{
		{
			ID:       "smoke",
			Question: "这张卡的选项能看到文字吗？",
			Options: map[string]string{
				"yes":     "能看到，选项有文字",
				"no":      "看不到，还是空白",
				"partial": "部分空白 / 只有复选框",
			},
		},
	}

	got := answerText(protocol.StructuredAnswerInput{
		Answers: []protocol.StructuredAnswer{
			{QuestionID: "smoke", OptionIDs: []string{"yes"}},
		},
	}, specs)
	want := "能看到，选项有文字"
	if got != want {
		t.Fatalf("answerText = %q, want %q (必须用 label，不能落到内部 id)", got, want)
	}
}

func TestAnswerTextMultiSelectAndCustomText(t *testing.T) {
	specs := []askQuestionSpec{
		{
			ID:       "q1",
			Question: "选哪些？",
			Options:  map[string]string{"a": "选项 A", "b": "选项 B"},
		},
	}
	// 多选 + 自定义文本：label 之间用「、」连，自定义文本前留全角空格
	got := answerText(protocol.StructuredAnswerInput{
		Answers: []protocol.StructuredAnswer{
			{QuestionID: "q1", OptionIDs: []string{"a", "b"}, Text: "补充说明"},
		},
	}, specs)
	want := "选项 A、选项 B　补充说明"
	if got != want {
		t.Fatalf("answerText = %q, want %q", got, want)
	}
}

// 老数据兼容：specs 缺失（或 option 没有 label 映射）时退回 id，不能变空。
func TestAnswerTextFallsBackToIDsWhenSpecsMissing(t *testing.T) {
	got := answerText(protocol.StructuredAnswerInput{
		Answers: []protocol.StructuredAnswer{
			{QuestionID: "smoke", OptionIDs: []string{"yes"}},
		},
	}, nil)
	if got != "yes" {
		t.Fatalf("answerText without specs = %q, want %q", got, "yes")
	}
}

func TestAnswerTextCancelled(t *testing.T) {
	if got := answerText(protocol.StructuredAnswerInput{Cancelled: true}, nil); got != "已取消" {
		t.Fatalf("cancelled answerText = %q, want 已取消", got)
	}
}
