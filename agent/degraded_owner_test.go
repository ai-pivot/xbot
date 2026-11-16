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

// Live bug (2026-11-13): a RESTORED interactive sub-agent created a background
// task, and it became the PARENT session's task — the completion arrived in the
// parent's context, so the child's own result looked forwarded by the parent.
// The child's Run config had degraded to AgentID="main" while still owning an
// interactive session key. Identity labels must never outrank the owner key.
func TestDegradedRestoredSessionKeepsTaskOwnership(t *testing.T) {
	const key = "web:chat_9C218CD94A75/taizi:dpsk-perf-1"
	want := sessionaddr.Agent(key)

	for _, agentID := range []string{"main", "", "  main  ", "taizi", "main/taizi"} {
		t.Run("agentID="+agentID, func(t *testing.T) {
			cfg := &RunConfig{
				AgentID:          agentID,
				CallerSessionKey: key, // assigned by spawn AND restore
				SessionKey:       "main/taizi",
				RootSessionKey:   "web:chat_9C218CD94A75",
				Channel:          "web",
				ChatID:           "chat_9C218CD94A75",
				BgTaskManager:    tools.NewBackgroundTaskManager(),
			}
			tc := buildToolContext(context.Background(), cfg)
			if tc.BgSessionKey != want {
				t.Fatalf("AgentID=%q: BgSessionKey=%q, want the interactive owner %q (parent is %q)",
					agentID, tc.BgSessionKey, want, cfg.RootSessionKey)
			}
			if tc.BgSessionKey == cfg.RootSessionKey {
				t.Fatalf("AgentID=%q: task ownership leaked onto the root session", agentID)
			}
		})
	}

	// The main agent itself has no interactive owner: it must keep using the
	// canonical root key, otherwise main-session tasks would regress.
	mainCfg := &RunConfig{
		AgentID: "main", RootSessionKey: "web:chat_1", Channel: "web", ChatID: "chat_1",
		BgTaskManager: tools.NewBackgroundTaskManager(),
	}
	if got := buildToolContext(context.Background(), mainCfg).BgSessionKey; got != "web:chat_1" {
		t.Fatalf("main agent BgSessionKey=%q, want its root session", got)
	}
}

// End-to-end through the real restore path: a restored child must own its tasks.
func TestRestoredSessionOwnsBackgroundTaskNotParent(t *testing.T) {
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
		RoleName: role, Instance: instance, Status: sqlite.InteractiveAgentIdle, OriginUserID: "u1",
	}
	if err := a.interactiveAgentSvc().Upsert(rec); err != nil {
		t.Fatal(err)
	}
	ctx := WithUserContext(context.Background(), &UserContext{SenderID: "u1", Model: "m", MaxContextTokens: 100000, MaxOutputTokens: 100})
	if err := a.restoreOneAgent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	v, _ := a.interactiveSubAgents.Load(key)
	ia := v.(*interactiveAgent)
	ia.mu.Lock()
	cfg := *ia.cfg
	ia.mu.Unlock()
	if cfg.BgTaskManager == nil {
		cfg.BgTaskManager = tools.NewBackgroundTaskManager()
	}
	tc := buildToolContext(context.Background(), &cfg)
	if tc.BgSessionKey != sessionaddr.Agent(key) {
		t.Fatalf("restored task owner=%q, want %q", tc.BgSessionKey, sessionaddr.Agent(key))
	}
	// Even if the rebuilt identity degrades to main, the owner key still wins.
	cfg.AgentID = "main"
	if got := buildToolContext(context.Background(), &cfg).BgSessionKey; got != sessionaddr.Agent(key) {
		t.Fatalf("degraded restored task owner=%q, want %q", got, sessionaddr.Agent(key))
	}
}
