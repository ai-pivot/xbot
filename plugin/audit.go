package plugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
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

// AuditLogger writes append-only JSONL audit logs with daily rotation.
// Files are named audit-YYYY-MM-DD.jsonl and stored alongside the original
// audit.jsonl path. Old files are cleaned up by pluginLogManager.
type AuditLogger struct {
	mu          sync.Mutex
	file        *os.File
	path        string        // directory containing audit log files
	rw          *rotateWriter // nil if rotateWriter creation failed, fall back to single file
	usingRotate bool
}

// NewAuditLogger creates an AuditLogger with daily-rotating files.
// Audit files are named audit-YYYY-MM-DD.jsonl in the same directory as path.
// The legacy single-file path parameter is kept for backward compat in tests.
func NewAuditLogger(path string) (*AuditLogger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	// Try daily-rotating writer first
	rw, err := newRotateWriterWithSuffix(dir, "audit", ".jsonl")
	if err != nil {
		// Fall back to single file
		log.Glob(log.CatPlugin).WithField("path", dir).Warn("Failed to create audit rotate writer, falling back to single file: ", err)
		f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if ferr != nil {
			return nil, ferr
		}
		return &AuditLogger{file: f, path: path}, nil
	}

	return &AuditLogger{path: dir, rw: rw, usingRotate: true}, nil
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

	if al.usingRotate {
		al.rw.Write(data)
	} else if al.file != nil {
		al.file.Write(data)
	}
}

// Query reads the audit log from disk and returns entries matching the filter.
// In rotation mode, it scans all audit-YYYY-MM-DD.jsonl files.
// In legacy mode, it reads the single audit.jsonl file.
// Results are sorted by Timestamp ascending.
func (al *AuditLogger) Query(filter AuditFilter) []AuditEntry {
	al.mu.Lock()
	if al.usingRotate {
		al.rw.Write(nil) // trigger rotation if needed (no-op write)
	}
	if al.file != nil {
		al.file.Sync()
	}
	al.mu.Unlock()

	var results []AuditEntry

	if al.usingRotate {
		results = al.queryRotated(filter)
	} else {
		results = al.querySingleFile(al.path, filter)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})
	return results
}

// queryRotated scans all audit-YYYY-MM-DD.jsonl files in the audit directory.
func (al *AuditLogger) queryRotated(filter AuditFilter) []AuditEntry {
	var results []AuditEntry
	entries, err := os.ReadDir(al.path)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Skip if date is outside filter range
		filePath := filepath.Join(al.path, name)
		results = append(results, al.querySingleFile(filePath, filter)...)
	}
	return results
}

// querySingleFile reads entries from a single audit log file.
func (al *AuditLogger) querySingleFile(path string, filter AuditFilter) []AuditEntry {
	f, err := os.Open(path)
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
	return results
}

// Clear truncates the audit log file. Safe for concurrent use with Log.
// In rotation mode, it truncates today's file and reopens it.
// Returns an error if the file cannot be reopened after truncation.
func (al *AuditLogger) Clear() error {
	al.mu.Lock()
	defer al.mu.Unlock()

	if al.usingRotate {
		// In rotation mode, close the current file, truncate today's audit file,
		// then recreate the rotateWriter.
		_ = al.rw.Close()
		today := time.Now().Format("2006-01-02")
		todayFile := filepath.Join(al.path, fmt.Sprintf("audit-%s.jsonl", today))
		// Truncate the file if it exists
		f, err := os.OpenFile(todayFile, os.O_WRONLY|os.O_TRUNC, 0600)
		if err == nil {
			f.Close()
		}
		rw, err := newRotateWriterWithSuffix(al.path, "audit", ".jsonl")
		if err != nil {
			return fmt.Errorf("audit: reopen after clear: %w", err)
		}
		al.rw = rw
		return nil
	}

	// Legacy single-file mode
	al.file.Close()
	f, err := os.OpenFile(al.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("audit: reopen after clear: %w", err)
	}
	al.file = f
	return nil
}

// Close closes the audit log file(s).
func (al *AuditLogger) Close() {
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.usingRotate {
		al.rw.Close()
	} else if al.file != nil {
		al.file.Close()
	}
}
