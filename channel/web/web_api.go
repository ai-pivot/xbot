package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ch "xbot/channel"
	"xbot/protocol"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Web-only session APIs
// ---------------------------------------------------------------------------

func (wc *WebChannel) resolveAPISession(w http.ResponseWriter, r *http.Request, senderID, channelName, chatID string) (SessionSelector, bool) {
	if channelName == "" && chatID == "" {
		return wc.GetCurrentSession(senderID), true
	}
	if channelName == "" {
		channelName = wc.inferAPISessionChannel(senderID, chatID)
	}
	if chatID == "" {
		chatID = senderID
	}
	if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, channelName, chatID) {
		jsonErrorResponse(w, http.StatusForbidden, "access denied")
		return SessionSelector{}, false
	}
	return SessionSelector{Channel: channelName, ChatID: chatID}, true
}

func (wc *WebChannel) inferAPISessionChannel(senderID, chatID string) string {
	current := wc.GetCurrentSession(senderID)
	if chatID == "" || current.ChatID == chatID {
		return current.Channel
	}
	if webChatIDLooksLikeSubAgent(chatID) {
		return "agent"
	}
	return "web"
}

func (wc *WebChannel) apiSessionFromQuery(w http.ResponseWriter, r *http.Request, senderID string) (SessionSelector, bool) {
	return wc.resolveAPISession(w, r, senderID, r.URL.Query().Get("channel"), r.URL.Query().Get("chat_id"))
}

// handleHistory handles GET /api/history for Web session snapshots.
func (wc *WebChannel) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sel, ok := wc.apiSessionFromQuery(w, r, senderID)
	if !ok {
		return
	}
	// Capture the replay boundary before the snapshot. Events sequenced while
	// the snapshot is being built remain above this cursor and are replayable.
	lastSeq := wc.getEventStream(sessionRouteKey(sel.Channel, sel.ChatID)).lastSeq()
	if wc.callbacks.HistorySnapshot == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": []any{}, "last_seq": lastSeq, "chat_id": sel.ChatID, "channel": sel.Channel})
		return
	}
	snapshot, err := wc.callbacks.HistorySnapshot(senderID, sel)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot.ChatID = sel.ChatID
	snapshot.Channel = sel.Channel
	snapshot.LastSeq = lastSeq
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"messages":        snapshot.Messages,
		"processing":      snapshot.Processing,
		"active_progress": snapshot.ActiveProgress,
		"last_seq":        snapshot.LastSeq,
		"chat_id":         snapshot.ChatID,
		"channel":         snapshot.Channel,
	})
}

// handleHistoryRewind handles POST /api/history/rewind.
func (wc *WebChannel) handleHistoryRewind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		CutoffMS int64  `json:"cutoff_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.CutoffMS <= 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "cutoff_ms is required")
		return
	}
	sel, ok := wc.resolveAPISession(w, r, senderID, body.Channel, body.ChatID)
	if !ok {
		return
	}
	if wc.callbacks.RewindHistory == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "rewind not available")
		return
	}
	result, err := wc.callbacks.RewindHistory(senderID, sel, time.UnixMilli(body.CutoffMS))
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "draft": result.Draft, "rewind_result": result.RewindResult})
}

// ---------------------------------------------------------------------------
// Settings API
// ---------------------------------------------------------------------------

type settingsResponse struct {
	OK       bool              `json:"ok"`
	Settings map[string]string `json:"settings,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type updateSettingsRequest struct {
	Settings map[string]interface{} `json:"settings"`
}

// handleSettings handles GET/PUT /api/settings
func (wc *WebChannel) handleSettings(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, settingsResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		wc.handleGetSettings(w, r, senderID)
	case http.MethodPut:
		wc.handleUpdateSettings(w, r, senderID)
	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGetSettings returns all settings for the current user
func (wc *WebChannel) handleGetSettings(w http.ResponseWriter, r *http.Request, senderID string) {
	rows, err := wc.db.Query(
		"SELECT key, value FROM user_settings WHERE channel = 'web' AND sender_id = ?", senderID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, settingsResponse{OK: false, Error: "query failed"})
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		// Mask sensitive values — never expose credentials to the browser
		if isSensitiveSettingKey(k) {
			settings[k] = "***"
		} else {
			settings[k] = v
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{OK: true, Settings: settings})
}

// handleUpdateSettings upserts settings for the current user
func (wc *WebChannel) handleUpdateSettings(w http.ResponseWriter, r *http.Request, senderID string) {
	var req updateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, settingsResponse{OK: false, Error: "invalid request body"})
		return
	}

	if len(req.Settings) == 0 {
		writeJSON(w, http.StatusBadRequest, settingsResponse{OK: false, Error: "no settings provided"})
		return
	}

	// Validate request size
	const maxSettingKeys = 20
	if len(req.Settings) > maxSettingKeys {
		writeJSON(w, http.StatusBadRequest, settingsResponse{
			OK: false, Error: fmt.Sprintf("too many settings (max %d)", maxSettingKeys),
		})
		return
	}

	// Convert all values to strings (front-end may send numbers/bools)
	settings := make(map[string]string, len(req.Settings))
	for k, v := range req.Settings {
		var sv string
		switch val := v.(type) {
		case string:
			sv = val
		case float64, int, int64, bool:
			sv = fmt.Sprintf("%v", val)
		case nil:
			sv = ""
		default:
			sv = fmt.Sprintf("%v", val)
		}
		if len(sv) > 32768 {
			writeJSON(w, http.StatusBadRequest, settingsResponse{
				OK:    false,
				Error: fmt.Sprintf("setting %q value too large (max 32768 bytes)", k),
			})
			return
		}
		settings[k] = sv
	}

	now := time.Now().Unix()
	for k, v := range settings {
		_, err := wc.db.Exec(
			"INSERT OR REPLACE INTO user_settings (channel, sender_id, key, value, updated_at) VALUES ('web', ?, ?, ?, ?)",
			senderID, k, v, now,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, settingsResponse{OK: false, Error: "update failed"})
			return
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{OK: true})
}

// ---------------------------------------------------------------------------
// Multi-Runner API
// ---------------------------------------------------------------------------

type runnersListResponse struct {
	OK       bool               `json:"ok"`
	Runners  []tools.RunnerInfo `json:"runners,omitempty"`
	WsURL    string             `json:"ws_url,omitempty"`
	SenderID string             `json:"sender_id,omitempty"`
	Error    string             `json:"error,omitempty"`
}

type runnerCreateRequest struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
}

type runnerActiveResponse struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

type runnerCommandResponse struct {
	OK      bool              `json:"ok"`
	Token   string            `json:"token,omitempty"`
	Command string            `json:"command,omitempty"`
	Runner  *tools.RunnerInfo `json:"runner,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// handleRunners handles GET /api/runners (list) and POST /api/runners (create).
func (wc *WebChannel) handleRunners(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnersListResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.RunnerList == nil {
			writeJSON(w, http.StatusOK, runnersListResponse{OK: true})
			return
		}
		runners, err := wc.callbacks.RunnerList(senderID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, runnersListResponse{OK: false, Error: "list failed"})
			return
		}
		// Mask sensitive fields before sending to frontend
		maskedRunners := make([]tools.RunnerInfo, len(runners))
		for i, r := range runners {
			maskedRunners[i] = r
			maskedRunners[i].LLMAPIKey = maskSensitive(r.LLMAPIKey)
		}
		writeJSON(w, http.StatusOK, runnersListResponse{
			OK:       true,
			Runners:  maskedRunners,
			WsURL:    wc.config.PublicURL,
			SenderID: senderID,
		})
	case http.MethodPost:
		if wc.callbacks.RunnerCreate == nil {
			writeJSON(w, http.StatusServiceUnavailable, runnerCommandResponse{OK: false, Error: "runner management not configured"})
			return
		}
		var req runnerCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, runnerCommandResponse{OK: false, Error: "invalid request body"})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, runnerCommandResponse{OK: false, Error: "name is required"})
			return
		}
		cmd, err := wc.callbacks.RunnerCreate(senderID, req.Name, req.Mode, req.DockerImage, req.Workspace, tools.RunnerLLMSettings{})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, runnerCommandResponse{OK: false, Error: err.Error()})
			return
		}
		created := &tools.RunnerInfo{
			Name:        req.Name,
			Mode:        req.Mode,
			DockerImage: req.DockerImage,
			Workspace:   req.Workspace,
		}
		if wc.callbacks.RunnerList != nil {
			if runners, listErr := wc.callbacks.RunnerList(senderID); listErr == nil {
				for _, runner := range runners {
					if runner.Name == req.Name {
						created = &runner
						created.LLMAPIKey = maskSensitive(created.LLMAPIKey)
						break
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, runnerCommandResponse{OK: true, Token: created.Token, Command: cmd, Runner: created})
	}
}

// handleRunnerActive handles GET /api/runners/active (get) and PUT /api/runners/active (set).
func (wc *WebChannel) handleRunnerActive(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnerActiveResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.RunnerGetActive == nil {
			writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: ""})
			return
		}
		name, err := wc.callbacks.RunnerGetActive(senderID)
		if err != nil {
			writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: ""})
			return
		}
		writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: name})
	case http.MethodPut:
		if wc.callbacks.RunnerSetActive == nil {
			writeJSON(w, http.StatusServiceUnavailable, runnerActiveResponse{OK: false, Error: "runner management not configured"})
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeJSON(w, http.StatusBadRequest, runnerActiveResponse{OK: false, Error: "name is required"})
			return
		}
		if err := wc.callbacks.RunnerSetActive(senderID, req.Name); err != nil {
			writeJSON(w, http.StatusInternalServerError, runnerActiveResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: req.Name})
	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleRunnerByName handles DELETE /api/runners/{name}.
func (wc *WebChannel) handleRunnerByName(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnerActiveResponse{OK: false, Error: "unauthorized"})
		return
	}

	// Extract runner name from URL path parameter
	name := r.PathValue("name")
	// Reject paths that look like other endpoints
	if name == "active" || name == "" {
		jsonErrorResponse(w, http.StatusNotFound, "not found")
		return
	}

	if r.Method == http.MethodDelete {
		if wc.callbacks.RunnerDelete == nil {
			writeJSON(w, http.StatusServiceUnavailable, runnerActiveResponse{OK: false, Error: "runner management not configured"})
			return
		}
		if name == tools.BuiltinDockerRunnerName {
			writeJSON(w, http.StatusBadRequest, runnerActiveResponse{OK: false, Error: "built-in docker sandbox cannot be deleted"})
			return
		}
		if err := wc.callbacks.RunnerDelete(senderID, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, runnerActiveResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true})
		return
	}

	jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
}

// ---------------------------------------------------------------------------
// Market API
// ---------------------------------------------------------------------------

type marketEntry struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Version     string `json:"version,omitempty"`
	CreatedAt   string `json:"created_at"`
	Installed   bool   `json:"installed"`
}

type marketResponse struct {
	OK      bool          `json:"ok"`
	Entries []marketEntry `json:"entries,omitempty"`
	Error   string        `json:"error,omitempty"`
}

type marketInstallRequest struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

type marketUninstallRequest struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// handleMarket handles GET /api/market?type=agent&limit=20&offset=0
func (wc *WebChannel) handleMarket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryBrowse == nil {
		writeJSON(w, http.StatusOK, marketResponse{OK: true, Entries: nil})
		return
	}

	entryType := r.URL.Query().Get("type")
	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	entries, err := wc.callbacks.RegistryBrowse(entryType, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: "browse failed"})
		return
	}

	// Compute installed set for the user
	installedSet := make(map[string]bool)
	if wc.callbacks.RegistryListMy != nil {
		_, installed, err := wc.callbacks.RegistryListMy(senderID, entryType)
		if err == nil {
			for _, name := range installed {
				installedSet[name] = true
			}
		}
	}

	// Build response entries
	result := make([]marketEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, marketEntry{
			ID:          e.ID,
			Type:        e.Type,
			Name:        e.Name,
			Description: e.Description,
			Author:      e.Author,
			Version:     e.Version,
			CreatedAt:   time.UnixMilli(e.CreatedAt).UTC().Format(time.RFC3339),
			Installed:   installedSet[e.Name],
		})
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true, Entries: result})
}

// handleMarketInstall handles POST /api/market/install
func (wc *WebChannel) handleMarketInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryInstall == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	var req marketInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "invalid request body"})
		return
	}

	if err := wc.callbacks.RegistryInstall(req.Type, req.ID, senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true})
}

// ---------------------------------------------------------------------------
// App Pack / Install-File API
// ---------------------------------------------------------------------------

type marketPackRequest struct {
	Name  string             `json:"name"`
	Items []packItemRequest  `json:"items"`
}

type packItemRequest struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type marketPackResponse struct {
	OK     bool   `json:"ok"`
	Path   string `json:"path,omitempty"`
	Error  string `json:"error,omitempty"`
}

// handleMarketPack handles POST /api/market/pack
func (wc *WebChannel) handleMarketPack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryPack == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	var req marketPackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Name == "" || len(req.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "name and items are required"})
		return
	}

	items := make([]ch.PackItemSpec, len(req.Items))
	for i, it := range req.Items {
		items[i] = ch.PackItemSpec{Type: it.Type, Name: it.Name}
	}

	outputPath := filepath.Join(os.TempDir(), req.Name+".xbot.zip")
	if err := wc.callbacks.RegistryPack(req.Name, items, outputPath, senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, marketPackResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, marketPackResponse{OK: true, Path: outputPath})
}

// handleMarketInstallFile handles POST /api/market/install-file
// Accepts multipart/form-data with a "file" field containing the .xbot.zip
func (wc *WebChannel) handleMarketInstallFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryInstallFile == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	// Parse multipart upload
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "failed to parse multipart form"})
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "no file uploaded"})
		return
	}
	defer file.Close()

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "xbot-install-*.xbot.zip")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: "failed to create temp file"})
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: "failed to save uploaded file"})
		return
	}
	tmpFile.Close()

	result, err := wc.callbacks.RegistryInstallFile(tmpPath, senderID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"name":     result.Name,
		"version":  result.Version,
		"items":    result.Installed,
	})
}

// ---------------------------------------------------------------------------

type llmConfigResponse struct {
	OK              bool                  `json:"ok"`
	IsGlobal        bool                  `json:"is_global,omitempty"`
	Provider        string                `json:"provider,omitempty"`
	BaseURL         string                `json:"base_url,omitempty"`
	Model           string                `json:"model,omitempty"`
	Models          []string              `json:"models,omitempty"`
	ModelEntries    []protocol.ModelEntry `json:"model_entries,omitempty"`
	MaxContext      int                   `json:"max_context,omitempty"`
	MaxOutputTokens int                   `json:"max_output_tokens,omitempty"`
	ThinkingMode    string                `json:"thinking_mode,omitempty"`
	Error           string                `json:"error,omitempty"`
}

type llmConfigSetRequest struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	MaxContext      int    `json:"max_context"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	ThinkingMode    string `json:"thinking_mode"`
}

type llmModelSetRequest struct {
	SubID string `json:"sub_id"`
	Model string `json:"model"`
}

type llmMaxContextRequest struct {
	MaxContext int `json:"max_context"`
}

// handleLLMConfig handles GET/POST/DELETE /api/llm-config
func (wc *WebChannel) handleLLMConfig(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, llmConfigResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		wc.handleLLMConfigGet(w, senderID)
	case http.MethodPost:
		wc.handleLLMConfigSet(w, r, senderID)
	case http.MethodDelete:
		wc.handleLLMConfigDelete(w, senderID)
	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (wc *WebChannel) handleLLMConfigGet(w http.ResponseWriter, senderID string) {
	if wc.callbacks.LLMGetConfig == nil {
		writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
		return
	}

	provider, baseURL, model, ok := wc.callbacks.LLMGetConfig(senderID)

	// Also fetch available models if a list callback exists
	var modelEntries []protocol.ModelEntry
	if wc.callbacks.LLMList != nil {
		entries, currentEntry := wc.callbacks.LLMList(senderID)
		modelEntries = entries
		if currentEntry.Model != "" {
			model = currentEntry.Model
		}
	}

	resp := llmConfigResponse{
		OK:           true,
		IsGlobal:     !ok,
		Provider:     provider,
		BaseURL:      baseURL,
		Model:        model,
		ModelEntries: modelEntries,
	}
	// Also populate the legacy Models []string field for backward compat
	for _, e := range modelEntries {
		resp.Models = append(resp.Models, e.Model)
	}
	// Also fetch max context if callback exists
	if wc.callbacks.LLMGetMaxContext != nil {
		resp.MaxContext = wc.callbacks.LLMGetMaxContext(senderID, "", "")
	}
	writeJSON(w, http.StatusOK, resp)

}

func (wc *WebChannel) handleLLMConfigSet(w http.ResponseWriter, r *http.Request, senderID string) {
	if wc.callbacks.LLMSetConfig == nil {
		writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
		return
	}

	var req llmConfigSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Provider == "" || req.BaseURL == "" || req.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "provider, base_url, api_key are required"})
		return
	}

	if err := wc.callbacks.LLMSetConfig(senderID, req.Provider, req.BaseURL, req.APIKey, req.Model, req.MaxOutputTokens, req.ThinkingMode); err != nil {
		writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
}

func (wc *WebChannel) handleLLMConfigDelete(w http.ResponseWriter, senderID string) {
	if wc.callbacks.LLMDelete == nil {
		writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
		return
	}

	if err := wc.callbacks.LLMDelete(senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
}

// handleLLMMaxContext handles GET/POST /api/llm-max-context
func (wc *WebChannel) handleLLMMaxContext(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, llmConfigResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.LLMGetMaxContext == nil {
			writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
			return
		}
		maxCtx := wc.callbacks.LLMGetMaxContext(senderID, "", "")
		writeJSON(w, http.StatusOK, llmConfigResponse{OK: true, MaxContext: maxCtx})

	case http.MethodPost:
		if wc.callbacks.LLMSetMaxContext == nil {
			writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
			return
		}
		var req llmMaxContextRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "invalid request body"})
			return
		}
		if req.MaxContext < 0 {
			writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "max_context must be >= 0"})
			return
		}
		if err := wc.callbacks.LLMSetMaxContext(senderID, "", "", req.MaxContext); err != nil {
			writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})

	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleLLMModelSet handles POST /api/llm-config/model (switch model only)
func (wc *WebChannel) handleLLMModelSet(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, llmConfigResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.LLMSet == nil {
		writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
		return
	}

	var req llmModelSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "model is required"})
		return
	}

	if err := wc.callbacks.LLMSet(senderID, req.SubID, req.Model); err != nil {
		writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
}

// ---------------------------------------------------------------------------
// Search API
// ---------------------------------------------------------------------------

type searchResponse struct {
	OK      bool        `json:"ok"`
	Results []searchHit `json:"results,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type searchHit struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at,omitempty"`
	Snippet   string `json:"snippet"`
}

// handleSearch handles GET /api/search?q=keyword&limit=20
func (wc *WebChannel) handleSearch(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, searchResponse{OK: false, Error: "unauthorized"})
		return
	}

	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, searchResponse{OK: false, Error: "method not allowed"})
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, searchResponse{OK: true, Results: nil})
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	// Find tenant ID for this user's active session
	sel := wc.GetCurrentSession(senderID)
	// Cross-channel access requires admin.
	if sel.Channel != "web" && !wc.isAdmin(r.Context(), senderID) {
		jsonErrorResponse(w, http.StatusForbidden, "access denied")
		return
	}
	var tenantID int64
	err := wc.db.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?", sel.Channel, sel.ChatID,
	).Scan(&tenantID)
	if err != nil {
		writeJSON(w, http.StatusOK, searchResponse{OK: true, Results: nil})
		return
	}

	// Case-insensitive LIKE search (escape wildcards in user input)
	like := "%" + escapeLike(q) + "%"
	rows, err := wc.db.Query(`
		SELECT id, role, content, created_at
		FROM session_messages
		WHERE tenant_id = ? AND role IN ('user', 'assistant') AND content LIKE ? COLLATE NOCASE ESCAPE '\'
		ORDER BY id DESC
		LIMIT ?
	`, tenantID, like, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, searchResponse{OK: false, Error: "search failed"})
		return
	}
	defer rows.Close()

	var results []searchHit
	qLower := strings.ToLower(q)
	for rows.Next() {
		var hit searchHit
		var content string
		if err := rows.Scan(&hit.ID, &hit.Role, &content, &hit.CreatedAt); err != nil {
			continue
		}
		hit.Snippet = snippetAround(content, qLower)
		results = append(results, hit)
	}

	writeJSON(w, http.StatusOK, searchResponse{OK: true, Results: results})
}

// escapeLike escapes SQL LIKE wildcard characters in user input.
func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c == '_' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// snippetAround returns a snippet of text around the first occurrence of the
// query keyword (case-insensitive), with up to 50 runes before and after.
// Uses []rune to avoid truncating multi-byte characters (CJK, emoji, etc.).
func snippetAround(content, queryLower string) string {
	runes := []rune(content)
	queryRunes := []rune(queryLower)
	contentLower := strings.ToLower(content)

	// Find byte offset of match, then convert to rune index
	byteIdx := strings.Index(contentLower, queryLower)
	if byteIdx == -1 {
		// Fallback: return first 150 runes
		if len(runes) <= 150 {
			return content
		}
		return "..." + string(runes[len(runes)-147:])
	}

	runeIdx := len([]rune(content[:byteIdx]))

	start := runeIdx - 50
	if start < 0 {
		start = 0
	} else {
		// Break at space to avoid cutting words
		for start < runeIdx && runes[start] != ' ' && runes[start] != '\n' {
			start++
		}
		if start < runeIdx {
			start++ // skip the space/newline
		}
	}

	end := runeIdx + len(queryRunes) + 50
	if end > len(runes) {
		end = len(runes)
	} else {
		// Break at space to avoid cutting words
		for end < len(runes) && runes[end] != ' ' && runes[end] != '\n' {
			end++
		}
	}

	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return snippet
}

// ── Chatroom Management APIs ──

// handleChats handles GET/POST /api/chats — list or create chatrooms.
func (wc *WebChannel) handleChats(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.ChatList == nil && wc.callbacks.SessionTree == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chats": []any{}})
			return
		}
		sel := wc.GetCurrentSession(senderID)

		// No channel parameter means the Web admin view: show web + cli in one
		// list. Keep ?channel=... for compatibility with older clients.
		if _, ok := r.URL.Query()["channel"]; !ok {
			if wc.callbacks.SessionTree != nil {
				result, err := wc.callbacks.SessionTree(senderID, sel, wc.isAdmin(r.Context(), senderID))
				if err != nil {
					jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
					return
				}
				chats := make([]UserChatWithPreview, 0, len(result.Sessions))
				for _, node := range result.Sessions {
					chats = append(chats, node.UserChatWithPreview)
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":               true,
					"chats":            chats, // compatibility for older Web builds
					"sessions":         result.Sessions,
					"orphan_subagents": result.OrphanSubAgents,
				})
				return
			}
			var all []UserChatWithPreview
			webCurrent := ""
			if sel.Channel == "web" {
				webCurrent = sel.ChatID
			}
			webChats, err := wc.callbacks.ChatList(senderID, webCurrent, "web")
			if err != nil {
				jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
				return
			}
			all = append(all, webChats...)

			if wc.isAdmin(r.Context(), senderID) {
				cliCurrent := ""
				if sel.Channel == "cli" {
					cliCurrent = sel.ChatID
				}
				cliChats, err := wc.callbacks.ChatList(senderID, cliCurrent, "cli")
				if err != nil {
					jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
					return
				}
				all = append(all, cliChats...)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chats": all})
			return
		}

		channel := r.URL.Query().Get("channel")
		if channel == "" {
			channel = "web"
		}
		if channel == "agent" {
			jsonErrorResponse(w, http.StatusBadRequest, "agent sessions are only available via /api/session-tree")
			return
		}
		if !wc.isAdmin(r.Context(), senderID) && channel != "web" {
			jsonErrorResponse(w, http.StatusForbidden, "access denied")
			return
		}
		currentChatID := ""
		if sel.Channel == channel {
			currentChatID = sel.ChatID
		}
		chats, err := wc.callbacks.ChatList(senderID, currentChatID, channel)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		chats = filterSubAgentChatRows(chats)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chats": chats})

	case http.MethodPost:
		if wc.callbacks.ChatCreate == nil {
			jsonErrorResponse(w, http.StatusNotImplemented, "chat creation not available")
			return
		}
		var body struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonErrorResponse(w, http.StatusBadRequest, "invalid body")
			return
		}
		chatID, err := wc.callbacks.ChatCreate(senderID, body.Label)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		wc.userCurrentSessionMu.Lock()
		wc.userCurrentSession[senderID] = SessionSelector{Channel: "web", ChatID: chatID}
		wc.userCurrentSessionMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chat_id": chatID})

	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func filterSubAgentChatRows(rows []UserChatWithPreview) []UserChatWithPreview {
	if len(rows) == 0 {
		return rows
	}
	filtered := rows[:0]
	for _, row := range rows {
		if webChatRowLooksLikeSubAgent(row) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func webChatRowLooksLikeSubAgent(row UserChatWithPreview) bool {
	if row.Channel == "agent" || row.Type == "agent" || row.Type == "subagent" {
		return true
	}
	if row.ParentChannel != "" || row.ParentChatID != "" || row.Role != "" || row.Instance != "" {
		return true
	}
	fullKey := row.FullKey
	if fullKey == "" {
		fullKey = row.ChatID
	}
	return webChatIDLooksLikeSubAgent(fullKey)
}

func webChatIDLooksLikeSubAgent(chatID string) bool {
	_, ok := parseWebAgentTenantChatID(chatID)
	return ok
}

type webAgentTenantInfo struct {
	parentChannel string
	parentChatID  string
}

func parseWebAgentTenantChatID(chatID string) (webAgentTenantInfo, bool) {
	slash := strings.LastIndex(chatID, "/")
	if slash <= 0 || slash == len(chatID)-1 {
		return webAgentTenantInfo{}, false
	}
	parent := chatID[:slash]
	channelSep := strings.Index(parent, ":")
	if channelSep <= 0 || channelSep == len(parent)-1 {
		return webAgentTenantInfo{}, false
	}
	channel := parent[:channelSep]
	for _, r := range channel {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return webAgentTenantInfo{}, false
		}
	}
	return webAgentTenantInfo{parentChannel: channel, parentChatID: parent[channelSep+1:]}, true
}

// handleSessionTree handles GET /api/session-tree. This is Web-only and keeps
// the sidebar's parent/child matching on the server, aligned with TUI session
// entry construction.
func (wc *WebChannel) handleSessionTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.SessionTree == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": []any{}})
		return
	}
	result, err := wc.callbacks.SessionTree(senderID, wc.GetCurrentSession(senderID), wc.isAdmin(r.Context(), senderID))
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"sessions":         result.Sessions,
		"orphan_subagents": result.OrphanSubAgents,
	})
}

// handleChatSwitch handles POST /api/chats/{chatID}/switch — switch active chatroom.
// Optional ?channel=cli query param switches to a non-web channel (admin only).
func (wc *WebChannel) handleChatSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	chatID := r.PathValue("chatID")
	if chatID == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "web"
	}

	// Non-admin users can only switch within web channel.
	if !wc.isAdmin(r.Context(), senderID) && channel != "web" {
		jsonErrorResponse(w, http.StatusForbidden, "access denied")
		return
	}

	if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, channel, chatID) {
		jsonErrorResponse(w, http.StatusForbidden, "not your chat")
		return
	}

	wc.userCurrentSessionMu.Lock()
	wc.userCurrentSession[senderID] = SessionSelector{Channel: channel, ChatID: chatID}
	wc.userCurrentSessionMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chat_id": chatID, "channel": channel})
}

// handleChatDelete handles DELETE /api/chats/{chatID} — delete a chatroom.
func (wc *WebChannel) handleChatDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	chatID := r.PathValue("chatID")
	if chatID == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	channelName := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channelName == "" {
		channelName = "web"
	}

	if wc.callbacks.ChatDelete == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "chat deletion not available")
		return
	}

	if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, channelName, chatID) {
		jsonErrorResponse(w, http.StatusForbidden, "not your chat")
		return
	}

	if err := wc.callbacks.ChatDelete(senderID, channelName, chatID); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	wc.clearSessionTransportState(channelName, chatID)

	// If deleting current chat, reset to default session
	wc.userCurrentSessionMu.Lock()
	if sel, ok := wc.userCurrentSession[senderID]; ok && sel.Channel == channelName && sel.ChatID == chatID {
		delete(wc.userCurrentSession, senderID)
	}
	wc.userCurrentSessionMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChatRename handles POST /api/chats/{chatID}/rename — rename a chatroom.
func (wc *WebChannel) handleChatRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	chatID := r.PathValue("chatID")
	if chatID == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "chat_id is required")
		return
	}

	var req struct {
		Channel string `json:"channel"`
		Label   string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Label == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "label is required")
		return
	}
	if req.Channel == "" {
		req.Channel = "web"
	}

	if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, req.Channel, chatID) {
		jsonErrorResponse(w, http.StatusForbidden, "not your chat")
		return
	}

	if wc.callbacks.ChatRename == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "chat rename not available")
		return
	}

	if err := wc.callbacks.ChatRename(senderID, req.Channel, chatID, req.Label); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// canAccessSession checks whether a browser-authenticated user may address a
// session. Web UUID chats are owned through user_chats; non-web sessions are
// admin-only unless canonical ownership is recorded. CLI metadata-only rows
// surfaced in the session tree are also addressable by admins.
func (wc *WebChannel) canAccessSession(ctx context.Context, webUserID int, senderID, channelName, chatID string) bool {
	if channelName == "" {
		channelName = "web"
	}
	if chatID == "" {
		return true
	}
	if channelName == "web" {
		if wc.userOwnsChat(senderID, chatID) {
			return true
		}
		return wc.isAdmin(ctx, senderID) && wc.db != nil && wc.tenantExists("web", chatID)
	}
	if wc.db == nil {
		return false
	}
	if channelName == "agent" {
		return wc.canAccessAgentSession(webUserID, senderID, chatID)
	}
	return wc.canAccessCanonicalSession(webUserID, senderID, channelName, chatID)
}

func (wc *WebChannel) canAccessCanonicalSession(webUserID int, senderID, channelName, chatID string) bool {
	// For non-web channels (cli, feishu, etc.): check admin role or canonical ownership.
	// Check IdentityResolver first (canonical role from DB)
	if wc.callbacks.IdentityResolver != nil {
		uid, role, err := wc.callbacks.IdentityResolver.Resolve("web", senderID)
		if err == nil && uid > 0 {
			if role == "admin" {
				return wc.canonicalSessionExists(channelName, chatID)
			}
			// Non-admin: check canonical session ownership
			var ownerUserID int64
			err := wc.db.QueryRow(
				"SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE channel = ? AND chat_id = ?",
				channelName, chatID,
			).Scan(&ownerUserID)
			if err == nil && ownerUserID == uid {
				return true
			}
			return false
		}
	}
	// Legacy fallback: web-1 or "admin" is admin
	if senderID == "admin" || webUserID == 1 {
		return wc.canonicalSessionExists(channelName, chatID)
	}
	return false
}

func (wc *WebChannel) canonicalSessionExists(channelName, chatID string) bool {
	if wc.tenantExists(channelName, chatID) {
		return true
	}
	return wc.callbacks.LocalSessionExists != nil && wc.callbacks.LocalSessionExists(channelName, chatID)
}

func (wc *WebChannel) tenantExists(channelName, chatID string) bool {
	var count int
	err := wc.db.QueryRow(
		"SELECT COUNT(*) FROM tenants WHERE channel = ? AND chat_id = ?",
		channelName, chatID,
	).Scan(&count)
	return err == nil && count > 0
}

func (wc *WebChannel) canAccessAgentSession(webUserID int, senderID, chatID string) bool {
	if !wc.tenantExists("agent", chatID) {
		return false
	}
	// Admin via IdentityResolver or legacy checks
	if wc.callbacks.IdentityResolver != nil {
		if uid, role, err := wc.callbacks.IdentityResolver.Resolve("web", senderID); err == nil && uid > 0 && role == "admin" {
			return true
		}
	}
	if senderID == "admin" || webUserID == 1 {
		return true
	}
	info, ok := parseWebAgentTenantChatID(chatID)
	if !ok {
		return false
	}
	for depth := 0; depth < 32; depth++ {
		if info.parentChannel == "web" {
			return wc.userOwnsChat(senderID, info.parentChatID)
		}
		if info.parentChannel != "agent" {
			return wc.canAccessCanonicalSession(webUserID, senderID, info.parentChannel, info.parentChatID)
		}
		parentExists := wc.tenantExists("agent", info.parentChatID)
		if !parentExists {
			return false
		}
		var next webAgentTenantInfo
		next, ok = parseWebAgentTenantChatID(info.parentChatID)
		if !ok {
			return false
		}
		info = next
	}
	return false
}

// IsAdminIdentity reports whether a Web senderID belongs to an admin web user.
// It is used by the server RPC bridge, where only the senderID survives after
// WebSocket auth and the original HTTP request context is no longer available.
func (wc *WebChannel) IsAdminIdentity(senderID string) bool {
	if wc.singleUser {
		return true
	}
	if senderID == "admin" {
		return true
	}
	// Use IdentityResolver when available (canonical role from DB)
	if wc.callbacks.IdentityResolver != nil {
		userID, _, err := wc.callbacks.IdentityResolver.Resolve("web", senderID)
		if err == nil && userID > 0 {
			return wc.callbacks.IdentityResolver.IsAdmin(userID)
		}
	}
	// Backward compat: first web user is admin
	if !strings.HasPrefix(senderID, "web-") || wc.db == nil {
		return false
	}
	id, err := strconv.Atoi(strings.TrimPrefix(senderID, "web-"))
	return err == nil && id == 1
}

// GetCurrentSession returns the active SessionSelector (channel + chatID).
// Used internally by web_api.go and web.go for session routing.
func (wc *WebChannel) GetCurrentSession(senderID string) SessionSelector {
	wc.userCurrentSessionMu.RLock()
	defer wc.userCurrentSessionMu.RUnlock()
	if sel, ok := wc.userCurrentSession[senderID]; ok {
		return sel
	}
	return SessionSelector{Channel: "web", ChatID: senderID}
}

// isAdmin returns true if the user is an admin.
// Uses IdentityResolver when available (canonical role), falls back to
// senderID == "admin" and web user ID == 1 for backward compat.
// In single-user mode, all users are admin (no identity isolation).
func (wc *WebChannel) isAdmin(ctx context.Context, senderID string) bool {
	if wc.singleUser {
		return true
	}
	if senderID == "admin" {
		return true
	}
	// Use IdentityResolver if available (canonical user role)
	if wc.callbacks.IdentityResolver != nil {
		userID, _, err := wc.callbacks.IdentityResolver.Resolve("web", senderID)
		if err == nil && userID > 0 {
			return wc.callbacks.IdentityResolver.IsAdmin(userID)
		}
	}
	// Backward compat: first web user is admin
	if userID := userIDFromContext(ctx); userID == 1 {
		return true
	}
	return false
}

// userOwnsChat checks whether senderID owns the given chatID.
// A user owns their default chat (chatID == senderID) or a chat in user_chats.
func (wc *WebChannel) userOwnsChat(senderID, chatID string) bool {
	// Default chat: chatID == senderID
	if chatID == senderID {
		return true
	}
	// Check user_chats table for ownership
	if wc.db != nil {
		var count int
		err := wc.db.QueryRow(
			"SELECT COUNT(*) FROM user_chats WHERE channel = 'web' AND sender_id = ? AND chat_id = ?",
			senderID, chatID,
		).Scan(&count)
		if err == nil && count > 0 {
			return true
		}
	}
	return false
}

// maskSensitive masks a sensitive string for display, showing only first 4 chars.
// Returns "***" for empty strings.
func maskSensitive(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + "***"
}

// sensitiveSettingKeys caches the set of keys marked Sensitive in ch.AllSettingDefs.
var sensitiveSettingKeys = func() map[string]bool {
	m := make(map[string]bool)
	for _, def := range ch.AllSettingDefs {
		if def.Sensitive {
			m[def.Key] = true
		}
	}
	return m
}()

// isSensitiveSettingKey returns true if the key is marked sensitive in setting definitions.
func isSensitiveSettingKey(key string) bool {
	return sensitiveSettingKeys[key]
}
