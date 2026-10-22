package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"xbot/protocol"
	"xbot/storage"
)

// The unified push layer's HTTP surface. Handlers only parse/validate the
// request shape and delegate to the injected callbacks — they never name a
// provider (FCM / WebPush / APNs / Feishu).

type pushRegisterRequest struct {
	Provider   string            `json:"provider"`
	Platform   string            `json:"platform"`
	Address    string            `json:"address"`
	Metadata   map[string]string `json:"metadata"`
	DeviceName string            `json:"device_name"`
	AppVersion string            `json:"app_version"`
}

func (wc *WebChannel) handlePushRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.PushRegister == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "push is not enabled")
		return
	}
	var req pushRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Platform = strings.TrimSpace(req.Platform)
	req.Address = strings.TrimSpace(req.Address)
	if req.Provider == "" || req.Address == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "provider and address are required")
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}

	target := storage.PushTarget{
		UserID:     int64(userIDFromContext(r.Context())),
		SenderID:   senderID,
		Provider:   req.Provider,
		Platform:   req.Platform,
		Address:    req.Address,
		Metadata:   req.Metadata,
		DeviceName: strings.TrimSpace(req.DeviceName),
		AppVersion: strings.TrimSpace(req.AppVersion),
	}
	if err := wc.callbacks.PushRegister(senderID, target.UserID, target); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

type pushUnregisterRequest struct {
	Provider string `json:"provider"`
	Address  string `json:"address"`
}

func (wc *WebChannel) handlePushUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.PushUnregister == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "push is not enabled")
		return
	}
	var req pushUnregisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Address = strings.TrimSpace(req.Address)
	if req.Provider == "" || req.Address == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "provider and address are required")
		return
	}
	if err := wc.callbacks.PushUnregister(senderID, int64(userIDFromContext(r.Context())), req.Provider, req.Address); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

type pushPresenceRequest struct {
	Provider string `json:"provider"`
	Address  string `json:"address"`
	Active   bool   `json:"active"`
}

func (wc *WebChannel) handlePushPresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if senderIDFromContext(r.Context()) == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.PushPresence == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "push is not enabled")
		return
	}
	var req pushPresenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Address = strings.TrimSpace(req.Address)
	if req.Provider == "" || req.Address == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "provider and address are required")
		return
	}
	wc.callbacks.PushPresence(req.Provider, req.Address, req.Active)
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (wc *WebChannel) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.PushTest == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "push is not enabled")
		return
	}
	sent, err := wc.callbacks.PushTest(senderID)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent})
}

// handlePushConfig returns the public push configuration (WebPush VAPID key).
// Always 200: a disabled WebPush provider answers {enabled:false} so the web
// client can hide the toggle instead of treating it as an error.
func (wc *WebChannel) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if senderIDFromContext(r.Context()) == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	cfg := map[string]any{"webpush": map[string]any{"enabled": false}}
	if wc.callbacks.PushConfig != nil {
		cfg = wc.callbacks.PushConfig()
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handlePushSettings reads/saves the persisted push configuration. GET is
// sanitized (VAPID private key masked); POST validates, persists, and hot-swaps
// the push layer without a server restart.
func (wc *WebChannel) handlePushSettings(w http.ResponseWriter, r *http.Request) {
	if senderIDFromContext(r.Context()) == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.PushSettings == nil {
			jsonErrorResponse(w, http.StatusServiceUnavailable, "push is not enabled")
			return
		}
		writeJSON(w, http.StatusOK, wc.callbacks.PushSettings())
	case http.MethodPost:
		if wc.callbacks.PushSaveSettings == nil {
			jsonErrorResponse(w, http.StatusServiceUnavailable, "push is not enabled")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonErrorResponse(w, http.StatusBadRequest, "read request body failed")
			return
		}
		if len(body) == 0 {
			jsonErrorResponse(w, http.StatusBadRequest, "empty request body")
			return
		}
		if err := wc.callbacks.PushSaveSettings(json.RawMessage(body)); err != nil {
			jsonErrorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// notifyPush forwards one semantic event; the callback is non-blocking.
func (wc *WebChannel) notifyPush(ev PushEvent) {
	if wc.callbacks.PushNotify == nil {
		return
	}
	wc.callbacks.PushNotify(ev)
}

// notifyAskUserPush publishes a high-priority notification for a pending
// AskUser prompt. Called only when the prompt is still pending (answered or
// cancelled prompts are never announced).
func (wc *WebChannel) notifyAskUserPush(channel, chatID string, p *protocol.ProgressEvent) {
	if p == nil {
		return
	}
	question := "Agent 有一个问题需要你回答"
	if len(p.Questions) > 0 {
		if q := strings.TrimSpace(p.Questions[0].Question); q != "" {
			question = q
		}
	}
	wc.notifyPush(PushEvent{
		Kind:         "ask_user",
		Channel:      channel,
		ChatID:       chatID,
		RequestID:    p.RequestID,
		Title:        "xbot 需要你的回答",
		Body:         question,
		HighPriority: true,
	})
}
