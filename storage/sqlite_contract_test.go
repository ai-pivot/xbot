package storage

import (
	"path/filepath"
	"testing"

	"xbot/config"
	"xbot/storage/sqlite"
)

// newSQLiteTestStorage creates a fresh SQLite Storage backed by a temp file.
func newSQLiteTestStorage(t *testing.T) Storage {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}

	// Create the shared tenant (schema init may not create it)
	db.Conn().Exec(`INSERT OR IGNORE INTO tenants (id, channel, chat_id) VALUES (0, '_shared', '_shared')`)

	store := NewSQLiteStorage(
		db,
		func(tenantID int64) (SessionStore, error) {
			// For SQLite: each session uses the same main DB (no per-session DB in tests)
			svc := sqlite.NewSessionService(db)
			return &sqliteSessionStore{svc}, nil
		},
		func() error { return db.Close() },
	)

	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteStorageContract(t *testing.T) {
	// Suppress config noise
	_ = config.DBFilePath
	RunStorageContractTests(t, newSQLiteTestStorage)
}

// PG contract test — gated by XBOT_PG_TEST_DSN env var
// Usage:
//   docker run -d --rm -e POSTGRES_PASSWORD=test -e POSTGRES_DB=xbot \
//     -p 127.0.0.1:55433:5432 postgres:16-alpine
//   XBOT_PG_TEST_DSN="postgres://postgres:test@127.0.0.1:55433/xbot?sslmode=disable" \
//     go test ./storage/ -run TestPGStorageContract -v -timeout 120s
