package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultPluginLogMaxAge is the default maximum age (in days) for plugin log files.
const DefaultPluginLogMaxAge = 7

// ---------------------------------------------------------------------------
// rotateWriter — daily-rotating log file writer (lightweight, no logrus dependency)
// ---------------------------------------------------------------------------

// rotateWriter is a thread-safe io.Writer that automatically rotates log files
// by date. Each day produces a new file: <baseName>-YYYY-MM-DD.<suffix>.
// If suffix is empty, defaults to ".log".
type rotateWriter struct {
	mu          sync.Mutex
	baseDir     string
	currentFile *os.File
	currentDate string
	baseName    string
	suffix      string // e.g. ".log" or ".jsonl"
}

// newRotateWriter creates a new daily-rotating log writer with default ".log" suffix.
// The parent directory is created if it doesn't exist.
func newRotateWriter(dir, baseName string) (*rotateWriter, error) {
	return newRotateWriterWithSuffix(dir, baseName, ".log")
}

// newRotateWriterWithSuffix creates a new daily-rotating log writer with custom suffix.
func newRotateWriterWithSuffix(dir, baseName, suffix string) (*rotateWriter, error) {
	if suffix == "" {
		suffix = ".log"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory %s: %w", dir, err)
	}
	rw := &rotateWriter{
		baseDir:  dir,
		baseName: baseName,
		suffix:   suffix,
	}
	if err := rw.rotateLocked(); err != nil {
		return nil, err
	}
	return rw, nil
}

// Write implements io.Writer. It checks for date change and rotates if needed.
func (rw *rotateWriter) Write(p []byte) (n int, err error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if rw.currentDate != today {
		if err := rw.rotateLocked(); err != nil {
			return 0, err
		}
	}

	if rw.currentFile == nil {
		return 0, fmt.Errorf("log file not initialized")
	}
	return rw.currentFile.Write(p)
}

// Close closes the current log file.
func (rw *rotateWriter) Close() error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.currentFile != nil {
		err := rw.currentFile.Close()
		rw.currentFile = nil
		return err
	}
	return nil
}

// Dir returns the log directory path.
func (rw *rotateWriter) Dir() string {
	return rw.baseDir
}

func (rw *rotateWriter) rotateLocked() error {
	today := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("%s-%s%s", rw.baseName, today, rw.suffix)
	logPath := filepath.Join(rw.baseDir, logFileName)

	if rw.currentDate == today && rw.currentFile != nil {
		return nil
	}

	if rw.currentFile != nil {
		_ = rw.currentFile.Close()
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}

	rw.currentFile = f
	rw.currentDate = today
	return nil
}

// pluginLogManager — manages per-plugin log writers + unified cleanup
// ---------------------------------------------------------------------------

// pluginLogManager creates and manages per-plugin log writers, and runs a
// single background goroutine for log cleanup across all plugin log directories.
type pluginLogManager struct {
	mu         sync.Mutex
	writers    map[string]*rotateWriter // pluginID → rotateWriter
	xbotHome   string
	maxAge     int
	stopCh     chan struct{}
	cleanupDir string // shared directory for cleanup scanning
}

// newPluginLogManager creates the log manager and starts the cleanup goroutine.
// Log files are stored under <xbotHome>/plugins/<pluginID>/logs/.
// Cleanup scans all plugin log directories.
func newPluginLogManager(xbotHome string, maxAge int) *pluginLogManager {
	if maxAge <= 0 {
		maxAge = DefaultPluginLogMaxAge
	}
	plm := &pluginLogManager{
		writers:    make(map[string]*rotateWriter),
		xbotHome:   xbotHome,
		maxAge:     maxAge,
		stopCh:     make(chan struct{}),
		cleanupDir: filepath.Join(xbotHome, "plugins"),
	}
	go plm.cleanupLoop()
	return plm
}

// GetWriter returns (or creates) a daily-rotating log writer for the given plugin.
// The log directory is <xbotHome>/plugins/<pluginID>/logs/.
func (plm *pluginLogManager) GetWriter(pluginID string) (*rotateWriter, error) {
	plm.mu.Lock()
	defer plm.mu.Unlock()

	if rw, ok := plm.writers[pluginID]; ok {
		return rw, nil
	}

	logDir := filepath.Join(plm.xbotHome, "plugins", pluginID, "logs")
	// Sanitize pluginID for use as filename (already validated by ID regex, but be safe).
	safeName := sanitizeBaseName(pluginID)
	rw, err := newRotateWriter(logDir, safeName)
	if err != nil {
		return nil, err
	}
	plm.writers[pluginID] = rw
	return rw, nil
}

// RemoveWriter closes and removes the log writer for a plugin.
func (plm *pluginLogManager) RemoveWriter(pluginID string) {
	plm.mu.Lock()
	defer plm.mu.Unlock()
	if rw, ok := plm.writers[pluginID]; ok {
		_ = rw.Close()
		delete(plm.writers, pluginID)
	}
}

// CloseAll closes all log writers and stops the cleanup goroutine.
func (plm *pluginLogManager) CloseAll() {
	// Stop cleanup goroutine
	select {
	case <-plm.stopCh:
		// already stopped
	default:
		close(plm.stopCh)
	}

	plm.mu.Lock()
	defer plm.mu.Unlock()
	for id, rw := range plm.writers {
		_ = rw.Close()
		delete(plm.writers, id)
	}
}

// cleanupLoop runs a periodic cleanup of old plugin log files.
func (plm *pluginLogManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run once at startup
	plm.doCleanup()

	for {
		select {
		case <-plm.stopCh:
			return
		case <-ticker.C:
			plm.doCleanup()
		}
	}
}

// doCleanup scans all plugin log directories and removes old .log files.
func (plm *pluginLogManager) doCleanup() {
	cutoff := time.Now().AddDate(0, 0, -plm.maxAge)

	// Scan the plugins root directory for per-plugin log subdirectories.
	pluginsDir := filepath.Join(plm.xbotHome, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[plugin-log] failed to read plugins directory: %v\n", err)
		return
	}

	var totalDeleted []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		logDir := filepath.Join(pluginsDir, entry.Name(), "logs")
		deleted := cleanLogDir(logDir, cutoff)
		totalDeleted = append(totalDeleted, deleted...)
	}

	// Also clean audit log directory (audit-YYYY-MM-DD.log files).
	// The audit log itself may have been moved to daily rotation.
	auditDeleted := cleanLogDir(pluginsDir, cutoff)
	totalDeleted = append(totalDeleted, auditDeleted...)

	if len(totalDeleted) > 0 {
		sort.Strings(totalDeleted)
		fmt.Fprintf(os.Stderr, "[plugin-log] cleaned up %d old log file(s): %s\n", len(totalDeleted), strings.Join(totalDeleted, ", "))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// cleanLogDir removes .log and .jsonl files older than cutoff in the given directory.
func cleanLogDir(dir string, cutoff time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // directory may not exist
	}

	var deleted []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		if ext != ".log" && ext != ".jsonl" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			filePath := filepath.Join(dir, name)
			if err := os.Remove(filePath); err == nil {
				deleted = append(deleted, name)
			}
		}
	}
	return deleted
}

// sanitizeBaseName replaces characters that are unsafe for filenames.
func sanitizeBaseName(name string) string {
	r := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			r = append(r, c)
		} else {
			r = append(r, '_')
		}
	}
	return string(r)
}
