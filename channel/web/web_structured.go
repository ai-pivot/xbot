package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

// web_structured.go — 结构化消息 + 卡片交互后端（sendmessage-structured 设计）。
//
//	POST /api/message   {kind,payload,correlation_id?,reply_to?,idempotency_key?,to?}
//	POST /api/answer    {correlation_id,answers[],cancelled?,idempotency_key?}
//	GET  /api/inbox     ?cursor=&limit=          未回答 ask_user（收件箱）
//	GET  /api/messages  ?kind=&cursor=&limit=    按 kind 过滤
//
// 权限：全部走 resolveAPISession（跨租户/未知会话 404/403，与既有 REST 一致）。
// 写入成功后立即向该会话广播 structured_message，前端按 message_id 去重。

// maxStructuredPayloadBytes 是 payload 的体积上限（设计 §4：256 KiB）。
const maxStructuredPayloadBytes = 256 * 1024

// handleStructuredMessageBody 是 handleMessage 已经解码 body 后的入口 —— 复用
// 同一个 WSClientMessage，避免重复读取 r.Body。
func (wc *WebChannel) handleStructuredMessageBody(w http.ResponseWriter, r *http.Request, request protocol.WSClientMessage) {
	wc.handleStructuredInput(w, r, protocol.StructuredMessageInput{
		Kind:           request.Kind,
		Payload:        request.Payload,
		CorrelationID:  request.CorrelationID,
		ReplyTo:        request.ReplyTo,
		IdempotencyKey: request.IdempotencyKey,
		To:             request.To,
		Text:           firstNonEmpty(request.Text, request.Content),
		Channel:        request.Channel,
		ChatID:         request.ChatID,
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (wc *WebChannel) handleStructuredInput(w http.ResponseWriter, r *http.Request, input protocol.StructuredMessageInput) {
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = protocolStructuredKindText
	}
	if !isStructuredKind(kind) {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid kind")
		return
	}
	input.Kind = kind
	if err := validateStructuredPayload(input); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if wc.callbacks.StructuredAppend == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "structured messages unavailable")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, input.Channel, input.ChatID)
	if !ok {
		return
	}
	msg, duplicate, err := wc.callbacks.StructuredAppend(senderID, sel, input)
	if err != nil {
		writeStructuredError(w, err)
		return
	}
	wc.broadcastStructured(sel, msg)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"message_id": msg.MessageID,
		"duplicate":  duplicate,
		"message":    msg,
	})
}

func (wc *WebChannel) handleStructuredAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input protocol.StructuredAnswerInput
	if err := decodeJSONBody(r, &input, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if input.CorrelationID == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "correlation_id is required")
		return
	}
	if wc.callbacks.StructuredAnswer == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "structured messages unavailable")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, input.Channel, input.ChatID)
	if !ok {
		return
	}
	msg, duplicate, err := wc.callbacks.StructuredAnswer(senderID, sel, input)
	if err != nil {
		writeStructuredError(w, err)
		return
	}
	wc.broadcastStructured(sel, msg)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"message_id": msg.MessageID,
		"duplicate":  duplicate,
		"message":    msg,
	})
}

func (wc *WebChannel) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, r.URL.Query().Get("channel"), r.URL.Query().Get("chat_id"))
	if !ok {
		return
	}
	if wc.callbacks.StructuredInbox == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "structured messages unavailable")
		return
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, hasMore, next, err := wc.callbacks.StructuredInbox(sel, cursor, limit)
	if err != nil {
		writeStructuredError(w, err)
		return
	}
	count := len(items)
	if wc.callbacks.StructuredCount != nil {
		if n, cerr := wc.callbacks.StructuredCount(sel); cerr == nil {
			count = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"items":       items,
		"has_more":    hasMore,
		"next_cursor": next,
		"unanswered":  count,
	})
}

func (wc *WebChannel) handleStructuredMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && !isStructuredKind(kind) {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid kind")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, r.URL.Query().Get("channel"), r.URL.Query().Get("chat_id"))
	if !ok {
		return
	}
	if wc.callbacks.StructuredMessages == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "structured messages unavailable")
		return
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, hasMore, next, err := wc.callbacks.StructuredMessages(sel, kind, cursor, limit)
	if err != nil {
		writeStructuredError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"items":       items,
		"has_more":    hasMore,
		"next_cursor": next,
	})
}

// broadcastStructured 把一条结构化消息推给该会话的所有连接。前端按
// message_id 去重（重复事件是 no-op）。
func (wc *WebChannel) broadcastStructured(sel SessionSelector, msg *protocol.StructuredMessage) {
	if msg == nil {
		return
	}
	wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{
		Type:       protocol.MsgTypeStructuredMessage,
		Channel:    sel.Channel,
		ChatID:     sel.ChatID,
		Structured: msg,
	})
}

const protocolStructuredKindText = "text"

var structuredKinds = map[string]bool{
	"text": true, "ask_user": true, "answer": true, "report": true,
	"decision": true, "command": true, "event": true,
}

func isStructuredKind(kind string) bool { return structuredKinds[kind] }

// validateStructuredPayload 校验 payload 体积与 kind 必需字段。
func validateStructuredPayload(input protocol.StructuredMessageInput) error {
	if input.Payload != nil {
		raw, err := json.Marshal(input.Payload)
		if err != nil {
			return errors.New("invalid payload")
		}
		if len(raw) > maxStructuredPayloadBytes {
			return errors.New("payload exceeds 256KB limit")
		}
	}
	switch input.Kind {
	case "text":
		if strings.TrimSpace(input.Text) == "" && payloadString(input.Payload, "text") == "" {
			return errors.New("text is required for kind=text")
		}
	case "ask_user":
		if _, ok := input.Payload["questions"]; !ok {
			return errors.New("payload.questions is required for kind=ask_user")
		}
	case "answer":
		if _, ok := input.Payload["answers"]; !ok && input.Payload["cancelled"] != true {
			return errors.New("payload.answers is required for kind=answer")
		}
	case "decision":
		verdict := payloadString(input.Payload, "verdict")
		if verdict != "approve" && verdict != "reject" && verdict != "modify" {
			return errors.New("payload.verdict must be approve/reject/modify")
		}
	case "report":
		if _, ok := input.Payload["summary"]; !ok {
			return errors.New("payload.summary is required for kind=report")
		}
	}
	return nil
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

func writeStructuredError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sqlite.ErrStructuredNotFound):
		jsonErrorResponse(w, http.StatusNotFound, "not found")
	case errors.Is(err, sqlite.ErrAskUserAnswered):
		jsonErrorResponse(w, http.StatusConflict, "already answered")
	case errors.Is(err, sqlite.ErrAskUserCancelled):
		jsonErrorResponse(w, http.StatusConflict, "cancelled")
	case strings.Contains(strings.ToLower(err.Error()), "access denied"):
		jsonErrorResponse(w, http.StatusForbidden, "access denied")
	case strings.Contains(strings.ToLower(err.Error()), "invalid"):
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
	default:
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
	}
}
