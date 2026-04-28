package plugin

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Audit Logger — append-only JSONL audit trail for plugin operations
// ---------------------------------------------------------------------------

// Audit action constants.
const (
	AuditActivate   = "activate"
	AuditDeactivate = "deactivate"
	AuditInstall    = "install"
	AuditUninstall  = "uninstall"
	AuditReload     = "reload"
	AuditDisable    = "disable"
)

// AuditEntry records a single auditable plugin event.
type AuditEntry struct {
	Timestamp time.Time      `json:"timestamp"`
	PluginID  string         `json:"plugin_id"`
	Action    string         `json:"action"`
	Details   map[string]any `json:"details,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// AuditFilter specifies query criteria for audit entries.
// Zero-value fields mean "no filter on that field".
type AuditFilter struct {
	PluginID string
	From     time.Time
	To       time.Time
}

// AuditLogger writes append-only JSONL audit logs.
// The underlying file is opened with O_APPEND|O_CREATE|O_WRONLY for atomic appends.
type AuditLogger struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// NewAuditLogger creates an AuditLogger that appends to the given file path.
// The parent directory is created if it doesn't exist.
func NewAuditLogger(path string) (*AuditLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &AuditLogger{file: f, path: path}, nil
}

// Log writes an audit entry. If Timestamp is zero, it is set to time.Now().
// Write errors are silently ignored — audit logging must not block the caller.
func (al *AuditLogger) Log(entry AuditEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')
	al.mu.Lock()
	defer al.mu.Unlock()
	_, _ = al.file.Write(data)
}

// Query reads the audit log from disk and returns entries matching the filter.
// It first syncs pending writes, then opens the file read-only.
// Results are sorted by Timestamp ascending.
func (al *AuditLogger) Query(filter AuditFilter) []AuditEntry {
	al.mu.Lock()
	al.file.Sync()
	al.mu.Unlock()

	f, err := os.Open(al.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var results []AuditEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if filter.PluginID != "" && entry.PluginID != filter.PluginID {
			continue
		}
		if !filter.From.IsZero() && entry.Timestamp.Before(filter.From) {
			continue
		}
		if !filter.To.IsZero() && entry.Timestamp.After(filter.To) {
			continue
		}
		results = append(results, entry)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})
	return results
}

// Clear truncates the audit log file. Safe for concurrent use with Log.
func (al *AuditLogger) Clear() {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.file.Close()
	al.file, _ = os.OpenFile(al.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

// Close closes the audit log file.
func (al *AuditLogger) Close() {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.file.Close()
}
