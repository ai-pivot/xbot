package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"xbot/bus"
	log "xbot/logger"
	"xbot/protocol"
)

func postOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next(w, r)
	}
}

func (wc *WebChannel) authenticatedPOST(next http.HandlerFunc) http.HandlerFunc {
	return postOnly(wc.authMiddleware(next))
}

func decodeJSONBody(r *http.Request, dst any, allowEmpty bool) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func legacyRequest(r *http.Request, method string, query url.Values, body []byte) *http.Request {
	clone := r.Clone(r.Context())
	clone.Method = method
	requestURL := *r.URL
	requestURL.RawQuery = query.Encode()
	clone.URL = &requestURL
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	return clone
}

type sessionBody struct {
	Channel string `json:"channel,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
}

func (wc *WebChannel) handleMessage(w http.ResponseWriter, r *http.Request) {
	var request protocol.WSClientMessage
	if err := decodeJSONBody(r, &request, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	identity := wc.inboundIdentityFromRequest(r)
	if request.ChatID != "" && request.Channel == "" {
		request.Channel = wc.inferAPISessionChannel(identity.SenderID, request.ChatID)
	}
	sel, msgID, ts, turnID, queued, interrupted, err := wc.dispatchUserMessage(r.Context(), identity, request)
	if err != nil {
		writeInboundError(w, err)
		return
	}
	resp := map[string]any{
		"chat_id":    sel.ChatID,
		"channel":    sel.Channel,
		"message_id": msgID,
		"timestamp":  ts.UnixMilli(),
		"queued":     queued,
	}
	if interrupted {
		// ⚡ INTERJECT: the message was delivered into the ACTIVE turn as a
		// synthetic user_interrupt tool result (no new turn, no queueing, no
		// user message row). No turn_id is allocated — the frontend renders
		// it inside the live turn (purple interject card on the tool timeline)
		// and shows a transient "⚡ delivered" chip.
		resp["interrupted"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if queued {
		// QUEUED: the chat was already busy; the message will be handled after
		// the current turn. turn_id IS already allocated (admitToMsgCh calls
		// ss.nextTurnID() at queue-admission time) — return it so the frontend
		// can bind the optimistic user row immediately (no need to wait for
		// turn_started). The turn_id is stable: queued messages are FIFO,
		// no insertion possible.
		if turnID != 0 {
			resp["turn_id"] = turnID
		}
	} else if turnID == 0 && !isSlashCommand(request.Content) {
		// INSERTED (non-queued): the response MUST carry a non-zero turn_id —
		// EXCEPT slash commands (e.g. /help, /new), which are handled
		// concurrently by the chatWorker and have no user-message turn
		// semantics (their turn_id is legitimately 0; the frontend does not
		// bind a turn for them). A 0 turn_id on a real user message means the
		// queue-admission allocation failed upstream — the frontend binds the
		// optimistic user row from this value and a 0 would break turn order
		// (replies rendering above the user msg). Fail fast.
		log.WithFields(log.Fields{
			"channel": sel.Channel,
			"chat_id": sel.ChatID,
			"msg_id":  msgID,
		}).Error("handleMessage: turn_id is 0 for a user message — refusing to return an unbound user message")
		writeInboundError(w, fmt.Errorf("internal error: message accepted without a turn_id"))
		return
	} else {
		// Non-queued, non-command: turn_id must be non-zero (guaranteed by
		// admitToMsgCh for user messages); commands keep turn_id omitted.
		if turnID != 0 {
			resp["turn_id"] = turnID
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleQueueList returns the pending queue snapshot for a session (the Web
// Staging Tray data source). Items are admitted-but-not-yet-dequeued messages
// (user messages, bg notifications) in strict FIFO order.
func (wc *WebChannel) handleQueueList(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.GetQueueState == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "queue state not available")
		return
	}
	var body sessionBody
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	identity := wc.inboundIdentityFromRequest(r)
	if body.ChatID != "" && body.Channel == "" {
		body.Channel = wc.inferAPISessionChannel(identity.SenderID, body.ChatID)
	}
	sel, err := wc.resolveInboundSession(r.Context(), identity, body.Channel, body.ChatID)
	if err != nil {
		writeInboundError(w, err)
		return
	}
	items := wc.callbacks.GetQueueState(sel.Channel, sel.ChatID)
	if items == nil {
		items = []protocol.QueueItemPayload{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chat_id": sel.ChatID,
		"channel": sel.Channel,
		"items":   items,
	})
}

// handleQueueCancel cancels a queued-but-unstarted message (Staging Tray ✕).
// The message is skipped at dequeue time — no turn runs for it.
func (wc *WebChannel) handleQueueCancel(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.CancelQueued == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "queue cancel not available")
		return
	}
	var body struct {
		Channel string `json:"channel,omitempty"`
		ChatID  string `json:"chat_id,omitempty"`
		MsgID   string `json:"msg_id"`
	}
	if err := decodeJSONBody(r, &body, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.MsgID == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "msg_id is required")
		return
	}
	identity := wc.inboundIdentityFromRequest(r)
	if body.ChatID != "" && body.Channel == "" {
		body.Channel = wc.inferAPISessionChannel(identity.SenderID, body.ChatID)
	}
	sel, err := wc.resolveInboundSession(r.Context(), identity, body.Channel, body.ChatID)
	if err != nil {
		writeInboundError(w, err)
		return
	}
	ok := wc.callbacks.CancelQueued(sel.Channel, sel.ChatID, body.MsgID)
	// "Not queued" (already dequeued/processing) is NOT an error — it means
	// the message was briefly in the queue (admitted while busy=true) but was
	// already dequeued (cancel completed → chatProcessLoop picked it up →
	// ss.busy cleared → message is now being processed). The user's intent
	// (remove from queue) is already satisfied. Return 200 with
	// already_processing=true so the frontend can distinguish (optional).
	writeJSON(w, http.StatusOK, map[string]any{
		"chat_id":            sel.ChatID,
		"channel":            sel.Channel,
		"cancelled":          ok,
		"already_processing": !ok,
	})
}

// isSlashCommand reports whether a message content is a slash command (e.g.
// /help, /new). Command messages are handled by the chatWorker's command
// branch — they have no user-message turn semantics, so their turn_id may
// legitimately be 0 and is omitted from the API response.
func isSlashCommand(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), "/")
}

func (wc *WebChannel) handleCancel(w http.ResponseWriter, r *http.Request) {
	var request sessionBody
	if err := decodeJSONBody(r, &request, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	identity := wc.inboundIdentityFromRequest(r)
	if request.ChatID != "" && request.Channel == "" {
		request.Channel = wc.inferAPISessionChannel(identity.SenderID, request.ChatID)
	}
	sel, err := wc.dispatchCancel(r.Context(), identity, request.Channel, request.ChatID)
	if err != nil {
		writeInboundError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat_id": sel.ChatID, "channel": sel.Channel})
}

func (wc *WebChannel) handleAskUserRespond(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Channel    string            `json:"channel,omitempty"`
		ChatID     string            `json:"chat_id,omitempty"`
		QuestionID string            `json:"question_id,omitempty"`
		Answer     string            `json:"answer,omitempty"`
		Answers    map[string]string `json:"answers,omitempty"`
		Cancelled  bool              `json:"cancelled,omitempty"`
	}
	if err := decodeJSONBody(r, &request, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if request.Answers == nil && (request.QuestionID != "" || request.Answer != "") {
		questionID := request.QuestionID
		if questionID == "" {
			questionID = "1"
		}
		request.Answers = map[string]string{questionID: request.Answer}
	}
	identity := wc.inboundIdentityFromRequest(r)
	if request.ChatID != "" && request.Channel == "" {
		request.Channel = wc.inferAPISessionChannel(identity.SenderID, request.ChatID)
	}
	sel, err := wc.dispatchAskUserResponse(r.Context(), identity, request.Channel, request.ChatID, protocol.AskUserResponse{
		Answers: request.Answers, Cancelled: request.Cancelled,
	})
	if err != nil {
		writeInboundError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat_id": sel.ChatID, "channel": sel.Channel})
}

func writeInboundError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errEmptyMessage):
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errInboundUnavailable), errors.Is(err, bus.ErrInboundQueueFull):
		jsonErrorResponse(w, http.StatusServiceUnavailable, err.Error())
	case strings.Contains(err.Error(), "access denied"):
		jsonErrorResponse(w, http.StatusForbidden, err.Error())
	default:
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
	}
}

func (wc *WebChannel) handleRPC(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.RPCHandler == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "RPC service unavailable")
		return
	}
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}
	if err := decodeJSONBody(r, &request, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if request.Method == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "method is required")
		return
	}
	identity := wc.rpcIdentityFromRequest(r)
	if status, err := wc.authorizeRESTRPC(r, identity, request.Method, request.Params); err != nil {
		jsonErrorResponse(w, status, err.Error())
		return
	}
	if len(request.Params) == 0 || string(request.Params) == "null" {
		request.Params = json.RawMessage(`{}`)
	}
	result, err := wc.callbacks.RPCHandler(request.Method, request.Params, identity)
	if err != nil {
		jsonErrorResponse(w, restRPCErrorStatus(err), err.Error())
		return
	}
	if len(result) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(result))
}

func (wc *WebChannel) rpcIdentityFromRequest(r *http.Request) RPCIdentity {
	identity := wc.inboundIdentityFromRequest(r)
	return RPCIdentity{
		SenderID: identity.SenderID,
	}
}

func (wc *WebChannel) authorizeRESTRPC(r *http.Request, identity RPCIdentity, method string, params json.RawMessage) (int, error) {
	// Multi-user removal: every web login IS the operator (password auth is
	// the trust boundary) — all REST RPC methods are authorized.
	return 0, nil
}
func restRPCErrorStatus(err error) int {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"access denied", "admin only", "requires admin", "not your"} {
		if strings.Contains(message, marker) {
			return http.StatusForbidden
		}
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return http.StatusBadRequest
	}
	for _, marker := range []string{"unknown rpc method", "invalid ", " is required", " requires "} {
		if strings.Contains(message, marker) {
			return http.StatusBadRequest
		}
	}
	return http.StatusInternalServerError
}

func (wc *WebChannel) handleSearchPOST(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeJSONBody(r, &body, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := url.Values{"q": []string{body.Query}}
	if body.Limit > 0 {
		query.Set("limit", strconv.Itoa(body.Limit))
	}
	wc.handleSearch(w, legacyRequest(r, http.MethodGet, query, nil))
}

func (wc *WebChannel) handleFsListPOST(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path       string `json:"path,omitempty"`
		ShowHidden bool   `json:"show_hidden,omitempty"`
	}
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := url.Values{"path": []string{body.Path}}
	if body.ShowHidden {
		query.Set("showHidden", "true")
	}
	wc.handleFsList(w, legacyRequest(r, http.MethodGet, query, nil))
}

func (wc *WebChannel) handleFsReadPOST(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Raw  bool   `json:"raw,omitempty"`
	}
	if err := decodeJSONBody(r, &body, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := url.Values{"path": []string{body.Path}}
	legacy := legacyRequest(r, http.MethodGet, query, nil)
	if body.Raw {
		wc.handleFsRaw(w, legacy)
		return
	}
	wc.handleFsRead(w, legacy)
}

func (wc *WebChannel) handleFsSearchPOST(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query      string `json:"query"`
		Path       string `json:"path,omitempty"`
		Limit      int    `json:"limit,omitempty"`
		ShowHidden bool   `json:"show_hidden,omitempty"`
	}
	if err := decodeJSONBody(r, &body, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := url.Values{"q": []string{body.Query}, "path": []string{body.Path}}
	if body.Limit > 0 {
		query.Set("limit", strconv.Itoa(body.Limit))
	}
	if body.ShowHidden {
		query.Set("showHidden", "true")
	}
	wc.handleFsSearch(w, legacyRequest(r, http.MethodGet, query, nil))
}

func (wc *WebChannel) handleSettingsPOST(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.RPCHandler == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "settings service unavailable")
		return
	}
	var body updateSettingsRequest
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	identity := wc.rpcIdentityFromRequest(r)
	if len(body.Settings) == 0 {
		params, _ := json.Marshal(map[string]string{"namespace": "web"})
		result, err := wc.callbacks.RPCHandler("get_settings", params, identity)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": json.RawMessage(result)})
		return
	}
	for key, value := range body.Settings {
		params, _ := json.Marshal(map[string]string{
			"namespace": "web",
			"key":       key,
			"value":     fmt.Sprint(value),
		})
		if _, err := wc.callbacks.RPCHandler("set_setting", params, identity); err != nil {
			jsonErrorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (wc *WebChannel) handleLLMConfigPOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var request struct {
		Action     string `json:"action,omitempty"`
		Provider   string `json:"provider,omitempty"`
		SubID      string `json:"sub_id,omitempty"`
		Model      string `json:"model,omitempty"`
		MaxContext *int   `json:"max_context,omitempty"`
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &request) != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	senderID := senderIDFromContext(r.Context())
	action := request.Action
	if action == "" && request.Provider == "" && request.Model != "" {
		action = "set_model"
	}
	if action == "" && request.MaxContext != nil {
		action = "set_max_context"
	}
	switch action {
	case "set_model", "model":
		if wc.callbacks.LLMSet == nil {
			jsonErrorResponse(w, http.StatusServiceUnavailable, "not configured")
			return
		}
		if request.Model == "" {
			jsonErrorResponse(w, http.StatusBadRequest, "model is required")
			return
		}
		if err := wc.callbacks.LLMSet(senderID, request.SubID, request.Model); err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	case "get_max_context":
		maxContext := 0
		if wc.callbacks.LLMGetMaxContext != nil {
			maxContext = wc.callbacks.LLMGetMaxContext(senderID, request.SubID, request.Model)
		}
		writeJSON(w, http.StatusOK, map[string]any{"max_context": maxContext})
		return
	case "set_max_context":
		if wc.callbacks.LLMSetMaxContext == nil {
			jsonErrorResponse(w, http.StatusServiceUnavailable, "not configured")
			return
		}
		if request.MaxContext == nil || *request.MaxContext < 0 {
			jsonErrorResponse(w, http.StatusBadRequest, "max_context must be >= 0")
			return
		}
		if err := wc.callbacks.LLMSetMaxContext(senderID, request.SubID, request.Model, *request.MaxContext); err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	method := http.MethodGet
	if request.Action == "delete" {
		method = http.MethodDelete
	} else if len(bytes.TrimSpace(body)) > 0 && request.Action != "get" {
		method = http.MethodPost
	}
	wc.handleLLMConfig(w, legacyRequest(r, method, nil, body))
}

func (wc *WebChannel) handleChatsListPOST(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel,omitempty"`
	}
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := make(url.Values)
	if body.Channel != "" {
		query.Set("channel", body.Channel)
	}
	wc.handleChats(w, legacyRequest(r, http.MethodGet, query, nil))
}

func (wc *WebChannel) handleChatsCreatePOST(w http.ResponseWriter, r *http.Request) {
	wc.handleChats(w, r)
}

func (wc *WebChannel) handleChatsReorderPOST(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Channel string         `json:"channel,omitempty"`
		Orders  map[string]int `json:"orders"`
	}
	if err := decodeJSONBody(r, &body, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Orders) == 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "orders is required")
		return
	}
	for chatID, order := range body.Orders {
		if order < 0 {
			jsonErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid sort_order %d for %s", order, chatID))
			return
		}
	}
	channel := body.Channel
	if channel == "" {
		channel = "web"
	}
	if wc.callbacks.ChatReorder == nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "reorder not available")
		return
	}
	if err := wc.callbacks.ChatReorder(senderID, channel, body.Orders); err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (wc *WebChannel) handleChatSwitchPOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var request struct {
		Channel string `json:"channel,omitempty"`
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &request) != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := make(url.Values)
	if request.Channel != "" {
		query.Set("channel", request.Channel)
	}
	wc.handleChatSwitch(w, legacyRequest(r, http.MethodPost, query, body))
}

func (wc *WebChannel) handleChatDeletePOST(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel,omitempty"`
	}
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := make(url.Values)
	if body.Channel != "" {
		query.Set("channel", body.Channel)
	}
	wc.handleChatDelete(w, legacyRequest(r, http.MethodDelete, query, nil))
}

func (wc *WebChannel) handleSessionTreePOST(w http.ResponseWriter, r *http.Request) {
	// Read optional pagination params from the POST body and forward them as
	// query params to the legacy GET handler. Absent limit → full list (limit=-1),
	// preserving backward compatibility for callers that don't paginate.
	var req struct {
		Offset int `json:"offset,omitempty"`
		Limit  int `json:"limit,omitempty"`
	}
	body, _ := io.ReadAll(r.Body)
	if len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	query := url.Values{}
	if req.Offset > 0 {
		query.Set("offset", strconv.Itoa(req.Offset))
	}
	if req.Limit != 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	wc.handleSessionTree(w, legacyRequest(r, http.MethodGet, query, nil))
}

func (wc *WebChannel) handleRunnersListPOST(w http.ResponseWriter, r *http.Request) {
	wc.handleRunners(w, legacyRequest(r, http.MethodGet, nil, nil))
}

func (wc *WebChannel) handleRunnersCreatePOST(w http.ResponseWriter, r *http.Request) {
	wc.handleRunners(w, r)
}

func (wc *WebChannel) handleRunnerDeletePOST(w http.ResponseWriter, r *http.Request) {
	wc.handleRunnerByName(w, legacyRequest(r, http.MethodDelete, nil, nil))
}

func (wc *WebChannel) handleRunnerActivePOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var request struct {
		Name string `json:"name,omitempty"`
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &request) != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	method := http.MethodGet
	if request.Name != "" {
		method = http.MethodPut
	}
	wc.handleRunnerActive(w, legacyRequest(r, method, nil, body))
}

func (wc *WebChannel) handleChannelsPOST(w http.ResponseWriter, r *http.Request) {
	wc.handleChannels(w, legacyRequest(r, http.MethodGet, nil, nil))
}

func (wc *WebChannel) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, body.Channel, body.ChatID)
	if !ok {
		return
	}
	tokenUsage, err := wc.sessionTokenUsage(wc.rpcIdentityFromRequest(r), sel)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Status endpoint is lightweight: only token_usage + cwd.
	// Cron tasks and background tasks have their own WS RPCs
	// (list_cron_jobs, list_bg_tasks) to avoid bundling large payloads
	// (e.g. completed bg task output ~1MB) into a frequently-polled endpoint.
	cwd := ""
	if wc.callbacks.GetCWD != nil {
		cwd, err = wc.callbacks.GetCWD(senderID, sel)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	todos := []protocol.TodoItem{}
	if wc.callbacks.GetTodos != nil {
		todos, err = wc.callbacks.GetTodos(senderID, sel)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token_usage": tokenUsage,
		"cwd":         cwd,
		"todos":       todos,
	})
}

// handleCronListPOST returns cron jobs for the session's canonical user.
func (wc *WebChannel) handleCronListPOST(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, body.Channel, body.ChatID)
	if !ok {
		return
	}
	tasks := any([]any{})
	if wc.callbacks.CronTasks != nil {
		var err error
		tasks, err = wc.callbacks.CronTasks(senderID, sel)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// handleTasksListPOST returns background shell tasks for the session.
func (wc *WebChannel) handleTasksListPOST(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, body.Channel, body.ChatID)
	if !ok {
		return
	}
	backgroundTasks := any([]any{})
	if wc.callbacks.BackgroundTasks != nil {
		var err error
		backgroundTasks, err = wc.callbacks.BackgroundTasks(senderID, sel)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"background_tasks": backgroundTasks})
}

func (wc *WebChannel) sessionTokenUsage(identity RPCIdentity, sel SessionSelector) (map[string]any, error) {
	var usage protocol.ContextUsage
	if wc.callbacks.RPCHandler != nil {
		params, _ := json.Marshal(map[string]string{"channel": sel.Channel, "chat_id": sel.ChatID})
		result, err := wc.callbacks.RPCHandler("get_context_usage", params, identity)
		if err != nil {
			return nil, err
		}
		if len(result) > 0 {
			if err := json.Unmarshal(result, &usage); err != nil {
				return nil, err
			}
		}
	}
	source := "none"
	if usage.Available {
		source = "api"
	}
	legacyUsagePercent := 0.0
	if usage.UsagePercent != nil {
		legacyUsagePercent = *usage.UsagePercent
	}
	return map[string]any{
		"available":          usage.Available,
		"prompt_tokens":      usage.PromptTokens,
		"completion_tokens":  usage.CompletionTokens,
		"max_context_tokens": usage.MaxContextTokens,
		"usage_percent":      usage.UsagePercent,
		"model":              usage.Model,
		"subscription_id":    usage.SubscriptionID,
		"subscription_name":  usage.SubscriptionName,
		// Legacy REST aliases retained for existing API consumers.
		"max_tokens": usage.MaxContextTokens,
		"usage_pct":  legacyUsagePercent,
		"source":     source,
	}, nil
}
