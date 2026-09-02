package xbot

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/memory"
)

// recordingLLM 记录每次 Generate 调用的消息（用于断言 LLM 调用打到了哪个 client）。
// 只实现 llm.LLM（非 StreamingLLM、非 RetryLLM）→ generateLLM 走非流式 fallback 路径。
type recordingLLM struct {
	calls  [][]llm.ChatMessage
	models []string
}

func (r *recordingLLM) Generate(_ context.Context, model string, messages []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	r.calls = append(r.calls, messages)
	r.models = append(r.models, model)
	return &llm.LLMResponse{Content: "ok", FinishReason: llm.FinishReasonStop}, nil
}

func (r *recordingLLM) ListModels() []string { return nil }

// coreUpdateCalls 统计 updateCoreSummary 的调用次数（以 system prompt 为特征）。
func (r *recordingLLM) coreUpdateCalls() int {
	n := 0
	for _, msgs := range r.calls {
		if len(msgs) > 0 && strings.Contains(msgs[0].Content, "memory consolidation agent") {
			n++
		}
	}
	return n
}

// seedHighImportanceMemory 种一条 importance >= 0.7 的长期记忆 —— updateCoreSummary
// 只有在存在高重要性记忆时才会构建 prompt 并发起 LLM 调用（memSB 为空直接 return）。
func seedHighImportanceMemory(t *testing.T, m *XbotMemory) {
	t.Helper()
	if _, err := m.AddMemory(t.Context(), LongTermMemory{
		Type:       "fact",
		Content:    "user prefers dark theme",
		Keywords:   "theme preference",
		Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPostCompressUsesOwnLLMClient —— 生产事故回放（2026-09-02 chat_BD94FA4BB469）。
//
// XbotMemory 是单 operator 共享实例，m.llmClient 曾是跨会话共享可变字段：
// 压缩管道 PreCompress(A) 设置的 client 会被并发会话的 ConsolidateTurn(B) 覆盖 →
// PostCompress 的 updateCoreSummary 用 B 的模型/端点/max_output（事故现场：F64D 的
// ConsolidateTurn 用了 feishu 的 deepseek 配置，feishu 用了 F64D 的 glm-5.3 ——
// 两个会话的内存提取配置完美互换）。PostCompress 的 client 必须来自自己的 input
// （压缩管道传入），不受共享字段影响。
func TestPostCompressUsesOwnLLMClient(t *testing.T) {
	m, _ := newTestMemory(t)
	seedHighImportanceMemory(t, m)

	clientA := &recordingLLM{} // 压缩管道（本会话）的 client
	clientB := &recordingLLM{} // 并发会话的 client
	msgs := []llm.ChatMessage{llm.NewUserMessage("hello"), llm.NewAssistantMessage("hi")}

	// 1. 压缩管道：PreCompress（旧代码把 m.llmClient 设为 A）
	if _, err := m.PreCompress(t.Context(), memory.PreCompressInput{
		MessagesToCompress: msgs,
		SessionID:          "s-compress",
		LLMClient:          clientA,
		Model:              "model-a",
	}); err != nil {
		t.Fatal(err)
	}
	// 2. 并发会话的 ConsolidateTurn 覆盖共享 m.llmClient（= B）——
	//    单 operator 下所有会话共享同一个 XbotMemory 实例
	if _, err := m.ConsolidateTurn(t.Context(), memory.MemorizeInput{
		Messages:         msgs,
		LastConsolidated: 0,
		LLMClient:        clientB,
		Model:            "model-b",
	}); err != nil {
		t.Fatal(err)
	}
	// 3. 压缩管道收尾：PostCompress 用【input 里自己的 client A】——
	//    不能用被 ConsolidateTurn 覆盖后的 B（修复前 PostCompress 不接收 client，
	//    读共享 m.llmClient → 打到 B 上）
	if err := m.PostCompress(t.Context(), memory.PostCompressInput{
		CompactionSummary: "压缩摘要",
		SessionID:         "s-compress",
		LLMClient:         clientA,
		Model:             "model-a",
	}); err != nil {
		t.Fatal(err)
	}

	if got := clientB.coreUpdateCalls(); got != 0 {
		t.Errorf("BUG REPRODUCED: PostCompress 的 core summary 更新打到了并发会话的 client 上（%d 次）"+
			"——共享 m.llmClient 被 ConsolidateTurn 覆盖，2026-09-02 事故根因", got)
	}
	if got := clientA.coreUpdateCalls(); got == 0 {
		t.Error("BUG REPRODUCED: PostCompress 没有用压缩管道自己的 client（input.LLMClient）更新 core summary")
	}
}

// TestPostCompressNilClientSkipsLLMButSavesSummary —— PostCompress 未传 client 时
// 不得发起任何 LLM 调用（更不得用遗留的共享 client），但压缩摘要照常入库。
func TestPostCompressNilClientSkipsLLMButSavesSummary(t *testing.T) {
	m, db := newTestMemory(t)
	seedHighImportanceMemory(t, m)

	// 先用一个 client 跑一次 ConsolidateTurn（旧代码会把 m.llmClient 留在共享字段上）
	sentinel := &recordingLLM{}
	if _, err := m.ConsolidateTurn(t.Context(), memory.MemorizeInput{
		Messages:         []llm.ChatMessage{llm.NewUserMessage("hi")},
		LastConsolidated: 0,
		LLMClient:        sentinel,
		Model:            "sentinel",
	}); err != nil {
		t.Fatal(err)
	}

	// PostCompress 不带 client → 不得使用 sentinel 发起 LLM 调用
	if err := m.PostCompress(t.Context(), memory.PostCompressInput{
		CompactionSummary: "nil-client-summary",
		SessionID:         "s-nil",
	}); err != nil {
		t.Fatal(err)
	}
	if got := sentinel.coreUpdateCalls(); got != 0 {
		t.Errorf("BUG REPRODUCED: PostCompress 未传 client 却发起了 %d 次 LLM 调用（用了遗留共享 client）", got)
	}

	// 压缩摘要仍正常入库
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM xbot_short_term_memories WHERE summary = 'nil-client-summary'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CompactionSummary 应入库 1 行，got %d", n)
	}
}

// TestOperationsUseTheirOwnInputClient —— 每次 LLM 使用操作（ConsolidateTurn/
// Memorize/PreCompress）必须全部走【本次 input 的 client】，本次操作的 client
// 不得泄漏给下一次操作（旧实现的共享 m.llmClient 会让 A 会话的配置污染 B 会话
// 的内存提取）。顺序执行 A、B 两个会话的操作：各自的 LLM 调用各自走各自的
// client，总数精确对账。
func TestOperationsUseTheirOwnInputClient(t *testing.T) {
	m, _ := newTestMemory(t)
	seedHighImportanceMemory(t, m)

	clientA := &recordingLLM{}
	clientB := &recordingLLM{}
	msgs := []llm.ChatMessage{llm.NewUserMessage("session A message"), llm.NewAssistantMessage("reply")}

	// 会话 A 的 ConsolidateTurn（旧代码：m.llmClient = A）
	if _, err := m.ConsolidateTurn(t.Context(), memory.MemorizeInput{
		Messages:         msgs,
		LastConsolidated: 0,
		LLMClient:        clientA,
		Model:            "model-a",
	}); err != nil {
		t.Fatal(err)
	}

	// 会话 B 的 PreCompress 紧随其后（旧代码：m.llmClient = B —— A 的调用若在
	// 共享字段被覆盖后仍未发出就会用 B；2026-09-02 事故中 F64D/feishu 配置互换）
	if _, err := m.PreCompress(t.Context(), memory.PreCompressInput{
		MessagesToCompress: msgs,
		SessionID:          "s-b",
		LLMClient:          clientB,
		Model:              "model-b",
	}); err != nil {
		t.Fatal(err)
	}

	// 对账：A 的所有调用模型必须是 model-a，B 的必须是 model-b。
	// （ConsolidateTurn 的 extractAtomicMemories 记 1 次；PreCompress 的
	// extractAtomicMemories + generateSessionSummary 记 2 次。）
	for i, model := range clientA.models {
		if model != "model-a" {
			t.Errorf("clientA call #%d used model %q, want model-a（A 的操作用了别人的配置）", i, model)
		}
	}
	for i, model := range clientB.models {
		if model != "model-b" {
			t.Errorf("clientB call #%d used model %q, want model-b（B 的操作用了别人的配置）", i, model)
		}
	}
	if len(clientA.calls) == 0 {
		t.Error("clientA 应至少发起 1 次提取调用（ConsolidateTurn）")
	}
	if len(clientB.calls) < 2 {
		t.Errorf("clientB 应至少发起 2 次调用（extract + summary），got %d", len(clientB.calls))
	}
}
