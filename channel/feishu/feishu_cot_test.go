package feishu

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	"xbot/protocol"
)

// feishu_cot_test.go — 飞书原生 CoT 的传输/渲染契约（对齐 dsh-lark）。
//
// 用可替换的 request 注入 fake，断言**请求形状**（端点/方法/字段）与 AG-UI 事件
// 序列 —— 不需要真实飞书租户。
type cotCall struct {
	Method string
	Path   string
	Body   map[string]any
	Events []map[string]any
}

func newFakeCoT(t *testing.T, chatID string) (*feishuCoT, *[]cotCall) {
	t.Helper()
	calls := &[]cotCall{}
	c := newFeishuCoT(nil, chatID, "om_parent", false)
	c.request = func(_ context.Context, method, path string, body any) (*larkcore.ApiResp, error) {
		raw, _ := json.Marshal(body)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		call := cotCall{Method: method, Path: path, Body: m}
		if evs, ok := m["events"].([]any); ok {
			for _, e := range evs {
				if em, ok := e.(map[string]any); ok {
					call.Events = append(call.Events, em)
				}
			}
		}
		*calls = append(*calls, call)
		if method == "POST" {
			return &larkcore.ApiResp{RawBody: []byte(`{"code":0,"data":{"cot_id":"cot-1","message_id":"om-cot-1"}}`)}, nil
		}
		return &larkcore.ApiResp{RawBody: []byte(`{"code":0}`)}, nil
	}
	return c, calls
}

/** 事件类型序列。 */
func eventTypes(t *testing.T, calls *[]cotCall) []string {
	t.Helper()
	var out []string
	for _, c := range *calls {
		for _, e := range c.Events {
			out = append(out, e["event_type"].(string))
		}
	}
	return out
}

/** 断言时间戳严格递增（飞书按它排序；重复/回退会乱序）。 */
func assertTimestampsIncreasing(t *testing.T, calls *[]cotCall) {
	t.Helper()
	last := int64(-1)
	for _, c := range *calls {
		for _, e := range c.Events {
			ts, err := strconv.ParseInt(e["timestamp"].(string), 10, 64)
			if err != nil {
				t.Fatalf("timestamp not int: %v", e["timestamp"])
			}
			if ts <= last {
				t.Fatalf("timestamps must be strictly increasing: %d after %d", ts, last)
			}
			last = ts
		}
	}
}

// 创建 + 写入的请求形状必须与 dsh-lark 一致（端点/方法/字段名/事件族）。
func TestFeishuCoT_CreateAndWriteShape(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{
		TurnID:    7,
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{{
			Name: "Shell", Iteration: 1, Status: "running", Label: "ls -la", Args: `{"command":"ls -la"}`,
		}},
		CompletedTools: []protocol.ToolProgress{{
			Name: "Read", Iteration: 1, Status: "done", Detail: "hello world",
		}},
	})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	if len(*calls) < 2 {
		t.Fatalf("expected create + write calls, got %d", len(*calls))
	}

	create := (*calls)[0]
	if create.Method != "POST" || !strings.Contains(create.Path, feishuCotAPI) {
		t.Fatalf("create request shape wrong: %s %s", create.Method, create.Path)
	}
	// receive_id_type 必须与渠道自身一致：非 oc_ 的会话 id 用 open_id
	//（硬编码 chat_id 会被平台拒为 code=10001 invalid receive_id —— 用户
	// 「完全看不到中间进度」的根因）。
	if !strings.Contains(create.Path, "receive_id_type=open_id") {
		t.Fatalf("non-oc_ chat must use open_id: %s", create.Path)
	}
	for k, want := range map[string]any{
		"receive_id":        "chat_1",
		"origin_message_id": "om_parent",
		"cot_hidden":        false,
		"enable_badge":      false,
		"update_feed_rank":  false,
	} {
		if got := create.Body[k]; got != want {
			t.Fatalf("create body[%s] = %v, want %v", k, got, want)
		}
	}

	write := (*calls)[1]
	if write.Method != "PUT" || write.Path != feishuCotAPI {
		t.Fatalf("write request shape wrong: %s %s", write.Method, write.Path)
	}
	if write.Body["cot_id"] != "cot-1" || write.Body["message_id"] != "om-cot-1" {
		t.Fatalf("write body must carry cot_id/message_id: %v", write.Body)
	}

	got := strings.Join(eventTypes(t, calls), ",")
	want := "RUN_STARTED,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END,TOOL_CALL_RESULT,RUN_FINISHED"
	if got != want {
		t.Fatalf("event sequence = %s, want %s", got, want)
	}
	assertTimestampsIncreasing(t, calls)

	// 工具图标词表（dsh-lark: read/write/search/bash）+ 结果按 code block
	for _, e := range (*calls)[1].Events {
		if e["event_type"] == "TOOL_CALL_START" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(e["content"].(string)), &payload); err != nil {
				t.Fatalf("TOOL_CALL_START content must be JSON: %v", err)
			}
			if payload["icon"] != "bash" {
				t.Fatalf("Shell icon = %v, want bash", payload["icon"])
			}
		}
		if e["event_type"] == "TOOL_CALL_RESULT" {
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			content, _ := payload["content"].(map[string]any)
			if content["type"] != "code" {
				t.Fatalf("tool result must be a code block, got %v", payload["content"])
			}
		}
	}
}

// 事件 content 超 4096 字符必须 rune 安全截断并显式标记（绝不静默丢内容）。
func TestFeishuCoT_EventContentTruncated(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	c.emit("TOOL_CALL_ARGS", map[string]any{"delta": strings.Repeat("中", cotMaxEventContentChars+500)})
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	for _, call := range *calls {
		for _, e := range call.Events {
			content := e["content"].(string)
			if n := len([]rune(content)); n > cotMaxEventContentChars {
				t.Fatalf("event content %d runes > %d", n, cotMaxEventContentChars)
			}
			if !strings.Contains(content, "truncated") {
				t.Fatalf("oversized event must carry the truncated marker: %s", content[:60])
			}
		}
	}
}

// 全量推送 ⇒ 只写增量（否则思考区把整段推理重复 N 遍）。
func TestFeishuCoTRenderer_ReasoningDeltas(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 3, Phase: "iteration", Iteration: 1})
	r.onStreamContent("", "推理")
	r.onStreamContent("", "推理一步")
	r.onStreamContent("", "推理一步一步")
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	var joined strings.Builder
	types := eventTypes(t, calls)
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] != "REASONING_MESSAGE_CONTENT" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			joined.WriteString(payload["delta"].(string))
		}
	}
	if joined.String() != "推理一步一步" {
		t.Fatalf("reasoning deltas concatenate to %q, want the full text exactly once", joined.String())
	}
	if !strings.Contains(strings.Join(types, ","), "REASONING_MESSAGE_START,REASONING_MESSAGE_CONTENT") {
		t.Fatalf("missing reasoning lifecycle: %v", types)
	}
}

// 工具结果按 dsh-lark 的 1500 字符上限截断（rune 安全）。
func TestFeishuCoTRenderer_ToolResultBounded(t *testing.T) {
	if got := cotBoundResult(strings.Repeat("x", 2000)); len([]rune(got)) != cotMaxToolResultRunes {
		t.Fatalf("bounded result length = %d, want exactly %d (dsh-lark: slice(0, limit-1)+…)", len([]rune(got)), cotMaxToolResultRunes)
	}
	if got := cotBoundResult("short"); got != "short" {
		t.Fatalf("short result must pass through, got %q", got)
	}
	// 中文按 rune 截断（绝不切碎 UTF-8）。
	if got := cotBoundResult(strings.Repeat("中", 2000)); !strings.HasSuffix(got, "…") || len([]rune(got)) != cotMaxToolResultRunes {
		t.Fatalf("rune-safe truncation failed: %d runes", len([]rune(got)))
	}
}

func TestCotDelta(t *testing.T) {
	cases := []struct{ prev, acc, want string }{
		{"", "abc", "abc"},
		{"abc", "abcdef", "def"},
		{"abc", "abc", ""},
		{"abc", "ab", "ab"},   // 回退：整段补
		{"abc", "xyz", "xyz"}, // 改写：整段补
	}
	for _, c := range cases {
		if got := cotDelta(c.prev, c.acc); got != c.want {
			t.Fatalf("cotDelta(%q,%q) = %q, want %q", c.prev, c.acc, got, c.want)
		}
	}
}

// 工具名 → dsh-lark 图标词表。
func TestCotToolKind(t *testing.T) {
	cases := map[string]string{
		"Shell": "bash", "Read": "read", "FileReplace": "write", "FileCreate": "write",
		"Grep": "search", "Glob": "search", "Fetch": "search", "WebSearch": "search",
		"SubAgent": "",
	}
	for name, want := range cases {
		if got := cotToolKind(name); got != want {
			t.Fatalf("cotToolKind(%q) = %q, want %q", name, got, want)
		}
	}
	if got := cotToolIcon("SubAgent"); got != "default" {
		t.Fatalf("unknown tool icon = %q, want default", got)
	}
}

// 创建失败 ⇒ 标记 broken，调用方据此回落卡片（答案从不依赖思考过程）。
func TestFeishuCoT_CreateFailureMarksBroken(t *testing.T) {
	c := newFeishuCoT(nil, "chat_1", "", false)
	c.request = func(_ context.Context, _ string, _ string, _ any) (*larkcore.ApiResp, error) {
		return nil, cotError("boom")
	}
	c.emit("RUN_STARTED", map[string]any{"threadId": "chat_1"})
	if err := c.flushNow(); err == nil {
		t.Fatal("expected create failure")
	}
	if !c.brokenNow() {
		t.Fatal("create failure must mark the CoT broken (caller falls back to the card)")
	}
}

// tool title 必须携带「这次调用在做什么」的关键参数（对齐 dsh-lark 的 presenter
// title 语义：短小、始终可见、描述本次调用，而不是工具名重复）。
func TestCotToolTitle_CarriesPrimaryArg(t *testing.T) {
	cases := []struct {
		name    string
		tp      protocol.ToolProgress
		want    string
		notWant string
	}{
		{"shell 带命令", protocol.ToolProgress{Name: "Shell", Args: `{"command":"cargo test --all"}`}, "Shell · cargo test --all", ""},
		{"read 带路径", protocol.ToolProgress{Name: "Read", Args: `{"path":"src/app.ts"}`}, "Read · src/app.ts", ""},
		{"grep 带模式", protocol.ToolProgress{Name: "Grep", Args: `{"pattern":"TODO","path":"src"}`}, "Grep · TODO", ""},
		{"write 带路径", protocol.ToolProgress{Name: "FileReplace", Args: `{"path":"a.go"}`}, "FileReplace · a.go", ""},
		{"无参数回落 label", protocol.ToolProgress{Name: "Read", Label: "src/x.go\nmore"}, "Read · src/x.go", ""},
		{"啥都没有就只有名字", protocol.ToolProgress{Name: "WebSearch"}, "WebSearch", ""},
	}
	for _, c := range cases {
		if got := cotToolTitle(c.tp); got != c.want {
			t.Fatalf("%s: cotToolTitle = %q, want %q", c.name, got, c.want)
		}
	}
	// 超长参数：单行 + rune 安全截断（标题永远只占一行）
	long := protocol.ToolProgress{Name: "Shell", Args: `{"command":"` + strings.Repeat("x", 300) + `"}`}
	got := cotToolTitle(long)
	if strings.Contains(got, "\n") {
		t.Fatal("title must stay on one line")
	}
	if len([]rune(got)) > 96 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long title must be rune-truncated with an ellipsis, got %d runes", len([]rune(got)))
	}
}

// 被顶替的正文（narration）必须 flush 进思考过程；**最后一次**正文不进 CoT（它是答案，
// 由普通消息路径发送）—— 与 dsh-lark 的 hold/supersede 规则一致。
func TestFeishuCoTRenderer_NarrationFlushedOnce(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)

	r.onProgress(&protocol.ProgressEvent{TurnID: 5, Phase: "iteration", Iteration: 1})
	r.onStreamContent("先看看目录结构，再决定改哪里", "")
	// 迭代推进 ⇒ 第一段正文变成「过程叙述」写进 CoT
	r.onProgress(&protocol.ProgressEvent{TurnID: 5, Phase: "iteration", Iteration: 2})
	r.onStreamContent("最终答案在这里", "")
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	var texts []string
	types := strings.Join(eventTypes(t, calls), ",")
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] != "TEXT_MESSAGE_CONTENT" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			texts = append(texts, payload["delta"].(string))
		}
	}
	if len(texts) != 1 || texts[0] != "先看看目录结构，再决定改哪里" {
		t.Fatalf("narration must be flushed exactly once (the superseded text), got %v", texts)
	}
	if strings.Contains(strings.Join(texts, ""), "最终答案在这里") {
		t.Fatal("the LAST text is the answer — it must not be written into the CoT")
	}
	if !strings.Contains(types, "TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END") {
		t.Fatalf("missing TEXT_MESSAGE lifecycle: %s", types)
	}
}

// receive_id_type 归一化：oc_ → chat_id；其余（ou_/会话键）→ open_id。
func TestCotReceiveIDType(t *testing.T) {
	cases := map[string]string{
		"oc_58ca927a7ae619ad7ec2d1ed14924924": "chat_id",
		"chat_1":                              "open_id",
		"ou_abc":                              "open_id",
	}
	for in, want := range cases {
		if got := cotReceiveIDType(in); got != want {
			t.Fatalf("cotReceiveIDType(%q) = %q, want %q", in, got, want)
		}
	}
}

// oc_ 会话必须用 chat_id（正例，防止把归一化写反）。
func TestFeishuCoT_OcChatUsesChatID(t *testing.T) {
	c, calls := newFakeCoT(t, "oc_test_chat")
	c.emit("RUN_STARTED", map[string]any{"threadId": "oc_test_chat"})
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	if !strings.Contains((*calls)[0].Path, "receive_id_type=chat_id") {
		t.Fatalf("oc_ chat must use chat_id: %s", (*calls)[0].Path)
	}
}

// ⚠️ 平台拒绝必须**可诊断**：错误信息带 code/msg（丢掉平台错误是本 bug 一开始
// 不可诊断的原因），且标记 broken 让调用方降级到卡片。
func TestFeishuCoT_CreateRejectedSurfacesPlatformMsg(t *testing.T) {
	c := newFeishuCoT(nil, "chat_1", "", false)
	c.request = func(_ context.Context, method, _ string, _ any) (*larkcore.ApiResp, error) {
		if method == "POST" {
			return &larkcore.ApiResp{RawBody: []byte(`{"code":10001,"msg":"Your request contains an invalid request parameter, ext=invalid receive_id"}`)}, nil
		}
		return &larkcore.ApiResp{RawBody: []byte(`{"code":0}`)}, nil
	}
	c.emit("RUN_STARTED", map[string]any{"threadId": "chat_1"})
	err := c.flushNow()
	if err == nil {
		t.Fatal("expected create rejection")
	}
	if !strings.Contains(err.Error(), "10001") || !strings.Contains(err.Error(), "invalid receive_id") {
		t.Fatalf("error must carry the platform code/msg, got: %v", err)
	}
	if !c.brokenNow() {
		t.Fatal("rejection must mark the CoT broken (callers fall back to the card)")
	}
}

// 合成会话键（`chat_…`）绝不能直接当 receive_id —— 必须用入站事件记下的真实
// chat_id（`oc_…`）。这是用户「完全看不到中间进度」的根因（平台 10001 invalid
// receive_id），也是卡片路径当年改为 reply-to-message 的同一个坑。
func TestFeishuCoT_CreateUsesRealChatID(t *testing.T) {
	f := &FeishuChannel{realChatIDs: map[string]string{"chat_SYNTHETIC": "oc_realsynthetic"}}
	if got := f.cotReceiveID("chat_SYNTHETIC"); got != "oc_realsynthetic" {
		t.Fatalf("cotReceiveID(synthetic) = %q, want the real oc_ id", got)
	}
	if got := f.cotReceiveID("oc_already_real"); got != "oc_already_real" {
		t.Fatalf("unknown key must fall back to the key itself, got %q", got)
	}
	// 真实 id 才允许 chat_id 形态
	if got := cotReceiveIDType(f.cotReceiveID("chat_SYNTHETIC")); got != "chat_id" {
		t.Fatalf("real oc_ id must use chat_id, got %q", got)
	}
}

// ⚠️ 推理**必须**能被结构化进度驱动：真实部署里推理随 ProgressEvent 的
// ReasoningStreamContent 下发（而不是 SendStreamContent 回调）——此前 onProgress
// 只消费工具字段 ⇒ 飞书思考区「只有工具、没有推理」（用户 2026-09-17 报告：
// web 上看得到 Thought/正文，飞书里只剩 Called tools）。
func TestFeishuCoTRenderer_ProgressCarriesReasoning(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	// 第一次结构化进度：带全量推理
	r.onProgress(&protocol.ProgressEvent{
		TurnID: 9, Phase: "thinking", Iteration: 1,
		ReasoningStreamContent: "我在看硬盘占用",
	})
	// 第二次：推理继续累积（全量）+ 一个工具
	r.onProgress(&protocol.ProgressEvent{
		TurnID: 9, Phase: "tool_exec", Iteration: 1,
		ReasoningStreamContent: "我在看硬盘占用，先跑 df",
		ActiveTools:            []protocol.ToolProgress{{Name: "Shell", Iteration: 1, Status: "running"}},
	})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	var reasoning strings.Builder
	types := eventTypes(t, calls)
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] != "REASONING_MESSAGE_CONTENT" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			reasoning.WriteString(payload["delta"].(string))
		}
	}
	if reasoning.String() != "我在看硬盘占用，先跑 df" {
		t.Fatalf("思考区必须拿到推理全文（增量拼接），got %q", reasoning.String())
	}
	if !strings.Contains(strings.Join(types, ","), "REASONING_MESSAGE_START,REASONING_MESSAGE_CONTENT") {
		t.Fatalf("缺少推理生命周期: %v", types)
	}
}

// ⚠️ 每个"推理块"必须独立 messageId：平台按 id 归并内容，整轮共用一个 id 会把
// 后续迭代的推理**合并追加到第一块** ⇒ 飞书里"推理全堆在最上方"、丢掉 web 上
// 逐迭代 `Thought N` 的结构（用户 2026-09-17 报告 + 截图）。
func TestFeishuCoTRenderer_ReasoningBlocksHaveUniqueIDs(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	// 迭代 1：推理 → 工具（工具开始 ⇒ 结束推理块）
	r.onProgress(&protocol.ProgressEvent{TurnID: 9, Phase: "thinking", Iteration: 1, ReasoningStreamContent: "第一段推理"})
	r.onProgress(&protocol.ProgressEvent{TurnID: 9, Phase: "tool_exec", Iteration: 1,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Iteration: 1, Status: "running"}}})
	// 迭代 2：再来一段推理
	r.onProgress(&protocol.ProgressEvent{TurnID: 9, Phase: "thinking", Iteration: 2, ReasoningStreamContent: "第二段推理"})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	var starts, ends []string
	for _, call := range *calls {
		for _, e := range call.Events {
			et := e["event_type"].(string)
			if et != "REASONING_MESSAGE_START" && et != "REASONING_MESSAGE_END" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			id, _ := payload["messageId"].(string)
			if et == "REASONING_MESSAGE_START" {
				starts = append(starts, id)
			} else {
				ends = append(ends, id)
			}
		}
	}
	if len(starts) != 2 {
		t.Fatalf("两个迭代应开两个推理块，got starts=%v", starts)
	}
	if starts[0] == starts[1] {
		t.Fatalf("推理块 messageId 必须唯一（否则平台会把第二段并进第一块）: %v", starts)
	}
	if len(ends) != 2 || ends[0] != starts[0] || ends[1] != starts[1] {
		t.Fatalf("REASONING_MESSAGE_END 必须闭合各自的块: starts=%v ends=%v", starts, ends)
	}
}

// ⚠️ 同一迭代内的**同名工具是不同调用**：旧 key（name#iteration）会把并发两个
// Shell 合并成一个 ⇒ 平台显示「Called tools 2 times」却只展开 1 条
// （用户 2026-09-17 报告）。
func TestFeishuCoTRenderer_SameNameToolsAreDistinct(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 4, Phase: "tool_exec", Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Iteration: 1, Status: "running", Args: `{"command":"a"}`},
			{Name: "Shell", Iteration: 1, Status: "running", Args: `{"command":"b"}`},
		}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	var ids []string
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] != "TOOL_CALL_START" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			ids = append(ids, payload["toolCallId"].(string))
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("同名工具的两个调用必须各自成一条（id 必须不同），got %v", ids)
	}
}

// ⚠️ narration（正文）**不得**在工具开始时 flush：否则每个工具批之间被插入文本，
// 平台会把连续的工具调用拆成多条「Called tools 1 time」（用户 2026-09-17 截图
// 里的五条）。dsh-lark 只在「被更新的文本顶替」时写 narration。
func TestFeishuCoTRenderer_NarrationNotFlushedOnToolStart(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 6, Phase: "thinking", Iteration: 1, StreamContent: "一段正文"})
	// 同一迭代内工具开始（旧实现会在此 flush 正文 ⇒ 插入 TEXT_MESSAGE）
	r.onProgress(&protocol.ProgressEvent{TurnID: 6, Phase: "tool_exec", Iteration: 1, StreamContent: "一段正文",
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Iteration: 1, Status: "running"}}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] == "TEXT_MESSAGE_CONTENT" {
				t.Fatalf("工具开始不得 flush narration（会把工具批拆开）: %v", e)
			}
		}
	}
}
