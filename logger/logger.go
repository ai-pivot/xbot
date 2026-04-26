package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Type aliases
type Fields = log.Fields
type Entry = log.Entry
type Formatter = log.Formatter
type Level = log.Level

// Level constants
var (
	InfoLevel = log.InfoLevel
)

// JSONFormatter formats log entries as JSON
type JSONFormatter = log.JSONFormatter

// TextFormatter formats log entries as plain text
type TextFormatter = log.TextFormatter

// SetupConfig holds logger configuration
type SetupConfig struct {
	Level      string // Log level: debug, info, warn, error
	Format     string // Log format: text, json
	WorkDir    string // Working directory (log file location; used when LogDir is empty)
	LogDir     string // Explicit log directory (takes priority over WorkDir)
	MaxAge     int    // Log retention days (default 7)
	MaxBackups int    // Max old log files to keep (default 0 = unlimited)
	FileOnly   bool   // true: only write to log file, suppress stdout (for TUI modes)
}

// dailyRotateFile is a log file writer with daily rotation support
type dailyRotateFile struct {
	mu          sync.Mutex
	baseDir     string   // log directory
	currentFile *os.File // current log file
	currentDate string   // current date (YYYY-MM-DD)
	baseName    string   // base filename (e.g. "xbot")
}

// newDailyRotateFile creates a daily-rotating log file writer
func newDailyRotateFile(dir, baseName string) (*dailyRotateFile, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	drf := &dailyRotateFile{
		baseDir:  dir,
		baseName: baseName,
	}
	if err := drf.rotate(); err != nil {
		return nil, err
	}
	return drf, nil
}

// Write implements io.Writer
func (drf *dailyRotateFile) Write(p []byte) (n int, err error) {
	drf.mu.Lock()
	defer drf.mu.Unlock()

	// Check if rotation is needed (date changed)
	today := time.Now().Format("2006-01-02")
	if drf.currentDate != today {
		if err := drf.rotateLocked(); err != nil {
			return 0, err
		}
	}

	if drf.currentFile == nil {
		return 0, fmt.Errorf("log file not initialized")
	}

	return drf.currentFile.Write(p)
}

// Close closes the current log file
func (drf *dailyRotateFile) Close() error {
	drf.mu.Lock()
	defer drf.mu.Unlock()
	if drf.currentFile != nil {
		err := drf.currentFile.Close()
		drf.currentFile = nil
		return err
	}
	return nil
}

// rotate performs log rotation (caller must hold lock)
func (drf *dailyRotateFile) rotate() error {
	drf.mu.Lock()
	defer drf.mu.Unlock()
	return drf.rotateLocked()
}

// rotateLocked performs log rotation (lock already held)
func (drf *dailyRotateFile) rotateLocked() error {
	today := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("%s-%s.log", drf.baseName, today)
	logPath := filepath.Join(drf.baseDir, logFileName)

	// If date unchanged and file is open, no rotation needed
	if drf.currentDate == today && drf.currentFile != nil {
		return nil
	}

	// Close old file
	if drf.currentFile != nil {
		_ = drf.currentFile.Close()
	}

	// Open new file (append mode)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	drf.currentFile = f
	drf.currentDate = today
	return nil
}

// Global log file writer (used by Close)
var globalRotateFile *dailyRotateFile
var globalRotateFileMu sync.Mutex // Guards concurrent writes to globalRotateFile (C-11)

// cleanupStopCh signals the cleanupOldLogs goroutine to stop
var cleanupStopCh chan struct{}

// Setup initializes the logging system (file output + daily rotation + auto cleanup)
func Setup(cfg SetupConfig) error {
	// Set log level
	level, err := log.ParseLevel(cfg.Level)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)

	// Set log format
	switch cfg.Format {
	case "json":
		log.SetFormatter(&log.JSONFormatter{})
	default:
		log.SetFormatter(&log.TextFormatter{
			FullTimestamp: true,
		})
	}

	// Enable file output if log directory is specified
	logDir := cfg.LogDir
	if logDir == "" && cfg.WorkDir != "" {
		logDir = filepath.Join(cfg.WorkDir, ".xbot", "logs")
	}
	if logDir != "" {
		drf, err := newDailyRotateFile(logDir, "xbot")
		if err != nil {
			return fmt.Errorf("failed to create log rotator: %w", err)
		}

		if cfg.FileOnly {
			log.SetOutput(drf)
		} else {
			log.SetOutput(io.MultiWriter(os.Stdout, drf))
		}
		globalRotateFileMu.Lock()
		globalRotateFile = drf
		globalRotateFileMu.Unlock()

		// Start background old-log cleanup
		maxAge := cfg.MaxAge
		if maxAge <= 0 {
			maxAge = 7 // Default: retain 7 days
		}
		cleanupStopCh = make(chan struct{})
		go cleanupOldLogs(logDir, maxAge, cleanupStopCh)
	}

	return nil
}

// Close shuts down the logging system (flushes buffer, closes file, stops background cleanup)
func Close() {
	// Stop background cleanup goroutine
	if cleanupStopCh != nil {
		close(cleanupStopCh)
		cleanupStopCh = nil
	}

	globalRotateFileMu.Lock()
	drf := globalRotateFile
	globalRotateFile = nil
	globalRotateFileMu.Unlock()
	if drf != nil {
		_ = drf.Close()
	}
}

// cleanupOldLogs removes expired log files
// goroutine exits on next tick after receiving signal on stopCh
func cleanupOldLogs(dir string, maxAge int, stopCh <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run once at startup
	doCleanup(dir, maxAge)

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			doCleanup(dir, maxAge)
		}
	}
}

// doCleanup performs the cleanup logic
func doCleanup(dir string, maxAge int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Use fmt.Fprintf(os.Stderr, ...) to avoid recursive logging
		fmt.Fprintf(os.Stderr, "[logger] failed to read log directory: %v\n", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -maxAge)
	var deleted []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Only process .log files
		if !strings.HasSuffix(name, ".log") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[logger] failed to get file info for %s: %v\n", name, err)
			continue
		}

		// Check expiry by modification time
		if info.ModTime().Before(cutoff) {
			filePath := filepath.Join(dir, name)
			if err := os.Remove(filePath); err != nil {
				fmt.Fprintf(os.Stderr, "[logger] failed to delete old log file %s: %v\n", name, err)
			} else {
				deleted = append(deleted, name)
			}
		}
	}

	// Sort by name for stable output
	if len(deleted) > 0 {
		sort.Strings(deleted)
		fmt.Fprintf(os.Stderr, "[logger] cleaned up %d old log file(s): %s\n", len(deleted), strings.Join(deleted, ", "))
	}
}

// SetFormatter sets the log formatter
func SetFormatter(formatter Formatter) { log.SetFormatter(formatter) }

// SetLevel sets the log level
func SetLevel(level Level) { log.SetLevel(level) }

// ParseLevel parses a log level string
func ParseLevel(lvl string) (Level, error) { return log.ParseLevel(lvl) }

// WithField creates a log entry with a single field
func WithField(key string, value any) *Entry {
	return log.WithField(key, value)
}

// WithFields creates a log entry with multiple fields
func WithFields(fields Fields) *Entry {
	return log.WithFields(fields)
}

// WithError creates a log entry with an error field
func WithError(err error) *Entry {
	return log.WithError(err)
}

// Debug logs at Debug level
func Debug(args ...any) { log.Debug(args...) }

// Debugf logs a formatted Debug message
func Debugf(format string, args ...any) { log.Debugf(format, args...) }

// Info logs at Info level
func Info(args ...any) { log.Info(args...) }

// Infof logs a formatted Info message
func Infof(format string, args ...any) { log.Infof(format, args...) }

// Warn logs at Warn level
func Warn(args ...any) { log.Warn(args...) }

// Warnf logs a formatted Warn message
func Warnf(format string, args ...any) { log.Warnf(format, args...) }

// Error logs at Error level
func Error(args ...any) { log.Error(args...) }

// Errorf logs a formatted Error message
func Errorf(format string, args ...any) { log.Errorf(format, args...) }

// Fatal logs at Fatal level and exits
func Fatal(args ...any) { log.Fatal(args...) }

// Fatalf logs a formatted Fatal message and exits
func Fatalf(format string, args ...any) { log.Fatalf(format, args...) }
