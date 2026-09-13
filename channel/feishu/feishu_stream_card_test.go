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
	return c
}

func fastStreamCard(t *testing.T) {
	t.Helper()
	oldText, oldPanel := streamCardMinInterval, streamCardPanelMinInterval
	streamCardMinInterval, streamCardPanelMinInterval = 0, 0
	t.Cleanup(func() { streamCardMinInterval, streamCardPanelMinInterval = oldText, oldPanel })
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
	if elems[1]["content"] != "answer one" {
		t.Errorf("elem1 content: got %v", elems[1]["content"])
	}
	if !strings.Contains(elems[2]["content"].(string), "ls -la") {
		t.Errorf("tool row should carry the command: %v", elems[2]["content"])
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

func TestToolLine_PrefersSummaryThenArgs(t *testing.T) {
	withSummary := toolLine(streamTool{name: "Shell", status: "done", summary: "total 12\nfile a"})
	if !strings.Contains(withSummary, "total 12") || strings.Contains(withSummary, "file a") {
		t.Errorf("summary row should use the first line: %q", withSummary)
	}

	withArgs := toolLine(streamTool{name: "Read", status: "running", args: `{"path":"/tmp/x.go","offset":1}`})
	if !strings.Contains(withArgs, "/tmp/x.go") {
		t.Errorf("args row should surface the path: %q", withArgs)
	}
	if !strings.Contains(withArgs, "运行中") {
		t.Errorf("running row should be labelled 运行中: %q", withArgs)
	}

	failed := toolLine(streamTool{name: "Shell", status: "error", args: `{"command":"boom"}`})
	if !strings.Contains(failed, "失败") || !strings.Contains(failed, "red") {
		t.Errorf("failed row: %q", failed)
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
	if sends := f.callsAt("/open-apis/im/v1/messages"); len(sends) != 1 {
		t.Fatalf("card sends: got %d, want 1", len(sends))
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
	contents := f.contentCalls()
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
	if id != "om_1" {
		t.Fatalf("final message id: got %q, want om_1", id)
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

func TestEnsureStreamCard_CreateFailureLatches(t *testing.T) {
	f := newFakeFeishu(t)
	f.failCreate = true
	c := newStreamCardChannel(t, f)

	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1})
	if !c.streamCardBroken {
		t.Fatal("create failure must latch the static-card fallback")
	}
	before := len(f.callsAt("/open-apis/cardkit/v1/cards"))
	c.SendProgress("oc_chat", &protocol.ProgressEvent{Iteration: 1, Content: "again"})
	if got := len(f.callsAt("/open-apis/cardkit/v1/cards")) - before; got != 0 {
		t.Fatalf("latched fallback retried creation: %d", got)
	}
}

func TestPreReplyNotify_DisabledForStructuredProgress(t *testing.T) {
	c := NewFeishuChannel(FeishuConfig{}, nil)
	if c.PreReplyNotify() {
		t.Error("feishu renders progress from the structured stream; text acks would double-render")
	}
}
