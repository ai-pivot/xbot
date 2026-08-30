package serverapp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// m3: set_default_subscription (per-session branch) must persist the session
// mapping under the CALLER'S channel. The hardcoded "cli" wrote web sessions
// to a (cli, chatID) tenant row — the mapping landed in the wrong channel and
// the web session never resolved it back.
func TestSetDefaultSubscriptionPersistsCallerChannel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	db := ag.MultiSession().DB()
	if db == nil {
		t.Fatal("multi-session DB is nil")
	}
	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)

	// Add a subscription as a web identity, then set it as default for a
	// web session with channel="web".
	webCtx := identityCtx("web-7", 7, "user")
	subID := addAuthTestSubscription(t, table, webCtx, "m3-channel-sub")

	params, _ := json.Marshal(map[string]any{
		"id":      subID,
		"chat_id": "webchat-m3",
		"channel": "web",
	})
	if _, err := table.Dispatch(webCtx, "set_default_subscription", params); err != nil {
		t.Fatalf("set_default_subscription: %v", err)
	}

	// The mapping must land under (web, webchat-m3), NOT (cli, webchat-m3).
	var webSubID string
	err = db.Conn().QueryRow(
		`SELECT subscription_id FROM tenants WHERE channel = 'web' AND chat_id = 'webchat-m3'`,
	).Scan(&webSubID)
	if err != nil {
		t.Fatalf("no tenant row under (web, webchat-m3): %v — mapping persisted under the hardcoded channel", err)
	}
	if webSubID != subID {
		t.Errorf("(web, webchat-m3) subscription_id = %q, want %q", webSubID, subID)
	}

	var cliRows int
	if err := db.Conn().QueryRow(
		`SELECT COUNT(*) FROM tenants WHERE channel = 'cli' AND chat_id = 'webchat-m3'`,
	).Scan(&cliRows); err != nil {
		t.Fatalf("count cli rows: %v", err)
	}
	if cliRows != 0 {
		t.Errorf("found %d tenant row(s) under the hardcoded (cli, webchat-m3) — cross-channel pollution", cliRows)
	}
}
