package serverapp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/channel"
	"xbot/config"
	llm "xbot/llm"
	"xbot/storage/sqlite"
)

// TestGetDefaultSubscriptionByLinkedIdentity verifies get_default_subscription
// resolves by canonical user_id (v45): a user's default set via identity A
// (cli_user) must be visible when queried via a linked identity B (web-7) on
// the same canonical user. The old sender-keyed GetDefault(bizID) missed and
// fell back to the system subscription.
func TestGetDefaultSubscriptionByLinkedIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(filepath.Join(dir, "xbot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	// Identity A (cli_user, uid 7) owns the subscription and set it as default.
	if err := subSvc.Add(&sqlite.LLMSubscription{
		ID: "sub-owned", SenderID: "cli_user", Name: "owned", Provider: "openai",
		BaseURL: "https://owned.example/v1", APIKey: "sk-owned", Model: "owned-model",
	}); err != nil {
		t.Fatalf("add owned sub: %v", err)
	}
	if err := subSvc.SetSubscriptionUserID("sub-owned", 7); err != nil {
		t.Fatalf("bind sub to user 7: %v", err)
	}
	if err := subSvc.SetUserDefaultModelByUserID(7, "sub-owned", "owned-model"); err != nil {
		t.Fatalf("set default by user: %v", err)
	}
	// Link identity web-7 to the same canonical user (uid 7).
	if _, err := db.Conn().Exec(
		"INSERT INTO users (id, role) VALUES (7, 'user')",
	); err != nil {
		t.Fatalf("create user 7: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (7, 'web', 'web-7')",
	); err != nil {
		t.Fatalf("link identity: %v", err)
	}

	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)

	// Query via the linked identity (web-7, uid 7) — NOT cli_user.
	ctx := WithRPCCtxResolved(context.Background(), "web-7", "web-7", 7, "user")
	raw, err := table.Dispatch(ctx, "get_default_subscription", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("get_default_subscription via linked identity: %v", err)
	}
	var sub channel.Subscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sub.ID != "sub-owned" {
		t.Fatalf("get_default_subscription via linked identity = %q, want sub-owned (user_id resolution)", sub.ID)
	}
}

// TestGetDefaultModelByLinkedIdentity verifies get_default_model resolves the
// last-used model by canonical user_id (user_default_model WHERE user_id = ?):
// the (sub, model) set via identity A must be returned for linked identity B.
// The old sender-keyed ResolveActiveSubModel(bizID) missed and fell through
// to the system default model.
func TestGetDefaultModelByLinkedIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(filepath.Join(dir, "xbot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "fallback-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	if err := subSvc.Add(&sqlite.LLMSubscription{
		ID: "sub-owned", SenderID: "cli_user", Name: "owned", Provider: "openai",
		BaseURL: "https://owned.example/v1", APIKey: "sk-owned", Model: "owned-model",
	}); err != nil {
		t.Fatalf("add owned sub: %v", err)
	}
	if err := subSvc.SetSubscriptionUserID("sub-owned", 7); err != nil {
		t.Fatalf("bind sub to user 7: %v", err)
	}
	if err := subSvc.SetUserDefaultModelByUserID(7, "sub-owned", "owned-model"); err != nil {
		t.Fatalf("set default model by user: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO users (id, role) VALUES (7, 'user')",
	); err != nil {
		t.Fatalf("create user 7: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (7, 'web', 'web-7')",
	); err != nil {
		t.Fatalf("link identity: %v", err)
	}

	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)

	ctx := WithRPCCtxResolved(context.Background(), "web-7", "web-7", 7, "user")
	raw, err := table.Dispatch(ctx, "get_default_model", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("get_default_model via linked identity: %v", err)
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if model != "owned-model" {
		t.Fatalf("get_default_model via linked identity = %q, want owned-model (user_id resolution)", model)
	}
}
