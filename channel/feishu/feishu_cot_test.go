package feishu

import (
	"context"
	"encoding/json"
	"fmt"
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
	// 测试一律走**同步** drain（调用方显式 flushNow）：异步 drainer 与 flushNow 并发
	// 会让 fake 被两个 goroutine 同时写（-race 红灯），也让断言非确定。
	c.draining = true
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
	// ⛔ 从未 START 的完成条目（此处故意喂一个孤儿 Read）必须补成**完整调用**
	//（START+END）再发 RESULT：孤儿 RESULT 会被平台当成一次新的调用而多数一次
	//（用户 2026-09-17 截图：web 2 个工具 ⇒ 飞书「Called tools 3 times」）。
	want := "RUN_STARTED,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END,TOOL_CALL_START,TOOL_CALL_END,TOOL_CALL_RESULT,RUN_FINISHED"
	if got != want {
		t.Fatalf("event sequence = %s, want %s", got, want)
	}
	assertTimestampsIncreasing(t, calls)

	// 工具图标词表（dsh-lark: read/write/search/bash/default）+ 结果按 code block
	// （孤儿 Read 会补成完整调用，icon=read 是词表正确行为 ⇒ 按工具名断言）。
	for _, e := range (*calls)[1].Events {
		if e["event_type"] == "TOOL_CALL_START" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(e["content"].(string)), &payload); err != nil {
				t.Fatalf("TOOL_CALL_START content must be JSON: %v", err)
			}
			if payload["toolCallName"] == "Shell" && payload["icon"] != "bash" {
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
//
// ⚠️ 这里**不能用 `err == nil` 判失败**：`emit` 会启动异步 drainer，它可能先把该批次
// 消费掉（调用注入的 request 失败）并置 `broken` ⇒ 随后 `flushNow` 看到 `broken`/空队列
// 直接返回 nil（Windows 调度下必现：CI 实测 `expected create failure`；Linux 通常是
// flushNow 抢到）。契约本身是「创建失败 ⇒ broken 置位」（调用方据此回落卡片）：
// 两者必居其一 —— 若 `flushNow` 自己处理了批次，它必须报错。
func TestFeishuCoT_CreateFailureMarksBroken(t *testing.T) {
	c := newFeishuCoT(nil, "chat_1", "", false)
	c.request = func(_ context.Context, _ string, _ string, _ any) (*larkcore.ApiResp, error) {
		return nil, cotError("boom")
	}
	c.emit("RUN_STARTED", map[string]any{"threadId": "chat_1"})
	flushErr := c.flushNow()
	if flushErr == nil && !c.brokenNow() {
		t.Fatalf("create failure must mark the CoT broken (caller falls back to the card); flushNow returned nil and broken is unset")
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

// ⚠️ 完成快照**丢了 CallID** 时（SubAgent 进度转换等来源），RESULT 必须挂回该槽位
// 已 START 的 id —— 否则 RESULT 成为孤儿，平台多数一次（用户 2026-09-17 截图：
// web 2 个工具 ⇒ 飞书「Called tools 3 times」）。
func TestFeishuCoTRenderer_ResultWithoutCallIDReusesStartedID(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	// 执行快照：CallID 在。
	r.onProgress(&protocol.ProgressEvent{TurnID: 3, Phase: "tool_exec", Iteration: 1,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", CallID: "call_x", Iteration: 1, Status: "running",
			Args: `{"command":"ls"}`}}})
	// 完成快照：CallID 被丢（只剩 label/args）。
	r.onProgress(&protocol.ProgressEvent{TurnID: 3, Phase: "tool_exec", Iteration: 1,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Iteration: 1, Status: "done",
			Label: "Shell: ls", Detail: "out"}}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	startIDs, resultIDs := map[string]bool{}, map[string]bool{}
	for _, call := range *calls {
		for _, e := range call.Events {
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			id, _ := payload["toolCallId"].(string)
			switch e["event_type"] {
			case "TOOL_CALL_START":
				startIDs[id] = true
			case "TOOL_CALL_RESULT":
				resultIDs[id] = true
			}
		}
	}
	if !startIDs["call_x"] || len(startIDs) != 1 {
		t.Fatalf("START 必须用 CallID，got %v", startIDs)
	}
	for id := range resultIDs {
		if id != "call_x" {
			t.Fatalf("RESULT 必须挂回 START 的 id（孤儿 RESULT 会被多数一次）: results=%v", resultIDs)
		}
	}
}

// ⚠️ 从未 START 过的完成条目（合成通知：bg 任务/reqerr 等）必须补成一次**完整**
// 调用（START+ARGS+END）再发 RESULT —— 孤儿 RESULT 会被平台多数一次。
func TestFeishuCoTRenderer_OrphanResultBecomesCompleteCall(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 4, Phase: "tool_exec", Iteration: 2,
		CompletedTools: []protocol.ToolProgress{{Name: "background_task_result", CallID: "bg_1",
			Iteration: 2, Status: "done", Label: "bg_1", Detail: "task finished"}}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	starts, results := 0, 0
	for _, call := range *calls {
		for _, e := range call.Events {
			switch e["event_type"] {
			case "TOOL_CALL_START":
				starts++
			case "TOOL_CALL_RESULT":
				results++
			}
		}
	}
	if starts != 1 || results != 1 {
		t.Fatalf("合成通知必须补成完整调用（start=1 result=1），got start=%d result=%d", starts, results)
	}
}

// ⚠️ **同一个工具调用绝不能被计两次** —— 尤其是它在事件流里经历
// generating（生成参数中）→ executing（running）→ done 的**状态切换**时
// （用户 2026-09-17 指出我把 generating/executing/done 搞混了；web 上只调了 1 个
// 工具，飞书却显示「Called tools 2 times」，应当显示 1）。
func TestFeishuCoTRenderer_StatusTransitionsCountOnce(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	base := protocol.ToolProgress{Name: "Shell", Label: "echo hi", Args: `{"command":"echo hi"}`, Iteration: 3}
	generating := base
	generating.Status = "generating"
	running := base
	running.Status = "running"
	done := base
	done.Status = "done"
	// 同一个调用的三种状态（数组位置也在变：第 2 位 → 第 1 位 → 完成列表）
	r.onProgress(&protocol.ProgressEvent{TurnID: 4, Phase: "tool_exec", Iteration: 3,
		ActiveTools: []protocol.ToolProgress{{Name: "Fetch", Label: "u", Iteration: 3, Status: "running"}, generating}})
	r.onProgress(&protocol.ProgressEvent{TurnID: 4, Phase: "tool_exec", Iteration: 3,
		ActiveTools: []protocol.ToolProgress{running}})
	r.onProgress(&protocol.ProgressEvent{TurnID: 4, Phase: "tool_exec", Iteration: 3,
		CompletedTools: []protocol.ToolProgress{done}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	// 只统计 **Shell**（事件 1 里的 Fetch 是"数组挪位"的噪声，计入会干扰断言）。
	starts, results := 0, 0
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] != "TOOL_CALL_START" && e["event_type"] != "TOOL_CALL_RESULT" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			if payload["toolCallName"] != "Shell" && payload["toolCallId"] != "Shell#3\x00echo hi" {
				continue
			}
			if e["event_type"] == "TOOL_CALL_START" {
				starts++
			} else {
				results++
			}
		}
	}
	if starts != 1 {
		t.Fatalf("同一调用的状态切换只应开一次，got %d（web 上只调了 1 个工具）", starts)
	}
	if results != 1 {
		t.Fatalf("同一调用只应出一份结果，got %d", results)
	}
}

// ⚠️ **同一个工具调用的身份必须跨快照稳定** —— 真实事故（用户 2026-09-17 截图）：
// web 上只调了 1 个 FileCreate，飞书却显示「Called tools 2 times」，应当显示 1。
//
// 根因：工具完成时 `updateToolResultLine` 会用 `formatToolProgress(Name, Arguments)`
// **重算 label**，而 active 快照里 label 还是空的（参数仍在成型的工具尤其明显）⇒
// 用「名字#迭代号#(Label|Args)」当身份会得到**两个 key** ⇒ START(toolCallId=A) 与
// RESULT(toolCallId=B) 被平台算成两次调用。
// 身份必须用宿主给的 call id（与 dsh-lark 的 `event.data.callId` 同源）。
func TestFeishuCoTRenderer_SameCallCountsOnceWhenSnapshotFieldsDrift(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	// active 快照：label 尚未解析，args 是原始 JSON。
	r.onProgress(&protocol.ProgressEvent{TurnID: 11, Phase: "tool_exec", Iteration: 1,
		ActiveTools: []protocol.ToolProgress{{
			Name: "FileCreate", CallID: "call_abc", Iteration: 1, Status: "running",
			Args: `{"path":"/tmp/ems_weather.py","content":"print(1)"}`,
		}}})
	// completed 快照：label 已被重算成路径，args 不再出现。
	r.onProgress(&protocol.ProgressEvent{TurnID: 11, Phase: "tool_exec", Iteration: 1,
		CompletedTools: []protocol.ToolProgress{{
			Name: "FileCreate", CallID: "call_abc", Iteration: 1, Status: "done",
			Label: "/tmp/ems_weather.py", Detail: "File created successfully: /tmp/ems_weather.py",
		}}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	startIDs := map[string]bool{}
	resultIDs := map[string]bool{}
	for _, call := range *calls {
		for _, e := range call.Events {
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			id, _ := payload["toolCallId"].(string)
			switch e["event_type"] {
			case "TOOL_CALL_START":
				startIDs[id] = true
			case "TOOL_CALL_RESULT":
				resultIDs[id] = true
			}
		}
	}
	if len(startIDs) != 1 {
		t.Fatalf("同一次调用只能有一个 toolCallId，got %v", startIDs)
	}
	for id := range resultIDs {
		if !startIDs[id] {
			t.Fatalf("结果必须挂在同一个 toolCallId 上（否则平台计 2 次）: starts=%v results=%v", startIDs, resultIDs)
		}
	}
	if !startIDs["call_abc"] {
		t.Fatalf("身份必须用宿主的 CallID（dsh 的 callId 同源），got %v", startIDs)
	}
}

// ⚠️ 迭代 1 的正文必须落在**它自己的**工具之前（用户 2026-09-17 报告：
// 「第一个迭代的 content 渲染在第一个迭代的 toolcall 之后」）。
// 工具出现即证明这段正文是过程叙述（不是最终答案）⇒ 立刻写出。
func TestFeishuCoTRenderer_NarrationRendersBeforeItsOwnTool(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 5, Phase: "thinking", Iteration: 1, StreamContent: "我用同样的方法查峨眉山。"})
	r.onProgress(&protocol.ProgressEvent{TurnID: 5, Phase: "tool_exec", Iteration: 1, StreamContent: "我用同样的方法查峨眉山。",
		ActiveTools: []protocol.ToolProgress{{Name: "FileCreate", CallID: "c1", Iteration: 1, Status: "running"}}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	var order []string
	var narration string
	for _, call := range *calls {
		for _, e := range call.Events {
			et := e["event_type"].(string)
			switch et {
			case "TOOL_CALL_START":
				order = append(order, "tool")
			case "TEXT_MESSAGE_CONTENT":
				var payload map[string]any
				_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
				narration += payload["delta"].(string)
				order = append(order, "text")
			}
		}
	}
	if len(order) != 2 || order[0] != "text" || order[1] != "tool" {
		t.Fatalf("正文必须排在它自己的工具之前（web 同序），got %v", order)
	}
	if narration != "我用同样的方法查峨眉山。" {
		t.Fatalf("正文内容必须完整，got %q", narration)
	}
}

// ⚠️ 瞬时写失败**绝不能**直接熔断：旧实现 `pending = nil` + broken=true ⇒ 该 turn
// 后面的思考过程全部静默丢弃（用户 2026-09-17 报告：「后面的 cot 都不渲染」）。
func TestFeishuCoT_TransientWriteFailureRetriesInsteadOfDropping(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	base := c.request
	fails := 0
	c.request = func(ctx context.Context, method, path string, body any) (*larkcore.ApiResp, error) {
		if method == "PUT" && fails < 2 {
			fails++
			return nil, cotError("boom")
		}
		return base(ctx, method, path, body)
	}
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 8, Phase: "thinking", Iteration: 1, ReasoningStreamContent: "推理一"})
	r.onProgress(&protocol.ProgressEvent{TurnID: 8, Phase: "tool_exec", Iteration: 1,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", CallID: "c1", Iteration: 1, Status: "running"}}})
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("瞬时失败必须重试成功，不得把后续内容丢掉: %v", err)
	}
	if c.brokenNow() {
		t.Fatalf("两次瞬时失败不得熔断（后续思考过程会被全部丢弃）")
	}
	if n := len(eventTypes(t, calls)); n == 0 {
		t.Fatalf("重试后事件必须真的送达")
	}
}

// ⚠️ **渲染消息线性一致性**（用户 2026-09-17 明确要求「必须保证渲染消息线性一致性」）：
// 合并/节流**只能改变推送频率，绝不能改变顺序**。真实的迭代结构是
// [推理1 正文1 工具1] [推理2 正文2 工具2] …，写进 CoT 的事件顺序必须与之一致
// （引擎单 goroutine 产生 ⇒ 单一有序队列 ⇒ 单一写线程 FIFO）。
func TestFeishuCoTRenderer_EventOrderIsLinear(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	for i := 1; i <= 2; i++ {
		r.onProgress(&protocol.ProgressEvent{TurnID: 12, Phase: "thinking", Iteration: i,
			ReasoningStreamContent: fmt.Sprintf("推理%d", i)})
		r.onProgress(&protocol.ProgressEvent{TurnID: 12, Phase: "tool_exec", Iteration: i,
			StreamContent: fmt.Sprintf("正文%d", i),
			ActiveTools: []protocol.ToolProgress{{
				Name: "Shell", CallID: fmt.Sprintf("call_%d", i), Iteration: i, Status: "running"}}})
	}
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}

	// 只取"内容锚点"（首条 CONTENT / 工具 START），忽略 START/END 包裹事件。
	var order []string
	for _, call := range *calls {
		for _, e := range call.Events {
			et := e["event_type"].(string)
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			switch et {
			case "REASONING_MESSAGE_CONTENT":
				order = append(order, "r:"+payload["delta"].(string))
			case "TEXT_MESSAGE_CONTENT":
				order = append(order, "t:"+payload["delta"].(string))
			case "TOOL_CALL_START":
				order = append(order, "tool:"+payload["toolCallId"].(string))
			}
		}
	}
	want := []string{"t:正文1", "tool:call_1", "t:正文2", "tool:call_2"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Fatalf("事件顺序必须线性一致（合并不得改变顺序）:\n got %v\nwant %v", order, want)
	}
}

// 同一工具在**不同迭代**里各跑一次 = 两次真实调用 ⇒ 两条。
func TestFeishuCoTRenderer_SameToolTwoIterations(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	for _, it := range []int{2, 5} {
		r.onProgress(&protocol.ProgressEvent{TurnID: 4, Phase: "tool_exec", Iteration: it,
			ActiveTools: []protocol.ToolProgress{{Name: "Shell", Label: "ls", Iteration: it, Status: "running"}}})
	}
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	n := 0
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] == "TOOL_CALL_START" {
				n++
			}
		}
	}
	if n != 2 {
		t.Fatalf("不同迭代各一次 = 两次调用，got %d", n)
	}
}

// ⚠️ 契约已随用户 2026-09-17 的反馈反转：正文**必须**在它自己的工具之前写出
// （见 NarrationRendersBeforeItsOwnTool），但**只能写一次** —— 工具的多次状态
// 快照（running → done 反复上报）绝不能重复写 TEXT_MESSAGE，否则同一段正文在
// 思考区出现 N 遍、并把工具批拆开。
func TestFeishuCoTRenderer_NarrationWrittenExactlyOnce(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 6, Phase: "thinking", Iteration: 1, StreamContent: "一段正文"})
	for _, st := range []string{"running", "done"} {
		r.onProgress(&protocol.ProgressEvent{TurnID: 6, Phase: "tool_exec", Iteration: 1, StreamContent: "一段正文",
			ActiveTools: []protocol.ToolProgress{{Name: "Shell", CallID: "c9", Iteration: 1, Status: st}}})
	}
	r.close("")
	if err := c.flushNow(); err != nil {
		t.Fatalf("flushNow: %v", err)
	}
	n := 0
	var text string
	for _, call := range *calls {
		for _, e := range call.Events {
			if e["event_type"] != "TEXT_MESSAGE_CONTENT" {
				continue
			}
			n++
			var payload map[string]any
			_ = json.Unmarshal([]byte(e["content"].(string)), &payload)
			text += payload["delta"].(string)
		}
	}
	if n != 1 || text != "一段正文" {
		t.Fatalf("正文必须恰好写一次且内容完整，got n=%d text=%q", n, text)
	}
}

// 用户 2026-09-17：「飞书 cot 模式不渲染 reasoning 了，只渲染 content。
// reasoning 太多了」—— CoT **绝不**再发任何 REASONING_MESSAGE_* 事件
// （推理只存在于 web / 本地，飞书思考过程只留正文与工具调用）。
// 恢复方式：把 cotEmitReasoning 置 true（渲染逻辑完整保留）。
func TestFeishuCoTRenderer_NoReasoningEvents(t *testing.T) {
	c, calls := newFakeCoT(t, "chat_1")
	r := newFeishuCoTRenderer("chat_1", c)
	r.onProgress(&protocol.ProgressEvent{TurnID: 11, Phase: "thinking", Iteration: 1, ReasoningStreamContent: "第一段推理"})
	r.onStreamContent("", "更多推理")
	r.onProgress(&protocol.ProgressEvent{TurnID: 11, Phase: "tool_exec", Iteration: 1,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running"}}})
	r.onProgress(&protocol.ProgressEvent{TurnID: 11, Phase: "done", Iteration: 1})
	r.close("")

	for _, et := range eventTypes(t, calls) {
		if strings.HasPrefix(et, "REASONING_MESSAGE") {
			t.Fatalf("CoT 不得再发 reasoning 事件（用户要求只发 content + 工具），got %s", et)
		}
	}
}
