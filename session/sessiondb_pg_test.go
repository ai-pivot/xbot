package session

import (
	"testing"

	"xbot/storage"
)

// fakeTenantStore embeds storage.TenantStore so only the methods reached by the
// test need implementations.
type fakeTenantStore struct {
	storage.TenantStore
	deleted bool
}

func (f *fakeTenantStore) GetTenantIDByChannelChatID(string, string) (int64, error) {
	return 1, nil
}
func (f *fakeTenantStore) DeleteTenant(int64) error {
	f.deleted = true
	return nil
}

type fakeStorage struct {
	storage.Storage
	tenant storage.TenantStore
}

func (f *fakeStorage) Tenant() storage.TenantStore { return f.tenant }

// TestSessionDBHelpersWithoutSQLiteHandle pins the PG contract: a session with
// no SQLite handle must reject SQLite-only helpers explicitly instead of
// dereferencing nil (m.tenantSvc / m.db).
func TestSessionDBHelpersWithoutSQLiteHandle(t *testing.T) {
	ms := NewMultiTenantWithStorage(nil)
	if ms == nil {
		t.Fatal("NewMultiTenantWithStorage returned nil")
	}
	if ms.DB() != nil {
		t.Fatal("session built from injected storage must not have a SQLite handle")
	}

	if _, err := ms.SessionDBFor(1); err == nil {
		t.Fatal("SessionDBFor must return an explicit error without a SQLite handle")
	}
	if _, err := ms.SessionServiceForSQLite(1); err == nil {
		t.Fatal("SessionServiceForSQLite must return an explicit error without a SQLite handle")
	}
	// Must be a no-op, not a panic.
	ms.deleteSessionDBFile(1)
}

// TestDestroySessionWithoutSQLiteHandle exercises the PG delete path: only the
// tenant row is deleted (PostgreSQL cascades session_messages/iteration_history)
// and no session DB file logic runs.
func TestDestroySessionWithoutSQLiteHandle(t *testing.T) {
	tenant := &fakeTenantStore{}
	ms := NewMultiTenantWithStorage(&fakeStorage{tenant: tenant})

	if err := ms.DestroySession("web", "chat_does_not_exist_in_cache"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}
	if !tenant.deleted {
		t.Fatal("DestroySession must delete the tenant row on a non-SQLite backend")
	}
}
