package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// A BackgroundTask's session key is the exact sub-agent AgentID path. It is
// the owner of the task, not the path's parent. Regression for the P0 where a
// child's Shell completion was routed to its interactive parent.
func TestBackgroundTaskCompletionRoutesToOwningSubAgent(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: context.Background(), lifecycleStopCh: make(chan struct{})}

	parentKey := "web:chat-owner/court.shoufu:main"
	childKey := "web:chat-owner/court.gongbu:build-1"
	parent := &interactiveAgent{
		roleName: "court.shoufu", instance: "main", running: true,
		cfg: &RunConfig{AgentID: "main/court.shoufu"},
	}
	child := &interactiveAgent{
		roleName: "court.gongbu", instance: "build-1", running: true,
		cfg: &RunConfig{AgentID: "main/court.shoufu/court.gongbu"},
	}
	a.interactiveSubAgents.Store(parentKey, parent)
	a.interactiveSubAgents.Store(childKey, child)

	const ownerID = "main/court.shoufu/court.gongbu"
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)
	go a.bgNotifyLoop()
	t.Cleanup(func() {
		if a.lifecycleStopCh != nil {
			close(a.lifecycleStopCh)
		}
	})
	mgr.Start(ownerID, "user-1", "go test ./...", func(context.Context, func(string)) (int, error) {
		return 0, nil
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		child.mu.Lock()
		childPending := len(child.pendingMessages)
		child.mu.Unlock()
		parent.mu.Lock()
		parentPending := len(parent.pendingMessages)
		parent.mu.Unlock()
		if childPending == 1 && parentPending == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	child.mu.Lock()
	childPending := len(child.pendingMessages)
	child.mu.Unlock()
	parent.mu.Lock()
	parentPending := len(parent.pendingMessages)
	parent.mu.Unlock()
	t.Fatalf("background task routed to wrong session: child pending=%d parent pending=%d", childPending, parentPending)
}
