package serverapp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// structured_callbacks.go — 结构化消息 WebCallbacks 的真实实现。
//
// 数据流（sendmessage-structured §7/§8/§9）：
//   1. HTTP 请求经 resolveAPISession 鉴权（callbacks 收到的 sel 已通过校验）；
//   2. 解析到会话库（TenantSession.SessionService()，绝不读主库）；
//   3. 幂等落库（AppendStructuredMessage / AnswerAskUser）；
//   4. 投递：answer/decision/command/text 进入 agent 插队优先队列
//      （运行中在 iteration 边界注入，空闲时开启新 turn）；
//   5. 返回可渲染消息，由 web 层广播 structured_message。

// structuredPriority 返回 kind 对应的插队优先级（设计 §9.2）。
func structuredPriority(kind string) int {
	switch kind {
	case "answer", "decision":
		return agent.StructuredPriorityHigh
	case "text", "command":
		return agent.StructuredPriorityNormal
	default:
		return agent.StructuredPriorityBackground
	}
}

// structuredDeliverable 判断该 kind 是否需要进入 agent run 循环。
func structuredDeliverable(kind string) bool {
	switch kind {
	case "answer", "decision", "command", "text":
		return true
	default:
		return false
	}
}

// structuredContent 生成人读文本（LLM 上下文/UI 缺省内容）。
func structuredContent(kind string, payload map[string]any, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if payload == nil {
		return ""
	}
	if s, ok := payload["text"].(string); ok && s != "" {
		return s
	}
	switch kind {
	case "ask_user":
		var parts []string
		if qs, ok := payload["questions"].([]any); ok {
			for _, q := range qs {
				if m, ok := q.(map[string]any); ok {
					if s, ok := m["question"].(string); ok && s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	case "report":
		if s, _ := payload["summary"].(string); s != "" {
			return s
		}
	case "decision":
		verdict, _ := payload["verdict"].(string)
		comments, _ := payload["comments"].(string)
		label := map[string]string{"approve": "准", "reject": "驳", "modify": "改"}[verdict]
		if label == "" {
			label = verdict
		}
		if comments != "" {
			return label + "：" + comments
		}
		return label
	}
	return ""
}

func structuredRole(kind string) string {
	switch kind {
	case "ask_user", "report":
		return "assistant"
	default:
		return "user"
	}
}

// buildStructuredEnvelope 生成完整信封 JSON（服务端填充 from/created_at）。
func buildStructuredEnvelope(senderID string, sel web.SessionSelector, input protocol.StructuredMessageInput) map[string]any {
	return map[string]any{
		"schema":          "xbot.message/v1",
		"kind":            input.Kind,
		"from":            map[string]any{"type": "user", "id": senderID, "session": sel.Channel + ":" + sel.ChatID},
		"to":              input.To,
		"correlation_id":  input.CorrelationID,
		"reply_to":        input.ReplyTo,
		"idempotency_key": input.IdempotencyKey,
		"payload":         input.Payload,
		"text":            structuredContent(input.Kind, input.Payload, input.Text),
		"created_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// toProtocolStructured 把存储行转换为前端可渲染形态。
func toProtocolStructured(m sqlite.StructuredMessage) *protocol.StructuredMessage {
	payload := map[string]any{}
	envelope := map[string]any{}
	if m.StructuredJSON != "" {
		_ = json.Unmarshal([]byte(m.StructuredJSON), &envelope)
		if p, ok := envelope["payload"].(map[string]any); ok {
			payload = p
		}
	}
	out := &protocol.StructuredMessage{
		MessageID:     m.ID,
		Kind:          m.Kind,
		CorrelationID: m.CorrelationID,
		ReplyTo:       m.ReplyTo,
		Direction:     m.Direction,
		Answered:      m.Answered,
		Cancelled:     m.Cancelled,
		Text:          m.Content,
		Payload:       payload,
		Sender:        protocol.SenderInfo{Type: m.SenderType, ID: m.SenderID, Name: m.SenderName},
		CreatedAt:     m.CreatedAt,
		Envelope:      envelope,
	}
	if out.Text == "" {
		out.Text = structuredContent(m.Kind, payload, "")
	}
	return out
}

// structuredCallbacks 构造 WebCallbacks 的结构化消息五件套。
func structuredCallbacks(ag *agent.Agent) (appendFn func(string, web.SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error), answerFn func(string, web.SessionSelector, protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error), inboxFn func(web.SessionSelector, int64, int) ([]protocol.StructuredMessage, bool, int64, error), countFn func(web.SessionSelector) (int, error), listFn func(web.SessionSelector, string, int64, int64, int) ([]protocol.StructuredMessage, bool, int64, error)) {
	sessionOf := func(sel web.SessionSelector) (*sqlite.SessionService, int64, error) {
		sess, err := ag.MultiSession().GetOrCreateSession(sel.Channel, sel.ChatID)
		if err != nil {
			return nil, 0, err
		}
		return sess.SessionService(), sess.TenantID(), nil
	}

	appendFn = func(senderID string, sel web.SessionSelector, input protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, err
		}
		// kind=answer 与 /api/answer 共用同一套 CAS 闭环（见 answerViaMessage）：
		// 否则 /api/message 写一行 answer 却不会把 ask_user 置 answered，收件箱永远不收敛。
		if input.Kind == "answer" {
			return answerViaMessage(senderID, sel, svc, tenantID, ag, input)
		}
		createdAt, err := parseStructuredCreatedAt(input.CreatedAt)
		if err != nil {
			return nil, false, err
		}
		envelope := buildStructuredEnvelope(senderID, sel, input)
		raw, _ := json.Marshal(envelope)
		senderName := senderID
		stored := sqlite.StructuredMessage{
			ChatID:         sel.ChatID,
			Kind:           input.Kind,
			CorrelationID:  input.CorrelationID,
			ReplyTo:        input.ReplyTo,
			IdempotencyKey: input.IdempotencyKey,
			StructuredJSON: string(raw),
			Direction:      "in",
			Role:           structuredRole(input.Kind),
			Content:        structuredContent(input.Kind, input.Payload, input.Text),
			SenderType:     "user",
			SenderID:       senderID,
			SenderName:     senderName,
			CreatedAt:      createdAt,
		}
		var id int64
		var created bool
		if input.Kind == "decision" {
			// 批红闭环：目标行必须存在且未批红/未取消（CAS）。
			// reply_to 是权威目标；兼容老客户端只用 payload.target_message_id。
			stored.ReplyTo = structuredFirstNonEmpty(input.ReplyTo, structuredPayloadString(input.Payload, "target_message_id"))
			id, created, err = svc.AppendDecision(tenantID, stored)
		} else {
			id, created, err = svc.AppendStructuredMessage(tenantID, stored)
		}
		if err != nil {
			return nil, false, err
		}
		row, err := svc.GetStructuredMessageByID(tenantID, id)
		if err != nil {
			return nil, false, err
		}
		if created && structuredDeliverable(input.Kind) {
			ag.DeliverStructuredMessage(&agent.StructuredQueueItem{
				ID:            fmt.Sprintf("structured:%d", id),
				Channel:       sel.Channel,
				ChatID:        sel.ChatID,
				SenderID:      senderID,
				SenderName:    senderName,
				Content:       row.Content,
				Kind:          input.Kind,
				CorrelationID: input.CorrelationID,
				Priority:      structuredPriority(input.Kind),
			})
		}
		return toProtocolStructured(*row), !created, nil
	}

	answerFn = func(senderID string, sel web.SessionSelector, input protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, err
		}
		return applyStructuredAnswer(senderID, sel, svc, tenantID, ag, input)
	}

	inboxFn = func(sel web.SessionSelector, cursor int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, 0, err
		}
		rows, more, next, err := svc.ListInbox(tenantID, cursor, limit)
		if err != nil {
			return nil, false, 0, err
		}
		out := make([]protocol.StructuredMessage, 0, len(rows))
		for _, r := range rows {
			out = append(out, *toProtocolStructured(r))
		}
		return out, more, next, nil
	}

	countFn = func(sel web.SessionSelector) (int, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return 0, err
		}
		return svc.CountInbox(tenantID)
	}

	listFn = func(sel web.SessionSelector, kind string, cursor, afterID int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, 0, err
		}
		// afterID > 0 ⇒ 增量补齐（SSE 重连），按 id 升序；否则走分页（id DESC）。
		var rows []sqlite.StructuredMessage
		var more bool
		var next int64
		if afterID > 0 {
			rows, more, next, err = svc.ListStructuredMessagesAfter(tenantID, kind, afterID, limit)
		} else {
			rows, more, next, err = svc.ListStructuredMessages(tenantID, kind, cursor, limit)
		}
		if err != nil {
			return nil, false, 0, err
		}
		out := make([]protocol.StructuredMessage, 0, len(rows))
		for _, r := range rows {
			out = append(out, *toProtocolStructured(r))
		}
		return out, more, next, nil
	}

	// ── 把同一份落地实现注入给工具层 ─────────────────────────────────────
	// SendMessage(kind = report/ask_user/answer/decision) 走的就是这个 appendFn：
	// agent 在自己会话里说"批了"→ 工具层调它 → 落到目标（部门）会话 + CAS。
	// 共用闭包而不是另写一份，是为了让"web 点按钮"和"自然语言批红"永远同一语义。
	tools.SetStructuredAppend(func(senderID, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error) {
		// 逻辑地址（agent:role/instance）→ 物理会话 key（web_web-4/role:instance）。
		// 不做这一步就会把批红写进一个新建的空壳会话：2026-10-22 实测事故。
		realChatID, rerr := ag.ResolveStructuredTarget(channel, chatID)
		if rerr != nil {
			return 0, false, rerr
		}
		msg, dup, err := appendFn(senderID, web.SessionSelector{Channel: channel, ChatID: realChatID}, in)
		if err != nil || msg == nil {
			return 0, dup, err
		}
		return msg.MessageID, dup, nil
	})
	return
}

// structuredFirstNonEmpty 返回第一个非空字符串（reply_to 与旧字段兼容用）。
func structuredFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// structuredPayloadString 读取 payload 的字符串字段（缺省空串）。
func structuredPayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

// parseStructuredCreatedAt 解析可选的 created_at（导入/回填/演示用）。// 空串 ⇒ 零值（由存储层取 now）；非法格式 ⇒ 错误（web 层映射 400）。
func parseStructuredCreatedAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid created_at %q (want RFC3339)", raw)
}

// answerViaMessage 让 POST /api/message 的 kind=answer 与 POST /api/answer 共用
// 同一套 CAS 闭环（方案 a）：目标不存在 → 404；已答/已取消 → 409；成功后目标
// ask_user 置 answered=1，收件箱立即收敛。
//
// 与 /api/answer 的唯一区别是寻址方式：这里用 reply_to（message_id）定位问题行，
// correlation_id 从目标行派生（客户端显式传了就必须一致）。
func answerViaMessage(senderID string, sel web.SessionSelector, svc *sqlite.SessionService, tenantID int64, ag *agent.Agent, input protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
	targetID, perr := strconv.ParseInt(strings.TrimSpace(input.ReplyTo), 10, 64)
	if perr != nil || targetID <= 0 {
		return nil, false, fmt.Errorf("invalid: reply_to must be the ask_user message_id for kind=answer")
	}
	target, err := svc.GetStructuredMessageByID(tenantID, targetID)
	if err != nil {
		return nil, false, err // ErrStructuredNotFound → 404
	}
	if target.Kind != "ask_user" {
		return nil, false, fmt.Errorf("invalid: reply_to %d points to kind=%s, want ask_user", targetID, target.Kind)
	}
	if strings.TrimSpace(target.CorrelationID) == "" {
		return nil, false, fmt.Errorf("invalid: ask_user %d has no correlation_id", targetID)
	}
	if input.CorrelationID != "" && input.CorrelationID != target.CorrelationID {
		return nil, false, fmt.Errorf("invalid: correlation_id %q does not match target %q", input.CorrelationID, target.CorrelationID)
	}
	answers, cancelled, err := answersFromPayload(input.Payload)
	if err != nil {
		return nil, false, err
	}
	return applyStructuredAnswer(senderID, sel, svc, tenantID, ag, protocol.StructuredAnswerInput{
		CorrelationID:  target.CorrelationID,
		Answers:        answers,
		Cancelled:      cancelled,
		IdempotencyKey: input.IdempotencyKey,
		Channel:        sel.Channel,
		ChatID:         sel.ChatID,
	})
}

// answersFromPayload 把 /api/message 的 payload.answers 转成协议结构。
func answersFromPayload(payload map[string]any) ([]protocol.StructuredAnswer, bool, error) {
	if payload == nil {
		return nil, false, fmt.Errorf("invalid: payload.answers is required for kind=answer")
	}
	cancelled, _ := payload["cancelled"].(bool)
	rawAnswers, ok := payload["answers"].([]any)
	if !ok {
		if cancelled && payload["answers"] == nil {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("invalid: payload.answers must be an array")
	}
	out := make([]protocol.StructuredAnswer, 0, len(rawAnswers))
	for i, raw := range rawAnswers {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("invalid: payload.answers[%d] must be an object", i)
		}
		a := protocol.StructuredAnswer{}
		a.QuestionID, _ = m["question_id"].(string)
		a.Text, _ = m["text"].(string)
		if opts, ok := m["option_ids"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					a.OptionIDs = append(a.OptionIDs, s)
				}
			}
		}
		out = append(out, a)
	}
	return out, cancelled, nil
}

// applyStructuredAnswer 是 /api/answer 与 /api/message(kind=answer) 的共同闭环：
// 校验答案形状 → CAS 置位目标 ask_user → 写 answer 行 → 投递 agent。
func applyStructuredAnswer(senderID string, sel web.SessionSelector, svc *sqlite.SessionService, tenantID int64, ag *agent.Agent, input protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error) {
	// 校验答案形状（multi_select=false 时 option_ids ≤ 1 等）。
	if err := validateAskUserAnswers(svc, tenantID, input); err != nil {
		return nil, false, err
	}
	payload := map[string]any{
		"answers":     input.Answers,
		"cancelled":   input.Cancelled,
		"answered_by": senderID,
	}
	raw, _ := json.Marshal(payload)
	text := answerText(input)
	id, _, created, err := svc.AnswerAskUser(tenantID, sel.ChatID, input.CorrelationID,
		text, string(raw), input.IdempotencyKey, senderID, senderID, input.Cancelled)
	if err != nil {
		return nil, false, err
	}
	stored, err := svc.GetStructuredMessageByID(tenantID, id)
	if err != nil {
		return nil, false, err
	}
	if created {
		ag.DeliverStructuredMessage(&agent.StructuredQueueItem{
			ID:            fmt.Sprintf("answer:%d", id),
			Channel:       sel.Channel,
			ChatID:        sel.ChatID,
			SenderID:      senderID,
			SenderName:    senderID,
			Content:       text,
			Kind:          "answer",
			CorrelationID: input.CorrelationID,
			Priority:      agent.StructuredPriorityHigh,
		})
	}
	return toProtocolStructured(*stored), !created, nil
}

// validateAskUserAnswers 按 ask_user 信封校验回答。
func validateAskUserAnswers(svc *sqlite.SessionService, tenantID int64, input protocol.StructuredAnswerInput) error {
	if input.Cancelled {
		return nil
	}
	rows, _, _, err := svc.ListStructuredMessages(tenantID, "ask_user", 0, 200)
	if err != nil {
		return err
	}
	var target *sqlite.StructuredMessage
	for i := range rows {
		if rows[i].CorrelationID == input.CorrelationID {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		// 不存在时交给 AnswerAskUser 返回 NOT_FOUND（保持单一判定点）。
		return nil
	}
	questions := askQuestions(target.StructuredJSON)
	byID := make(map[string]askQuestionSpec, len(questions))
	for _, q := range questions {
		byID[q.ID] = q
	}
	for _, ans := range input.Answers {
		q, ok := byID[ans.QuestionID]
		if !ok {
			return fmt.Errorf("invalid answer: unknown question_id %q", ans.QuestionID)
		}
		if !q.MultiSelect && len(ans.OptionIDs) > 1 {
			return fmt.Errorf("invalid answer: question %q is single-select", ans.QuestionID)
		}
		if ans.Text != "" && !q.AllowOther {
			return fmt.Errorf("invalid answer: question %q does not allow custom text", ans.QuestionID)
		}
	}
	return nil
}

type askQuestionSpec struct {
	ID          string
	MultiSelect bool
	AllowOther  bool
	Required    bool
}

func askQuestions(structuredJSON string) []askQuestionSpec {
	var env struct {
		Payload struct {
			Questions []struct {
				ID          string `json:"id"`
				MultiSelect bool   `json:"multi_select"`
				AllowOther  bool   `json:"allow_other"`
				Required    bool   `json:"required"`
			} `json:"questions"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(structuredJSON), &env) != nil {
		return nil
	}
	out := make([]askQuestionSpec, 0, len(env.Payload.Questions))
	for i, q := range env.Payload.Questions {
		id := q.ID
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		out = append(out, askQuestionSpec{ID: id, MultiSelect: q.MultiSelect, AllowOther: q.AllowOther, Required: q.Required})
	}
	return out
}

func answerText(input protocol.StructuredAnswerInput) string {
	if input.Cancelled {
		return "已取消"
	}
	var parts []string
	for _, a := range input.Answers {
		piece := a.QuestionID + ":"
		if len(a.OptionIDs) > 0 {
			piece += strings.Join(a.OptionIDs, ",")
		}
		if a.Text != "" {
			if len(a.OptionIDs) > 0 {
				piece += " "
			}
			piece += a.Text
		}
		parts = append(parts, piece)
	}
	return strings.Join(parts, "; ")
}
