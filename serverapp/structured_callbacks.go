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
func structuredCallbacks(ag *agent.Agent) (appendFn func(string, web.SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error), answerFn func(string, web.SessionSelector, protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error), inboxFn func(web.SessionSelector, int64, int) ([]protocol.StructuredMessage, bool, int64, error), countFn func(web.SessionSelector) (int, error), listFn func(web.SessionSelector, string, int64, int) ([]protocol.StructuredMessage, bool, int64, error)) {
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
		envelope := buildStructuredEnvelope(senderID, sel, input)
		raw, _ := json.Marshal(envelope)
		senderName := senderID
		id, created, err := svc.AppendStructuredMessage(tenantID, sqlite.StructuredMessage{
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
		})
		if err != nil {
			return nil, false, err
		}
		stored, err := svc.GetStructuredMessageByID(tenantID, id)
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
				Content:       stored.Content,
				Kind:          input.Kind,
				CorrelationID: input.CorrelationID,
				Priority:      structuredPriority(input.Kind),
			})
		}
		// 批红：把被批的报告卡标记为已回答（持久化，刷新后仍显示「已批红」）。
		if created && input.Kind == "decision" {
			if rawID, ok := input.Payload["target_message_id"].(string); ok {
				if targetID, perr := strconv.ParseInt(rawID, 10, 64); perr == nil && targetID > 0 {
					_ = svc.MarkStructuredAnswered(tenantID, targetID)
				}
			}
		}
		return toProtocolStructured(*stored), !created, nil
	}

	answerFn = func(senderID string, sel web.SessionSelector, input protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, err
		}
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

	listFn = func(sel web.SessionSelector, kind string, cursor int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, 0, err
		}
		rows, more, next, err := svc.ListStructuredMessages(tenantID, kind, cursor, limit)
		if err != nil {
			return nil, false, 0, err
		}
		out := make([]protocol.StructuredMessage, 0, len(rows))
		for _, r := range rows {
			out = append(out, *toProtocolStructured(r))
		}
		return out, more, next, nil
	}
	return
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
