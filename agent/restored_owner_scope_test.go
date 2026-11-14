package agent

import (
	"context"
	"path/filepath"
	"testing"

	"xbot/internal/sessionaddr"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// A RESTORED interactive sub-agent has no spawn frame; its identity is rebuilt
// from the registry. Background tasks it starts must still belong to itself.
// Regression for the 2026-11-13 finding: 57 live tasks created by the restored
// taizi/dpsk-perf-1 landed in its parent web session instead of the child.
func TestRestoredSubAgentToolContextKeepsOwnerScope(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "background"}[background], func(t *testing.T) {
			dir := t.TempDir()
			mt, err := session.NewMultiTenant(filepath.Join(dir, "main.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mt.Close() })
			config := Config{WorkDir: dir, XbotHome: dir, AgentsDir: dir, MemoryProvider: "none"}
			skills, agents, history, registry, cards := initStores(config)
			a := &Agent{multiSession: mt, tools: registry, skills: skills, agents: agents,
				chatHistory: history, cardBuilder: cards, workDir: dir, agentsDir: dir, xbotHome: dir}
			const origin, role, instance = "chat_9C218CD94A75", "explore", "dpsk-perf-1"
			key := interactiveKey("web", origin, role, instance)
			rec := sqlite.InteractiveAgentRecord{
				AgentKey: "agent:" + role + "/" + instance, ParentChannel: "web", ParentChatID: origin,
				RoleName: role, Instance: instance, Background: background,
				Status: sqlite.InteractiveAgentIdle, OriginUserID: "u1",
			}
			if err := a.interactiveAgentSvc().Upsert(rec); err != nil {
				t.Fatal(err)
			}
			ctx := WithUserContext(context.Background(), &UserContext{SenderID: "u1", Model: "test-model", MaxContextTokens: 100000, MaxOutputTokens: 100})
			if err := a.restoreOneAgent(ctx, rec); err != nil {
				t.Fatal(err)
			}
			v, _ := a.interactiveSubAgents.Load(key)
			ia := v.(*interactiveAgent)
			ia.mu.Lock()
			cfg := ia.cfg
			// Restore must leave nothing for Run() to fill in later: an idle restored
			// session builds its tool context from exactly this config.
			if cfg.BgTaskManager == nil {
				cfg.BgTaskManager = tools.NewBackgroundTaskManager()
			}
			ia.mu.Unlock()

			tc := buildToolContext(context.Background(), cfg)
			want := sessionaddr.Agent(key)
			if tc.BgSessionKey != want {
				t.Fatalf("restored sub-agent BgSessionKey = %q, want its own %q (parent session is a child leak)", tc.BgSessionKey, want)
			}
			if isSubAgentID(cfg.AgentID) != true {
				t.Fatalf("restored cfg.AgentID = %q must classify as sub-agent", cfg.AgentID)
			}
			if cfg.CallerSessionKey != key {
				t.Fatalf("restored CallerSessionKey = %q, want %q", cfg.CallerSessionKey, key)
			}
		})
	}
}
