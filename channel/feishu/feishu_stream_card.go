package feishu

// feishu_stream_card.go — CardKit streaming progress card.
//
// Feishu's agent UX (mirrored from the official larksuite/openclaw-lark card
// builder) streams the answer into a *card entity* instead of rebuilding and
// patching a whole card on every progress tick:
//
//	POST  cardkit/v1/cards                            create entity (streaming_mode=true)
//	POST  im/v1/messages  msg_type=interactive        send {type:card,data:{card_id}}
//	PUT   cardkit/v1/cards/:id/elements/:eid/content  push the ACCUMULATED text
//	PUT   cardkit/v1/cards/:id                        full-card update (timeline/finalize)
//
// Layout of the card (all Card JSON 2.0):
//
//	header   coloured title bar + subtitle (phase)
//	panel    collapsible_panel 「🛠️ 执行过程 · N 步」 — one step per tool trace
//	content  markdown element_id="content" — the streamed answer (typewriter)
//	status   small grey footer line
//
// The content API animates the delta between the previous and the new text as a
// typewriter — but only while the old text is a prefix of the new one. The agent
// hands us the accumulated text on every tick, so the prefix property holds
// during normal streaming and the card types itself out.
//
// The collapsible panel cannot be streamed (only the `content` element can), so
// it is synced with a full-card update, throttled independently. This mirrors
// the official implementation, which uses `card.update` for the tool panel and
// `cardElement.content` for the answer.
//
// Feishu requires the streaming mode to be closed explicitly: an open stream
// keeps the card stuck in its "generating" state until Feishu force-closes it
// after 10 minutes. finalize() therefore always closes.
//
// A card entity can be sent exactly once and only by the app that created it.

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
	"xbot/tools"
)

const (
	// streamCardElementID identifies the markdown element that receives the
	// streamed text. Feishu validates element_id: letters/digits/underscore,
	// must start with a letter, at most 20 characters.
	streamCardElementID = "content"

	// streamCardStatusElementID identifies the small grey footer line.
	streamCardStatusElementID = "status"

	// streamCardHeaderTemplate is the header colour of the progress card.
	streamCardHeaderTemplate = "blue"

	// streamCardPanelIconToken is the chevron of the collapsible panel
	// (rotated 180° when expanded). Token taken from the official card builder.
	streamCardPanelIconToken = "down-small-ccm_outlined"

	// streamCardSummary is the chat-list preview shown while streaming.
	streamCardSummary = "🔄 生成中…"

	// streamCardPreviewBytes bounds the chat-list preview taken from the final
	// text (rune-safe cut via tools.TruncateHeadPreview).
	streamCardPreviewBytes = 120
)

// streamCardMinInterval throttles content pushes. The content API allows
// 50 req/s, but every push costs a round-trip and drives a client-side
// typewriter animation; ~4/s reads as smooth without wasting the quota.
// Dropped frames are safe — the caller always hands us the full accumulated
// text, so the next push supersedes the dropped one and finalize() writes the
// final state anyway.
//
// A var (not a const) so tests can disable/force the throttle.
var streamCardMinInterval = 250 * time.Millisecond

// streamCardPanelMinInterval throttles the full-card updates that sync the
// timeline panel. Full-card updates are heavier than element pushes, so they
// run at a lower rate.
var streamCardPanelMinInterval = 800 * time.Millisecond

// streamCardTraceMarkers are the emoji the engine prefixes on its progress
// trace lines (`> ⏳ Shell(...) ...`, `> ✅ Shell (12ms)`, ...). A line is only
// treated as a timeline step when it starts with "> " AND carries one of these
// markers right after — a model quoting markdown (`> something`) must not be
// mistaken for a tool step.
var streamCardTraceMarkers = []string{"⏳", "✅", "❌", "⚠️", "📦", "🎭", "🔄"}

// feishuStreamCard drives one CardKit streaming card (one per turn).
// All methods are safe for concurrent use.
type feishuStreamCard struct {
	client    *lark.Client
	cardID    string
	title     string
	messageID string

	// mu serializes the card operations. It is held across the API calls so a
	// push cannot interleave with finalize() or with another push; the call
	// rate is throttled so the contention is trivial.
	mu          sync.Mutex
	seq         int
	lastBody    string
	lastTrace   string
	lastSent    time.Time
	lastPanelAt time.Time
	finished    bool
}

// newFeishuStreamCard creates the card entity with streaming enabled and the
// given initial text. The entity is invisible until send() posts it.
func newFeishuStreamCard(client *lark.Client, title, initialText string) (*feishuStreamCard, error) {
	trace, body := splitStreamCardText(initialText)
	data, err := json.Marshal(buildStreamCardJSON(streamCardView{
		title:     title,
		body:      body,
		trace:     trace,
		streaming: true,
	}))
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

	now := time.Now()
	return &feishuStreamCard{
		client:      client,
		cardID:      *resp.Data.CardId,
		title:       title,
		lastBody:    body,
		lastTrace:   strings.Join(trace, "\n"),
		lastSent:    now,
		lastPanelAt: now,
	}, nil
}

// send posts the card entity as an interactive message — fresh, or in reply to
// replyTo — and remembers the resulting message_id. A card entity can be sent
// exactly once.
func (c *feishuStreamCard) send(chatID, replyTo string) (string, error) {
	content, err := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]string{"card_id": c.cardID},
	})
	if err != nil {
		return "", fmt.Errorf("marshal card reference: %w", err)
	}

	msgID := ""
	if replyTo != "" {
		resp, err := c.client.Im.Message.Reply(context.Background(),
			larkim.NewReplyMessageReqBuilder().
				MessageId(replyTo).
				Body(larkim.NewReplyMessageReqBodyBuilder().
					MsgType("interactive").
					Content(string(content)).
					Build()).
				Build())
		if err != nil {
			return "", fmt.Errorf("send stream card reply: %w", err)
		}
		if !resp.Success() {
			return "", fmt.Errorf("send stream card reply: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data != nil {
			msgID = derefString(resp.Data.MessageId)
		}
	} else {
		resp, err := c.client.Im.Message.Create(context.Background(),
			larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(feishuReceiveIDType(chatID)).
				Body(larkim.NewCreateMessageReqBodyBuilder().
					ReceiveId(chatID).
					MsgType("interactive").
					Content(string(content)).
					Build()).
				Build())
		if err != nil {
			return "", fmt.Errorf("send stream card: %w", err)
		}
		if !resp.Success() {
			return "", fmt.Errorf("send stream card: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data != nil {
			msgID = derefString(resp.Data.MessageId)
		}
	}

	if msgID == "" {
		return "", fmt.Errorf("send stream card: empty message_id")
	}
	c.messageID = msgID
	return msgID, nil
}

// push streams the accumulated text into the card. It is a no-op when nothing
// changed, the card is already finalized, or the throttle window has not
// elapsed.
func (c *feishuStreamCard) push(text string) error {
	trace, body := splitStreamCardText(text)
	traceKey := strings.Join(trace, "\n")

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}

	// Answer text: element-level push (typewriter).
	if body != c.lastBody && time.Since(c.lastSent) >= streamCardMinInterval {
		c.seq++
		if err := c.setContent(body, c.seq); err != nil {
			return err
		}
		c.lastBody = body
		c.lastSent = time.Now()
	}

	// Timeline: only the full-card update can change the collapsible panel.
	if traceKey != c.lastTrace && time.Since(c.lastPanelAt) >= streamCardPanelMinInterval {
		if err := c.updateCard(c.lastBody, trace, true); err != nil {
			return err
		}
		c.lastTrace = traceKey
		c.lastPanelAt = time.Now()
	}
	return nil
}

// finalize renders the finished card and closes the streaming mode. Closing is
// mandatory and runs even when the full-card update fails: an open stream
// leaves the card showing "生成中" for up to 10 minutes.
func (c *feishuStreamCard) finalize(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}
	c.finished = true

	if text != "" {
		trace, body := splitStreamCardText(text)
		if body != "" {
			c.lastBody = body
		}
		// The final reply is the model's answer, not the accumulated progress
		// log — it usually carries no trace lines. Keep the timeline collected
		// during the turn unless the final text actually brings its own.
		if len(trace) > 0 {
			c.lastTrace = strings.Join(trace, "\n")
		}
	}

	// One full-card update writes the final text AND closes streaming mode, and
	// collapses the timeline into its finished form.
	if err := c.updateCard(c.lastBody, traceOf(c.lastTrace), false); err != nil {
		log.WithError(err).WithField("card_id", c.cardID).
			Warn("Feishu: stream card final update failed, closing streaming mode")
		return c.setStreamingMode(false)
	}
	return nil
}

// setContent pushes the full text into the streaming element.
// The caller must hold c.mu.
func (c *feishuStreamCard) setContent(text string, seq int) error {
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(c.cardID).
		ElementId(streamCardElementID).
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

// updateCard replaces the whole card (timeline + answer + chrome).
// The caller must hold c.mu.
func (c *feishuStreamCard) updateCard(body string, trace []string, streaming bool) error {
	cardJSON, err := json.Marshal(buildStreamCardJSON(streamCardView{
		title:     c.title,
		body:      body,
		trace:     trace,
		streaming: streaming,
	}))
	if err != nil {
		return fmt.Errorf("marshal final card: %w", err)
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

// streamCardView is the rendered state of the progress card.
type streamCardView struct {
	title     string
	body      string   // assistant text (streamed into element_id="content")
	trace     []string // engine progress trace lines (timeline panel)
	streaming bool
}

// buildStreamCardJSON builds the Card JSON 2.0 for the progress card.
func buildStreamCardJSON(v streamCardView) map[string]any {
	elements := []map[string]any{}
	if panel := buildTimelinePanel(v.trace, v.streaming); panel != nil {
		elements = append(elements, panel)
	}
	elements = append(elements,
		map[string]any{
			"tag":        "markdown",
			"element_id": streamCardElementID,
			"content":    v.body,
			"text_size":  "normal",
		},
		map[string]any{"tag": "hr"},
		map[string]any{
			"tag":        "markdown",
			"element_id": streamCardStatusElementID,
			"content":    streamCardStatusLine(v.trace, v.streaming),
			"text_size":  "notation",
		},
	)

	subtitle := "实时进度 · 流式输出"
	summary := streamCardSummary
	if !v.streaming {
		subtitle = "回复完成"
		summary = cardPreview(v.body)
	}

	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			// update_multi MUST stay true: the streaming content API rejects
			// exclusive (non-shared) cards.
			"update_multi":   true,
			"width_mode":     "fill",
			"streaming_mode": v.streaming,
			"streaming_config": map[string]any{
				"print_frequency_ms": map[string]int{"default": 30, "android": 30, "ios": 30, "pc": 30},
				"print_step":         map[string]int{"default": 2, "android": 2, "ios": 2, "pc": 2},
				"print_strategy":     "fast",
			},
			"summary": map[string]any{"content": summary},
		},
		"header": map[string]any{
			"template": streamCardHeaderTemplate,
			"title":    map[string]any{"tag": "plain_text", "content": "🤖 " + v.title},
			"subtitle": map[string]any{"tag": "plain_text", "content": subtitle},
		},
		"body": map[string]any{"elements": elements},
	}
}

// buildTimelinePanel builds the collapsible timeline of tool steps, mirroring
// the official card builder: collapsed while idle, expanded while a step runs,
// collapsed again on completion.
func buildTimelinePanel(trace []string, streaming bool) map[string]any {
	if len(trace) == 0 && !streaming {
		return nil
	}

	title := "🛠️ 等待工具执行"
	if n := len(trace); n > 0 {
		title = fmt.Sprintf("🛠️ 执行过程 · %d 步", n)
	}

	steps := make([]map[string]any, 0, len(trace))
	for _, line := range trace {
		steps = append(steps, buildTimelineStep(line))
	}

	return map[string]any{
		"tag":      "collapsible_panel",
		"expanded": streaming && hasRunningStep(trace),
		"header": map[string]any{
			"title": map[string]any{
				"tag":        "plain_text",
				"content":    title,
				"text_color": "grey",
				"text_size":  "notation",
			},
			"vertical_align": "center",
			"icon": map[string]any{
				"tag":   "standard_icon",
				"token": streamCardPanelIconToken,
				"color": "grey",
				"size":  "16px 16px",
			},
			"icon_position":       "right",
			"icon_expanded_angle": -180,
		},
		"border":           map[string]any{"color": "grey", "corner_radius": "5px"},
		"vertical_spacing": "4px",
		"padding":          "8px 8px 8px 8px",
		"elements":         steps,
	}
}

// buildTimelineStep renders one trace line as a step row:
// `**label** · <font color='green'>完成</font>`.
func buildTimelineStep(line string) map[string]any {
	label, color, state := parseTimelineStep(line)
	return map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":       "lark_md",
			"content":   fmt.Sprintf("%s · <font color='%s'>%s</font>", label, color, state),
			"text_size": "notation",
		},
	}
}

// streamCardStatusLine renders the small grey footer.
func streamCardStatusLine(trace []string, streaming bool) string {
	steps := fmt.Sprintf("%d 步", len(trace))
	if streaming {
		return fmt.Sprintf("<font color='grey'>⚡ 流式输出中 · 已完成 %s</font>", steps)
	}
	return fmt.Sprintf("<font color='grey'>✅ 已完成 · %s</font>", steps)
}

// parseTimelineStep normalizes one engine trace line into (label, colour,
// state). Unknown shapes fall back to the raw line with a neutral state.
func parseTimelineStep(line string) (label, color, state string) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ">"))
	for _, m := range streamCardTraceMarkers {
		if !strings.HasPrefix(trimmed, m) {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, m))
		switch m {
		case "⏳", "🔄":
			return "🔄 " + body, "turquoise", "运行中"
		case "✅":
			return "✅ " + body, "green", "完成"
		case "❌":
			return "❌ " + body, "red", "失败"
		case "⚠️":
			return "⚠️ " + body, "orange", "警告"
		default: // 📦 🎭 — context management notices
			return m + " " + body, "grey", "信息"
		}
	}
	return line, "grey", ""
}

// hasRunningStep reports whether any trace line is still in flight.
func hasRunningStep(trace []string) bool {
	for _, line := range trace {
		if _, _, state := parseTimelineStep(line); state == "运行中" {
			return true
		}
	}
	return false
}

// splitStreamCardText separates the engine's progress trace lines from the
// assistant's visible text. Trace lines are the engine's own contract
// (engine_run_tools.go / engine_run.go): a "> " prefix immediately followed by
// one of streamCardTraceMarkers. Everything else is answer text.
func splitStreamCardText(text string) (trace []string, body string) {
	if text == "" {
		return nil, ""
	}
	var bodyLines []string
	for _, line := range strings.Split(text, "\n") {
		if isTraceLine(line) {
			trace = append(trace, line)
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	return trace, strings.TrimSpace(strings.Join(bodyLines, "\n"))
}

// isTraceLine reports whether a line is an engine progress trace line.
func isTraceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, ">") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
	for _, m := range streamCardTraceMarkers {
		if strings.HasPrefix(rest, m) {
			return true
		}
	}
	return false
}

// traceOf splits the joined trace key back into lines.
func traceOf(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, "\n")
}

// cardPreview derives the chat-list preview from the final text: the first
// non-empty line, rune-safe truncated.
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

// streamCardSend renders one progress/final send through the per-chat CardKit
// streaming card. It returns (messageID, true) when the streaming card handled
// the send, or ("", false) to let the caller fall back to the static card path
// (streaming unavailable, or a final reply with no open card).
func (f *FeishuChannel) streamCardSend(msg ch.OutboundMsg, content string, final bool) (string, bool) {
	if f.client == nil {
		return "", false
	}

	f.streamCardsMu.Lock()
	if f.streamCardBroken {
		f.streamCardsMu.Unlock()
		return "", false
	}
	card := f.streamCards[msg.ChatID]
	f.streamCardsMu.Unlock()

	if final {
		// No open card means this turn never produced an ack/progress (e.g. an
		// opted-out reply) — nothing to finalize, use the normal path.
		if card == nil {
			return "", false
		}
		if err := card.finalize(content); err != nil {
			log.WithError(err).WithField("card_id", card.cardID).
				Warn("Feishu: stream card finalize failed")
		}
		f.streamCardsMu.Lock()
		delete(f.streamCards, msg.ChatID)
		f.streamCardsMu.Unlock()
		return card.messageID, true
	}

	// A missing update_message_id means this is the first send of the turn. Any
	// card still open belongs to a previous turn that never got a final reply
	// (e.g. it was cancelled) — close it so it does not keep streaming.
	if card != nil && msg.Metadata["update_message_id"] == "" {
		if err := card.finalize(""); err != nil {
			log.WithError(err).WithField("card_id", card.cardID).
				Warn("Feishu: stale stream card finalize failed")
		}
		f.streamCardsMu.Lock()
		delete(f.streamCards, msg.ChatID)
		f.streamCardsMu.Unlock()
		card = nil
	}

	if card == nil {
		newCard, err := newFeishuStreamCard(f.client, f.streamCardTitle(), content)
		if err != nil {
			log.WithError(err).Warn("Feishu: stream card unavailable, using static card")
			f.streamCardsMu.Lock()
			f.streamCardBroken = true
			f.streamCardsMu.Unlock()
			return "", false
		}
		msgID, err := newCard.send(msg.ChatID, msg.Metadata["message_id"])
		if err != nil {
			log.WithError(err).Warn("Feishu: stream card send failed, using static card")
			f.streamCardsMu.Lock()
			f.streamCardBroken = true
			f.streamCardsMu.Unlock()
			return "", false
		}
		f.streamCardsMu.Lock()
		f.streamCards[msg.ChatID] = newCard
		f.streamCardsMu.Unlock()
		return msgID, true
	}

	if err := card.push(content); err != nil {
		log.WithError(err).WithField("card_id", card.cardID).
			Warn("Feishu: stream card update failed")
	}
	return card.messageID, true
}

// takeStreamCard removes and returns the open stream card for chatID, if any.
// Used when a fully-built card message supersedes the streaming card.
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

// streamCardTitle returns the card header title (the bot's display name).
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

// feishuReceiveIDType mirrors the receive_id_type inference used when sending
// messages: group chats ("oc_…") are addressed by chat_id, single chats by
// open_id.
func feishuReceiveIDType(chatID string) string {
	if strings.HasPrefix(chatID, "oc_") {
		return "chat_id"
	}
	return "open_id"
}
