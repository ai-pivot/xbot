package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/session"
	"xbot/storage/sqlite"
)

// Exercise restore -> SendToInteractiveSession -> Run -> ProgressSender, not
// just manual invocation of an already-wired callback. A restored config used
// to have no progress/stream callbacks, so a genuinely running agent was silent.
func TestRestoreOneAgent_SendEmitsProgressAndStream(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) {
			dir := t.TempDir()
			mt, err := session.NewMultiTenant(filepath.Join(dir, "main.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mt.Close() })
			config := Config{WorkDir: dir, XbotHome: dir, AgentsDir: dir, MemoryProvider: "none"}
			skills, agents, history, registry, cards := initStores(config)
			a := &Agent{multiSession: mt, tools: registry, skills: skills, agents: agents, chatHistory: history, cardBuilder: cards, workDir: dir, agentsDir: dir, xbotHome: dir}
			ch := &mockProgressChannel2{}
			a.channelRange = func(fn func(string, channel.Channel) bool) { fn("web", ch) }
			const origin = "chat_9C218CD94A75"
			const instance = "dpsk-perf-1"
			const role = "explore"
			key := interactiveKey("web", origin, role, instance)
			rec := sqlite.InteractiveAgentRecord{AgentKey: "agent:" + role + "/" + instance, ParentChannel: "web", ParentChatID: origin, RoleName: role, Instance: instance, Background: background, Status: sqlite.InteractiveAgentIdle, OriginUserID: "u1"}
			if err := a.interactiveAgentSvc().Upsert(rec); err != nil {
				t.Fatal(err)
			}
			ctx := WithUserContext(context.Background(), &UserContext{SenderID: "u1", Model: "test-model", MaxContextTokens: 100000, MaxOutputTokens: 100})
			if err := a.restoreOneAgent(ctx, rec); err != nil {
				t.Fatal(err)
			}
			v, ok := a.interactiveSubAgents.Load(key)
			if !ok {
				t.Fatalf("restored session missing: %s", key)
			}
			ia := v.(*interactiveAgent)
			if ia.cfg.ProgressEventHandler == nil || ia.cfg.StreamContentFunc == nil || ia.cfg.StreamReasoningFunc == nil || ia.cfg.StreamUsageFunc == nil || !ia.cfg.Stream {
				t.Error("restore left structured progress or streaming callbacks unwired")
			}
			for turn := 1; turn <= 2; turn++ {
				before := len(ch.getEvents())
				client := &streamModeLLM{content: fmt.Sprintf("reply-%d", turn)}
				ia.mu.Lock()
				ia.cfg.LLMClient = client
				ia.cfg.Tools = newTestRegistry()
				ia.mu.Unlock()
				out, err := a.SendToInteractiveSession(ctx, role, bus.InboundMessage{Channel: "web", ChatID: origin, SenderID: "u1", ChatType: "p2p", Content: fmt.Sprintf("question-%d", turn), Metadata: map[string]string{"instance_id": instance}})
				if err != nil {
					t.Fatal(err)
				}
				if out != nil && out.Error != nil {
					t.Fatal(out.Error)
				}
				if !waitForCondition(5*time.Second, func() bool {
					ia.mu.Lock()
					defer ia.mu.Unlock()
					return !ia.running && ia.lastReply == client.content
				}) {
					t.Fatal("restored agent did not finish its real send Run")
				}
				if !client.generateStreamCalled {
					t.Error("Run did not use streaming LLM")
				}
				structured, streamed, done := false, false, false
				for _, ev := range ch.getEvents()[before:] {
					if ev.ChatID != "agent:"+key || ev.TurnID != uint64(turn) {
						t.Errorf("wrong event identity: chat=%q turn=%d, want agent:%s turn=%d", ev.ChatID, ev.TurnID, key, turn)
					}
					structured = structured || ev.Phase != ""
					streamed = streamed || ev.StreamContent == client.content
					done = done || ev.Phase == string(PhaseDone)
				}
				if !structured || !streamed || !done {
					t.Errorf("turn %d missing real push: structured=%v stream=%v done=%v", turn, structured, streamed, done)
				}
			}
		})
	}
}
