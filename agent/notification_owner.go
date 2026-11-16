package agent

import (
	"context"
	"fmt"

	"xbot/bus"
	"xbot/channel"
	"xbot/internal/sessionaddr"
	log "xbot/logger"
	"xbot/tools"
)

type exactInteractiveTarget struct{}

// sendToExactInteractiveSession is the single automated-notification delivery
// primitive. Unlike human role lookup, it may NEVER use fuzzy/cross-tree lookup.
func (a *Agent) sendToExactInteractiveSession(key, sender, content string, metadata map[string]string) (*channel.OutboundMsg, error) {
	ch, chat, role, _ := sessionaddr.SplitChild(key)
	if ch == "" || chat == "" || role == "" {
		return nil, fmt.Errorf("invalid interactive owner address %q", key)
	}
	m := make(map[string]string, len(metadata)+3)
	for k, v := range metadata {
		m[k] = v
	}
	m["instance_id"] = key
	m[bgNotificationMetadataKey] = "true"
	ctx := context.WithValue(context.Background(), exactInteractiveTarget{}, key)
	return a.SendToInteractiveSession(ctx, role, bus.InboundMessage{
		Channel: ch, ChatID: chat, SenderID: sender, Content: content, Metadata: m,
	})
}

// routeBackgroundTaskToOwner separates task ownership from SubAgentBgNotify's
// explicit parent-recipient semantics. true means handled OR retained pending;
// a missing owner must never fall through to a parent/main agent Run.
func (a *Agent) routeBackgroundTaskToOwner(n tools.BgNotification) bool {
	task, ok := n.(*tools.BackgroundTask)
	if !ok || task == nil {
		return false
	}
	key := task.SessionKey()
	addr, valid := sessionaddr.Parse(key)
	var target string
	if valid {
		if addr.Channel != sessionaddr.AgentChannel {
			return false
		}
		target = addr.ChatID
	} else {
		// Compatibility for already-running legacy tasks. A role/path can only
		// resolve if exactly one live owner has that ID. Ambiguity is NOT a vote.
		if k, _, found := a.interactiveSessionByAgentID(key); found {
			target = k
		}
	}
	value, exists := a.interactiveSubAgents.Load(target)
	ia, typed := value.(*interactiveAgent)
	if !exists || !typed || ia == nil || ia.isDestroyed() {
		log.WithFields(log.Fields{"task_id": task.ID, "owner": key}).Warn("background task owner unavailable; retaining notification, never forwarding to parent")
		return true
	}
	ia.mu.Lock()
	ready := ia.cfg != nil
	ia.mu.Unlock()
	if !ready {
		return true
	}
	if !a.takeOneBgNotification(key, n) {
		return true
	}
	go func() {
		out, err := a.sendToExactInteractiveSession(target, task.SenderID(), tools.FormatBgTaskCompletion(task, ""), map[string]string{
			triggerSourceMetadataKey: triggerSourceBackgroundTask,
		})
		if interactiveDeliveryFailed(out, err) {
			a.enqueueBgNotification(n)
			log.WithError(firstNonNilError(err, out)).WithField("owner", key).Warn("background task owner delivery failed; retained for same owner")
		}
	}()
	return true
}

// Retry only notifications retaining this exact owner, for example when an
// interactive session is restored after its task finished during initialization.
func (a *Agent) retryOwnerNotifications(mapKey string) {
	for _, n := range a.pendingBgNotifications(sessionaddr.Agent(mapKey)) {
		a.routeBackgroundTaskToOwner(n)
	}
}
