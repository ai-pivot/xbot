package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "xbot/logger"

	"xbot/storage/internal"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database connection with schema management.
// Uses WAL mode with a read-write pool (max 4 conns) for concurrent reads.
// WAL allows one writer + multiple readers simultaneously, so API read
// queries (session-tree, get_history, get_context_usage) don't block on
// agent writes (IncrementalPersist, SaveState).
//
// busy_timeout is set via DSN (_pragma) so ALL connections in the pool
// retry on SQLITE_BUSY, not just the first one. This is critical when
// MaxOpenConns > 1 — without DSN-level pragma, only the first connection
// gets busy_timeout, and concurrent writes on other connections fail
// immediately with SQLITE_BUSY.
type DB struct {
	conn *sql.DB
	path string
	mu   sync.RWMutex
}

const schemaVersion = 48

// Open opens or creates a SQLite database at the given path
// If the database doesn't exist, it will be created with the required schema
func Open(path string) (*DB, error) {
	// Ensure directory exists (skip for :memory: which is in-memory SQLite)
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	// Build DSN with pragmas that apply to ALL connections in the pool.
	// _pragma is essential when MaxOpenConns > 1: it ensures every connection
	// gets busy_timeout and journal_mode, not just the first one.
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Set connection pool settings.
	// WAL mode allows concurrent reads while a write is in progress.
	// MaxOpenConns > 1 enables Go's connection pool to serve reads from
	// idle connections even when one connection is mid-write.
	// This prevents API read queries from blocking on agent DB writes.
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)
	conn.SetConnMaxLifetime(0)

	// For non-:memory: databases, WAL/busy_timeout/foreign_keys are already
	// set via DSN _pragma. For :memory: (tests), set them here as fallback.
	if path == ":memory:" {
		if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("set WAL mode: %w", err)
		}
		if _, err := conn.Exec("PRAGMA busy_timeout=10000"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("set busy_timeout: %w", err)
		}
		if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("enable foreign keys: %w", err)
		}
	}

	db := &DB{
		conn: conn,
		path: path,
	}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	log.WithField("path", path).Info("SQLite database opened")
	return db, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.conn != nil {
		if err := db.conn.Close(); err != nil {
			return fmt.Errorf("close database: %w", err)
		}
		db.conn = nil
	}
	return nil
}

// Conn returns the underlying database connection
func (db *DB) Conn() *sql.DB {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.conn
}

// initSchema creates the database schema if it doesn't exist, and runs migrations
func (db *DB) initSchema() error {
	conn := db.Conn()

	// Check if schema already exists by checking tenants table
	var tableName string
	err := conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='tenants'").Scan(&tableName)
	if err == sql.ErrNoRows {
		if err := db.createSchema(); err != nil {
			return err
		}
		// createSchema only creates v2 base; run full migration chain
		return db.migrateSchema(2)
	}
	if err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	// Schema exists — check version and run migrations
	var version int
	err = conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		version = 1
	}
	if version < schemaVersion {
		return db.migrateSchema(version)
	}
	return nil
}

// parseSQLiteTime parses a time string from SQLite into time.Time.
// Delegates to internal.ParseTimestamp which correctly handles timezone
// interpretation for values stored by the modernc.org/sqlite driver.
func parseSQLiteTime(s string) time.Time {
	return internal.ParseTimestamp(s)
}
