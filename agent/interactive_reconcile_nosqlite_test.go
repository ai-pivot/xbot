package agent

import (
	"testing"
	"time"

	"xbot/session"
	"xbot/storage"
	"xbot/storage/sqlite"
)

// fakeInteractiveAgentStore is a no-dependency InteractiveAgentStore used to
// exercise the agent's registry paths WITHOUT any SQLite handle.
type fakeInteractiveAgentStore struct {
	interrupted int64
	orphanCalls int
	upserts     int
}

func (f *fakeInteractiveAgentStore) Upsert(sqlite.InteractiveAgentRecord) error {
	f.upserts++
	return nil
}
func (f *fakeInteractiveAgentStore) Get(string) (*sqlite.InteractiveAgentRecord, error) {
	return nil, sqlite.ErrInteractiveAgentNotFound
}
func (f *fakeInteractiveAgentStore) ListChildAgents(string, string) ([]sqlite.InteractiveAgentRecord, error) {
	return nil, nil
}
func (f *fakeInteractiveAgentStore) MarkStatus(string, string, uint64) error { return nil }
func (f *fakeInteractiveAgentStore) MarkAllRunningAsInterrupted() (int64, error) {
	f.interrupted++
	return 1, nil
}
func (f *fakeInteractiveAgentStore) MarkUnloadedCascade(string) error { return nil }
func (f *fakeInteractiveAgentStore) MarkOrphanedIdleUnloaded([]string) (int64, error) {
	f.orphanCalls++
	return 0, nil
}
func (f *fakeInteractiveAgentStore) ListActive(time.Time, bool) ([]sqlite.AgentActivity, error) {
	return nil, nil
}

// fakeStorage embeds storage.Storage so only the methods actually reached by
// these tests need real implementations.
type fakeStorage struct {
	storage.Storage
	agents storage.InteractiveAgentStore
}

func (f *fakeStorage) InteractiveAgents() storage.InteractiveAgentStore { return f.agents }

// TestReconcileInteractiveAgentsOnStartup_NoSQLiteHandle is the regression test
// for the CLI panic: on a PG deployment there is no SQLite handle, and the old
// `interactiveAgentSvc()` called `multiSession.DB().Conn()` unconditionally →
// nil pointer dereference during startup reconcile. The session is built with
// an injected storage and no DB handle; reconcile must use the storage seam.
func TestReconcileInteractiveAgentsOnStartup_NoSQLiteHandle(t *testing.T) {
	fake := &fakeInteractiveAgentStore{}
	ms := session.NewMultiTenantWithStorage(&fakeStorage{agents: fake})
	if ms.DB() != nil {
		t.Fatal("test setup: session must have no SQLite handle")
	}

	ag := &Agent{multiSession: ms}
	if err := ag.ReconcileInteractiveAgentsOnStartup(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if fake.interrupted != 1 {
		t.Fatalf("MarkAllRunningAsInterrupted called %d times, want 1", fake.interrupted)
	}
	if fake.orphanCalls != 1 {
		t.Fatalf("MarkOrphanedIdleUnloaded called %d times, want 1", fake.orphanCalls)
	}
}

// TestPersistInteractiveAgent_NoSQLiteHandle covers the other registry write
// path (spawn / status changes): it must not touch DB() either.
func TestPersistInteractiveAgent_NoSQLiteHandle(t *testing.T) {
	fake := &fakeInteractiveAgentStore{}
	ms := session.NewMultiTenantWithStorage(&fakeStorage{agents: fake})
	ag := &Agent{multiSession: ms}

	ia := &interactiveAgent{roleName: "tester", instance: "main", parentKey: "web:p1"}
	ag.persistInteractiveAgent(ia, sqlite.InteractiveAgentIdle)
	ag.markPersistedAgentStatus(ia, sqlite.InteractiveAgentIdle, 0)

	// 只校验「写路径确实走了 storage seam」，不校验精确次数。
	// markPersistedAgentStatus 复用 persistInteractiveAgent —— 注册表的字段是
	// 一起描述「这个子代理现在是什么样」的，两条独立写路径必然漂移，所以设计上
	// 状态变更也走一次全量 upsert（见 interactive_persist.go 的注释）。把
	// "两次调用 = 两次 upsert" 写死成断言，等于把这个正确设计判成回归。
	if fake.upserts == 0 {
		t.Fatalf("no Upsert reached the storage seam; registry writes are not using storage")
	}
}

// TestInteractiveAgentSvc_NilStorage ensures the degraded path (no storage at
// all) returns a nil handle instead of panicking on a nil receiver.
func TestInteractiveAgentSvc_NilStorage(t *testing.T) {
	ag := &Agent{}
	if svc := ag.interactiveAgentSvc(); svc != nil {
		t.Fatal("interactiveAgentSvc() must be nil when storage is unavailable")
	}
	ag.persistInteractiveAgent(&interactiveAgent{roleName: "x", instance: "y"}, sqlite.InteractiveAgentIdle)
	ag.markPersistedAgentStatus(&interactiveAgent{roleName: "x", instance: "y"}, sqlite.InteractiveAgentIdle, 0)
}
