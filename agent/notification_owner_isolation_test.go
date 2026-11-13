package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/internal/sessionaddr"
	"xbot/tools"
)

func newOwnerRoutingAgent(t *testing.T) (*Agent, *tools.BackgroundTaskManager) {
	t.Helper()
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: context.Background(), lifecycleStopCh: make(chan struct{})}
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)
	t.Cleanup(func() { close(a.lifecycleStopCh) })
	return a, mgr
}

func storeTaskOwner(a *Agent, key string) *interactiveAgent {
	_, _, role, instance := sessionaddr.SplitChild(key)
	ia := &interactiveAgent{roleName: role, instance: instance, running: true, cfg: &RunConfig{AgentID: "main/taizi", CallerSessionKey: key}}
	a.interactiveSubAgents.Store(key, ia)
	return ia
}

func ownerPending(ia *interactiveAgent) int {
	ia.mu.Lock()
	defer ia.mu.Unlock()
	return len(ia.pendingMessages)
}

// All four agents intentionally share the same AgentID. Only the exact address
// distinguishes instances, roots, and nested parents. Role lookup cannot pass.
func TestBackgroundTaskOwnersAreIsolatedAcrossInstancesAndRoots(t *testing.T) {
	a, mgr := newOwnerRoutingAgent(t)
	keys := []string{"web:a/taizi:one", "web:a/taizi:two", "web:b/taizi:one", "agent:web:a/parent:p/taizi:one"}
	owners := make([]*interactiveAgent, len(keys))
	for i, k := range keys {
		owners[i] = storeTaskOwner(a, k)
	}
	go a.bgNotifyLoop()
	for _, k := range keys {
		cfg := RunConfig{AgentID: "main/taizi", CallerSessionKey: k, SessionKey: "main/taizi", RootSessionKey: "web:parent", BgTaskManager: mgr}
		tc := buildToolContext(context.Background(), &cfg)
		mgr.Start(tc.BgSessionKey, "u", k, func(context.Context, func(string)) (int, error) { return 0, nil })
	}
	if !waitForCondition(3*time.Second, func() bool {
		for _, o := range owners {
			if ownerPending(o) != 1 {
				return false
			}
		}
		return true
	}) {
		t.Fatal("not all exact owners received exactly one completion")
	}
	for i, o := range owners {
		o.mu.Lock()
		body := o.pendingMessages[0].content
		o.mu.Unlock()
		if !strings.Contains(body, keys[i]) {
			t.Errorf("owner %s received another session's task: %s", keys[i], body)
		}
	}
	select {
	case msg := <-a.bus.Inbound:
		t.Fatalf("leaked to main: %+v", msg)
	default:
	}
}

func TestBackgroundTaskMissingOwnerRetainedThenDeliveredOnlyToSameOwner(t *testing.T) {
	a, mgr := newOwnerRoutingAgent(t)
	const key = "web:a/taizi:missing"
	sibling := storeTaskOwner(a, "web:a/taizi:sibling")
	mgr.Start(sessionaddr.Agent(key), "u", "missing-owner", func(context.Context, func(string)) (int, error) { return 0, nil })
	var n tools.BgNotification
	select {
	case n = <-mgr.NotifyCh:
	case <-time.After(time.Second):
		t.Fatal("no completion")
	}
	a.enqueueBgNotification(n)
	a.drainAndProcessNotifications(n.SessionKey())
	if len(a.pendingBgNotifications(n.SessionKey())) != 1 || ownerPending(sibling) != 0 {
		t.Fatal("missing owner event lost or misrouted")
	}
	select {
	case msg := <-a.bus.Inbound:
		t.Fatalf("missing owner fell back to main: %+v", msg)
	default:
	}
	owner := storeTaskOwner(a, key)
	a.retryOwnerNotifications(key)
	if !waitForCondition(time.Second, func() bool { return ownerPending(owner) == 1 }) {
		t.Fatal("restored owner did not receive retained result")
	}
	if len(a.pendingBgNotifications(n.SessionKey())) != 0 || ownerPending(sibling) != 0 {
		t.Fatal("duplicate or sibling delivery")
	}
}

func TestExactNotificationAddressCannotFuzzyMatchAnotherSession(t *testing.T) {
	a, _ := newOwnerRoutingAgent(t)
	sibling := storeTaskOwner(a, "web:other/taizi:one")
	out, err := a.sendToExactInteractiveSession("web:missing/taizi:one", "u", "must not escape", nil)
	if !interactiveDeliveryFailed(out, err) || ownerPending(sibling) != 0 {
		t.Fatal("exact routing escaped to a role/instance match in another tree")
	}
}

func TestLegacyBackgroundTaskWithAmbiguousAgentIDIsNotDelivered(t *testing.T) {
	a, mgr := newOwnerRoutingAgent(t)
	one := storeTaskOwner(a, "web:a/taizi:one")
	two := storeTaskOwner(a, "web:b/taizi:one")
	mgr.Start("main/taizi", "u", "legacy", func(context.Context, func(string)) (int, error) { return 0, nil })
	var n tools.BgNotification
	select {
	case n = <-mgr.NotifyCh:
	case <-time.After(time.Second):
		t.Fatal("no completion")
	}
	a.enqueueBgNotification(n)
	a.drainAndProcessNotifications(n.SessionKey())
	if len(a.pendingBgNotifications(n.SessionKey())) != 1 || ownerPending(one) != 0 || ownerPending(two) != 0 {
		t.Fatal("ambiguous legacy role path was guessed")
	}
}

// A background-task-triggered turn is real work with a real conclusion: the
// parent must receive the completion report. Suppressing it (an earlier version
// did) silently dropped the sub-agent's last output before going idle.
func TestBackgroundTaskTurnStillNotifiesParent(t *testing.T) {
	cfg := &RunConfig{TriggerSource: triggerSourceBackgroundTask}
	if !shouldNotifyParent(cfg, false) {
		t.Fatal("background-task turn must still report completion to the parent")
	}
	cronCfg := &RunConfig{TriggerSource: triggerSourceCron}
	if shouldNotifyParent(cronCfg, false) {
		t.Fatal("cron turn must still be suppressed")
	}
	if !shouldNotifyParent(cfg, true) {
		t.Fatal("explicit user input lost its completion report")
	}
}

func TestAutomaticOwnerEventsDoNotMasqueradeAsUserInput(t *testing.T) {
	ia := &interactiveAgent{}
	ia.pendingMessages = []pendingUserMsg{{content: "task complete", automatic: true}, {content: "cron", automatic: true}}
	ia.wirePendingMessageDrain("agent:web:a/taizi:one")()
	if ia.userInputJoined.Load() {
		t.Fatal("automatic task event enabled parent notification")
	}
	ia.pendingMessages = []pendingUserMsg{{content: "real user input"}}
	ia.wirePendingMessageDrain("agent:web:a/taizi:one")()
	if !ia.userInputJoined.Load() {
		t.Fatal("explicit user request lost its completion")
	}
}

func TestExactOwnerInitializingRetainsResult(t *testing.T) {
	a, mgr := newOwnerRoutingAgent(t)
	const key = "web:a/taizi:initializing"
	owner := &interactiveAgent{roleName: "taizi", instance: "initializing", running: true}
	a.interactiveSubAgents.Store(key, owner)
	mgr.Start(sessionaddr.Agent(key), "u", "init", func(context.Context, func(string)) (int, error) { return 0, nil })
	var n tools.BgNotification
	select {
	case n = <-mgr.NotifyCh:
	case <-time.After(time.Second):
		t.Fatal("no completion")
	}
	a.enqueueBgNotification(n)
	if !a.routeBackgroundTaskToOwner(n) || len(a.pendingBgNotifications(n.SessionKey())) != 1 {
		t.Fatal("initializing result was lost")
	}
	owner.mu.Lock()
	owner.cfg = &RunConfig{AgentID: "main/taizi", CallerSessionKey: key}
	owner.mu.Unlock()
	a.retryOwnerNotifications(key)
	if !waitForCondition(time.Second, func() bool { return ownerPending(owner) == 1 }) {
		t.Fatal("initialized owner did not receive its result")
	}
	if len(a.pendingBgNotifications(n.SessionKey())) != 0 {
		t.Fatal("duplicate retained after successful claim")
	}
}
