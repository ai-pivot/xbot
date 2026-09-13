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
	"sort"
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
	streamCardElementID = "content"

	// streamCardSummary is the chat-list preview shown while streaming.
	streamCardSummary = "🔄 生成中…"

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
	// count in "💭 思考 N 字" can only change via a full-card update).
	lastReasonCountAt time.Time
}

// newFeishuStreamCard creates the card entity (streaming enabled) and posts it.
// It is the single creation path for both entry points (ack-free structured
// progress and the legacy final-reply fallback).
func newFeishuStreamCard(client *lark.Client, title, chatID, replyTo string) (*feishuStreamCard, error) {
	card := &feishuStreamCard{
		client: client,
		title:  title,
		iters:  map[int]*streamIteration{},
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

// pushText streams the current iteration's answer text (typewriter).
func (c *feishuStreamCard) pushText(n int, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || text == c.lastText {
		return
	}
	if time.Since(c.lastTextAt) < streamCardMinInterval {
		return
	}
	it := c.iter(n)
	it.content = text
	c.seq++
	if err := c.setElementContent(streamCardElementID, text, c.seq); err != nil {
		log.WithError(err).WithField("card_id", c.cardID).Debug("Feishu: stream text push failed")
		return
	}
	c.lastText = text
	c.lastTextAt = time.Now()
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
	if err := c.updateCard(true); err != nil {
		log.WithError(err).WithField("card_id", c.cardID).Debug("Feishu: stream card layout update failed")
		return
	}
	c.lastCardAt = time.Now()
}

// finalize renders the finished card and closes the streaming mode. Closing is
// mandatory and runs even when the full-card update fails: an open stream leaves
// the card showing "生成中" for up to 10 minutes.
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
	if err := c.updateCard(false); err != nil {
		log.WithError(err).WithField("card_id", c.cardID).
			Warn("Feishu: stream card final update failed, closing streaming mode")
		return c.setStreamingMode(false)
	}
	return nil
}

// renderCard builds the Card JSON 2.0 for the current state.
// The caller must hold c.mu (or hold exclusive ownership, e.g. at creation).
func (c *feishuStreamCard) renderCard(streaming bool) map[string]any {
	nums := make([]int, 0, len(c.iters))
	for n := range c.iters {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	elements := make([]map[string]any, 0, len(nums)*3)
	for _, n := range nums {
		it := c.iters[n]
		if it == nil {
			continue
		}
		if it.reasoning != "" {
			elements = append(elements, reasoningPanel(n, it.reasoning))
		}
		// 当前迭代的正文用可流式元素；已完结迭代用普通 markdown。
		if n == c.current {
			elements = append(elements, map[string]any{
				"tag": "markdown", "element_id": streamCardElementID,
				"content": it.content, "text_size": "normal",
			})
		} else if it.content != "" {
			elements = append(elements, map[string]any{
				"tag": "markdown", "content": it.content, "text_size": "normal",
			})
		}
		if len(it.tools) > 0 {
			// 同一迭代的连续工具**聚成一行**（Web 的 pill 组形态：一组 pill 排在一行），
			// 每项只显示工具名 + 状态，无 emoji、无 per-tool 折叠
			// （用户反馈 2026-09-13：`✅` + 4-5 行折叠是纯噪声）。
			elements = append(elements, map[string]any{
				"tag": "markdown", "content": toolRow(it.tools), "text_size": "notation",
			})
		}
	}
	if len(elements) == 0 {
		// Keep the streaming element present from the very first frame so the
		// typewriter has somewhere to land.
		elements = append(elements, map[string]any{
			"tag": "markdown", "element_id": streamCardElementID, "content": "",
		})
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

// reasoningPanel renders the folded thinking block (web parity: 💭 思考 N 字).
// The inner markdown element carries a per-iteration element id so the thinking
// text streams independently of the answer text.
func reasoningPanel(n int, reasoning string) map[string]any {
	title := fmt.Sprintf("💭 思考 %d 字", len([]rune(reasoning)))
	return map[string]any{
		"tag":      "collapsible_panel",
		"expanded": false,
		"header": map[string]any{
			"title": map[string]any{
				"tag": "plain_text", "content": title,
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

// setElementContent pushes the full text into one streamable element.
// The caller must hold c.mu.
func (c *feishuStreamCard) setElementContent(elementID, text string, seq int) error {
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(c.cardID).
		ElementId(elementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Content(text).
			Sequence(seq).
			Uuid(newStreamCardUUID()).
			Build()).
		Build()

	resp, err := c.client.Cardkit.V1.CardElement.Content(context.Background(), req)
	if err != nil {
		return fmt.Errorf("stream card content: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("stream card content: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// pushReasoning streams iteration n's thinking into its own element — thinking
// and the answer stream independently (Feishu's content API is per element).
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
	c.seq++
	if err := c.setElementContent(reasoningElementID(n), text, c.seq); err != nil {
		log.WithError(err).WithField("card_id", c.cardID).Debug("Feishu: thinking push failed")
		return
	}
	c.lastReasoning = text
	c.lastReasonAt = time.Now()

	// The thinking TEXT just streamed (typewriter); the panel title still shows the
	// old character count. A header can only change through a full-card update —
	// refresh it on its own throttle so "💭 思考 N 字" counts up live.
	if time.Since(c.lastReasonCountAt) >= streamCardReasonCountMinInterval {
		if err := c.updateCard(true); err != nil {
			log.WithError(err).WithField("card_id", c.cardID).
				Debug("Feishu: thinking count refresh failed")
			return
		}
		c.lastCardAt, c.lastReasonCountAt = time.Now(), time.Now()
	}
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

// updateCard replaces the whole card.
// The caller must hold c.mu.
func (c *feishuStreamCard) updateCard(streaming bool) error {
	cardJSON, err := json.Marshal(c.renderCard(streaming))
	if err != nil {
		return fmt.Errorf("marshal card: %w", err)
	}
	c.seq++
	req := larkcardkit.NewUpdateCardReqBuilder().
		CardId(c.cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Card(larkcardkit.NewCardBuilder().
				Type("card_json").
				Data(string(cardJSON)).
				Build()).
			Sequence(c.seq).
			Uuid(newStreamCardUUID()).
			Build()).
		Build()

	resp, err := c.client.Cardkit.V1.Card.Update(context.Background(), req)
	if err != nil {
		return fmt.Errorf("stream card update: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("stream card update: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
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
func (f *FeishuChannel) streamCardSend(msg ch.OutboundMsg, content string, final bool) (string, bool) {
	if !final || f.client == nil {
		return "", false
	}
	card := f.takeStreamCard(msg.ChatID)
	if card == nil {
		return "", false
	}
	if err := card.finalize(content); err != nil {
		log.WithError(err).WithField("card_id", card.cardID).
			Warn("Feishu: stream card finalize failed")
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
