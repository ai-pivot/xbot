package feishu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	ch "xbot/channel"
	"xbot/protocol"
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
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	f := &fakeFeishu{}
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
		f.record(r)
		if f.failCreate {
			writeJSON(w, map[string]any{"code": 99991672, "msg": "no cardkit:card:write permission"})
			return
		}
		ok(w, map[string]any{"card_id": "card_1"})
	})
	mux.HandleFunc("/open-apis/cardkit/v1/cards/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
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

func (f *fakeFeishu) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cardCall{Method: r.Method, Path: r.URL.Path, Body: string(body)})
}

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

// callsToElement returns the content pushes targeting one element_id.
func (f *fakeFeishu) callsToElement(elementID string) []cardCall {
	var out []cardCall
	suffix := "/elements/" + elementID + "/content"
	for _, c := range f.snapshot() {
		if strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// decodeCardField unwraps the full-card update body:
// {"card":{"type":"card_json","data":"<card json>"}, ...}.
func decodeCardField(t *testing.T, body string) map[string]any {
	t.Helper()
	var outer map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &outer); err != nil {
		t.Fatalf("decode update body: %v (%s)", err, body)
	}
	cardField, ok := outer["card"]
	if !ok {
		t.Fatalf("update body has no card field: %s", body)
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(cardField, &inner); err != nil {
		t.Fatalf("decode card field: %v", err)
	}
	var encoded string
	if err := json.Unmarshal(inner["data"], &encoded); err != nil {
		t.Fatalf("card.data is not an encoded string: %v", err)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(encoded), &card); err != nil {
		t.Fatalf("decode card json: %v (%s)", err, encoded)
	}
	return card
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

// cardUpdates returns the full-card update calls (PUT /cards/:id).
func (f *fakeFeishu) cardUpdates() []cardCall {
	var out []cardCall
	for _, c := range f.snapshot() {
		if c.Method == http.MethodPut && strings.HasPrefix(c.Path, "/open-apis/cardkit/v1/cards/") &&
			!strings.Contains(c.Path, "/elements/") && !strings.HasSuffix(c.Path, "/settings") {
			out = append(out, c)
		}
	}
	return out
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

// lastThinkingCount returns the N in the latest rendered "💭 思考 N 字" panel title.
func lastThinkingCount(t *testing.T, f *fakeFeishu) int {
	t.Helper()
	updates := f.cardUpdates()
	if len(updates) == 0 {
		t.Fatal("no card update rendered")
	}
	card := decodeCardField(t, updates[len(updates)-1].Body)
	for _, e := range cardElements(t, card) {
		if e["tag"] != "collapsible_panel" {
			continue
		}
		title := panelTitle(e)
		if m := thinkingCountRe.FindStringSubmatch(title); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("bad count in %q: %v", title, err)
			}
			return n
		}
	}
	t.Fatalf("no 💭 思考 N 字 panel found in the card")
	return 0
}

var thinkingCountRe = regexp.MustCompile(`思考 (\d+) 字`)

// TestReasoningCount_UpdatesLive — 思考字数必须**实时递增**（用户 2026-09-13）。
// 思考正文走元素级内容 API（打字机），但面板标题只能靠整卡更新刷新 —— 所以
// 每次思考增长都要（节流地）重算标题里的字数。
func TestReasoningCount_UpdatesLive(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendStreamContent("oc_chat", "", "第一段思考")
	first := lastThinkingCount(t, f)
	if first != len([]rune("第一段思考")) {
		t.Fatalf("first count: got %d, want %d", first, len([]rune("第一段思考")))
	}

	c.SendStreamContent("oc_chat", "", "第一段思考，继续第二段思考")
	second := lastThinkingCount(t, f)
	if second != len([]rune("第一段思考，继续第二段思考")) {
		t.Fatalf("second count: got %d, want %d", second, len([]rune("第一段思考，继续第二段思考")))
	}
	if second <= first {
		t.Fatalf("thinking count must count UP live: first=%d second=%d", first, second)
	}
}

// mapElements accepts both builder output ([]map[string]any) and decoded JSON
// ([]any).
func mapElements(t *testing.T, v any) []map[string]any {
	t.Helper()
	switch raw := v.(type) {
	case []map[string]any:
		return raw
	case []any:
		out := make([]map[string]any, 0, len(raw))
		for _, e := range raw {
			m, ok := e.(map[string]any)
			if !ok {
				t.Fatalf("element is not an object: %T", e)
			}
			out = append(out, m)
		}
		return out
	default:
		t.Fatalf("not an element array: %T", v)
		return nil
	}
}

func cardElements(t *testing.T, card map[string]any) []map[string]any {
	t.Helper()
	body, _ := card["body"].(map[string]any)
	if body == nil {
		t.Fatal("card has no body")
	}
	return mapElements(t, body["elements"])
}

// --- card layout -------------------------------------------------------------

// 用户要求（2026-09-13）：无花哨 header；每个迭代按 T(思考折叠) → O(正文) → C(工具)
// 排列。
func TestRenderCard_PerIterationLayout(t *testing.T) {
	c := &feishuStreamCard{iters: map[int]*streamIteration{}}

	it1 := c.iter(1)
	it1.reasoning = "think one"
	it1.content = "answer one"
	c.mergeTool(&protocol.ToolProgress{
		Name: "Shell", Label: "Shell", Status: "done", Iteration: 1,
		Args: `{"command":"ls -la"}`, Elapsed: 12,
	})
	it2 := c.iter(2)
	it2.reasoning = "think two"
	it2.content = "answer two"

	card := c.renderCard(true)

	if _, ok := card["header"]; ok {
		t.Error("card must not render a header (user request: no flashy header)")
	}

	elems := cardElements(t, card)
	if len(elems) != 5 {
		t.Fatalf("elements: got %d, want 5 (think1, text1, tool1, think2, text2)", len(elems))
	}

	// Iteration 1: thinking panel → content → tool row.
	if elems[0]["tag"] != "collapsible_panel" {
		t.Errorf("elem0: got %v, want collapsible_panel (thinking)", elems[0]["tag"])
	}
	if title := panelTitle(elems[0]); !strings.Contains(title, "思考") {
		t.Errorf("thinking title: got %q", title)
	}
	// The thinking panel owns its own streamable element (thinking streams
	// independently of the answer text).
	thinkElems := mapElements(t, elems[0]["elements"])
	if thinkElems[0]["element_id"] != reasoningElementID(1) {
		t.Errorf("thinking element id: got %v, want %s", thinkElems[0]["element_id"], reasoningElementID(1))
	}
	if elems[1]["content"] != "answer one" {
		t.Errorf("elem1 content: got %v", elems[1]["content"])
	}
	// 连续工具聚成一行（Web 的 pill 组形态）：markdown 单行，含命令；
	// 不再有 per-tool 折叠面板（用户反馈 2026-09-13：`✅` + 4-5 行折叠是噪声）。
	if elems[2]["tag"] != "markdown" {
		t.Fatalf("tool row must be a single markdown line, got %v", elems[2]["tag"])
	}
	if content, _ := elems[2]["content"].(string); !strings.Contains(content, "ls -la") {
		t.Errorf("tool row should carry the command: %q", content)
	}

	// Iteration 2: thinking panel → the STREAMING content element.
	if elems[3]["tag"] != "collapsible_panel" {
		t.Errorf("elem3: got %v, want collapsible_panel", elems[3]["tag"])
	}
	if elems[4]["element_id"] != streamCardElementID {
		t.Errorf("current iteration content must own the streaming element: %v", elems[4]["element_id"])
	}
	if elems[4]["content"] != "answer two" {
		t.Errorf("elem4 content: got %v", elems[4]["content"])
	}

	config, _ := card["config"].(map[string]any)
	if config["streaming_mode"] != true {
		t.Errorf("streaming_mode: got %v", config["streaming_mode"])
	}
	if config["update_multi"] != true {
		t.Error("update_multi must stay true (content API rejects exclusive cards)")
	}
}

func TestRenderCard_EmptyHasStreamingElement(t *testing.T) {
	c := &feishuStreamCard{iters: map[int]*streamIteration{}}
	elems := cardElements(t, c.renderCard(true))
	if len(elems) != 1 || elems[0]["element_id"] != streamCardElementID {
		t.Fatalf("empty card must still carry the streaming element, got %v", elems)
	}
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

func TestReasoningPanel_StreamableElement(t *testing.T) {
	panel := reasoningPanel(3, "thinking")
	if panel["tag"] != "collapsible_panel" {
		t.Fatalf("panel tag: %v", panel["tag"])
	}
	if panel["expanded"] != false {
		t.Error("thinking panel should start collapsed")
	}
	inner := mapElements(t, panel["elements"])
	if inner[0]["element_id"] != reasoningElementID(3) {
		t.Errorf("element id: got %v", inner[0]["element_id"])
	}
	if len(reasoningElementID(3)) > 20 {
		t.Errorf("element id too long: %q", reasoningElementID(3))
	}
	if !strings.Contains(panelTitle(panel), "思考") {
		t.Errorf("title: %q", panelTitle(panel))
	}
	// Every iteration gets its own id (independent streams).
	if reasoningElementID(1) == reasoningElementID(2) {
		t.Error("iterations must not share a thinking element id")
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
	// The answer text streams through SendStreamContent (the agent's stream
	// callback), the thinking through the structured snapshot.
	c.SendStreamContent("oc_chat", "answer so far", "thinking hard")

	// Thinking streams into its own element.
	thinkPushes := f.callsToElement(reasoningElementID(1))
	if len(thinkPushes) == 0 {
		t.Fatalf("thinking was not streamed; calls: %v", f.snapshot())
	}
	// The answer streams into the content element.
	if len(f.callsToElement(streamCardElementID)) == 0 {
		t.Error("answer text was not streamed")
	}
	// The rendered card carries the generating state inside the tool row (a single
	// markdown line — no per-tool panel).
	update := f.cardUpdates()[len(f.cardUpdates())-1]
	card := decodeCardField(t, update.Body)
	elems := cardElements(t, card)
	found := false
	for _, e := range elems {
		if e["tag"] != "markdown" {
			continue
		}
		if c, _ := e["content"].(string); strings.Contains(c, "生成参数中") {
			found = true
		}
	}
	if !found {
		t.Errorf("card should render the generating state in the tool row: %v", elems)
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

// --- channel lifecycle -------------------------------------------------------

func TestSendProgress_CreatesCardAndStreams(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Reasoning: "thinking…", Content: "hello"})

	creates := f.callsAt("/open-apis/cardkit/v1/cards")
	if len(creates) != 1 {
		t.Fatalf("card entity creates: got %d, want 1", len(creates))
	}
	// The card is posted as a REPLY to the chat's latest inbound message (create
	// cannot address our synthetic chat ids — Feishu 99992351).
	if sends := f.callsAt("/reply"); len(sends) != 1 {
		t.Fatalf("card sends (reply): got %d, want 1", len(sends))
	}
	if len(f.cardUpdates()) == 0 {
		t.Fatal("structure change should trigger a full-card update")
	}
	card := f.cardUpdates()[len(f.cardUpdates())-1]
	if strings.Contains(card.Body, `"header"`) {
		t.Error("rendered card must not carry a header")
	}

	// Live text goes through the streaming element (typewriter), not a rebuild.
	c.SendStreamContent("oc_chat", "hello world", "")
	contents := f.callsToElement(streamCardElementID)
	if len(contents) != 1 {
		t.Fatalf("content pushes: got %d, want 1", len(contents))
	}
	if !strings.Contains(contents[0].Body, "hello world") {
		t.Errorf("content push body: %s", contents[0].Body)
	}
}

func TestSendStreamContent_Throttled(t *testing.T) {
	oldText, oldPanel := streamCardMinInterval, streamCardPanelMinInterval
	streamCardMinInterval, streamCardPanelMinInterval = time.Hour, time.Hour
	t.Cleanup(func() { streamCardMinInterval, streamCardPanelMinInterval = oldText, oldPanel })

	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)
	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1})
	before := len(f.contentCalls())
	for _, s := range []string{"a", "ab", "abc"} {
		c.SendStreamContent("oc_chat", s, "")
	}
	if got := len(f.contentCalls()) - before; got != 0 {
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
	last := f.cardUpdates()[len(f.cardUpdates())-1]
	if !strings.Contains(last.Body, `streaming_mode\":false`) && !strings.Contains(last.Body, `"streaming_mode":false`) {
		t.Errorf("finalize must close streaming mode: %s", last.Body)
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
