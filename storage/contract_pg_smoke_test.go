package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
	"xbot/storage/pg"

	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestPGStorage_Smoke(t *testing.T) {
	dsn := os.Getenv("XBOT_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set XBOT_PG_TEST_DSN to run PG smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. Open + init schema
	store, err := pg.NewStorage(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPGStorage: %v", err)
	}
	defer store.Close()

	// 2. TenantStore
	// Unique per run: the PG database is persistent (a fixed chat id would make
	// the message assertions below depend on whether a previous run reached
	// DeleteTenant — a failed run leaves the old rows behind).
	tenant := store.Tenant()
	chatID := fmt.Sprintf("chat_TEST_PG_%d", time.Now().UnixNano())
	tid, err := tenant.GetOrCreateTenantID("web", chatID)
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}
	if tid == 0 {
		t.Fatal("expected non-zero tenant id")
	}
	t.Logf("created tenant id=%d", tid)

	// Idempotent
	tid2, _ := tenant.GetOrCreateTenantID("web", chatID)
	if tid2 != tid {
		t.Errorf("idempotent: got %d, want %d", tid2, tid)
	}

	// 3. SessionStore
	sess, err := store.Session(tid)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := sess.AddMessage(tid, llm.ChatMessage{Role: "user", Content: "hello pg"}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	msgs, err := sess.GetHistory(tid, 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello pg" {
		t.Fatalf("expected 1 msg 'hello pg', got %d msgs", len(msgs))
	}
	t.Logf("retrieved: role=%s content=%s", msgs[0].Role, msgs[0].Content)

	// 4. MemoryStore
	mem := store.Memory()
	if err := mem.InitBlocks(tid, "test-user"); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}
	if err := mem.SetBlock(tid, "persona", "I am a test agent", "test-user"); err != nil {
		t.Fatalf("SetBlock: %v", err)
	}
	content, _, err := mem.GetBlock(tid, "persona", "test-user")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if content != "I am a test agent" {
		t.Errorf("GetBlock: got %q, want %q", content, "I am a test agent")
	}

	// 5. ConfigStore
	// Unique per run: the PG database is persistent (a fixed id would trip the
	// PK on the second run — see entrypoint.sh's shared DSN), and the row is
	// removed on cleanup so repeated runs stay green.
	cfg := store.Config()
	subID := fmt.Sprintf("test-sub-%d", time.Now().UnixNano())
	sub := &sqlite.LLMSubscription{ID: subID, SenderID: "web-1", Name: "Test Sub", Provider: "openai", Enabled: true}
	if err := cfg.AddSubscription(sub); err != nil {
		t.Fatalf("AddSubscription: %v", err)
	}
	defer func() { _ = cfg.RemoveSubscription(subID) }()
	subs, err := cfg.ListSubscriptions("web-1")
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	found := false
	for _, s := range subs {
		if s.ID == subID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListSubscriptions did not return the subscription just added (%s)", subID)
	}

	// 6. WebAuthStore (web_users — web UI authentication)
	// Unique username per run: the PG database is persistent.
	username := fmt.Sprintf("pg_web_test_%d", time.Now().UnixNano())
	uid, err := cfg.CreateWebUser(username, "hashed-password")
	if err != nil {
		t.Fatalf("CreateWebUser: %v", err)
	}
	if uid == 0 {
		t.Fatal("CreateWebUser: expected non-zero id")
	}
	defer func() { _ = cfg.DeleteWebUser(username) }()

	gotID, gotHash, err := cfg.GetWebUserForLogin(username)
	if err != nil {
		t.Fatalf("GetWebUserForLogin: %v", err)
	}
	if gotID != uid || gotHash != "hashed-password" {
		t.Fatalf("GetWebUserForLogin: got (id=%d, hash=%q), want (id=%d, hash=%q)", gotID, gotHash, uid, "hashed-password")
	}

	users, err := cfg.ListWebUsers()
	if err != nil {
		t.Fatalf("ListWebUsers: %v", err)
	}
	listed := false
	for _, u := range users {
		if u.Username == username {
			listed = true
			if u.CreatedAt == "" {
				t.Errorf("ListWebUsers: created_at is empty (TIMESTAMPTZ → text scan broken)")
			}
		}
	}
	if !listed {
		t.Fatalf("ListWebUsers did not return the user just created (%s)", username)
	}

	if n, err := cfg.CountWebUsers(); err != nil {
		t.Fatalf("CountWebUsers: %v", err)
	} else if n < 1 {
		t.Fatalf("CountWebUsers: got %d, want >= 1", n)
	}

	// Duplicate username must surface as an error (UNIQUE backstop).
	if _, err := cfg.CreateWebUser(username, "again"); err == nil {
		t.Fatal("CreateWebUser: duplicate username must fail")
	}

	// 7. Cleanup
	if err := tenant.DeleteTenant(tid); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	t.Log("smoke test passed ✓")
}
