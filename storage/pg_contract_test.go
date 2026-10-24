package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/llm"
)

// newPGTestPool creates a fresh PG database (unique per test) with the runtime
// schema applied, and returns its pool. Tests that need raw SQL — to seed a
// column the public interface cannot write, or to assert one was not touched —
// use this instead of newPGTestStorage.
func newPGTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("XBOT_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set XBOT_PG_TEST_DSN to run PG contract tests")
	}

	// Use a unique database per test for full isolation.
	// (pgxpool multiplexes connections, so SET search_path doesn't propagate —
	// a per-test database is the only reliable isolation.)
	dbName := fmt.Sprintf("xbot_test_%d", time.Now().UnixNano())

	// Connect to 'postgres' to create the test database
	adminPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("admin pgxpool: %v", err)
	}
	ctx := context.Background()
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", dbName)); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", dbName, err)
	}
	adminPool.Close()

	// Connect to the fresh test database
	testDSN := strings.Replace(dsn, "/xbot?", "/"+dbName+"?", 1)
	if !strings.Contains(testDSN, dbName) {
		// Fallback: append database name
		testDSN = strings.Replace(dsn, "/postgres?", "/"+dbName+"?", 1)
	}

	pool, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("test pgxpool: %v", err)
	}

	if err := initPGSchema(ctx, pool); err != nil {
		t.Fatalf("initPGSchema: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		// Drop the test database
		dropPool, _ := pgxpool.New(context.Background(), dsn)
		defer dropPool.Close()
		dropPool.Exec(ctx, fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='%s'", dbName))
		dropPool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	})

	return pool
}

// newPGTestStorage creates a fresh PG Storage for contract testing.
// Uses XBOT_PG_TEST_DSN env var. Each test gets a fresh schema to avoid
// interference; all test schemas are dropped in t.Cleanup.
func newPGTestStorage(t *testing.T) Storage {
	t.Helper()
	pool := newPGTestPool(t)
	store, err := NewPGStorageWithPool(pool)
	if err != nil {
		t.Fatalf("NewPGStorageWithPool: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// NewPGStorageWithPool creates a PG Storage from an existing pool (for testing).
// In production, NewPGStorage creates its own pool.
//
// Every store must be wired here — a nil field panics on first use, and the
// contract suite calls each accessor, so this is also a wiring guard.
func NewPGStorageWithPool(pool *pgxpool.Pool) (Storage, error) {
	pg := &pgStorage{pool: pool}
	pg.tenant = newPGTenantStore(pool)
	pg.memory = newPGMemoryStore(pool)
	pg.config = newPGConfigStore(pool)
	pg.cron = newPGCronStore(pool)
	pg.trigger = newPGTriggerStore(pool)
	pg.oauth = newPGOAuthTokenStore(pool)
	pg.session = newPGSessionStore(pool)
	return pg, nil
}

// initPGSchemaInSchema creates all tables in a specific schema.
// This is a variant of initPGSchema that uses SET search_path for isolation.

func TestPGStorageContract(t *testing.T) {
	RunStorageContractTests(t, newPGTestStorage)
}

// TestPGSessionStoreNULMessageStoredSanitized is the backend-specific half of
// the binary-tool-output regression: SQLite silently stores NUL bytes (and
// invalid UTF-8), so the failure only surfaced after switching to PostgreSQL,
// which rejects the whole INSERT with SQLSTATE 22021
// ("invalid byte sequence for encoding UTF8": 0x00). A binary tool result must
// be storable and read back as valid UTF-8 without NUL.
func TestPGSessionStoreNULMessageStoredSanitized(t *testing.T) {
	s := newPGTestStorage(t)
	defer s.Close()
	tenantID := createTestTenant(t, s, uniqueChatID(t))
	ss := getSessionStore(t, s, tenantID)

	content := "ELF header\x00\x00 then \xff\xfe bytes"
	if err := ss.AddMessage(tenantID, llm.ChatMessage{Role: "tool", Content: content}); err != nil {
		t.Fatalf("AddMessage with NUL must not fail on PG: %v", err)
	}
	msgs, err := ss.GetHistory(tenantID, 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("GetHistory returned %d messages, want 1", len(msgs))
	}
	got := msgs[0].Content
	if strings.ContainsRune(got, 0) {
		t.Errorf("stored content still carries NUL: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("stored content is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "ELF header") || !strings.Contains(got, "bytes") {
		t.Errorf("sanitisation dropped payload: %q", got)
	}
}
