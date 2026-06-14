package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	ch "xbot/channel"

	"xbot/config"
)

// ── Session name utilities ──

const defaultSessionName = "default"

// sessionNameRe validates session names: alphanumeric, hyphens, underscores, CJK.
var sessionNameRe = regexp.MustCompile(`^[\p{Han}\p{Hiragana}\p{Katakana}a-zA-Z0-9_-]{1,64}$`)

// SessionChatID builds a chatID from workDir and session name.
// When sessionName is "default" or empty, returns just workDir (backward compat).
func SessionChatID(workDir, sessionName string) string {
	if sessionName == "" || sessionName == defaultSessionName {
		return workDir
	}
	return workDir + ":" + sessionName
}

// isWorkDirPath returns true if s looks like a valid filesystem path (Unix or Windows).
func isWorkDirPath(s string) bool {
	if s == "" {
		return false
	}
	// Unix: absolute (/...), relative (./...), or home (~...)
	if s[0] == '/' || s[0] == '.' || s[0] == '~' {
		return true
	}
	// Windows absolute: drive letter + colon + separator (e.g. "C:\", "D:/")
	return isWindowsAbs(s)
}

// isWindowsAbs returns true if s looks like a Windows absolute path (drive letter + colon + separator).
func isWindowsAbs(s string) bool {
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return (s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')
	}
	return false
}

// ParseChatID extracts the workDir and sessionName from a chatID.
// Returns (workDir, sessionName). If there's no ":" separator, sessionName is "default".
func ParseChatID(chatID string) (workDir, sessionName string) {
	idx := strings.LastIndex(chatID, ":")
	if idx <= 0 || idx == len(chatID)-1 {
		return chatID, defaultSessionName
	}

	prefix := chatID[:idx]
	suffix := chatID[idx+1:]

	// Validate: workDir should look like an absolute or relative path
	if !isWorkDirPath(prefix) {
		return chatID, defaultSessionName
	}

	workDir = prefix
	sessionName = suffix
	// Resolve relative workDir (e.g. "." from legacy sessions) to absolute path.
	// Skip for Windows absolute paths (drive letter) since filepath.IsAbs
	// returns false for them on non-Windows OS.
	if !isWindowsAbs(workDir) && !filepath.IsAbs(workDir) {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}
	return workDir, sessionName
}

// ValidateSessionName checks if a name is valid for a session.
func ValidateSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	if name == defaultSessionName {
		return fmt.Errorf("session name %q is reserved", name)
	}
	if !sessionNameRe.MatchString(name) {
		return fmt.Errorf("session name must contain only letters, numbers, hyphens, underscores, or CJK characters (1-64 chars)")
	}
	return nil
}

// ── Flexible time format for backward compat ──

// flexTime wraps time.Time with JSON unmarshal that accepts both RFC3339 (T-separator)
// and the space-separator format produced by some older versions.
// Marshal always outputs RFC3339Nano for consistency.
type flexTime time.Time

// flexTimeFormats lists time formats accepted during unmarshal, in priority order.
var flexTimeFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999Z07:00", // space-separator with nano + zone
	"2006-01-02 15:04:05Z07:00",           // space-separator with zone
	"2006-01-02 15:04:05",                 // space-separator without zone
}

func (ft flexTime) MarshalJSON() ([]byte, error) {
	return time.Time(ft).MarshalJSON()
}

func (ft *flexTime) UnmarshalJSON(data []byte) error {
	// Try standard time.Time unmarshal first (handles RFC3339 + quotes)
	var t time.Time
	if err := t.UnmarshalJSON(data); err == nil {
		*ft = flexTime(t)
		return nil
	}
	// Strip quotes and try space-separator formats
	s := strings.Trim(string(data), `"`)
	for _, layout := range flexTimeFormats {
		if parsed, err := time.Parse(layout, s); err == nil {
			*ft = flexTime(parsed)
			return nil
		}
	}
	return fmt.Errorf("flexTime: cannot parse %q", s)
}

func (ft flexTime) Time() time.Time { return time.Time(ft) }

func (ft flexTime) After(u flexTime) bool { return time.Time(ft).After(time.Time(u)) }

// ── Per-directory session storage ──

// dirSessions stores the list of sessions for a given directory.
// Persisted to ~/.xbot/sessions/<sha256>.json
type dirSessions struct {
	Dir        string       `json:"dir"`
	Sessions   []dirSession `json:"sessions"`
	LastActive string       `json:"last_active,omitempty"` // chatID of last active session
}

type dirSession struct {
	Name             string   `json:"name"`
	ChatID           string   `json:"chat_id"`
	CreatedAt        flexTime `json:"created_at"`
	CWD              string   `json:"cwd,omitempty"`                // per-session working directory (worktree path, etc.)
	SubscriptionID   string   `json:"subscription_id,omitempty"`    // per-session subscription override
	Model            string   `json:"model,omitempty"`              // per-session model override (within subscription)
	MaxContextTokens int      `json:"max_context_tokens,omitempty"` // per-session max context tokens override
	MaxOutputTokens  int      `json:"max_output_tokens,omitempty"`  // per-session max output tokens override
}

// sessionsDir returns the directory where per-directory session files are stored.
func sessionsDir() string {
	return filepath.Join(config.XbotHome(), "sessions")
}

// sessionDirHash creates a safe, collision-free filename from a directory path.
// Uses SHA256 truncated to 16 hex chars (64 bits of entropy, sufficient for local files).
func sessionDirHash(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	abs = strings.TrimRight(abs, string(filepath.Separator))
	h := sha256.Sum256([]byte(abs))
	return fmt.Sprintf("%x", h[:8])
}

// LoadDirSessions loads the session list for a given work directory.
func LoadDirSessions(workDir string) (*dirSessions, error) {
	// Resolve relative workDir to absolute path so ds.Dir is always absolute
	if !filepath.IsAbs(workDir) {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, sessionDirHash(workDir)+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &dirSessions{
				Dir: workDir,
				Sessions: []dirSession{{
					Name:      defaultSessionName,
					ChatID:    workDir,
					CreatedAt: flexTime(time.Now()),
				}},
			}, nil
		}
		return nil, err
	}

	var ds dirSessions
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, fmt.Errorf("parse sessions file: %w", err)
	}
	ds.Dir = workDir
	if !ds.hasSession(defaultSessionName) {
		ds.Sessions = append([]dirSession{{
			Name:      defaultSessionName,
			ChatID:    workDir,
			CreatedAt: flexTime(time.Now()),
		}}, ds.Sessions...)
	}
	return &ds, nil
}

// save persists the session list to disk using atomic write (tmp+rename).
func (ds *dirSessions) save() error {
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, sessionDirHash(ds.Dir)+".json")
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (ds *dirSessions) hasSession(name string) bool {
	for _, s := range ds.Sessions {
		if s.Name == name {
			return true
		}
	}
	return false
}

// addSession adds a new session to the directory.
func (ds *dirSessions) addSession(name string) (string, error) {
	if err := ValidateSessionName(name); err != nil {
		return "", err
	}
	if ds.hasSession(name) {
		return "", fmt.Errorf("session %q already exists", name)
	}
	chatID := SessionChatID(ds.Dir, name)
	ds.Sessions = append(ds.Sessions, dirSession{
		Name:      name,
		ChatID:    chatID,
		CreatedAt: flexTime(time.Now()),
	})
	return chatID, ds.save()
}

// addSessionAuto creates a new session with an auto-generated "Agent-xxxxxx" name.
func (ds *dirSessions) addSessionAuto() (name string, chatID string, err error) {
	for i := 0; i < 10; i++ {
		name, err = ch.GenerateSessionName()
		if err != nil {
			return "", "", err
		}
		if !ds.hasSession(name) {
			break
		}
		name = ""
	}
	if name == "" {
		return "", "", fmt.Errorf("failed to generate unique session name after 10 attempts")
	}
	chatID, err = ds.addSession(name)
	if err != nil {
		return "", "", err
	}
	return name, chatID, nil
}

// NewAutoSession creates a new auto-named session for the given workDir and
// immediately persists it as the last active session. Returns the display name,
// full chatID, and any error.
func NewAutoSession(workDir string) (name, chatID string, err error) {
	ds, err := LoadDirSessions(workDir)
	if err != nil {
		return "", "", fmt.Errorf("load sessions: %w", err)
	}
	name, chatID, err = ds.addSessionAuto()
	if err != nil {
		return "", "", err
	}
	ds.LastActive = chatID
	if err := ds.save(); err != nil {
		return "", "", fmt.Errorf("save sessions: %w", err)
	}
	SetLastActiveSession(workDir, chatID)
	return name, chatID, nil
}

// RenameSession renames a session in the directory (local JSON only).
// If the new name collides with an existing session, a random "-adj-noun" suffix
// is appended automatically to avoid duplicates.
func (ds *dirSessions) RenameSession(oldName, newName string) (string, error) {
	if oldName == newName {
		return oldName, nil
	}
	if err := ValidateSessionName(newName); err != nil {
		return "", err
	}
	// Deduplicate against other sessions in this directory.
	for i, s := range ds.Sessions {
		if s.Name == oldName {
			finalName := ch.DeduplicateSessionName(newName, s.ChatID, func() []ch.NameEntry {
				entries := make([]ch.NameEntry, len(ds.Sessions))
				for j, sess := range ds.Sessions {
					entries[j] = ch.NameEntry{Name: sess.Name, ChatID: sess.ChatID}
				}
				return entries
			})
			ds.Sessions[i].Name = finalName
			// ChatID is immutable: it's the primary key in DB (tenants table).
			// Changing it would disconnect the session from its message history.
			return finalName, ds.save()
		}
	}
	return "", fmt.Errorf("session %q not found", oldName)
}

// NameByChatID returns the display name for a session by its chatID.
// Returns "" if not found.
func (ds *dirSessions) NameByChatID(chatID string) string {
	for _, s := range ds.Sessions {
		if s.ChatID == chatID {
			return s.Name
		}
	}
	return ""
}

// removeSessionByChatID removes a session by its chatID (not display name).
// Used when the display name may have been renamed in DB but local JSON
// still has the original auto-name.
func (ds *dirSessions) removeSessionByChatID(chatID string) error {
	for i, s := range ds.Sessions {
		if s.ChatID == chatID {
			ds.Sessions = append(ds.Sessions[:i], ds.Sessions[i+1:]...)
			return ds.save()
		}
	}
	return fmt.Errorf("session with chatID %q not found", chatID)
}

// sortedSessions returns sessions sorted by creation time (newest first).
func (ds *dirSessions) sortedSessions() []dirSession {
	sorted := make([]dirSession, len(ds.Sessions))
	copy(sorted, ds.Sessions)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})
	return sorted
}

// listLocalDirSessions returns all sessions in the current directory from
// the local session store (used by the sessions panel).
func (m *cliModel) listLocalDirSessions() []SessionPanelEntry {
	ds, err := LoadDirSessions(m.workDir)
	if err != nil {
		return nil
	}
	var entries []SessionPanelEntry
	for _, s := range ds.sortedSessions() {
		active := s.ChatID == m.chatID
		entries = append(entries, SessionPanelEntry{
			ID:      s.ChatID,
			Label:   s.Name,
			Type:    "main",
			Channel: "cli",
			Active:  active,
		})
	}
	return entries
}

// ListLocalDirSessions returns all local sessions for a work directory,
// sorted by creation time.
func ListLocalDirSessions(workDir string) []SessionPanelEntry {
	ds, err := LoadDirSessions(workDir)
	if err != nil {
		return nil
	}
	var result []SessionPanelEntry
	for _, s := range ds.sortedSessions() {
		result = append(result, SessionPanelEntry{
			ID:    s.ChatID,
			Label: s.Name,
		})
	}
	return result
}

// SetLastActiveSession persists the last active session for a workDir.
// chatID may be a full chatID (workDir:sessionName) or bare workDir.
// The workDir is extracted via ParseChatID to ensure correct file lookup.
// IsEphemeralChatID returns true if the chatID belongs to an ephemeral session
// (started with --ephemeral). These sessions skip all disk persistence.
func IsEphemeralChatID(chatID string) bool {
	return strings.HasPrefix(chatID, "_ephemeral:")
}

func SetLastActiveSession(workDirOrChatID, chatID string) {
	// Ephemeral sessions: skip sessions.json persistence entirely.
	if IsEphemeralChatID(chatID) {
		return
	}
	workDir, _ := ParseChatID(workDirOrChatID)
	ds, err := LoadDirSessions(workDir)
	if err != nil {
		return
	}
	ds.LastActive = chatID
	_ = ds.save()
}

// GetLastActiveSession returns the last active session chatID for a workDir.
func GetLastActiveSession(workDir string) string {
	ds, err := LoadDirSessions(workDir)
	if err != nil {
		return ""
	}
	return ds.LastActive
}

// ── Session LLM state: single source of truth ───────────────
//
// All per-session LLM state (subscription, model, max_context, max_output)
// is stored in the dirSession JSON and accessed ONLY through these two functions.
// This eliminates the "partial write" class of bugs where SaveSessionLLM and
// SaveSessionMaxContext independently loaded/modified/saved the JSON file,
// causing race conditions and state desync.
//
// RULES:
//   1. NEVER read dirSession.SubscriptionID/Model/MaxContextTokens directly — use LoadSessionLLMState
//   2. NEVER write them individually — use SaveSessionLLMState
//   3. To derive effective max_context for display, use ResolveEffectiveMaxContext

// SessionLLMState bundles ALL per-session LLM state.
// Zero value means "use global defaults".
type SessionLLMState struct {
	SubscriptionID   string // active subscription for this session
	Model            string // active model within the subscription
	MaxContextTokens int    // per-session max_context override (0 = derive from sub)
	MaxOutputTokens  int    // per-session max_output_tokens override (0 = derive from sub)
}

// IsZero returns true if no LLM state has been configured.
func (s SessionLLMState) IsZero() bool {
	return s.SubscriptionID == "" && s.Model == "" && s.MaxContextTokens == 0 && s.MaxOutputTokens == 0
}

// SaveSessionLLMState atomically writes ALL per-session LLM state to disk.
// This replaces the old SaveSessionLLM + SaveSessionMaxContext pair.
// Partial writes are impossible — either all fields are persisted or none.
//
// In remote mode (skipBackendFields=true), SubscriptionID/Model/MaxContextTokens
// are NOT written to local JSON — the backend DB is the source of truth.
func SaveSessionLLMState(workDir, chatID string, state SessionLLMState, skipBackendFields ...bool) {
	// Ephemeral sessions: skip sessions.json persistence entirely.
	if IsEphemeralChatID(chatID) {
		return
	}
	skipSub := len(skipBackendFields) > 0 && skipBackendFields[0]
	ds, err := LoadDirSessions(workDir)
	if err != nil {
		return
	}
	for i := range ds.Sessions {
		if ds.Sessions[i].ChatID == chatID {
			if !skipSub {
				ds.Sessions[i].SubscriptionID = state.SubscriptionID
				ds.Sessions[i].Model = state.Model
				ds.Sessions[i].MaxContextTokens = state.MaxContextTokens
				ds.Sessions[i].MaxOutputTokens = state.MaxOutputTokens
			}
			_ = ds.save()
			return
		}
	}
}

// LoadSessionLLMState reads ALL per-session LLM state from disk.
// Returns zero-value SessionLLMState if the session doesn't exist or has no LLM state.
func LoadSessionLLMState(workDir, chatID string) SessionLLMState {
	ds, err := LoadDirSessions(workDir)
	if err != nil {
		return SessionLLMState{}
	}
	for i := range ds.Sessions {
		if ds.Sessions[i].ChatID == chatID {
			return SessionLLMState{
				SubscriptionID:   ds.Sessions[i].SubscriptionID,
				Model:            ds.Sessions[i].Model,
				MaxContextTokens: ds.Sessions[i].MaxContextTokens,
				MaxOutputTokens:  ds.Sessions[i].MaxOutputTokens,
			}
		}
	}
	return SessionLLMState{}
}

// ResolveEffectiveMaxContext derives the effective max_context for a session.
// Priority (strict, no ambiguity):
//
//  1. Session JSON MaxContextTokens (user explicitly set, or inherited from parent)
//  2. ch.Subscription's PerModelConfigs[model].MaxContext (bound to subscription+model)
//  3. 0 (caller falls back to schema DefaultValue, typically 200000)
//
// This is the ONLY function that should be called to get max_context for display
// or runtime use. It replaces the old resolveMaxContextTokens which had broken
// fallback chains through GetCurrentValues().
func ResolveEffectiveMaxContext(state SessionLLMState, subMgr SubscriptionManager) int {
	// 1. Session JSON override (highest priority — user manually set this)
	if state.MaxContextTokens > 0 {
		return state.MaxContextTokens
	}
	// 2. ch.Subscription's per-model config
	if subMgr != nil && state.SubscriptionID != "" {
		if subs, err := subMgr.List(""); err == nil {
			for _, sub := range subs {
				if sub.ID == state.SubscriptionID {
					// 2a. Per-model config (highest specificity)
					model := state.Model
					if model == "" {
						model = sub.Model
					}
					if model != "" {
						if pmc, ok := sub.PerModelConfigs[model]; ok && pmc.MaxContext > 0 {
							return pmc.MaxContext
						}
					}
					// 2b. ch.Subscription-level MaxContext (fallback within sub)
					if sub.MaxContext > 0 {
						return sub.MaxContext
					}
					break
				}
			}
		}
	}
	// 3. No override found — use global default
	return config.DefaultMaxContextTokens
}

// ResolveEffectiveMaxOutputTokens derives the effective max_output_tokens for a session.
func ResolveEffectiveMaxOutputTokens(state SessionLLMState, subMgr SubscriptionManager) int {
	if state.MaxOutputTokens > 0 {
		return state.MaxOutputTokens
	}
	if subMgr != nil && state.SubscriptionID != "" {
		if subs, err := subMgr.List(""); err == nil {
			for _, sub := range subs {
				if sub.ID == state.SubscriptionID {
					return sub.MaxOutputTokens
				}
			}
		}
	}
	return 0
}

// SaveSessionMaxContext and LoadSessionMaxContext have been removed.
// Use SaveSessionLLMState/LoadSessionLLMState with MaxContextTokens field instead.
