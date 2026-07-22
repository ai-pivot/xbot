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

func sessionQuery(body sessionBody) url.Values {
	query := make(url.Values)
	if body.Channel != "" {
		query.Set("channel", body.Channel)
	}
	if body.ChatID != "" {
		query.Set("chat_id", body.ChatID)
	}
	return query
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
	sel, err := wc.dispatchUserMessage(r.Context(), identity, request)
	if err != nil {
		writeInboundError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat_id": sel.ChatID, "channel": sel.Channel})
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
		SenderID:        identity.SenderID,
		CanonicalUserID: identity.CanonicalUserID,
		CanonicalRole:   identity.CanonicalRole,
	}
}

var nonAdminRESTRPCMethods = map[string]struct{}{
	"get_context_mode":                   {},
	"set_cwd":                            {},
	"get_settings":                       {},
	"list_command_names":                 {},
	"set_setting":                        {},
	"get_default_model":                  {},
	"get_user_max_context":               {},
	"set_user_max_context":               {},
	"get_user_max_output_tokens":         {},
	"set_user_max_output_tokens":         {},
	"get_user_thinking_mode":             {},
	"set_user_thinking_mode":             {},
	"get_llm_concurrency":                {},
	"set_llm_concurrency":                {},
	"list_models":                        {},
	"list_all_models":                    {},
	"list_all_model_entries":             {},
	"refresh_model_entries":              {},
	"clear_proxy_llm":                    {},
	"list_subscriptions":                 {},
	"get_default_subscription":           {},
	"get_session_subscription":           {},
	"get_context_usage":                  {},
	"add_subscription":                   {},
	"update_subscription":                {},
	"remove_subscription":                {},
	"rename_subscription":                {},
	"set_default_subscription":           {},
	"set_subscription_enabled":           {},
	"select_model":                       {},
	"set_default_model":                  {},
	"update_per_model_config":            {},
	"set_model_enabled":                  {},
	"remove_model":                       {},
	"upsert_model":                       {},
	"get_user_token_usage":               {},
	"get_daily_token_usage":              {},
	"get_agent_session_dump":             {},
	"get_agent_session_dump_by_full_key": {},
	"get_session_messages":               {},
	"get_active_progress":                {},
	"kill_bg_task":                       {},
	"plugin_widgets":                     {},
	"genui_action":                       {},
}

func (wc *WebChannel) authorizeRESTRPC(r *http.Request, identity RPCIdentity, method string, params json.RawMessage) (int, error) {
	senderID := identity.SenderID
	if identity.CanonicalRole == "admin" || (identity.CanonicalRole == "" && wc.isAdmin(r.Context(), senderID)) {
		return 0, nil
	}
	if _, ok := nonAdminRESTRPCMethods[method]; !ok {
		return http.StatusForbidden, fmt.Errorf("RPC method requires admin access")
	}
	if method == "plugin_widgets" {
		var request struct {
			ChatID string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return http.StatusBadRequest, fmt.Errorf("invalid params: %w", err)
		}
		if request.ChatID == "" {
			return http.StatusBadRequest, fmt.Errorf("chat_id is required")
		}
		if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, "cli", request.ChatID) {
			return http.StatusForbidden, fmt.Errorf("access denied")
		}
	}
	if method == "get_session_subscription" {
		var request sessionBody
		if err := json.Unmarshal(params, &request); err != nil {
			return http.StatusBadRequest, fmt.Errorf("invalid params: %w", err)
		}
		if request.ChatID == "" {
			return http.StatusBadRequest, fmt.Errorf("chat_id is required")
		}
		if request.Channel == "" {
			request.Channel = "cli"
		}
		if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, request.Channel, request.ChatID) {
			return http.StatusForbidden, fmt.Errorf("access denied")
		}
	}
	if method == "get_active_progress" {
		var request sessionBody
		if err := json.Unmarshal(params, &request); err != nil {
			return http.StatusBadRequest, fmt.Errorf("invalid params: %w", err)
		}
		if request.ChatID == "" {
			return http.StatusBadRequest, fmt.Errorf("chat_id is required")
		}
		if request.Channel == "" {
			request.Channel = "web"
		}
		if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, request.Channel, request.ChatID) {
			return http.StatusForbidden, fmt.Errorf("access denied")
		}
	}
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

func (wc *WebChannel) handleHistoryPOST(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	wc.handleHistory(w, legacyRequest(r, http.MethodGet, sessionQuery(body), nil))
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
	wc.handleSessionTree(w, legacyRequest(r, http.MethodGet, nil, nil))
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

func (wc *WebChannel) handleIdentitiesListPOST(w http.ResponseWriter, r *http.Request) {
	wc.handleIdentities(w, legacyRequest(r, http.MethodGet, nil, nil))
}

func (wc *WebChannel) handleUnlinkIdentityPOST(w http.ResponseWriter, r *http.Request) {
	wc.handleUnlinkIdentity(w, legacyRequest(r, http.MethodDelete, nil, nil))
}

func (wc *WebChannel) handleAdminUsersListPOST(w http.ResponseWriter, r *http.Request) {
	wc.handleAdminUsers(w, legacyRequest(r, http.MethodGet, nil, nil))
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
	writeJSON(w, http.StatusOK, map[string]any{
		"token_usage": tokenUsage,
		"cwd":         cwd,
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
