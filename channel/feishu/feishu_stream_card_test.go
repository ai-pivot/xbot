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
)

// cardCall records one CardKit / IM API call.
type cardCall struct {
	Method string
	Path   string
	Body   string
}

// fakeFeishu emulates the endpoints the streaming card drives, so the whole
// create → send → stream → finalize lifecycle is exercised without a tenant.
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
		writeJSON(w, map[string]any{
			"code": 0, "msg": "ok",
			"tenant_access_token": "t-test", "expire": 7200,
		})
	})
	mux.HandleFunc("/open-apis/cardkit/v1/cards", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.failCreate {
			writeJSON(w, map[string]any{"code": 99991672, "msg": "no cardkit:card:write permission"})
			return
		}
		ok(w, map[string]any{"card_id": "card_1"})
	})
	// card update / settings / element content (subtree)
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

func (f *fakeFeishu) setFailCreate(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCreate = v
}

// callsAt returns the calls whose path ends with suffix.
func (f *fakeFeishu) callsAt(suffix string) []cardCall {
	var out []cardCall
	for _, c := range f.snapshot() {
		if strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// contentCalls returns the element-content pushes
// (PUT /cards/:id/elements/:element_id/content).
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

// newStreamCardChannel builds a FeishuChannel wired to the fake tenant.
func newStreamCardChannel(t *testing.T, f *fakeFeishu) *FeishuChannel {
	t.Helper()
	c := NewFeishuChannel(FeishuConfig{AppID: "cli_test", AppSecret: "secret"}, nil)
	c.client = lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(f.URL))
	return c
}

// fastStreamCard removes both throttle windows for the duration of a test.
func fastStreamCard(t *testing.T) {
	t.Helper()
	oldContent, oldPanel := streamCardMinInterval, streamCardPanelMinInterval
	streamCardMinInterval, streamCardPanelMinInterval = 0, 0
	t.Cleanup(func() {
		streamCardMinInterval, streamCardPanelMinInterval = oldContent, oldPanel
	})
}

// --- card JSON shape ---------------------------------------------------------

func TestBuildStreamCardJSON_StreamingForm(t *testing.T) {
	card := buildStreamCardJSON(streamCardView{
		title:     "xbot",
		body:      "hello",
		streaming: true,
	})

	if got := card["schema"]; got != "2.0" {
		t.Fatalf("schema: got %v, want 2.0", got)
	}
	config, _ := card["config"].(map[string]any)
	if config["streaming_mode"] != true {
		t.Errorf("config.streaming_mode: got %v, want true", config["streaming_mode"])
	}
	// The content API rejects exclusive cards — update_multi MUST stay true.
	if config["update_multi"] != true {
		t.Errorf("config.update_multi: got %v, want true", config["update_multi"])
	}
	if _, ok := config["streaming_config"]; !ok {
		t.Error("config.streaming_config missing")
	}
	if _, ok := config["summary"]; !ok {
		t.Error("config.summary missing (chat-list preview)")
	}

	body, _ := card["body"].(map[string]any)
	elems := mapElements(t, body["elements"])
	if len(elems) != 4 {
		t.Fatalf("body elements: got %d, want 4 (panel + content + hr + status)", len(elems))
	}
	// The pending timeline panel is a collapsible_panel so it can be expanded.
	if elems[0]["tag"] != "collapsible_panel" {
		t.Errorf("first element: got %v, want collapsible_panel", elems[0]["tag"])
	}
	hdr, _ := elems[0]["header"].(map[string]any)
	title, _ := hdr["title"].(map[string]any)
	if !strings.Contains(title["content"].(string), "等待工具执行") {
		t.Errorf("pending panel title: got %v", title["content"])
	}
	if _, ok := hdr["icon"].(map[string]any); !ok {
		t.Error("panel header icon missing (expand affordance)")
	}
	if hdr["icon_expanded_angle"] != -180 {
		t.Errorf("icon_expanded_angle: got %v, want -180", hdr["icon_expanded_angle"])
	}

	elem := elems[1]
	if elem["tag"] != "markdown" || elem["element_id"] != streamCardElementID {
		t.Errorf("content element: got tag=%v id=%v", elem["tag"], elem["element_id"])
	}
	if elem["content"] != "hello" {
		t.Errorf("element content: got %v, want hello", elem["content"])
	}
	if elems[2]["tag"] != "hr" {
		t.Errorf("divider: got %v, want hr", elems[2]["tag"])
	}
	if elems[3]["element_id"] != streamCardStatusElementID {
		t.Errorf("status element_id: got %v, want %s", elems[3]["element_id"], streamCardStatusElementID)
	}
	for _, id := range []string{streamCardElementID, streamCardStatusElementID} {
		if len(id) > 20 {
			t.Errorf("element_id too long: %q (%d)", id, len(id))
		}
	}

	header, _ := card["header"].(map[string]any)
	hdrTitle, _ := header["title"].(map[string]any)
	if hdrTitle["content"] != "🤖 xbot" {
		t.Errorf("header title: got %v, want 🤖 xbot", hdrTitle["content"])
	}
	if header["template"] != streamCardHeaderTemplate {
		t.Errorf("header template: got %v, want %s", header["template"], streamCardHeaderTemplate)
	}
	if subtitle, _ := header["subtitle"].(map[string]any); subtitle == nil || subtitle["content"] == "" {
		t.Error("header subtitle missing")
	}
}

func TestBuildStreamCardJSON_TimelinePanelFromTrace(t *testing.T) {
	trace := []string{
		"> ⏳ Shell(ls -la) ...",
		"> ✅ Shell (12ms)",
		"> ❌ Read (3ms)",
	}
	card := buildStreamCardJSON(streamCardView{title: "xbot", body: "answer", trace: trace, streaming: true})
	elems := cardElements(t, card)

	panel := elems[0]
	if panel["tag"] != "collapsible_panel" {
		t.Fatalf("timeline panel tag: got %v", panel["tag"])
	}
	hdr := panel["header"].(map[string]any)
	title := hdr["title"].(map[string]any)
	if !strings.Contains(title["content"].(string), "3 步") {
		t.Errorf("panel title should count steps: got %v", title["content"])
	}
	steps := mapElements(t, panel["elements"])
	if len(steps) != 3 {
		t.Fatalf("panel steps: got %d, want 3", len(steps))
	}
	// One step is still running → the panel is expanded so the user sees it.
	if panel["expanded"] != true {
		t.Errorf("panel should be expanded while a step runs: got %v", panel["expanded"])
	}

	first := steps[0]["text"].(map[string]any)["content"].(string)
	if !strings.Contains(first, "Shell") || !strings.Contains(first, "turquoise") {
		t.Errorf("running step markdown: %s", first)
	}
	third := steps[2]["text"].(map[string]any)["content"].(string)
	if !strings.Contains(third, "red") {
		t.Errorf("failed step should be red: %s", third)
	}

	// All steps settled → collapsed again.
	done := buildStreamCardJSON(streamCardView{
		title:     "xbot",
		trace:     []string{"> ✅ Shell (12ms)", "> ✅ Read (3ms)"},
		streaming: true,
	})
	donePanel := cardElements(t, done)[0]
	if donePanel["expanded"] != false {
		t.Error("panel should collapse once no step is running")
	}
}

func TestBuildStreamCardJSON_ExpandedWhileRunning(t *testing.T) {
	card := buildStreamCardJSON(streamCardView{
		title:     "xbot",
		trace:     []string{"> ⏳ Shell(ls) ..."},
		streaming: true,
	})
	panel := cardElements(t, card)[0]
	if panel["expanded"] != true {
		t.Error("panel should be expanded while a step is running")
	}
}

func TestBuildStreamCardJSON_FinalFormIsClosed(t *testing.T) {
	card := buildStreamCardJSON(streamCardView{
		title:     "xbot",
		body:      "final answer",
		trace:     []string{"> ✅ Shell (12ms)"},
		streaming: false,
	})

	config, _ := card["config"].(map[string]any)
	if config["streaming_mode"] != false {
		t.Errorf("final streaming_mode: got %v, want false", config["streaming_mode"])
	}
	summary, _ := config["summary"].(map[string]any)
	if summary["content"] != "final answer" {
		t.Errorf("final summary: got %v, want the answer preview", summary["content"])
	}
	elems := cardElements(t, card)
	if elems[1]["content"] != "final answer" {
		t.Errorf("final content: got %v", elems[1]["content"])
	}
	// Completed timeline collapses.
	if elems[0]["expanded"] != false {
		t.Error("final panel should be collapsed")
	}
}

// --- trace parsing -----------------------------------------------------------

func TestSplitStreamCardText(t *testing.T) {
	trace, body := splitStreamCardText("> ⏳ Shell(ls) ...\nhello world\n> ✅ Shell (12ms)\nsecond line")
	if len(trace) != 2 {
		t.Fatalf("trace: got %v", trace)
	}
	if body != "hello world\nsecond line" {
		t.Errorf("body: got %q", body)
	}
}

func TestSplitStreamCardText_MarkdownQuoteIsNotATrace(t *testing.T) {
	// A model quoting markdown must not be mistaken for a tool step.
	_, body := splitStreamCardText("> 这是一段引用\n> ⚠️nothing special")
	if body == "" || strings.Contains(body, "Shell") {
		t.Fatalf("quote misclassified: body=%q", body)
	}
	if _, body2 := splitStreamCardText("> 引用行"); body2 != "> 引用行" {
		t.Errorf("plain quote should stay in body: %q", body2)
	}
}

func TestParseTimelineStep(t *testing.T) {
	cases := []struct {
		line  string
		state string
		color string
	}{
		{"> ⏳ Shell(ls) ...", "运行中", "turquoise"},
		{"> ✅ Shell (12ms)", "完成", "green"},
		{"> ❌ Read (3ms)", "失败", "red"},
		{"> ⚠️ LLM 请求失败，重试中 1/3 ...", "警告", "orange"},
		{"> 📦 上下文过大 (200000 tokens)，正在压缩", "信息", "grey"},
	}
	for _, c := range cases {
		label, color, state := parseTimelineStep(c.line)
		if state != c.state || color != c.color {
			t.Errorf("%q → (%q,%q,%q), want state=%q color=%q", c.line, label, color, state, c.state, c.color)
		}
		if strings.TrimSpace(label) == "" {
			t.Errorf("%q produced an empty label", c.line)
		}
	}
}

func TestCardPreview_FirstNonEmptyLineRuneSafe(t *testing.T) {
	long := strings.Repeat("中文", 200)
	got := cardPreview("\n\n  \n" + long)
	if got == "" {
		t.Fatal("preview empty")
	}
	if len(got) > streamCardPreviewBytes {
		t.Errorf("preview exceeds byte budget: %d > %d", len(got), streamCardPreviewBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("preview is not valid UTF-8 (cut mid-rune)")
	}
	if cardPreview("\n  \n") == "" {
		t.Error("blank text should still yield a preview")
	}
}

// --- lifecycle ---------------------------------------------------------------

func TestStreamCardSend_Lifecycle(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	// 1) First send of a turn (no update_message_id) → create entity + send it.
	id, ok := c.streamCardSend(progressMsg("oc_chat", "", "ack"), "ack", false)
	if !ok {
		t.Fatal("first progress send not handled by stream card")
	}
	if id != "om_1" {
		t.Fatalf("message id: got %q, want om_1", id)
	}
	creates := f.callsAt("/open-apis/cardkit/v1/cards")
	if len(creates) != 1 {
		t.Fatalf("card entity creates: got %d, want 1", len(creates))
	}
	cardJSON := decodeCardField(t, creates[0].Body, "data")
	cfg, _ := cardJSON["config"].(map[string]any)
	if cfg["streaming_mode"] != true {
		t.Errorf("create body streaming_mode: got %v, want true", cfg["streaming_mode"])
	}
	if sends := f.callsAt("/open-apis/im/v1/messages"); len(sends) != 1 {
		t.Fatalf("card sends: got %d, want 1", len(sends))
	}
	if !strings.Contains(f.callsAt("/open-apis/im/v1/messages")[0].Body, "card_1") {
		t.Error("send body does not reference the card entity id")
	}
	if _, exists := c.streamCards["oc_chat"]; !exists {
		t.Fatal("open stream card not tracked")
	}

	// 2) Progress tick (answer text only) → element-level push (typewriter).
	if _, ok := c.streamCardSend(progressMsg("oc_chat", "om_1", "step 1"), "step 1", false); !ok {
		t.Fatal("progress send not handled")
	}
	contents := f.contentCalls()
	if len(contents) != 1 {
		t.Fatalf("content calls: got %d, want 1", len(contents))
	}
	if contents[0].Method != http.MethodPut {
		t.Errorf("content method: got %s, want PUT", contents[0].Method)
	}
	if !strings.Contains(contents[0].Body, `"content":"step 1"`) {
		t.Errorf("content body: %s", contents[0].Body)
	}
	// Pure answer text must NOT trigger a full-card update.
	if got := len(f.cardUpdates()); got != 0 {
		t.Fatalf("full-card updates for answer-only tick: got %d, want 0", got)
	}

	// 3) A tool trace line → full-card update carrying the timeline panel.
	updatesBefore := len(f.cardUpdates())
	if _, ok := c.streamCardSend(progressMsg("oc_chat", "om_1", "> ⏳ Shell(ls) ..."), "> ⏳ Shell(ls) ...", false); !ok {
		t.Fatal("timeline send not handled")
	}
	updates := f.cardUpdates()
	if len(updates)-updatesBefore != 1 {
		t.Fatalf("timeline did not sync the panel: updates=%d", len(updates))
	}
	panelCard := decodeCardField(t, updates[len(updates)-1].Body, "data")
	elems := cardElements(t, panelCard)
	if elems[0]["tag"] != "collapsible_panel" {
		t.Errorf("panel missing from the timeline update: %v", elems[0]["tag"])
	}

	// 4) Final reply → full-card update, streaming closed, card released.
	if _, ok := c.streamCardSend(progressMsg("oc_chat", "om_1", "answer"), "answer", true); !ok {
		t.Fatal("final send not handled")
	}
	if _, exists := c.streamCards["oc_chat"]; exists {
		t.Error("finalized card still tracked as open")
	}
	updates = f.cardUpdates()
	finalCard := decodeCardField(t, updates[len(updates)-1].Body, "data")
	fcfg, _ := finalCard["config"].(map[string]any)
	if fcfg["streaming_mode"] != false {
		t.Errorf("final card streaming_mode: got %v, want false", fcfg["streaming_mode"])
	}
	felems := cardElements(t, finalCard)
	if felems[0]["expanded"] != false {
		t.Error("final timeline panel should be collapsed")
	}
}

func TestStreamCardSend_SequencesStrictlyIncrease(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.streamCardSend(progressMsg("oc_chat", "", "one"), "one", false)
	for _, s := range []string{"one two", "one two three", "> ⏳ Shell(x) ...", "final"} {
		c.streamCardSend(progressMsg("oc_chat", "om_1", s), s, false)
	}

	var seqs []int
	for _, call := range f.snapshot() {
		if !strings.Contains(call.Path, "cardkit") {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
			continue
		}
		if s, ok := body["sequence"].(float64); ok {
			seqs = append(seqs, int(s))
		}
	}
	if len(seqs) < 2 {
		t.Fatalf("expected multiple sequenced operations, got %v", seqs)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("sequence not strictly increasing: %v", seqs)
		}
	}
}

func TestStreamCardSend_ThrottleDropsRapidFrames(t *testing.T) {
	oldContent, oldPanel := streamCardMinInterval, streamCardPanelMinInterval
	streamCardMinInterval, streamCardPanelMinInterval = time.Hour, time.Hour
	t.Cleanup(func() { streamCardMinInterval, streamCardPanelMinInterval = oldContent, oldPanel })

	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	if _, ok := c.streamCardSend(progressMsg("oc_chat", "", "ack"), "ack", false); !ok {
		t.Fatal("create not handled")
	}
	before := len(f.contentCalls())
	for _, s := range []string{"a", "ab", "abc", "abcd"} {
		if _, ok := c.streamCardSend(progressMsg("oc_chat", "om_1", s), s, false); !ok {
			t.Fatal("progress send not handled")
		}
	}
	if got := len(f.contentCalls()) - before; got != 0 {
		t.Fatalf("throttled pushes reached the API: got %d, want 0", got)
	}
}

func TestStreamCardSend_UnchangedTextIsNoop(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.streamCardSend(progressMsg("oc_chat", "", "same"), "same", false)
	c.streamCardSend(progressMsg("oc_chat", "om_1", "same"), "same", false)
	if got := len(f.contentCalls()); got != 0 {
		t.Fatalf("unchanged text produced %d content calls, want 0", got)
	}
}

func TestStreamCardSend_FinalWithoutOpenCardFallsBack(t *testing.T) {
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	if _, ok := c.streamCardSend(progressMsg("oc_chat", "", "answer"), "answer", true); ok {
		t.Fatal("final reply with no open card was handled by the stream card")
	}
	if len(f.snapshot()) != 0 {
		t.Fatalf("fallback still made API calls: %v", f.snapshot())
	}
}

func TestStreamCardSend_CreateFailureLatchesStaticFallback(t *testing.T) {
	f := newFakeFeishu(t)
	f.setFailCreate(true)
	c := newStreamCardChannel(t, f)

	if _, ok := c.streamCardSend(progressMsg("oc_chat", "", "ack"), "ack", false); ok {
		t.Fatal("send reported handled despite card creation failure")
	}
	if !c.streamCardBroken {
		t.Fatal("create failure did not latch the static-card fallback")
	}

	f.setFailCreate(false)
	before := len(f.callsAt("/open-apis/cardkit/v1/cards"))
	if _, ok := c.streamCardSend(progressMsg("oc_chat", "", "ack2"), "ack2", false); ok {
		t.Fatal("second send handled despite latched fallback")
	}
	if got := len(f.callsAt("/open-apis/cardkit/v1/cards")) - before; got != 0 {
		t.Fatalf("latched fallback retried creation: got %d calls", got)
	}
}

func TestStreamCardSend_NewTurnClosesStaleCard(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	if _, ok := c.streamCardSend(progressMsg("oc_chat", "", "turn1"), "turn1", false); !ok {
		t.Fatal("turn 1 create not handled")
	}
	// Turn 1 is cancelled (no final reply). Turn 2's ack has no
	// update_message_id → the stale card must be closed and a fresh one opened.
	if _, ok := c.streamCardSend(progressMsg("oc_chat", "", "turn2"), "turn2", false); !ok {
		t.Fatal("turn 2 create not handled")
	}
	if got := len(f.cardUpdates()); got != 1 {
		t.Fatalf("stale card close updates: got %d, want 1", got)
	}
	if got := len(f.callsAt("/open-apis/cardkit/v1/cards")); got != 2 {
		t.Fatalf("card entity creates: got %d, want 2", got)
	}
}

// --- Send routing ------------------------------------------------------------

func TestSend_RoutesProgressCardThroughStreamCard(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	msg := ch.OutboundMsg{
		Channel:  "feishu",
		ChatID:   "oc_chat",
		Content:  "working…",
		Metadata: map[string]string{ch.MetaProgressCard: "true"},
	}
	id, err := c.Send(msg)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "om_1" {
		t.Fatalf("Send message id: got %q, want om_1", id)
	}
	if len(f.callsAt("/open-apis/cardkit/v1/cards")) != 1 {
		t.Fatal("Send did not create a card entity for a progress send")
	}
	if got := len(f.callsAt("/open-apis/im/v1/messages")); got != 1 {
		t.Fatalf("message sends: got %d, want 1 (the card entity)", got)
	}
}

func TestSend_RoutesFinalReplyThroughStreamCard(t *testing.T) {
	fastStreamCard(t)
	f := newFakeFeishu(t)
	c := newStreamCardChannel(t, f)

	c.Send(ch.OutboundMsg{
		Channel: "feishu", ChatID: "oc_chat", Content: "ack",
		Metadata: map[string]string{ch.MetaProgressCard: "true"},
	})
	id, err := c.Send(ch.OutboundMsg{
		Channel: "feishu", ChatID: "oc_chat", Content: "final answer",
		Metadata: map[string]string{ch.MetaFinalReply: "true", "update_message_id": "om_1"},
	})
	if err != nil {
		t.Fatalf("Send final: %v", err)
	}
	if id != "om_1" {
		t.Fatalf("final message id: got %q, want om_1", id)
	}
	if _, exists := c.streamCards["oc_chat"]; exists {
		t.Error("final reply did not release the stream card")
	}
	updates := f.cardUpdates()
	if len(updates) != 1 {
		t.Fatalf("finalize card updates: got %d, want 1", len(updates))
	}
	card := decodeCardField(t, updates[0].Body, "data")
	cfg, _ := card["config"].(map[string]any)
	if cfg["streaming_mode"] != false {
		t.Error("finalize did not close streaming mode")
	}
}

// mapElements converts an element list into object maps. Encoding/json decodes
// arrays as []any, while the in-memory builder returns []map[string]any — the
// helper accepts both so assertions work on built and decoded cards alike.
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
				t.Fatalf("array item is not an object: %T", e)
			}
			out = append(out, m)
		}
		return out
	default:
		t.Fatalf("value is not an element array: %T", v)
		return nil
	}
}

// cardElements returns card.body.elements.
func cardElements(t *testing.T, card map[string]any) []map[string]any {
	t.Helper()
	body, _ := card["body"].(map[string]any)
	if body == nil {
		t.Fatal("card has no body")
	}
	return mapElements(t, body["elements"])
}

// progressMsg builds a progress/final outbound for the stream-card tests.
func progressMsg(chatID, updateMsgID, content string) ch.OutboundMsg {
	return ch.OutboundMsg{
		Channel: "feishu",
		ChatID:  chatID,
		Content: content,
		Metadata: map[string]string{
			"update_message_id": updateMsgID,
		},
	}
}

// decodeCardField unwraps a request body: the CardKit card JSON 2.0 is
// JSON-encoded into a string. Create carries it at top-level `data`; the
// full-card update nests it as `card.data`.
func decodeCardField(t *testing.T, body, field string) map[string]any {
	t.Helper()
	var outer map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &outer); err != nil {
		t.Fatalf("decode request body: %v (%s)", err, body)
	}
	raw, ok := outer[field]
	if !ok {
		// Update shape: {"card":{"type":"card_json","data":"<card json>"},...}
		if nested, hasCard := outer["card"]; hasCard {
			var inner map[string]json.RawMessage
			if err := json.Unmarshal(nested, &inner); err == nil {
				raw, ok = inner["data"]
			}
		}
	}
	if !ok {
		t.Fatalf("field %q missing from body: %s", field, body)
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatalf("field %q is not an encoded string: %v (%s)", field, err, body)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(encoded), &card); err != nil {
		t.Fatalf("decode card json: %v (%s)", err, encoded)
	}
	return card
}
