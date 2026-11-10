package session

import (
	"testing"
)

// none 模式：会话不创建任何记忆 provider（无 recall 注入、无自动整理、
// 无压缩钩子），因此 Memory() 必须为 nil。
func TestMultiTenantSession_NoneMemoryProvider(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath, WithMemoryProvider("none"))
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat_none")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	if mem := sess.Memory(); mem != nil {
		t.Fatalf("none provider: session memory must be nil, got %T (%s)", mem, mem.Name())
	}

	// Control: a real provider must still be constructed, otherwise the nil
	// assertion above could pass for the wrong reason.
	mtFlat, err := NewMultiTenant(t.TempDir()+"/test.db", WithMemoryProvider("flat"))
	if err != nil {
		t.Fatalf("Failed to create flat multi-tenant session: %v", err)
	}
	defer mtFlat.Close()
	flatSess, err := mtFlat.GetOrCreateSession("test", "chat_flat")
	if err != nil {
		t.Fatalf("Failed to create flat session: %v", err)
	}
	if flatSess.Memory() == nil {
		t.Fatal("flat provider: session memory must not be nil")
	}
}
