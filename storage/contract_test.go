package storage

import (
	"fmt"
	"testing"
	"time"

	"xbot/llm"
)

// StorageFactory creates a fresh Storage instance for testing.
// Each test gets its own Storage; cleanup is handled by t.Cleanup.
type StorageFactory func(t *testing.T) Storage

// RunStorageContractTests runs the full contract suite against a factory.
// Both SQLite and PG backends (and any future backend) pass the same tests.
func RunStorageContractTests(t *testing.T, factory StorageFactory) {
	t.Run("SessionStore", func(t *testing.T) { testSessionStoreContract(t, factory) })
	t.Run("TenantStore", func(t *testing.T) { testTenantStoreContract(t, factory) })
	t.Run("MemoryStore", func(t *testing.T) { testMemoryStoreContract(t, factory) })
	t.Run("AgentMemoryStore", func(t *testing.T) { testAgentMemoryStoreContract(t, factory) })
	t.Run("ConfigStore", func(t *testing.T) { testConfigStoreContract(t, factory) })
	t.Run("CrossStore", func(t *testing.T) { testCrossStoreContract(t, factory) })
	t.Run("EdgeCases", func(t *testing.T) { testEdgeCasesContract(t, factory) })
}

// ─── Helpers ───

// getSessionStore gets a SessionStore from the Storage, failing the test on error.
func getSessionStore(t *testing.T, s Storage, tenantID int64) SessionStore {
	t.Helper()
	ss, err := s.Session(tenantID)
	if err != nil {
		t.Fatalf("Storage.Session(%d): %v", tenantID, err)
	}
	return ss
}

// createTestTenant creates a tenant and returns its ID.
func createTestTenant(t *testing.T, s Storage, chatID string) int64 {
	t.Helper()
	tenantID, err := s.Tenant().GetOrCreateTenantID("web", chatID)
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}
	return tenantID
}

// uniqueChatID generates a unique chat ID for a test.
func uniqueChatID(t *testing.T) string {
	return fmt.Sprintf("chat_test_%s_%d", t.Name(), time.Now().UnixNano())
}

// dummyMsg creates a ChatMessage with the given role and content.
func dummyMsg(role, content string) llm.ChatMessage {
	return llm.ChatMessage{Role: role, Content: content, TurnID: 1}
}
