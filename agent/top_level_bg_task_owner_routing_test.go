package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// Top-level interactive children used to have AgentID "taizi". buildToolContext
// classified that as the main agent and set BgSessionKey to RootSessionKey,
// so Shell completion re-entered the parent. The owner must remain isolated
// and the completion must resume the exact owning child session.
func TestTopLevelBackgroundTaskCompletionRoutesToOwningSubAgent(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: context.Background(), lifecycleStopCh: make(chan struct{})}
	t.Cleanup(func() { close(a.lifecycleStopCh) })

	const rootKey = "web:chat-owner"
	topAgentID := subAgentPath("", "taizi")
	mgr := tools.NewBackgroundTaskManager()
	toolCtx := buildToolContext(context.Background(), &RunConfig{
		AgentID: topAgentID, Channel: "web", ChatID: "chat-owner",
		SessionKey: topAgentID, RootSessionKey: rootKey, BgTaskManager: mgr,
	})
	ownerID := toolCtx.BgSessionKey
	childKey := interactiveKey("web", "chat-owner", "taizi", "build-1")
	child := &interactiveAgent{
		roleName: "taizi", instance: "build-1", running: true,
		cfg: &RunConfig{AgentID: topAgentID},
	}
	a.interactiveSubAgents.Store(childKey, child)

	a.bgTaskMgr.Store(mgr)
	go a.bgNotifyLoop()
	mgr.Start(ownerID, "user-1", "go test ./...", func(context.Context, func(string)) (int, error) {
		return 0, nil
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		child.mu.Lock()
		pending := len(child.pendingMessages)
		child.mu.Unlock()
		if pending == 1 {
			select {
			case msg := <-a.bus.Inbound:
				t.Fatalf("background completion also leaked to parent: channel=%s chat=%s content=%q", msg.Channel, msg.ChatID, msg.Content)
			default:
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	child.mu.Lock()
	pending := len(child.pendingMessages)
	child.mu.Unlock()
	select {
	case msg := <-a.bus.Inbound:
		t.Fatalf("background completion went to parent instead of child %q: %s:%s %q", childKey, msg.Channel, msg.ChatID, msg.Content)
	default:
	}
	t.Fatalf("child did not receive its background completion (owner=%q root=%q), pending=%d", ownerID, rootKey, pending)
}
