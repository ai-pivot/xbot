package feishu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	ch "xbot/channel"
	"xbot/protocol"
)

// ─── 建卡模板契约（故意写成字面量）───────────────────────────────────────────
//
// CardElement.Content（打字机）**只认建卡时在模板里声明过的 element_id**：对之后
// add_elements 追加出来的元素做 Content，飞书返回 300313（真实 API 探针实测）。
// 因此这两个 id 是硬契约 —— 必须出现在建卡模板里，且迭代推进时只能复用、不能新建。
// 测试用字面量钉住它们（而不是引用实现里的常量），这样改名/挪位置会立刻被测出来。
const ()

// Feishu 错误码（探针实测）。
const (
	codeElementNotFound = 300313   // not find elementID
	codeEmptyContent    = 99992402 // field validation failed
)

// --- fake Feishu tenant ------------------------------------------------------

type cardCall struct {
	Method string
	Path   string
	Body   string
}

type fakeFeishu struct {
	*httptest.Server
	mu         sync.Mutex
	calls      []cardCall
	failCreate bool

	// ─── card model ─────────────────────────────────────────────────────────
	//
	// 只建模本修复依赖的 CardKit 契约，全部来自真实 API 探针（2026-09-14）：
	//   * CardElement.Content（`/elements/:id/content`）只认**建卡模板里声明过**的
	//     element_id；对 append 出来的元素 ⇒ 300313（本 bug 静默丢失的根源）；
	//   * Content / 补丁的 content 为**空** ⇒ 99992402 field validation failed；
	//   * partial_update_element（补丁，含 `content`）对**任意已存在元素**都成功
	//     ⇒ 文本统一走补丁（这是修复采用的通路）；
	//   * add_elements 里出现重复 element_id 是**被接受**的（不失败，只记录）。
	//
	// cardElements：卡片上实际存在的 id（模板 + 追加），值是该元素当前内容。
	cardElements map[string]string
	// repeatedAdds：被重复 append 的 id（真实 API 不报错 → 本地记账要避免）。
	repeatedAdds []string

	rejectedEmptyContent int            // 空 content 打到 /content 被拒次数
	emptyPatchContent    int            // 空 content 出现在补丁里（实现里必须恒为 0）
	patchPayloads        []patchRecord  // 成功落地的文本补丁（元素 id + 全量文本）
	batchAttempts        int            // batch_update 尝试次数（含失败）
	batchPayloads        []string       // 每次 batch_update 的 actions 原文
	failBatches          map[int]string // 第 N 次（1-based）→ 注入错误
	failAllPatches       bool           // 所有 partial_update_element 都失败（模拟 API 拒绝）
	failAllContent       bool           // 所有 CardElement.Content 都失败
}

// patchRecord is one successfully applied partial_update_element text patch.
type patchRecord struct {
	ElementID string
	Content   string
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	f := &fakeFeishu{cardElements: map[string]string{}}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	ok := func(w http.ResponseWriter, v any) {
		body := map[string]any{"code": 0, "msg": "ok"}
		if v != nil {
			body["data"] = v
		}
		writeJSON(w, body)
	}
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "t-test", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/cardkit/v1/cards", func(w http.ResponseWriter, r *http.Request) {
		body := f.record(r)
		if f.failCreate {
			writeJSON(w, map[string]any{"code": 99991672, "msg": "no cardkit:card:write permission"})
			return
		}
		// 建卡模板里声明的元素 ⇒ Content 白名单（含折叠面板内的子元素）。
		f.mu.Lock()
		for _, id := range collectElementIDs(createCardJSON(body)) {
			f.cardElements[id] = ""
		}
		f.mu.Unlock()
		ok(w, map[string]any{"card_id": "card_1"})
	})
	mux.HandleFunc("/open-apis/cardkit/v1/cards/", func(w http.ResponseWriter, r *http.Request) {
		body := f.record(r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/batch_update"):
			actions := batchActions(body)
			f.mu.Lock()
			f.batchAttempts++
			n := f.batchAttempts
			msg := f.failBatches[n]
			// 记录**每一次**尝试的载荷（含失败），重试断言要靠它比对。
			f.batchPayloads = append(f.batchPayloads, actions)
			f.mu.Unlock()
			if msg != "" {
				writeJSON(w, map[string]any{"code": 230001, "msg": msg})
				return
			}
			if msg := f.applyBatch(actions); msg != "" {
				writeJSON(w, map[string]any{"code": 230001, "msg": msg})
				return
			}
		case strings.HasSuffix(r.URL.Path, "/content"):
			if code, msg := f.applyContent(pathElementID(r.URL.Path), body); code != 0 {
				writeJSON(w, map[string]any{"code": code, "msg": msg})
				return
			}
		}
		ok(w, nil)
	})
	mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		ok(w, map[string]any{"message_id": "om_1"})
	})
	mux.HandleFunc("/open-apis/im/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		ok(w, map[string]any{"message_id": "om_reply_1"})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFeishu) record(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cardCall{Method: r.Method, Path: r.URL.Path, Body: string(body)})
	return string(body)
}

// applyContent models CardElement.Content. 返回 (code, msg)；code==0 表示接受。
func (f *fakeFeishu) applyContent(elementID, body string) (int, string) {
	var req struct {
		Content *string `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return 230001, "bad content payload"
	}
	// 探针：空串（omitempty 下相当于字段缺失）被拒。
	if req.Content == nil || *req.Content == "" {
		f.mu.Lock()
		f.rejectedEmptyContent++
		f.mu.Unlock()
		return codeEmptyContent, "field validation failed"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// 探针：只认建卡模板声明过的 element_id。
	if _, declared := f.cardElements[elementID]; !declared {
		return codeElementNotFound, "ErrMsg: not find elementID : " + elementID + ";"
	}
	if f.failAllContent {
		return codeElementNotFound, "ErrMsg: not find elementID : " + elementID + ";"
	}
	f.cardElements[elementID] = *req.Content
	return 0, ""
}

// batchActions unwraps {"actions":"<json array string>"} from a batch_update body.
func batchActions(body string) string {
	var req struct {
		Actions string `json:"actions"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	return req.Actions
}

// addedIDsFromPayload extracts the element ids of every add_elements action in one
// batch_update actions payload.
func addedIDsFromPayload(actions string) map[string]bool {
	out := map[string]bool{}
	var acts []map[string]any
	if err := json.Unmarshal([]byte(actions), &acts); err != nil {
		return out
	}
	for _, a := range acts {
		raw, _ := a["add_elements"].(map[string]any)
		elems, _ := raw["elements"].([]any)
		for _, e := range elems {
			m, _ := e.(map[string]any)
			if id, _ := m["element_id"].(string); id != "" {
				out[id] = true
			}
		}
	}
	return out
}

// applyBatch 落地一次 batch_update。返回非空即表示该批被拒绝（不做任何变更）。
func (f *fakeFeishu) applyBatch(actions string) string {
	var acts []map[string]any
	if err := json.Unmarshal([]byte(actions), &acts); err != nil {
		return "bad actions json"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range acts {
		// add_elements（append 新元素）
		if raw, ok := a["add_elements"].(map[string]any); ok {
			elems, _ := raw["elements"].([]any)
			for _, e := range elems {
				m, _ := e.(map[string]any)
				topID, _ := m["element_id"].(string)
				if topID == "" {
					continue
				}
				if _, exists := f.cardElements[topID]; exists {
					// 真实 API 接受重复 id → 只记录（测试用它守护"结构只 append 一次"）。
					f.repeatedAdds = append(f.repeatedAdds, topID)
				}
				content, _ := m["content"].(string)
				f.cardElements[topID] = content
				// 折叠面板里的元素（think_n）也要记进卡片模型 —— 否则针对它的补丁会被
				// 判成"元素不存在"，测试就测了个假卡。
				if inner, err := json.Marshal(m); err == nil {
					for _, id := range collectElementIDs(string(inner)) {
						if id == topID {
							continue
						}
						if _, exists := f.cardElements[id]; !exists {
							f.cardElements[id] = ""
						}
					}
				}
			}
			continue
		}
		// partial_update_element（补丁：文本的全量替换就走它）
		if raw, ok := a["partial_update_element"].(map[string]any); ok {
			id, _ := raw["element_id"].(string)
			if id == "" {
				return "partial_update_element without element_id"
			}
			if _, exists := f.cardElements[id]; !exists {
				// 元素不存在时补丁无从落地（真实 API 同样拒绝）。
				return "element not found: " + id
			}
			if f.failAllPatches {
				return "injected patch failure"
			}
			if partial, ok := raw["partial_element"].(map[string]any); ok {
				if content, ok := partial["content"].(string); ok {
					if content == "" {
						// 探针：空 content 被拒（99992402）—— 补丁同样不能写空串。
						f.emptyPatchContent++
						continue
					}
					f.cardElements[id] = content
					f.patchPayloads = append(f.patchPayloads, patchRecord{ElementID: id, Content: content})
				}
			}
		}
	}
	return ""
}

// pathElementID extracts element_id from …/elements/:element_id/content.
func pathElementID(path string) string {
	parts := strings.Split(strings.TrimSuffix(path, "/content"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// createCardJSON unwraps {"type":"card_json","data":"<card json>"}.
func createCardJSON(body string) string {
	var req struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	return req.Data
}

// collectElementIDs walks a decoded card JSON and returns EVERY element_id
// (the 💭 panel's inner markdown element is nested, and it is one of the two
// elements that must accept CardElement.Content).
func collectElementIDs(cardJSON string) []string {
	if cardJSON == "" {
		return nil
	}
	var root any
	if err := json.Unmarshal([]byte(cardJSON), &root); err != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := t["element_id"].(string); ok && id != "" {
				out = append(out, id)
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(root)
	return out
}

// --- fake helpers ------------------------------------------------------------

func (f *fakeFeishu) snapshot() []cardCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]cardCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeFeishu) callsAt(suffix string) []cardCall {
	var out []cardCall
	for _, c := range f.snapshot() {
		if strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// contentCalls returns the streaming element pushes.
func (f *fakeFeishu) contentCalls() []cardCall {
	var out []cardCall
	for _, c := range f.snapshot() {
		if strings.Contains(c.Path, "/elements/") && strings.HasSuffix(c.Path, "/content") {
			out = append(out, c)
		}
	}
	return out
}

// declaredElements returns the Content whitelist (ids declared by the create
// template) captured when the card entity was created.
func (f *fakeFeishu) declaredElements() map[string]bool {
	out := map[string]bool{}
	for _, c := range f.callsAt("/open-apis/cardkit/v1/cards") {
		for _, id := range collectElementIDs(createCardJSON(c.Body)) {
			out[id] = true
		}
	}
	return out
}

// elementIDs returns every element id present on the fake card, plus its content.
func (f *fakeFeishu) elementIDs() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.cardElements))
	for id, content := range f.cardElements {
		out[id] = content
	}
	return out
}

// contentContains reports whether ANY element on the card renders a text that
// contains s. Used to assert "this text reached the card" without hard-coding
// element id prefixes.
func (f *fakeFeishu) contentContains(s string) bool {
	for _, content := range f.elementIDs() {
		if strings.Contains(content, s) {
			return true
		}
	}
	return false
}

func (f *fakeFeishu) batchPayload() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.batchPayloads))
	copy(out, f.batchPayloads)
	return out
}

func (f *fakeFeishu) batchAttemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batchAttempts
}

// repeatedAddIDs returns the element ids that were appended more than once.
// The real API accepts duplicates (probe: DUP_ADD code=0), so this is a local
// bookkeeping guard rather than an API failure.
func (f *fakeFeishu) repeatedAddIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.repeatedAdds))
	copy(out, f.repeatedAdds)
	return out
}

func (f *fakeFeishu) setFailBatches(m map[int]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failBatches = m
}

// sawPanelTitle reports whether some request carried a thinking-panel header
// title equal to want (the live "💭 思考 N 字" counter).
func (f *fakeFeishu) sawPanelTitle(want string) bool {
	for _, c := range f.snapshot() {
		if strings.Contains(c.Body, want) {
			return true
		}
	}
	return false
}

// rememberInboundMessageForTest seeds the reply target the card will be sent as
// a reply to (normally written by onMessage).
func (f *FeishuChannel) rememberInboundMessageForTest(chatID, msgID string) {
	f.inboundMsgIDsMu.Lock()
	defer f.inboundMsgIDsMu.Unlock()
	if f.inboundMsgIDs == nil {
		f.inboundMsgIDs = map[string]string{}
	}
	f.inboundMsgIDs[chatID] = msgID
}

func newStreamCardChannel(t *testing.T, f *fakeFeishu) *FeishuChannel {
	t.Helper()
	c := NewFeishuChannel(FeishuConfig{AppID: "cli_test", AppSecret: "secret"}, nil)
	c.client = lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(f.URL))
	// The progress card is posted as a REPLY to the chat's latest inbound message
	// (im.message.create cannot address our synthetic chat ids — Feishu 99992351).
	// Tests therefore need a reply target, exactly like a real inbound message.
	c.inboundMsgIDs["oc_chat"] = "om_inbound"
	return c
}

func fastStreamCard(t *testing.T) {
	t.Helper()
	oldText, oldPanel, oldCount := streamCardMinInterval, streamCardPanelMinInterval, streamCardReasonCountMinInterval
	streamCardMinInterval, streamCardPanelMinInterval, streamCardReasonCountMinInterval = 0, 0, 0
	t.Cleanup(func() {
		streamCardMinInterval, streamCardPanelMinInterval, streamCardReasonCountMinInterval = oldText, oldPanel, oldCount
	})
}

func panelTitle(panel map[string]any) string {
	hdr, _ := panel["header"].(map[string]any)
	if hdr == nil {
		return ""
	}
	title, _ := hdr["title"].(map[string]any)
	if title == nil {
		return ""
	}
	s, _ := title["content"].(string)
	return s
}

func TestRenderCard_FinalDisablesStreaming(t *testing.T) {
	c := &feishuStreamCard{iters: map[int]*streamIteration{}}
	c.iter(1).content = "done answer"
	card := c.renderCard(false)
	config, _ := card["config"].(map[string]any)
	if config["streaming_mode"] != false {
		t.Errorf("final streaming_mode: got %v, want false", config["streaming_mode"])
	}
	summary, _ := config["summary"].(map[string]any)
	if summary["content"] != "done answer" {
		t.Errorf("final summary: got %v, want the answer preview", summary["content"])
	}
}

func TestPanelTitleHelper(t *testing.T) {
	// guard the test helper itself
	if panelTitle(map[string]any{}) != "" {
		t.Fatal("missing header must yield empty title")
	}
}

// --- tool rows ---------------------------------------------------------------
//
// 形态（用户 2026-09-13 要求，对齐 Web）：同一迭代的连续工具**聚成一行**
// （toolRow），每个工具只占一个 chip：`**名字** 关键参数 <状态色字>` ——
// 无 emoji、无 per-tool 折叠面板。

func TestToolChip_PrefersSummaryThenArgs(t *testing.T) {
	withSummary := toolChip(streamTool{name: "Shell", status: "done", summary: "total 12\nfile a"})
	if !strings.Contains(withSummary, "total 12") || strings.Contains(withSummary, "file a") {
		t.Errorf("summary chip should use the first line: %q", withSummary)
	}

	withArgs := toolChip(streamTool{name: "Read", status: "running", args: `{"path":"/tmp/x.go","offset":1}`})
	if !strings.Contains(withArgs, "/tmp/x.go") {
		t.Errorf("args chip should surface the path: %q", withArgs)
	}
	if !strings.Contains(withArgs, "执行中") {
		t.Errorf("running chip should be labelled 执行中: %q", withArgs)
	}

	failed := toolChip(streamTool{name: "Shell", status: "error", args: `{"command":"boom"}`})
	if !strings.Contains(failed, "失败") {
		t.Errorf("failed chip: %q", failed)
	}
}

func TestToolChip_ThreeStates(t *testing.T) {
	// Web parity: generating (args still streaming) / executing / done, plus error.
	cases := []struct {
		status string
		want   string
	}{
		{"generating", "生成参数中"},
		{"running", "执行中"},
		{"done", "完成"},
		{"error", "失败"},
	}
	for _, c := range cases {
		got := toolChip(streamTool{name: "Shell", status: c.status})
		if !strings.Contains(got, c.want) {
			t.Errorf("status %q → %q, want it to contain %q", c.status, got, c.want)
		}
	}
	// The three states must be visually distinct.
	seen := map[string]bool{}
	for _, c := range cases {
		line := toolChip(streamTool{name: "Shell", status: c.status})
		if seen[line] {
			t.Errorf("status %q renders identically to another state: %q", c.status, line)
		}
		seen[line] = true
	}
}

// TestToolRow_OneToolPerLineWithEmoji — 形态契约（用户 2026-09-13 明确要求）：
// **一行一个工具 + emoji**；detail 必须限长，否则长命令会把行糊成一坨
// （现场："Shell: cd … && for d in … ════ ferrite ════ … 完成"）。
func TestToolRow_OneToolPerLineWithEmoji(t *testing.T) {
	row := toolRow([]streamTool{
		{name: "Shell", status: "done", args: `{"command":"ls -la"}`},
		{name: "Read", status: "running", args: `{"path":"a.go"}`},
	})
	lines := strings.Split(row, "\n")
	if len(lines) != 2 {
		t.Fatalf("each tool must get its own line, got %d line(s):\n%s", len(lines), row)
	}
	for _, want := range []string{"Shell", "Read", "✅", "🔄", "完成", "执行中"} {
		if !strings.Contains(row, want) {
			t.Errorf("tool row must contain %q (got: %s)", want, row)
		}
	}

	// 长命令：detail 必须被截断（限长），不能把整行糊满。
	long := toolChip(streamTool{
		name:   "Shell",
		status: "done",
		args:   `{"command":"cd /home/smith/src && for d in ferrite sglang-b300-glm52 xbot mint-infer; do echo ================ $d ================; done"}`,
	})
	if !strings.Contains(long, "…") {
		t.Errorf("a long command must be truncated with an ellipsis: %s", long)
	}
	// detail 本身被限长（标题/状态/字色标记另算）：40 runes + 省略号。
	detail := toolDetailShort(streamTool{
		name:   "Shell",
		status: "done",
		args:   `{"command":"cd /home/smith/src && for d in ferrite sglang-b300-glm52 xbot mint-infer; do echo ================ $d ================; done"}`,
	})
	if r := []rune(detail); len(r) > streamCardToolRowRunes+1 {
		t.Errorf("tool detail must be bounded to %d runes, got %d: %s", streamCardToolRowRunes+1, len(r), detail)
	}
}

func TestToolDetail_RuneSafeTruncation(t *testing.T) {
	long := strings.Repeat("中文", 200)
	got := firstNonEmptyLine(long)
	if len(got) > streamCardToolSummaryRunes {
		t.Errorf("detail exceeds budget: %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Error("detail cut mid-rune")
	}
}

func TestCardPreview_RuneSafe(t *testing.T) {
	long := strings.Repeat("中文", 200)
	got := cardPreview("\n\n" + long)
	if len(got) > streamCardPreviewBytes || !utf8.ValidString(got) {
		t.Errorf("preview: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestApplyProgress_MergesIterationsAndTools(t *testing.T) {
	c := &feishuStreamCard{iters: map[int]*streamIteration{}}
	c.applyProgress(&protocol.ProgressEvent{
		Iteration: 1, Reasoning: "r1", Content: "c1",
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running", Iteration: 1, Args: `{"command":"ls"}`}},
	})
	c.applyProgress(&protocol.ProgressEvent{
		Iteration:        2,
		IterationHistory: []protocol.ProgressEvent{{Iteration: 1, Reasoning: "r1", Content: "c1-final"}},
		CompletedTools:   []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 1, Elapsed: 9}},
	})

	if got := c.iters[1].content; got != "c1-final" {
		t.Errorf("iteration history must be authoritative: got %q", got)
	}
	if len(c.iters[1].tools) != 1 {
		t.Fatalf("tool should be upserted once, got %d", len(c.iters[1].tools))
	}
	if c.iters[1].tools[0].status != "done" {
		t.Errorf("tool status should be updated in place: %q", c.iters[1].tools[0].status)
	}
	if c.current != 2 {
		t.Errorf("current iteration: got %d, want 2", c.current)
	}
}
func TestFinalReply_WithoutOpenCardFallsBack(t *testing.T) {
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)
	if _, ok := c.streamCardSend(ch.OutboundMsg{ChatID: "oc_chat"}, "answer", true); ok {
		t.Fatal("no open card → the caller must fall back to a normal send")
	}
	if len(f.snapshot()) != 0 {
		t.Fatalf("fallback still called the API: %v", f.snapshot())
	}
}

// TestEnsureStreamCard_CreateFailureIsPerChat — 回归守护（用户报告 2026-09-13：
// "发消息后不回复也不显示中间迭代，只有整个 turn 完成才有回复"）。
//
// 旧实现用【全局】的 streamCardBroken：任一 chat 失败一次（例如群聊没有 reply
// 目标 → Feishu 99992351）就静默关掉**所有** chat 的进度卡片 → 用户什么都看不到。
// 现在失败只标记该 chat，且新入站消息会解除标记（新 turn 重新尝试）。
func TestEnsureStreamCard_CreateFailureIsPerChat(t *testing.T) {
	f := newFakeFeishu(t)
	f.failCreate = true
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1})
	if _, broken := c.streamCardsBroken["oc_chat"]; !broken {
		t.Fatal("create failure must mark THIS chat broken")
	}
	if _, broken := c.streamCardsBroken["oc_other"]; broken {
		t.Fatal("one chat's failure must NOT silence progress in another chat")
	}
	before := len(f.callsAt("/open-apis/cardkit/v1/cards"))
	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Content: "again"})
	if got := len(f.callsAt("/open-apis/cardkit/v1/cards")) - before; got != 0 {
		t.Fatalf("broken chat retried creation: %d", got)
	}
	// 新入站消息（reply 目标已就绪）→ 允许重新尝试。
	f.failCreate = false
	c.clearStreamCardsBroken("oc_chat")
	if _, ok := c.ensureStreamCard("oc_chat"); !ok {
		t.Fatal("a fresh inbound message must allow a new card attempt")
	}
}

// TestEnsureStreamCard_NoReplyTargetSkipsCreate — 没有 reply 目标时绝不调用
// im.message.create：合成 chat id 不是合法的 receive_id（Feishu 99992351），
// 该调用必然失败（旧实现因此把全局 latch 打开）。
func TestEnsureStreamCard_NoReplyTargetSkipsCreate(t *testing.T) {
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)
	c.SendProgress("oc_unknown", &protocol.ProgressEvent{Iteration: 1})

	if got := len(f.callsAt("/open-apis/im/v1/messages")); got != 0 {
		t.Fatalf("must not attempt im.message.create without a reply target (%d calls)", got)
	}
}

// TestPreReplyNotify_CardOnlyNoDoubleAck — feishu must NOT enable the agent's
// ack: progress renders ONLY through the CardKit streaming card, otherwise the
// user sees TWO cards (old ack card + streaming card) — reported 2026-09-13.
// Silence is prevented on the channel side instead (streamCardFallbackAck sends
// one short line per turn when the card is unavailable).
func TestPreReplyNotify_CardOnlyNoDoubleAck(t *testing.T) {
	c := NewFeishuChannel(FeishuConfig{}, nil)
	if c.PreReplyNotify() {
		t.Error("feishu must not send an ack card: it would double-render alongside the streaming card")
	}
}

// --- 修复后的写文本通路（partial_update_element）─────────────────────────────

func (f *fakeFeishu) setFailAllPatches(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAllPatches = v
}

// lastPatchContent returns the text most recently applied to elementID via a
// partial_update_element patch ("" when it was never patched).
func (f *fakeFeishu) lastPatchContent(elementID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	content := ""
	for _, p := range f.patchPayloads {
		if p.ElementID == elementID {
			content = p.Content
		}
	}
	return content
}

func (f *fakeFeishu) patchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.patchPayloads)
}

func (f *fakeFeishu) emptyPatchRejections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.emptyPatchContent
}

// TestRenderCard_PerIterationSkeleton — 建卡模板只声明 ans_1 这一个元素（卡片至少要有
// 一个元素才能创建）；此后每个迭代的元素（💭 面板 / 正文 / 工具行）都用 add_elements
// 追加，文本用 partial_update_element 补丁写（不能再用 CardElement.Content：见下）。
func TestRenderCard_PerIterationSkeleton(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	card, ok := c.ensureStreamCard("oc_chat")
	if !ok {
		t.Fatal("no stream card")
	}
	// 建卡请求体：只能有 ans_1（Content 的白名单只有它）。
	declared := f.declaredElements()
	if len(declared) != 1 || !declared[answerElementID(1)] {
		t.Fatalf("create template must declare exactly %q: %v", answerElementID(1), declared)
	}
	if _, hasHeader := card.renderCard(true)["header"]; hasHeader {
		t.Error("card must not render a header (user request)")
	}
	// 迭代 1/2 + 一个工具 → 结构只 append，卡片上出现各迭代的元素。
	c.SendProgress("oc_chat", &protocol.ProgressEvent{
		Iteration: 1, Reasoning: "think-one", Content: "answer-one",
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running", Iteration: 1, Args: `{"command":"ls"}`}},
	})
	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 2, Reasoning: "think-two", Content: "answer-two"})

	ids := f.elementIDs()
	for _, want := range []string{answerElementID(1), panelElementID(1), reasoningElementID(1), answerElementID(2), reasoningElementID(2)} {
		if _, exists := ids[want]; !exists {
			t.Errorf("element %q must be appended to the card: %v", want, ids)
		}
	}
	if dup := f.repeatedAddIDs(); len(dup) != 0 {
		t.Errorf("structure must be append-once: %v", dup)
	}
}

// TestStreamCard_Iteration2TextLandsViaPatch — ① 迭代 2+ 的正文与思考必须真的落到卡上。
//
// 通路必须是 partial_update_element 补丁：CardElement.Content 只认建卡模板声明过的
// 元素，对 append 出来的 ans_2/think_2 会返回 300313（本 bug 的根因）。所以断言：
//   - 没有任何 /content 请求打到"未声明"的元素上（实际上是完全不用 /content）；
//   - 迭代 2 的文本出现在补丁载荷里，并已落到卡片模型上；
//   - 全程没有空 content 被拒（99992402）。
func TestStreamCard_Iteration2TextLandsViaPatch(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Reasoning: "think-1", Content: "answer-1"})
	c.SendStreamContent("oc_chat", "answer-1", "think-1")
	c.SendProgress("oc_chat", &protocol.ProgressEvent{
		Iteration: 2, Reasoning: "think-2", Content: "answer-2",
		IterationHistory: []protocol.ProgressEvent{{Iteration: 1, Reasoning: "think-1", Content: "answer-1"}},
	})
	c.SendStreamContent("oc_chat", "answer-2", "think-2")

	declared := f.declaredElements()
	for _, call := range f.contentCalls() {
		if id := pathElementID(call.Path); !declared[id] {
			t.Errorf("/content targeted %q which is NOT declared at create time → Feishu 300313, text lost: %v", id, f.snapshot())
		}
	}
	if got := f.lastPatchContent(answerElementID(2)); !strings.Contains(got, "answer-2") {
		t.Errorf("iteration 2's answer must be written via partial_update_element: %q (patches=%d)", got, f.patchCount())
	}
	if got := f.lastPatchContent(reasoningElementID(2)); !strings.Contains(got, "think-2") {
		t.Errorf("iteration 2's thinking must be written via partial_update_element: %q", got)
	}
	card := f.elementIDs()
	if !strings.Contains(card[answerElementID(2)], "answer-2") || !strings.Contains(card[reasoningElementID(2)], "think-2") {
		t.Errorf("iteration 2's text never reached the card: %v", card)
	}
	if n := f.emptyPatchRejections(); n != 0 {
		t.Errorf("empty content must never be written (%d rejected – 99992402)", n)
	}
}

// TestStreamCard_TextNeverUsesCardElementContent — 回归守护：文本通路**不得**回退到
// CardElement.Content（它只能写建卡模板声明过的 ans_1，写 ans_2+ 会 300313 静默失败，
// 正是"卡片冻结在第一个 content"的成因）。
func TestStreamCard_TextNeverUsesCardElementContent(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Reasoning: "think-1", Content: "answer-1"})
	c.SendStreamContent("oc_chat", "answer-1", "think-1")
	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 2, Reasoning: "think-2", Content: "answer-2"})
	c.SendStreamContent("oc_chat", "answer-2", "think-2")

	if n := len(f.contentCalls()); n != 0 {
		t.Errorf("text must go through partial_update_element, not CardElement.Content (%d /content calls): %v", n, f.contentCalls())
	}
	if f.patchCount() == 0 {
		t.Error("expected text patches, got none")
	}
}

// TestStreamCard_FinalizeFailureFallsBackToPlainMessage — ③ 最终文本没能落到卡上时
// streamCardSend 必须返回 ok=false，让上层走普通消息把回复发出去（Bug B：旧实现忽略
// finalize 的 error 一律返回 true ⇒ 卡不更新、最终答复也丢了，用户彻底收不到回复）。
func TestStreamCard_FinalizeFailureFallsBackToPlainMessage(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Reasoning: "think", Content: "partial"})
	// 所有文本补丁都被拒（模拟元素缺失 / API 拒绝 / 300313 类失败）。
	f.setFailAllPatches(true)

	id, ok := c.streamCardSend(ch.OutboundMsg{
		Channel: "feishu", ChatID: "oc_chat", Content: "final answer",
		Metadata: map[string]string{ch.MetaFinalReply: "true"},
	}, "final answer", true)
	if ok {
		t.Errorf("final text never landed on the card → streamCardSend must return ok=false so the caller sends a plain message (id=%q)", id)
	}
	if id != "" {
		t.Errorf("failed finalize must not report a message id: %q", id)
	}
	// 关流式仍然必须无条件执行：开着的流会让卡片卡在"生成中"直到飞书 10 分钟后强关。
	if len(f.callsAt("/settings")) == 0 {
		t.Errorf("finalize must close streaming_mode even when the text patch failed: %v", f.snapshot())
	}
	if _, exists := c.streamCards["oc_chat"]; exists {
		t.Error("a finalized card must be released (otherwise the next turn reuses a broken card)")
	}
}

// TestStreamCard_FailedBatchRetriesAndBreaksAfterThree — ④ 失败的 batch_update 必须
// 保留 ops 供重试（commit-on-success），且连续 3 次失败熔断本会话的进度卡片
// （回落普通消息），不再死吊在一张永远更新不了的卡上。
func TestStreamCard_FailedBatchRetriesAndBreaksAfterThree(t *testing.T) {
	newEv := func() *protocol.ProgressEvent {
		return &protocol.ProgressEvent{
			Iteration: 1, Reasoning: "think-1", Content: "answer-1",
			ActiveTools: []protocol.ToolProgress{{
				Name: "Shell", Label: "Shell", Status: "running", Iteration: 1,
				Args: `{"command":"ls -la"}`,
			}},
		}
	}

	t.Run("failed batch keeps its ops and is retried", func(t *testing.T) {
		fastStreamCard(t)
		f := newFakeFeishu(t)
		c := newStreamCardChannel(t, f)
		// 前两次尝试都失败（同一次结构同步内部就会重试一次）。
		f.setFailBatches(map[int]string{1: "boom", 2: "boom"})

		c.SendProgress("oc_chat", newEv())

		card, okCard := c.ensureStreamCard("oc_chat")
		if !okCard || card == nil {
			t.Fatal("no stream card")
		}
		card.mu.Lock()
		queued := len(card.ops)
		card.mu.Unlock()
		if queued == 0 {
			t.Error("a failed batch must KEEP its queued ops for retry (old code cleared c.ops before the request → the structure and text were lost forever)")
		}
		payloads := f.batchPayload()
		if len(payloads) == 0 {
			t.Fatal("no batch_update attempt was made")
		}
		failedAdds := addedIDsFromPayload(payloads[0])

		// 放行后再同步一次：同一批 ops 必须重发并落地。
		f.setFailBatches(nil)
		c.SendProgress("oc_chat", newEv())
		payloads = f.batchPayload()
		if len(payloads) < 3 {
			t.Fatalf("the failed batch was never retried (attempts=%d)", f.batchAttemptCount())
		}
		retryAdds := addedIDsFromPayload(payloads[len(payloads)-1])
		for id := range failedAdds {
			if !retryAdds[id] {
				t.Errorf("the retry dropped queued element %q (failed=%v retry=%v)", id, failedAdds, retryAdds)
			}
		}
		card.mu.Lock()
		queued = len(card.ops)
		card.mu.Unlock()
		if queued != 0 {
			t.Errorf("a successful retry must commit (clear) the ops, got %d left", queued)
		}
		if !f.contentContains("answer-1") {
			t.Errorf("after the retry the text must be on the card: %v", f.elementIDs())
		}
	})

	t.Run("three consecutive failures break the card for this chat", func(t *testing.T) {
		fastStreamCard(t)
		f := newFakeFeishu(t)
		c := newStreamCardChannel(t, f)
		f.setFailBatches(map[int]string{1: "boom", 2: "boom", 3: "boom"})

		for i := 0; i < 4; i++ {
			c.SendProgress("oc_chat", newEv())
		}
		if got := f.batchAttemptCount(); got < 3 {
			t.Fatalf("failures must be retried: only %d batch_update attempt(s)", got)
		}
		if _, broken := c.streamCardsBroken["oc_chat"]; !broken {
			t.Error("after 3 consecutive card failures this chat must fall back to plain replies (markStreamCardsBroken)")
		}
	})
}

// TestReasoningCount_UpdatesLive — 思考字数必须**实时递增**（用户 2026-09-13）。
// 思考正文走元素级补丁，面板标题靠 partial_update_element 刷新。
func TestReasoningCount_UpdatesLive(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendStreamContent("oc_chat", "", "第一段思考")
	want1 := thinkingPanelTitle("第一段思考")
	if !f.sawPanelTitle(want1) {
		t.Fatalf("thinking panel header was never patched to %q: %v", want1, f.snapshot())
	}
	c.SendStreamContent("oc_chat", "", "第一段思考，继续第二段思考")
	want2 := thinkingPanelTitle("第一段思考，继续第二段思考")
	if !f.sawPanelTitle(want2) {
		t.Fatalf("thinking panel header was never patched to %q: %v", want2, f.snapshot())
	}
	if len([]rune(want2)) <= len([]rune(want1)) {
		t.Fatalf("thinking count must count UP: %q -> %q", want1, want2)
	}
	if got := f.lastPatchContent(reasoningElementID(1)); !strings.Contains(got, "继续第二段思考") {
		t.Errorf("thinking text must be written via a patch: %q", got)
	}
}

func TestSendProgress_CreatesCardAndStreams(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{
		Iteration: 1, Reasoning: "thinking…", Content: "hello",
		ActiveTools: []protocol.ToolProgress{{
			Name: "Shell", Label: "Shell", Status: "running", Iteration: 1,
			Args: `{"command":"ls -la"}`,
		}},
	})

	creates := f.callsAt("/open-apis/cardkit/v1/cards")
	if len(creates) != 1 {
		t.Fatalf("card entity creates: got %d, want 1", len(creates))
	}
	if sends := f.callsAt("/reply"); len(sends) != 1 {
		t.Fatalf("card sends (reply): got %d, want 1", len(sends))
	}
	if len(f.callsAt("/batch_update")) == 0 {
		t.Fatal("structure change must append elements via batch_update")
	}
	if strings.Contains(creates[0].Body, `"header"`) {
		t.Error("rendered card must not carry a header")
	}
	// 正文走补丁（不走 CardElement.Content）。
	c.SendStreamContent("oc_chat", "hello world", "")
	if got := f.lastPatchContent(answerElementID(1)); !strings.Contains(got, "hello world") {
		t.Errorf("answer text must be patched into the iteration's answer element: %q", got)
	}
}

func TestSendProgress_StreamsReasoningAndTools(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)
	c.rememberInboundMessageForTest("oc_chat", "om_in")

	c.SendProgress("oc_chat", &protocol.ProgressEvent{
		Iteration: 1, Reasoning: "thinking hard", Content: "answer so far",
		ActiveTools: []protocol.ToolProgress{{
			Name: "Shell", Label: "Shell", Status: "generating", Iteration: 1,
			Args: `{"command":"ls -la"}`,
		}},
	})
	c.SendStreamContent("oc_chat", "answer so far", "thinking hard")

	if got := f.lastPatchContent(reasoningElementID(1)); !strings.Contains(got, "thinking hard") {
		t.Fatalf("thinking was not patched into %q: %q (%v)", reasoningElementID(1), got, f.snapshot())
	}
	if got := f.lastPatchContent(answerElementID(1)); !strings.Contains(got, "answer so far") {
		t.Errorf("answer text was not patched: %q", got)
	}
	// 工具行以元素级 append 落卡；内容里必须带生成状态。
	if !f.contentContains("生成参数中") {
		t.Errorf("card should render the generating state in a tool row element: %v", f.elementIDs())
	}
	if dup := f.repeatedAddIDs(); len(dup) != 0 {
		t.Errorf("tool rows must be appended exactly once: %v", dup)
	}
}

func TestSendStreamContent_Throttled(t *testing.T) {
	oldText, oldPanel := streamCardMinInterval, streamCardPanelMinInterval
	streamCardMinInterval, streamCardPanelMinInterval = time.Hour, time.Hour
	t.Cleanup(func() { streamCardMinInterval, streamCardPanelMinInterval = oldText, oldPanel })

	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)
	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1})
	before := f.patchCount() + f.batchAttemptCount()
	for _, s := range []string{"a", "ab", "abc"} {
		c.SendStreamContent("oc_chat", s, "")
	}
	if got := f.patchCount() + f.batchAttemptCount() - before; got != 0 {
		t.Fatalf("throttled pushes reached the API: %d", got)
	}
}

func TestFinalReply_FinalizesOpenCard(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Content: "partial"})

	id, ok := c.streamCardSend(ch.OutboundMsg{
		Channel: "feishu", ChatID: "oc_chat", Content: "final answer",
		Metadata: map[string]string{ch.MetaFinalReply: "true"},
	}, "final answer", true)
	if !ok {
		t.Fatal("final reply should be handled by the open card")
	}
	if id != "om_reply_1" {
		t.Fatalf("final message id: got %q, want om_reply_1", id)
	}
	if _, exists := c.streamCards["oc_chat"]; exists {
		t.Error("finalized card must be released")
	}
	// 最终文本必须真的写到当前迭代的正文元素上。
	if got := f.elementIDs()[answerElementID(1)]; !strings.Contains(got, "final answer") {
		t.Errorf("final answer never landed on the card: %q (%v)", got, f.elementIDs())
	}
	if len(f.callsAt("/settings")) == 0 {
		t.Error("finalize must close the streaming mode")
	}
}
