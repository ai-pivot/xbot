package feishu

// feishu_stream_card.go — CardKit progress card, 与 Web 端同一形态。
//
// 用户要求（2026-09-13）：不要花哨 header；**每个迭代**按 T → O → C 排列
// （💭 思考折叠 → 正文 → 工具），而不是把所有工具塞进一个面板。对齐
// web/src/components/agent/IterationHistory.tsx 的 IterationGroup。
//
// 数据来自**结构化进度**（channel.ProgressSender：SendProgress /
// SendStreamContent），不再解析扁平文本 —— 扁平文本里既没有思考正文，也没有
// 迭代边界。飞书渠道因此和其它 ProgressSender（web/cli）走同一条广播。
//
// 卡片生命周期：
//
//	POST  cardkit/v1/cards                            create entity (streaming_mode=true)
//	POST  im/v1/messages  msg_type=interactive        send {type:card,data:{card_id}}
//	PUT   cardkit/v1/cards/:id/elements/content       push 当前迭代正文（打字机）
//	PUT   cardkit/v1/cards/:id                        full-card update（迭代/工具结构）
//
// 只有 `element_id="content"` 这个元素能流式更新，所以「当前迭代的正文」用它，
// 已完结迭代的正文/工具/思考都靠整卡更新重排。
//
// 飞书要求显式关闭流式模式：开着的流会让卡片停在「生成中」直到 10 分钟后被强制
// 关闭，因此 finalize() 一定执行（即使整卡更新失败也要兜底关流式）。
//
// 卡片实体只能发送一次，且只能由创建它的应用发送。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	ch "xbot/channel"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/tools"
)

const (
	// streamCardElementID identifies the markdown element that receives the
	// streamed text of the CURRENT iteration. Feishu validates element_id:
	// letters/digits/underscore, must start with a letter, ≤20 characters.

	// streamCardSummary is the chat-list preview shown while streaming.
	streamCardSummary = "🔄 生成中…"

	// streamCardMaxConsecutiveFailures 是卡片连续写失败多少次后熔断本会话的进度卡片。
	// 反复失败的卡片（例如每次 batch_update 都被拒）只会冻住进度 —— 熔断比死吊更好。
	streamCardMaxConsecutiveFailures = 3

	// streamCardPanelIconToken is the chevron of the collapsible thinking panel
	// (rotated 180° when expanded). Token taken from the official card builder.
	streamCardPanelIconToken = "down-small-ccm_outlined"

	// streamCardPreviewBytes bounds the chat-list preview (rune-safe cut).
	streamCardPreviewBytes = 120

	// streamCardToolSummaryRunes bounds a tool summary line so one long command
	// cannot blow up the card.
	streamCardToolSummaryRunes = 80

	// streamCardToolRowRunes bounds what a tool ROW shows: one tool per line must
	// stay scannable, so the detail is cut (with an ellipsis) well before the
	// full command fits — otherwise the row degenerates into a wall of text.
	streamCardToolRowRunes = 40
)

// streamCardMinInterval throttles the streaming-text element pushes.
// A var (not a const) so tests can disable/force the throttle.
var streamCardMinInterval = 250 * time.Millisecond

// streamCardReasonCountMinInterval throttles the thinking-panel HEADER refresh
// ("💭 思考 N 字"). The thinking TEXT streams through the per-element content API
// (typewriter), but a panel header can only change via a full-card update — so
// the character count is refreshed on its own, slower throttle to keep it
// visibly counting up without flooding the card API.
// A var (not a const) so tests can force it.
var streamCardReasonCountMinInterval = 400 * time.Millisecond

// streamCardPanelMinInterval throttles the full-card updates that re-lay out the
// iterations (thinking blocks / finished iterations / tool rows).
var streamCardPanelMinInterval = 600 * time.Millisecond

// streamTool is one tool row inside an iteration.
type streamTool struct {
	key       string
	name      string
	label     string
	status    string
	summary   string
	args      string
	detail    string
	elapsedMs int64
}

// toolStatusLabel maps the engine's tool status to the Web UI's three states
// (generating = arguments still streaming, executing = running, done) plus the
// error state. Emoji are intentional: they make the state scannable at a glance
// (user 2026-09-13: "一行一个工具并且加回emoji").
func toolStatusLabel(status string) (icon, color, label string) {
	switch status {
	case "generating":
		return "✍️", "grey", "生成参数中"
	case "pending":
		return "⏸️", "grey", "等待执行"
	case "running":
		return "🔄", "turquoise", "执行中"
	case "error", "failed":
		return "❌", "red", "失败"
	default:
		return "✅", "green", "完成"
	}
}

// streamCardArgKeys are the tool-argument fields surfaced in a tool row, in
// priority order — so a row reads like "Shell · ls -la" instead of dumping the
// whole JSON arguments blob.
var streamCardArgKeys = []string{
	"command", "cmd", "path", "file_path", "filepath", "pattern", "query",
	"url", "task", "prompt", "message", "name", "role", "instance",
}

// toolDetail picks the human-readable "what" of a tool row: the result summary
// when the tool already finished, otherwise the most telling argument.
func toolDetail(t streamTool) string {
	if line := firstNonEmptyLine(t.summary); line != "" {
		return line
	}
	if t.args != "" {
		var m map[string]any
		if json.Unmarshal([]byte(t.args), &m) == nil {
			for _, k := range streamCardArgKeys {
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					return firstNonEmptyLine(v)
				}
			}
		}
		return firstNonEmptyLine(t.args)
	}
	return ""
}

// firstNonEmptyLine returns the first non-blank line, rune-safe truncated.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return tools.TruncateHeadPreview(line, streamCardToolSummaryRunes)
		}
	}
	return ""
}

// streamIteration accumulates one iteration's thinking / answer / tools.
type streamIteration struct {
	reasoning string
	content   string
	tools     []streamTool
	toolIndex map[string]int // tool key → index into tools
}

// feishuStreamCard drives one CardKit streaming card (one per turn).
// All methods are safe for concurrent use.
type feishuStreamCard struct {
	client    *lark.Client
	cardID    string
	messageID string

	// channel/chatID 让卡片能在"反复更新失败"时上报（熔断该 chat 的进度卡片）。
	channel *FeishuChannel
	chatID  string

	// consecutiveCardFailures 连续失败的卡片写请求数（成功即清零）。
	// 达到 streamCardMaxConsecutiveFailures 即熔断（回落普通消息）。
	consecutiveCardFailures int

	title string

	mu      sync.Mutex
	iters   map[int]*streamIteration
	current int

	seq        int
	lastTextAt time.Time
	lastCardAt time.Time
	lastText   string
	finished   bool

	// Per-iteration thinking stream state (each thinking panel owns its own
	// streamable element id, so thinking and answer stream independently).
	lastReasonIter int
	lastReasoning  string
	lastReasonAt   time.Time
	// lastReasonCountAt throttles the thinking-panel HEADER refresh (the character
	// count in "💭 思考 N 字" is refreshed via partial_update_element — never a full
	// card replace; see feishu_stream_card_ops.go).
	lastReasonCountAt time.Time

	// ─── 元素级增量（方案 A，见 feishu_stream_card_ops.go）───
	// ops 是待提交的元素级变更队列（合并成一次 BatchUpdateCard）。
	ops []cardOp
	// appendThink/appendAnswer/appendTool 记录**已经追加过**的元素，保证每个元素
	// 只 append 一次（结构变化只增不改 → 不重排整卡）。
	appendThink  map[int]bool
	appendAnswer map[int]bool
	appendTool   map[string]bool
	// elements 是卡片的**虚拟布局**：创建时的骨架 + 之后每一次**成功** append 的元素，
	// 顺序即飞书卡片里的顺序（commit-on-success：失败的批次不会记进来）。
	elements []map[string]any
	// panelTitles 记录各思考面板标题的当前值（实时字数）。
	panelTitles map[int]string
}

// newFeishuStreamCard creates the card entity (streaming enabled) and posts it.
// It is the single creation path for both entry points (ack-free structured
// progress and the legacy final-reply fallback).
func newFeishuStreamCard(client *lark.Client, title, chatID, replyTo string) (*feishuStreamCard, error) {
	card := &feishuStreamCard{
		client:       client,
		title:        title,
		iters:        map[int]*streamIteration{},
		appendThink:  map[int]bool{},
		appendAnswer: map[int]bool{},
		appendTool:   map[string]bool{},
		panelTitles:  map[int]string{},
	}
	data, err := json.Marshal(card.renderCard(true))
	if err != nil {
		return nil, fmt.Errorf("marshal stream card: %w", err)
	}
	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(string(data)).
			Build()).
		Build()
	resp, err := client.Cardkit.V1.Card.Create(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("create stream card entity: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("create stream card entity: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.CardId == nil || *resp.Data.CardId == "" {
		return nil, fmt.Errorf("create stream card entity: empty card_id")
	}
	card.cardID = *resp.Data.CardId

	msgID, err := card.sendCardMessage(chatID, replyTo)
	if err != nil {
		return nil, err
	}
	card.messageID = msgID
	now := time.Now()
	card.lastCardAt = now
	card.lastTextAt = now
	return card, nil
}

// sendCardMessage posts the card entity as an interactive message — fresh, or in
// reply to replyTo. A card entity can be sent exactly once.
func (c *feishuStreamCard) sendCardMessage(chatID, replyTo string) (string, error) {
	content, err := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]string{"card_id": c.cardID},
	})
	if err != nil {
		return "", fmt.Errorf("marshal card reference: %w", err)
	}

	if replyTo == "" {
		// No reply target → the card cannot be posted at all: `im.message.create`
		// needs a receive_id_type that our synthetic chat ids (e.g. "chat_…")
		// cannot provide — Feishu rejects it with code 99992351. Attempting it
		// would only issue a guaranteed-failing request (and it used to latch a
		// GLOBAL failure that silenced progress in every chat). Skip the card for
		// this turn; the next inbound message provides a reply target.
		return "", fmt.Errorf("send stream card: no inbound message id for chat %s (reply target required)", chatID)
	}
	resp, err := c.client.Im.Message.Reply(context.Background(),
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyTo).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("interactive").Content(string(content)).Build()).
			Build())
	if err != nil {
		return "", fmt.Errorf("send stream card reply: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("send stream card reply: code=%d msg=%s", resp.Code, resp.Msg)
	}
	msgID := ""
	if resp.Data != nil {
		msgID = derefString(resp.Data.MessageId)
	}
	if msgID == "" {
		return "", fmt.Errorf("send stream card: empty message_id")
	}
	return msgID, nil
}

// markStreamCardsBroken records that the progress card is unavailable for ONE
// chat, so its turns stop retrying until a new inbound message arrives.
func (f *FeishuChannel) markStreamCardsBroken(chatID string) {
	f.streamCardsMu.Lock()
	if f.streamCardsBroken == nil {
		f.streamCardsBroken = make(map[string]struct{})
	}
	f.streamCardsBroken[chatID] = struct{}{}
	f.streamCardsMu.Unlock()
}

// clearStreamCardsBroken re-enables card attempts for ONE chat (called on a fresh
// inbound message: the reply target is known again).
func (f *FeishuChannel) clearStreamCardsBroken(chatID string) {
	f.streamCardsMu.Lock()
	delete(f.streamCardsBroken, chatID)
	f.streamCardsMu.Unlock()
}

// clearStreamCardAcked re-arms the one-shot fallback ack for ONE chat (new turn).
func (f *FeishuChannel) clearStreamCardAcked(chatID string) {
	f.streamCardsMu.Lock()
	delete(f.streamCardAcked, chatID)
	f.streamCardsMu.Unlock()
}

// iter returns (creating if needed) the state of one iteration.
// The caller must hold c.mu.
func (c *feishuStreamCard) iter(n int) *streamIteration {
	it, ok := c.iters[n]
	if !ok {
		it = &streamIteration{toolIndex: map[string]int{}}
		c.iters[n] = it
	}
	if n > c.current {
		c.current = n
	}
	return it
}

// applyProgress merges one structured progress snapshot into the card state.
func (c *feishuStreamCard) applyProgress(p *protocol.ProgressEvent) {
	if p == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if p.Iteration > 0 {
		it := c.iter(p.Iteration)
		if p.Reasoning != "" {
			it.reasoning = p.Reasoning
		}
		if p.Content != "" {
			it.content = p.Content
		}
	}
	// Completed iterations arrive as a delta log — authoritative for the text.
	for i := range p.IterationHistory {
		e := &p.IterationHistory[i]
		if e.Iteration <= 0 {
			continue
		}
		it := c.iter(e.Iteration)
		if e.Reasoning != "" {
			it.reasoning = e.Reasoning
		}
		if e.Content != "" {
			it.content = e.Content
		}
	}
	// Tools carry their own iteration number, so they land in the right block.
	for i := range p.CompletedTools {
		c.mergeTool(&p.CompletedTools[i])
	}
	for i := range p.ActiveTools {
		c.mergeTool(&p.ActiveTools[i])
	}
}

// mergeTool upserts one tool row. The caller must hold c.mu.
func (c *feishuStreamCard) mergeTool(t *protocol.ToolProgress) {
	n := t.Iteration
	if n <= 0 {
		n = c.current
	}
	if n <= 0 {
		n = 1
	}
	key := t.Name
	if t.CallID != "" {
		key = t.CallID
	}
	if key == "" {
		return
	}
	it := c.iter(n)
	row := streamTool{
		key: key, name: t.Name, label: t.Label, status: t.Status,
		summary: t.Summary, args: t.Args, detail: t.Detail, elapsedMs: t.Elapsed,
	}
	if idx, ok := it.toolIndex[key]; ok {
		it.tools[idx] = row
		return
	}
	it.toolIndex[key] = len(it.tools)
	it.tools = append(it.tools, row)
}

// pushText updates the current iteration's answer element with the live text.
//
// 走 partial_update_element 补丁（全量替换该元素 content），不走 CardElement.Content
// —— 后者只认建卡模板声明过的元素，而本实现除 ans_1 外都是 append 出来的（300313）。
func (c *feishuStreamCard) pushText(n int, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || text == "" || text == c.lastText {
		return
	}
	if time.Since(c.lastTextAt) < streamCardMinInterval {
		return
	}
	// 该迭代的元素必须先存在（结构只 append 一次），文本补丁才有地方落；
	// submitOpsLocked 会把 add_elements 排在补丁之前（同一批里元素先出现）。
	c.ensureElementsLocked(n)
	c.iter(n).content = text
	c.queueElementContentLocked(answerElementID(n), text)
	c.lastText = text
	c.lastTextAt = time.Now()
	c.submitOpsLocked()
}

// syncLayout re-lays out the whole card (thinking blocks, finished iterations,
// tool rows). Throttled independently of the text stream.
func (c *feishuStreamCard) syncLayout(force bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	if !force && time.Since(c.lastCardAt) < streamCardPanelMinInterval {
		return
	}
	// 结构变化只 append / patch（元素级）—— 不再整卡替换。
	c.ensureAllElementsLocked()
	// 快照里带的正文也要落到元素上（progress 事件可能先于流式文本到达）。
	if it := c.iters[c.current]; it != nil && it.content != "" && it.content != c.lastText {
		c.queueElementContentLocked(answerElementID(c.current), it.content)
		c.lastText, c.lastTextAt = it.content, time.Now()
	}
	c.flushOps(true)
}

// finalize renders the finished card and closes the streaming mode. Closing is
// mandatory and runs even when the text update fails: an open stream leaves the
// card showing "生成中" for up to 10 minutes.
//
// 返回值语义（Bug B）：非 nil 表示**最终文本没能落到卡上**（元素缺失 / API 拒绝）。
// 调用方据此回落到普通消息 —— 最终答复绝不丢。
func (c *feishuStreamCard) finalize(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}
	c.finished = true
	if text != "" {
		it := c.iter(c.current)
		it.content = text
	}
	// 结构先落地（只 append，不整卡替换），最终文本的补丁才有元素可落。
	c.ensureAllElementsLocked()
	var structErr error
	if err := c.submitOpsLocked(); err != nil {
		structErr = err
	}
	// 最终文本：partial_update_element 补丁（全量替换 content）。
	// 只有当 ops 队列非空、且提交失败时才算"没落卡"——提交成功即已落卡。
	var textErr error
	if text != "" {
		c.queueElementContentLocked(answerElementID(c.current), text)
		if err := c.submitOpsLocked(); err != nil {
			log.WithError(err).WithField("card_id", c.cardID).
				Warn("Feishu: final text push failed")
			textErr = err
		} else {
			c.lastText, c.lastTextAt = text, time.Now()
		}
	}
	// 收尾：折叠各思考面板（一张卡片的"干净"形态 = 思考 + 正文 + 工具行）。
	for n := range c.iters {
		c.queuePatch(panelElementID(n), map[string]any{"expanded": false})
	}
	if err := c.submitOpsLocked(); err != nil && structErr == nil {
		structErr = err
	}
	// 关流式是强制项（开着的流会让卡片卡在"生成中"直到飞书 10 分钟后强关）。
	// 它失败同样意味着"最终答复没能可靠呈现" → 一并回落普通消息（宁可重复，不丢答复）。
	if err := c.setStreamingMode(false); err != nil {
		log.WithError(err).WithField("card_id", c.cardID).
			Warn("Feishu: close streaming mode failed")
		if textErr == nil {
			textErr = err
		}
	}
	if textErr != nil {
		return textErr
	}
	_ = structErr // 结构性问题已 Warn + 熔断兜底；最终文本已落卡就不必再发一条普通消息
	return nil
}

// renderCard builds the Card JSON 2.0 for the current state.
// The caller must hold c.mu (or hold exclusive ownership, e.g. at creation).
func (c *feishuStreamCard) renderCard(streaming bool) map[string]any {
	// 卡片正文 = 已经在卡片上的元素（commit-on-success 记进 c.elements）+ 尚未提交的
	// 新增元素。建卡模板至少要有一个元素：`ans_1` 在创建请求体里就已经"在卡片上"了，
	// 若不连带把 appendAnswer[1] 置位，第一次 flush 会再 append 一个同名元素
	// （真实 API 接受重复 id，但会多出一个重复元素）。
	pending := func() []map[string]any {
		var out []map[string]any
		for _, op := range c.ops {
			if op.kind == opAdd {
				out = append(out, op.elements...)
			}
		}
		return out
	}
	if len(c.elements) == 0 && len(pending()) == 0 {
		c.elements = []map[string]any{
			{"tag": "markdown", "element_id": answerElementID(1), "content": ""},
		}
		if c.appendAnswer == nil {
			c.appendAnswer = map[int]bool{}
		}
		c.appendAnswer[1] = true
	}
	elements := c.elements
	if p := pending(); len(p) > 0 {
		elements = append(append([]map[string]any{}, elements...), p...)
	}

	summary := streamCardSummary
	if !streaming {
		summary = cardPreview(currentContent(c.iters, c.current))
	}

	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			// update_multi MUST stay true: the content API rejects exclusive cards.
			"update_multi":   true,
			"width_mode":     "fill",
			"streaming_mode": streaming,
			"streaming_config": map[string]any{
				"print_frequency_ms": map[string]int{"default": 30, "android": 30, "ios": 30, "pc": 30},
				"print_step":         map[string]int{"default": 2, "android": 2, "ios": 2, "pc": 2},
				"print_strategy":     "fast",
			},
			"summary": map[string]any{"content": summary},
		},
		// 用户要求：不要花哨 header —— 卡片正文直接就是迭代内容。
		"body": map[string]any{"elements": elements},
	}
}

// currentContent returns the current iteration's answer text.
func currentContent(iters map[int]*streamIteration, current int) string {
	if it, ok := iters[current]; ok && it != nil {
		return it.content
	}
	return ""
}

// thinkingPanelTitle renders the folded thinking panel's header title.
// The count is refreshed live via partial_update_element (patchThinkingCountLocked).
func thinkingPanelTitle(reasoning string) string {
	return fmt.Sprintf("💭 思考 %d 字", len([]rune(reasoning)))
}

// reasoningPanel renders the folded thinking block (💭 思考 N 字).
//
// The PANEL carries element_id panelElementID(n) so its header title can be
// patched in place (live character count), and the inner markdown element carries
// reasoningElementID(n) so the thinking text streams independently of the answer
// — both without ever re-rendering the whole card.
func reasoningPanel(n int, reasoning string) map[string]any {
	return map[string]any{
		"tag":        "collapsible_panel",
		"element_id": panelElementID(n),
		"expanded":   false,
		"header": map[string]any{
			"title": map[string]any{
				"tag": "plain_text", "content": thinkingPanelTitle(reasoning),
				"text_color": "grey", "text_size": "notation",
			},
			"vertical_align": "center",
			"icon": map[string]any{
				"tag": "standard_icon", "token": streamCardPanelIconToken,
				"color": "grey", "size": "16px 16px",
			},
			"icon_position":       "right",
			"icon_expanded_angle": -180,
		},
		"border":           map[string]any{"color": "grey", "corner_radius": "5px"},
		"vertical_spacing": "4px",
		"padding":          "8px 8px 8px 8px",
		"elements": []map[string]any{
			{
				"tag": "markdown", "element_id": reasoningElementID(n),
				"content": reasoning, "text_size": "notation",
			},
		},
	}
}

// reasoningElementID is the streamable element id of iteration n's thinking.
// Feishu element ids allow letters/digits/underscore, ≤20 chars.
func reasoningElementID(n int) string {
	return fmt.Sprintf("think_%d", n)
}

// toolRow renders the iteration's tools as ONE LINE PER TOOL (user 2026-09-13:
// "一行一个工具并且加回emoji" — squeezing them onto one line made the command and
// the output run together into an unreadable blob).
func toolRow(ts []streamTool) string {
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
		lines = append(lines, toolChip(t))
	}
	return strings.Join(lines, "\n")
}

// toolChip renders one tool as `<emoji> **Shell** · ls -la · <状态>`.
func toolChip(t streamTool) string {
	icon, color, state := toolStatusLabel(t.status)
	label := t.label
	if label == "" {
		label = t.name
	}
	parts := []string{icon + " **" + label + "**"}
	if detail := toolDetailShort(t); detail != "" {
		parts = append(parts, detail)
	}
	parts = append(parts, fmt.Sprintf("<font color='%s'>%s</font>", color, state))
	return strings.Join(parts, " · ")
}

// toolDetailShort returns a SINGLE bounded line for a tool row.
//
// The row must stay readable: the engine's shell summary starts with the whole
// command followed by its output, so taking the first line unbounded (80 runes,
// cut mid-word) produced a wall of text ("Shell: cd … && for d in …
// ════ ferrite ════ … 完成"). 40 runes + ellipsis keeps the row scannable.
func toolDetailShort(t streamTool) string {
	line := toolDetail(t)
	if line == "" {
		return ""
	}
	if r := []rune(line); len(r) > streamCardToolRowRunes {
		return string(r[:streamCardToolRowRunes]) + "…"
	}
	return line
}

// pushReasoning updates iteration n's thinking element with the live text.
//
// 与正文同样走 partial_update_element 补丁：thinking 元素（think_n）是 append 出来
// 的，用 CardElement.Content 会被 300313 拒绝（真实 API 探针实测）—— 这正是
// "迭代 2+ 的思考完全不更新"的根因。
func (c *feishuStreamCard) pushReasoning(n int, text string) {
	if n <= 0 || text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	if n != c.lastReasonIter {
		c.lastReasonIter, c.lastReasoning, c.lastReasonAt = n, "", time.Time{}
	}
	if text == c.lastReasoning || time.Since(c.lastReasonAt) < streamCardMinInterval {
		return
	}
	c.iter(n).reasoning = text
	c.ensureElementsLocked(n)
	c.queueElementContentLocked(reasoningElementID(n), text)
	c.lastReasoning = text
	c.lastReasonAt = time.Now()

	// 标题里的字数改用 partial_update_element 刷新 —— 只改这一个元素的 header，
	// 不重排整卡。
	if time.Since(c.lastReasonCountAt) >= streamCardReasonCountMinInterval {
		c.patchThinkingCountLocked(n, text)
		c.lastReasonCountAt = time.Now()
	}
	c.submitOpsLocked()
}

// pushCurrentReasoning streams the current iteration's thinking, if any.
func (c *feishuStreamCard) pushCurrentReasoning() {
	c.mu.Lock()
	n := c.current
	var text string
	if it := c.iters[n]; it != nil {
		text = it.reasoning
	}
	c.mu.Unlock()
	c.pushReasoning(n, text)
}

// setStreamingMode toggles the card's streaming_mode via the settings API.
// The caller must hold c.mu.
func (c *feishuStreamCard) setStreamingMode(on bool) error {
	settings, err := json.Marshal(map[string]any{
		"config": map[string]any{"streaming_mode": on},
	})
	if err != nil {
		return fmt.Errorf("marshal card settings: %w", err)
	}
	c.seq++
	req := larkcardkit.NewSettingsCardReqBuilder().
		CardId(c.cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().
			Settings(string(settings)).
			Sequence(c.seq).
			Build()).
		Build()

	resp, err := c.client.Cardkit.V1.Card.Settings(context.Background(), req)
	if err != nil {
		return fmt.Errorf("stream card settings: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("stream card settings: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// cardPreview derives the chat-list preview from the final text.
func cardPreview(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return tools.TruncateHeadPreview(line, streamCardPreviewBytes)
	}
	return "回复完成"
}

// --- channel-side plumbing ---------------------------------------------------

// SendProgress implements channel.ProgressSender: the structured snapshot is the
// sole source for the card layout (iterations / thinking / tools).
func (f *FeishuChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if payload == nil || !f.isFeishuChat(chatID) {
		return
	}
	card, ok := f.ensureStreamCard(chatID)
	if !ok {
		f.streamCardFallbackAck(chatID)
		return
	}
	card.applyProgress(payload)
	// Layout first (creates the thinking panel + its element id on iteration
	// change), then stream the thinking text into that element.
	card.syncLayout(false)
	card.pushCurrentReasoning()
}

// SendStreamContent implements channel.ProgressSender: the live answer text of
// the current iteration, streamed into the card with a typewriter. The reasoning
// argument (thinking text) streams into its own element.
func (f *FeishuChannel) SendStreamContent(chatID, content, reasoning string) {
	if (content == "" && reasoning == "") || !f.isFeishuChat(chatID) {
		return
	}
	card, ok := f.ensureStreamCard(chatID)
	if !ok {
		return
	}

	iter := 1
	card.mu.Lock()
	if card.current > 0 {
		iter = card.current
	}
	if reasoning != "" {
		card.iter(iter).reasoning = reasoning
	}
	card.mu.Unlock()

	if reasoning != "" {
		card.pushReasoning(iter, reasoning)
	}
	if content != "" {
		card.pushText(iter, content)
	}
}

// ensureStreamCard returns the open card for chatID, creating (and posting) it on
// first use. The card is posted as a REPLY to the chat's latest inbound message —
// `im.message.reply` only needs that parent id, whereas `im.message.create`
// requires a receive_id_type that our synthetic chat ids cannot provide (Feishu
// rejects e.g. "chat_…" as neither an open_id nor a chat_id).
//
// Returns ok=false when streaming is unavailable (the caller then falls back to
// the legacy static card).
func (f *FeishuChannel) ensureStreamCard(chatID string) (*feishuStreamCard, bool) {
	if f.client == nil {
		return nil, false
	}
	f.streamCardsMu.Lock()
	if _, broken := f.streamCardsBroken[chatID]; broken {
		f.streamCardsMu.Unlock()
		return nil, false
	}
	if card, ok := f.streamCards[chatID]; ok {
		f.streamCardsMu.Unlock()
		return card, true
	}
	f.streamCardsMu.Unlock()

	card, err := newFeishuStreamCard(f.client, f.streamCardTitle(), chatID, f.lastInboundMessageID(chatID))
	if err != nil {
		// Per-chat only: never let one chat's failure silence progress elsewhere.
		log.WithError(err).WithField("chat_id", chatID).
			Warn("Feishu: progress card unavailable for this chat, falling back to plain replies")
		f.markStreamCardsBroken(chatID)
		return nil, false
	}
	f.streamCardsMu.Lock()
	// Another goroutine may have created one meanwhile — keep the first.
	// 让卡片能在"反复更新失败"时熔断**本会话**（见 recordCardFailureLocked）。
	card.channel = f
	card.chatID = chatID
	if existing, ok := f.streamCards[chatID]; ok {
		f.streamCardsMu.Unlock()
		return existing, true
	}
	f.streamCards[chatID] = card
	f.streamCardsMu.Unlock()
	return card, true
}

// lastInboundMessageID returns the reply target for a chat (empty when unknown).
func (f *FeishuChannel) lastInboundMessageID(chatID string) string {
	f.inboundMsgIDsMu.Lock()
	defer f.inboundMsgIDsMu.Unlock()
	return f.inboundMsgIDs[chatID]
}

// isFeishuChat reports whether chatID is a chat this channel can post to.
// The structured progress broadcast reaches EVERY channel with EVERY session's
// chatID (web/cli included) — those must be skipped silently. Creating cards for
// a web session's chat id (e.g. chat_XXXX: neither an open_id nor a chat_id)
// failed with Feishu 99992351 and used to latch the whole channel.
func (f *FeishuChannel) isFeishuChat(chatID string) bool {
	return f.lastInboundMessageID(chatID) != ""
}

// streamCardFallbackAck sends ONE short line per turn when the progress card is
// unavailable for a real chat. PreReplyNotify is false (the card is the only
// progress channel — an ack card would double-render), so without this fallback
// a broken card would leave the user with total silence until the turn ends.
func (f *FeishuChannel) streamCardFallbackAck(chatID string) {
	f.streamCardsMu.Lock()
	if f.streamCardAcked == nil {
		f.streamCardAcked = map[string]struct{}{}
	}
	if _, done := f.streamCardAcked[chatID]; done {
		f.streamCardsMu.Unlock()
		return
	}
	f.streamCardAcked[chatID] = struct{}{}
	f.streamCardsMu.Unlock()
	if id := f.lastInboundMessageID(chatID); id != "" {
		f.sendTextReply(chatID, id, "收到，正在处理…")
	}
}

// streamCardSend renders the turn's FINAL send through the open card (finalize).
// It returns (messageID, true) when handled, or ("", false) when there is no open
// card — the caller then sends the reply as a regular message.
//
// finalize 返回 error 说明**最终文本没落到卡上**（元素缺失 / API 拒绝 / 关流式失败）
// —— 此时必须返回 ok=false，让调用方走普通消息把回复发出去（Bug B：旧实现忽略
// finalize 的 error 一律返回 true ⇒ 卡不更新、最终答复也丢了，用户彻底收不到回复）。
func (f *FeishuChannel) streamCardSend(msg ch.OutboundMsg, content string, final bool) (string, bool) {
	if !final || f.client == nil {
		return "", false
	}
	card := f.takeStreamCard(msg.ChatID)
	if card == nil {
		return "", false
	}
	if err := card.finalize(content); err != nil {
		log.WithError(err).WithField("card_id", card.cardID).WithField("chat_id", msg.ChatID).
			Warn("Feishu: stream card finalize failed; sending the reply as a plain message")
		return "", false
	}
	return card.messageID, true
}

// takeStreamCard removes and returns the open stream card for chatID, if any.
func (f *FeishuChannel) takeStreamCard(chatID string) *feishuStreamCard {
	f.streamCardsMu.Lock()
	defer f.streamCardsMu.Unlock()
	card := f.streamCards[chatID]
	delete(f.streamCards, chatID)
	return card
}

// closeStreamCard finalizes and removes the open stream card for chatID, if any.
func (f *FeishuChannel) closeStreamCard(chatID string) {
	if card := f.takeStreamCard(chatID); card != nil {
		if err := card.finalize(""); err != nil {
			log.WithError(err).WithField("card_id", card.cardID).
				Warn("Feishu: stream card close failed")
		}
	}
}

// streamCardTitle returns the card title (kept for the entity metadata; the card
// itself no longer renders a header — user request 2026-09-13).
func (f *FeishuChannel) streamCardTitle() string {
	if v := f.botName.Load(); v != nil {
		if name, ok := v.(string); ok && name != "" {
			return name
		}
	}
	return "xbot"
}

// newStreamCardUUID returns a fresh idempotency id for one card operation.
func newStreamCardUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("xbot-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// derefString safely unwraps a *string.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
