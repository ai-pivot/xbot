package sqlite

import (
	"path/filepath"
	"testing"
)

// TestDB_ConnAfterCloseIsNotNil guards the regression behind the 2026-10-27
// crash loop: DB.Close() used to set conn = nil, so any goroutine that touched
// the database after shutdown dereferenced a nil *sql.DB. The stack was
//
//	database/sql.(*DB).conn(0x0, ...)
//	  <- sqlite.(*TenantService).GetTenantDBInfo
//	  <- session.(*MultiTenantSession).deleteSessionDBFile
//	  <- DestroySession <- destroyInteractiveSession <- cancelChildSessions
//
// panicking the background interactive session teardown.
//
// A closed *sql.DB is safe to call — every method returns sql.ErrDBClosed —
// so keeping the handle converts the panic into an ordinary error.
func TestDB_ConnAfterCloseIsNotNil(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "conn-after-close.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db.IsClosed() {
		t.Fatal("freshly opened DB must not report closed")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !db.IsClosed() {
		t.Error("IsClosed must report true after Close")
	}

	conn := db.Conn()
	if conn == nil {
		t.Fatal("Conn() returned nil after Close — this is the nil-deref trap")
	}

	// Must return an error, not panic.
	var n int
	err = conn.QueryRow("SELECT 1").Scan(&n)
	if err == nil {
		t.Fatal("query on a closed DB should fail")
	}
	t.Logf("query on closed DB returned: %v", err)
}

// TestGetTenantDBInfo_ClosedDBReturnsError pins the exact call site that
// panicked: reading the tenant registry during teardown must degrade to an
// error so deleteSessionDBFile can skip the unlink.
func TestGetTenantDBInfo_ClosedDBReturnsError(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tenant-info-closed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	svc := NewTenantService(db)

	id, err := svc.GetOrCreateTenantID("cli", "chat-1")
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}
	if _, err := svc.GetTenantDBInfo(id); err != nil {
		t.Fatalf("GetTenantDBInfo on a live DB must succeed: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Must not panic and must not report success.
	if info, err := svc.GetTenantDBInfo(id); err == nil {
		t.Fatalf("GetTenantDBInfo on a closed DB must fail, got %+v", info)
	}
}

// TestTenantService_NilDBStillErrors keeps the original guard honest.
func TestTenantService_NilDBStillErrors(t *testing.T) {
	var svc *TenantService
	if _, err := svc.GetTenantDBInfo(1); err == nil {
		t.Fatal("nil TenantService must error")
	}
	if _, err := (&TenantService{}).GetTenantDBInfo(1); err == nil {
		t.Fatal("TenantService with nil db must error")
	}
}
