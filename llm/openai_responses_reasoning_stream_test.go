package llm

// Responses API 流式 reasoning 拼接回归测试。
//
// 背景：Responses API 的 reasoning item 会同时/先后给出两种文本流——
//   - `response.reasoning_summary_text.delta`：概括（占位），先成型；
//   - `response.reasoning_text.delta`：权威全文，后到。
// 旧实现把两路都直接映射成 EventReasoningContent 往同一个 builder 里追加，
// 于是「摘要先到、全文后到」的正常顺序会拼成 `"[摘要占位]真正全文"`；
// 多工具轮次里多个 item 的占位摘要还会无分隔粘成 `"思考中思考中"`，
// 最后被 agent/engine_run.go 当作正文写进 iteration_history。
//
// 修复不变量（全部落在 llm/openai_responses.go，共用的 stream.go 一行不动）：
//   1. summary 只是占位，不实时发；text 才是权威，实时发且只在 text 缺失时被 summary 兜底。
//   2. 兜底补发按 item 边界（新 item 首次出现 / response.completed / 流结束）触发；
//      分隔符挂在 **item 边界** 上（该 item 的第一段内容前补 "\n\n"），与文本种类
//      （summary/text）无关，同一 item 内的多段 delta 原样拼接，绝不粘连。
//   3. `*.done` 事件带完整文本：delta 正常时只用来校验长度，delta 缺失时才补发。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newResponsesSSEServer 起一个只吐 SSE 的假网关：每个入参是一条完整事件的 JSON body。
func newResponsesSSEServer(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for _, ev := range events {
			if _, err := io.WriteString(w, "data: "+ev+"\n\n"); err != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
		}
	}))
}

// collectResponsesStream 用 Responses API 跑一次完整流式请求并聚合结果。
func collectResponsesStream(t *testing.T, srvURL string) *LLMResponse {
	t.Helper()
	c := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srvURL,
		APIKey:       "k",
		DefaultModel: "m",
		APIType:      APITypeResponses,
	})
	ch, err := c.GenerateStream(context.Background(), "m", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "think")
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	return resp
}

const responsesCompletedEvent = `{"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7},"output":[]}}`

// 判据 a：同一个 item 内「摘要先到、全文后到」时，摘要绝不能留在结果里。
// 这是服务端正常顺序（摘要先成型），旧实现只防得住「全文先、摘要在后」，故 RED。
func TestResponsesStream_SummaryBeforeText_TextIsAuthoritative(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"sequence_number":1,"delta":"[摘要占位]"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"sequence_number":2,"delta":"真正全文"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	resp := collectResponsesStream(t, srv.URL)
	if resp.ReasoningContent != "真正全文" {
		t.Errorf("ReasoningContent = %q, want %q（summary 是占位，必须被同 item 的 text 取代，不能拼接）",
			resp.ReasoningContent, "真正全文")
	}
}

// 判据 b：多工具轮次里两个 reasoning item 各带占位摘要，不能无分隔粘成一串。
func TestResponsesStream_MultiItemSummaryOnly_ItemsNotConcatenated(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_a","output_index":0,"summary_index":0,"sequence_number":1,"delta":"思考中"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"c1","name":"Shell","arguments":""}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_b","output_index":2,"summary_index":0,"sequence_number":2,"delta":"思考中"}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"id":"fc_2","type":"function_call","call_id":"c2","name":"Shell","arguments":""}}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	resp := collectResponsesStream(t, srv.URL)
	if strings.Contains(resp.ReasoningContent, "思考中思考中") {
		t.Fatalf("ReasoningContent = %q：两个 item 的摘要被无分隔拼接", resp.ReasoningContent)
	}
	if n := strings.Count(resp.ReasoningContent, "思考中"); n != 2 {
		t.Errorf("ReasoningContent = %q：应保留两个 item 各自的摘要（want 2 occurrences, got %d）",
			resp.ReasoningContent, n)
	}
	if want := "思考中\n\n思考中"; resp.ReasoningContent != want {
		t.Errorf("ReasoningContent = %q, want %q（item 之间必须补分隔符）", resp.ReasoningContent, want)
	}
}

// 判据 c：只给 summary、全程没有 reasoning_text 的模型不能丢内容——
// 兜底补发是唯一出路，删掉兜底这条就 RED（判据 g 的变异靶子）。
func TestResponsesStream_SummaryOnlySingleItem_Preserved(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_only","output_index":0,"summary_index":0,"sequence_number":1,"delta":"只有摘要"}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_only","output_index":0,"summary_index":0,"sequence_number":2,"delta":"没有全文"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	resp := collectResponsesStream(t, srv.URL)
	if got := strings.TrimSpace(resp.ReasoningContent); got != "只有摘要没有全文" {
		t.Errorf("ReasoningContent = %q, want %q（summary-only 模型的摘要必须兜底补发，不能丢）",
			resp.ReasoningContent, "只有摘要没有全文")
	}
}

// `reasoning_text.done` 的 Text 是完整最终文本：delta 一个都没来时用它补发。
func TestResponsesStream_ReasoningTextDoneWithoutDelta_EmitsFullText(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_text.done","item_id":"rs_done","output_index":0,"content_index":0,"sequence_number":1,"text":"全文由done给出"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	resp := collectResponsesStream(t, srv.URL)
	if resp.ReasoningContent != "全文由done给出" {
		t.Errorf("ReasoningContent = %q, want %q（delta 缺失时必须用 done 的完整文本补发）",
			resp.ReasoningContent, "全文由done给出")
	}
}

// delta 已经发过时，`reasoning_text.done` 只做长度校验，不能重复发（否则全文翻倍）。
func TestResponsesStream_ReasoningTextDoneAfterDelta_NotDuplicated(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_text.delta","item_id":"rs_dup","output_index":0,"content_index":0,"sequence_number":1,"delta":"abc"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_dup","output_index":0,"content_index":0,"sequence_number":2,"text":"abcdef"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	resp := collectResponsesStream(t, srv.URL)
	if resp.ReasoningContent != "abc" {
		t.Errorf("ReasoningContent = %q, want %q（done 在 delta 之后只校验长度，不得重复发）",
			resp.ReasoningContent, "abc")
	}
}

// item 之间的分隔符必须挂在 item 边界上，而不是只挂在「兜底补发的 summary」上：
// 一个 summary-only item 后面跟一个带全文的 item，同样要分隔，不能粘成 "摘要A全文B"。
// （2026-10-29 09:53 外部探针 llm/mixed_items_probe_test.go 在 02e02e89 上暴露的缺口。）
func TestResponsesStream_SummaryOnlyItemThenTextItem_Separated(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_a","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_a","output_index":0,"summary_index":0,"sequence_number":1,"delta":"摘要A"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"rs_b","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_b","output_index":1,"content_index":0,"sequence_number":2,"delta":"全文B"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	if got, want := collectResponsesStream(t, srv.URL).ReasoningContent, "摘要A\n\n全文B"; got != want {
		t.Errorf("ReasoningContent = %q, want %q（summary-only item 与后续 text item 之间必须分隔）", got, want)
	}
}

// 两个都带全文的 item 之间同样要分隔（分隔语义是「item 边界」，与文本种类无关）。
func TestResponsesStream_TwoTextItems_Separated(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_text.delta","item_id":"rs_a","output_index":0,"content_index":0,"sequence_number":1,"delta":"全文A"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_b","output_index":1,"content_index":0,"sequence_number":2,"delta":"全文B"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	if got, want := collectResponsesStream(t, srv.URL).ReasoningContent, "全文A\n\n全文B"; got != want {
		t.Errorf("ReasoningContent = %q, want %q（两个 item 的全文之间必须分隔）", got, want)
	}
}

// 判据 h2：分隔符是 per-item 而不是 per-delta —— 同一个 item 内部的多段 delta
// 必须原样拼接（"全文续写"），绝不能变成 "全文\n\n续写"。
// 专防把「该 item 第一段」误写成「每一段 delta」。
func TestResponsesStream_SingleItemMultiDelta_NoSeparatorInsideItem(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.reasoning_text.delta","item_id":"rs_one","output_index":0,"content_index":0,"sequence_number":1,"delta":"全文"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_one","output_index":0,"content_index":0,"sequence_number":2,"delta":"续写"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()

	if got, want := collectResponsesStream(t, srv.URL).ReasoningContent, "全文续写"; got != want {
		t.Errorf("ReasoningContent = %q, want %q（同一 item 内的多段 delta 不得插入分隔符）", got, want)
	}
}
