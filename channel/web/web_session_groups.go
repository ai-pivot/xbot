package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"xbot/storage/sqlite"
)

const sessionGroupsSettingKey = sqlite.SessionGroupsSettingKey
const maxSessionGroupsDocument = 512 * 1024

type sessionGroup struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DefaultWorkDir string `json:"defaultWorkDir"`
}
type sessionGroupsState struct {
	Revision    int64             `json:"revision"`
	Groups      []sessionGroup    `json:"groups"`
	Memberships map[string]string `json:"memberships"`
}

// createdGroupId is response-only; never persist it in the user document.
type sessionGroupsResponse struct {
	sessionGroupsState
	CreatedGroupID string `json:"createdGroupId,omitempty"`
}

type sessionGroupSelector struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chatID"`
}
type sessionGroupsRequest struct {
	Action         string                 `json:"action,omitempty"`
	ID             string                 `json:"id,omitempty"`
	Name           *string                `json:"name,omitempty"`
	DefaultWorkDir *string                `json:"defaultWorkDir,omitempty"`
	Sessions       []sessionGroupSelector `json:"sessions,omitempty"`
	GroupID        *string                `json:"groupId,omitempty"`
	OrderedIDs     []string               `json:"orderedIds,omitempty"`
}

// Deliberately bypass SettingsService: that service normalizes all senders to
// cli_user. Groups are private Web UI metadata, not operator runtime settings.
// Caller holds sessionGroupsMu, including across read/modify/write.
func (wc *WebChannel) loadSessionGroups(sender string) (sessionGroupsState, error) {
	state := sessionGroupsState{Groups: []sessionGroup{}, Memberships: map[string]string{}}
	if wc.db == nil {
		return state, errors.New("session groups database unavailable")
	}
	var raw string
	err := wc.db.QueryRow("SELECT value FROM user_settings WHERE channel='web' AND sender_id=? AND key=?", sender, sessionGroupsSettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if len(raw) > maxSessionGroupsDocument {
		return state, errors.New("session groups document too large")
	}
	// Decode into an uninitialized wire document, not the empty-state defaults.
	// Missing/null fields must not silently reset a persisted revision or data.
	var stored struct {
		Revision    *int64            `json:"revision"`
		Groups      []sessionGroup    `json:"groups"`
		Memberships map[string]string `json:"memberships"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return state, fmt.Errorf("invalid stored session groups: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return state, errors.New("invalid stored session groups: trailing JSON")
	}
	if stored.Revision == nil || *stored.Revision < 0 || stored.Groups == nil || stored.Memberships == nil {
		return state, errors.New("invalid stored session groups")
	}
	state = sessionGroupsState{Revision: *stored.Revision, Groups: stored.Groups, Memberships: stored.Memberships}
	seen := map[string]bool{}
	for _, g := range state.Groups {
		if g.ID == "" || seen[g.ID] {
			return state, errors.New("invalid stored group id")
		}
		seen[g.ID] = true
	}
	for _, id := range state.Memberships {
		if !seen[id] {
			return state, errors.New("invalid stored membership")
		}
	}
	return state, nil
}
func (wc *WebChannel) saveSessionGroups(sender string, state sessionGroupsState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > maxSessionGroupsDocument {
		return errors.New("session groups document too large")
	}
	_, err = wc.db.Exec(`INSERT INTO user_settings(channel,sender_id,key,value,updated_at) VALUES('web',?,?,?,?) ON CONFLICT(channel,sender_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, sender, sessionGroupsSettingKey, string(raw), time.Now().UnixMilli())
	return err
}
func sessionGroupKey(sel sessionGroupSelector) (string, error) {
	// Only main Web/CLI sessions are groupable. Chat IDs may contain ':' and '/'.
	if (sel.Channel != "web" && sel.Channel != "cli") || strings.TrimSpace(sel.ChatID) == "" || len(sel.ChatID) > 4096 || strings.ContainsAny(sel.ChatID, "\x00\r\n") || !utf8.ValidString(sel.ChatID) {
		return "", errors.New("invalid session selector")
	}
	return sel.Channel + ":" + sel.ChatID, nil
}
func applySessionGroups(state *sessionGroupsState, req sessionGroupsRequest) error {
	index := -1
	for i, g := range state.Groups {
		if g.ID == req.ID {
			index = i
		}
	}
	if req.Name != nil && (strings.TrimSpace(*req.Name) == "" || len(*req.Name) > 256 || !utf8.ValidString(*req.Name) || strings.ContainsAny(*req.Name, "\x00\r\n")) {
		return errors.New("name must be nonempty and at most 256 bytes")
	}
	if req.DefaultWorkDir != nil && (len(*req.DefaultWorkDir) > 4096 || !utf8.ValidString(*req.DefaultWorkDir) || strings.ContainsAny(*req.DefaultWorkDir, "\x00\r\n")) {
		return errors.New("invalid defaultWorkDir (max 4096 bytes)")
	}
	if len(req.Sessions) > 10000 {
		return errors.New("too many sessions")
	}
	keys := make([]string, 0, len(req.Sessions))
	for _, sel := range req.Sessions {
		key, err := sessionGroupKey(sel)
		if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	switch req.Action {
	case "create", "convert":
		if req.Name == nil {
			return errors.New("name is required")
		}
		if len(state.Groups) >= 1000 {
			return errors.New("too many groups")
		}
		if req.Action == "create" && len(keys) > 0 {
			return errors.New("use convert to create a group with sessions")
		}
		// A conversion must not steal sessions from a concurrently assigned group.
		for _, key := range keys {
			if _, ok := state.Memberships[key]; ok {
				return errors.New("session already belongs to a group")
			}
		}
		g := sessionGroup{ID: uuid.NewString(), Name: strings.TrimSpace(*req.Name)}
		if req.DefaultWorkDir != nil {
			g.DefaultWorkDir = *req.DefaultWorkDir
		}
		state.Groups = append(state.Groups, g)
		for _, key := range keys {
			state.Memberships[key] = g.ID
		}
	case "update":
		if index < 0 {
			return errors.New("group not found")
		}
		if req.Name == nil && req.DefaultWorkDir == nil {
			return errors.New("no changes provided")
		}
		if req.Name != nil {
			state.Groups[index].Name = strings.TrimSpace(*req.Name)
		}
		if req.DefaultWorkDir != nil {
			state.Groups[index].DefaultWorkDir = *req.DefaultWorkDir
		}
	case "delete":
		if index < 0 {
			return errors.New("group not found")
		}
		state.Groups = append(state.Groups[:index], state.Groups[index+1:]...)
		for key, id := range state.Memberships {
			if id == req.ID {
				delete(state.Memberships, key)
			}
		}
	case "move":
		if len(keys) == 0 {
			return errors.New("sessions are required")
		}
		if req.GroupID != nil {
			found := false
			for _, g := range state.Groups {
				if g.ID == *req.GroupID {
					found = true
				}
			}
			if !found {
				return errors.New("group not found")
			}
		}
		for _, key := range keys {
			if req.GroupID == nil {
				delete(state.Memberships, key)
			} else {
				state.Memberships[key] = *req.GroupID
			}
		}
	case "reorder":
		if len(req.OrderedIDs) != len(state.Groups) {
			return errors.New("orderedIds must contain every group exactly once")
		}
		byID := map[string]sessionGroup{}
		for _, g := range state.Groups {
			byID[g.ID] = g
		}
		ordered := make([]sessionGroup, 0, len(state.Groups))
		for _, id := range req.OrderedIDs {
			g, ok := byID[id]
			if !ok {
				return errors.New("invalid or duplicate group id")
			}
			ordered = append(ordered, g)
			delete(byID, id)
		}
		state.Groups = ordered
	default:
		return errors.New("invalid action")
	}
	return nil
}
func (wc *WebChannel) handleSessionGroups(w http.ResponseWriter, r *http.Request) {
	sender := senderIDFromContext(r.Context())
	if sender == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req sessionGroupsRequest
	if err := decodeJSONBody(r, &req, true); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		jsonErrorResponse(w, status, "invalid request body")
		return
	}
	wc.sessionGroupsMu.Lock()
	defer wc.sessionGroupsMu.Unlock()
	state, err := wc.loadSessionGroups(sender)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "read session groups failed")
		return
	}
	createdGroupID := ""
	if req.Action != "" {
		if err := applySessionGroups(&state, req); err != nil {
			jsonErrorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		if state.Revision >= 1<<53-1 {
			jsonErrorResponse(w, http.StatusConflict, "revision exhausted")
			return
		}
		if req.Action == "create" || req.Action == "convert" {
			createdGroupID = state.Groups[len(state.Groups)-1].ID
		}
		state.Revision++
		if err := wc.saveSessionGroups(sender, state); err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, "save session groups failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, sessionGroupsResponse{sessionGroupsState: state, CreatedGroupID: createdGroupID})
}

// Fork metadata is inherited only after the conversation fork succeeds. Holding
// the same mutex as the API prevents concurrent moves/deletes being overwritten.
func (wc *WebChannel) inheritForkSessionGroup(sender, sourceChannel, sourceID, destinationID string) error {
	wc.sessionGroupsMu.Lock()
	defer wc.sessionGroupsMu.Unlock()
	state, err := wc.loadSessionGroups(sender)
	if err != nil {
		return err
	}
	groupID, ok := state.Memberships[sourceChannel+":"+sourceID]
	if !ok {
		return nil
	}
	if state.Revision >= 1<<53-1 {
		return errors.New("revision exhausted")
	}
	state.Memberships["web:"+destinationID] = groupID
	state.Revision++
	return wc.saveSessionGroups(sender, state)
}
