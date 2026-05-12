package channel

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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

// ParseChatID extracts the workDir and sessionName from a chatID.
// Returns (workDir, sessionName). If there's no ":" separator, sessionName is "default".
func ParseChatID(chatID string) (workDir, sessionName string) {
	idx := strings.LastIndex(chatID, ":")
	if idx <= 0 || idx == len(chatID)-1 {
		return chatID, defaultSessionName
	}
	workDir = chatID[:idx]
	sessionName = chatID[idx+1:]
	// Validate: workDir should look like an absolute or relative path
	if !strings.HasPrefix(workDir, "/") && !strings.HasPrefix(workDir, ".") && !strings.HasPrefix(workDir, "~") {
		return chatID, defaultSessionName
	}
	// Resolve relative workDir (e.g. "." from legacy sessions) to absolute path
	if !filepath.IsAbs(workDir) {
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

// ── Per-directory session storage ──

// dirSessions stores the list of sessions for a given directory.
// Persisted to ~/.xbot/sessions/<sha256>.json
type dirSessions struct {
	Dir        string       `json:"dir"`
	Sessions   []dirSession `json:"sessions"`
	LastActive string       `json:"last_active,omitempty"` // chatID of last active session
}

type dirSession struct {
	Name      string    `json:"name"`
	ChatID    string    `json:"chat_id"`
	CreatedAt time.Time `json:"created_at"`
	CWD       string    `json:"cwd,omitempty"` // per-session working directory (worktree path, etc.)
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
					CreatedAt: time.Now(),
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
			CreatedAt: time.Now(),
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

// autoNamePrefix is the prefix for auto-generated session names.
const autoNamePrefix = "Agent-"

// sessionAdj and sessionNoun provide word lists for generating natural-sounding session names
// like "Agent-brave-fox" or "Agent-calm-stone". 16×16 = 256 unique combinations.
var (
	sessionAdj = []string{
		"brave", "calm", "swift", "keen", "warm", "witty", "sage", "brisk",
		"cool", "bold", "sharp", "lucid", "sunny", "frank", "deft", "astute",
	}
	sessionNoun = []string{
		"fox", "hawk", "lynx", "dove", "panda", "otter", "falcon", "heron",
		"stone", "flame", "brook", "cedar", "comet", "coral", "ember", "zephyr",
	}
)

// generateSessionName creates a random session name like "Agent-brave-fox".
func generateSessionName() (string, error) {
	adjIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(sessionAdj))))
	if err != nil {
		return "", err
	}
	nounIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(sessionNoun))))
	if err != nil {
		return "", err
	}
	return autoNamePrefix + sessionAdj[adjIdx.Int64()] + "-" + sessionNoun[nounIdx.Int64()], nil
}

// IsAutoSessionName returns true if the name looks like an auto-generated session name.
func IsAutoSessionName(name string) bool {
	return strings.HasPrefix(name, autoNamePrefix)
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
		CreatedAt: time.Now(),
	})
	return chatID, ds.save()
}

// addSessionAuto creates a new session with an auto-generated "Agent-xxxxxx" name.
func (ds *dirSessions) addSessionAuto() (name string, chatID string, err error) {
	for i := 0; i < 10; i++ {
		name, err = generateSessionName()
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

// RenameSession renames a session in the directory (local JSON only).
func (ds *dirSessions) RenameSession(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if err := ValidateSessionName(newName); err != nil {
		return err
	}
	if ds.hasSession(newName) {
		return fmt.Errorf("session %q already exists", newName)
	}
	for i, s := range ds.Sessions {
		if s.Name == oldName {
			ds.Sessions[i].Name = newName
			ds.Sessions[i].ChatID = SessionChatID(ds.Dir, newName)
			return ds.save()
		}
	}
	return fmt.Errorf("session %q not found", oldName)
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
func SetLastActiveSession(workDirOrChatID, chatID string) {
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
