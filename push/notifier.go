package push

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"
	"xbot/storage"
)

const (
	presenceTTL     = 90 * time.Second
	askUserDedupe   = 10 * time.Minute
	turnDoneDedupe  = 2 * time.Minute
	perTargetPerMin = 10
)

// TargetStore is the slice of storage the Notifier needs (easy to fake in tests).
type TargetStore interface {
	ListPushTargetsBySender(senderID string) ([]storage.PushTarget, error)
	DeletePushTargetByID(id int64) error
	TouchPushTargets(ids []int64, at time.Time) error
}

// SessionResolver resolves a session's owner and its last reply preview.
type SessionResolver interface {
	GetSenderForChat(channel, chatID string) (string, error)
	GetTenantIDByChannelChatID(channel, chatID string) (int64, error)
	GetLastAssistantPreview(tenantID int64, maxRunes int) (string, error)
}

// Options configures a Notifier.
type Options struct {
	Targets        TargetStore
	Sessions       SessionResolver
	Registry       *Registry
	IncludePreview bool
	QueueSize      int
	Workers        int
}

// Notifier turns semantic events into per-target deliveries. It is deliberately
// provider-agnostic: it only calls Registry.Get(target.Provider).Send(...).
type Notifier struct {
	opts      Options
	queue     chan Event
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    atomic.Bool
	closeOnce sync.Once

	mu        sync.Mutex
	presence  map[string]time.Time // "provider:address" -> lease expiry
	dedupe    map[string]time.Time // event key -> suppression expiry
	rate      map[int64][]time.Time
	busySince map[string]int64 // "channel:chatID" -> unix seconds of the busy event
}

// NewNotifier starts the delivery workers.
func NewNotifier(opts Options) *Notifier {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{
		opts:      opts,
		queue:     make(chan Event, opts.QueueSize),
		ctx:       ctx,
		cancel:    cancel,
		presence:  make(map[string]time.Time),
		dedupe:    make(map[string]time.Time),
		rate:      make(map[int64][]time.Time),
		busySince: make(map[string]int64),
	}
	for i := 0; i < opts.Workers; i++ {
		n.wg.Add(1)
		go n.worker()
	}
	return n
}

// Notify is non-blocking: it only enqueues (or records a busy/idle transition).
// A full queue drops the event — notifications are never on the critical path.
func (n *Notifier) Notify(ev Event) {
	if n == nil || n.closed.Load() {
		return
	}
	// Session-state events are cheap in-memory transitions: busy starts a turn,
	// idle completes it. A lone idle (no busy seen) must NOT notify.
	if ev.Kind == "" && ev.Action != "" {
		key := ev.Channel + ":" + ev.ChatID
		switch ev.Action {
		case "busy":
			n.mu.Lock()
			if _, ok := n.busySince[key]; !ok {
				n.busySince[key] = time.Now().Unix()
			}
			n.mu.Unlock()
			return
		case "idle":
			n.mu.Lock()
			since, ok := n.busySince[key]
			delete(n.busySince, key)
			n.mu.Unlock()
			if !ok {
				return
			}
			ev.Kind = KindTurnDone
			ev.CollapseKey = key + ":" + strconv.FormatInt(since, 10)
		default:
			return
		}
	}
	if ev.Kind == "" {
		return
	}
	if ev.TS == 0 {
		ev.TS = time.Now().Unix()
	}
	select {
	case n.queue <- ev:
	default:
		log.WithFields(log.Fields{"kind": ev.Kind, "chat_id": ev.ChatID}).
			Warn("push: queue full, dropping event")
	}
}

// MarkPresence records that a target is currently visible to the user, so the
// Notifier skips it. Clients refresh the lease periodically; call with
// active=false on background.
func (n *Notifier) MarkPresence(provider, address string, active bool) {
	if n == nil || address == "" {
		return
	}
	key := provider + ":" + address
	n.mu.Lock()
	defer n.mu.Unlock()
	if active {
		n.presence[key] = time.Now().Add(presenceTTL)
	} else {
		delete(n.presence, key)
	}
}

// Close stops the workers and waits for in-flight deliveries.
func (n *Notifier) Close() {
	if n == nil {
		return
	}
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		n.cancel()
		n.wg.Wait()
	})
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for {
		select {
		case <-n.ctx.Done():
			return
		case ev := <-n.queue:
			n.deliver(ev)
		}
	}
}

func (n *Notifier) deliver(ev Event) {
	if n.ctx.Err() != nil {
		return
	}
	sender := ev.SenderID
	if sender == "" && n.opts.Sessions != nil {
		s, err := n.opts.Sessions.GetSenderForChat(ev.Channel, ev.ChatID)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{"channel": ev.Channel, "chat_id": ev.ChatID}).
				Warn("push: resolve session owner failed")
			return
		}
		sender = s
	}
	if sender == "" {
		log.WithFields(log.Fields{"channel": ev.Channel, "chat_id": ev.ChatID}).
			Debug("push: session has no owner, dropped")
		return
	}
	if n.opts.Targets == nil {
		return
	}
	targets, err := n.opts.Targets.ListPushTargetsBySender(sender)
	if err != nil {
		log.WithError(err).WithField("sender_id", sender).Warn("push: list targets failed")
		return
	}
	if len(targets) == 0 {
		return
	}

	n.fillContent(&ev)
	if key := dedupeKey(ev); key != "" && !n.claim(key, dedupeTTL(ev.Kind)) {
		return
	}

	for _, t := range targets {
		if n.ctx.Err() != nil {
			return
		}
		if n.isPresent(t.Provider, t.Address) {
			continue
		}
		if !n.allow(t.ID) {
			continue
		}
		p, ok := n.opts.Registry.Get(t.Provider)
		if !ok {
			continue
		}
		if err := p.Send(n.ctx, t, ev); err != nil {
			kind := ErrorKindOf(err)
			log.WithError(err).WithFields(log.Fields{
				"provider": t.Provider, "kind": ev.Kind, "chat_id": ev.ChatID, "target_id": t.ID,
			}).Warn("push: delivery failed")
			switch kind {
			case Permanent:
				_ = n.opts.Targets.DeletePushTargetByID(t.ID)
			case Transient:
				n.retry(p, t, ev)
			case Drop:
				// Message-level failure (bad payload/config): keep the target.
			}
			continue
		}
		_ = n.opts.Targets.TouchPushTargets([]int64{t.ID}, time.Now())
	}
}

// retry runs inline (up to ~7s) so it cannot outlive Close.
func (n *Notifier) retry(p Provider, t storage.PushTarget, ev Event) {
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		select {
		case <-n.ctx.Done():
			return
		case <-time.After(wait):
		}
		err := p.Send(n.ctx, t, ev)
		if err == nil {
			_ = n.opts.Targets.TouchPushTargets([]int64{t.ID}, time.Now())
			return
		}
		switch ErrorKindOf(err) {
		case Permanent:
			_ = n.opts.Targets.DeletePushTargetByID(t.ID)
			return
		case Drop:
			return
		}
	}
}

func (n *Notifier) fillContent(ev *Event) {
	if ev.Title == "" {
		switch ev.Kind {
		case KindAskUser:
			ev.Title = "xbot 需要你的回答"
		case KindTurnDone:
			ev.Title = "xbot 回复完成"
		default:
			ev.Title = "xbot"
		}
	}
	if ev.Kind == KindTurnDone && ev.Body == "" && n.opts.IncludePreview && n.opts.Sessions != nil {
		if tenantID, err := n.opts.Sessions.GetTenantIDByChannelChatID(ev.Channel, ev.ChatID); err == nil && tenantID > 0 {
			if preview, err := n.opts.Sessions.GetLastAssistantPreview(tenantID, 120); err == nil && preview != "" {
				ev.Body = preview
			}
		}
	}
	if ev.Body == "" {
		if ev.Kind == KindTurnDone {
			ev.Body = "本轮回复已完成。"
		} else {
			ev.Body = "打开 xbot 查看详情。"
		}
	}
	// FCM data messages are capped at 4096 bytes (WebPush payloads are also
	// ~4KB). An AskUser question has no length limit, so clamp before sending;
	// otherwise FCM returns 400/INVALID_ARGUMENT (which must not delete the
	// token — see classifyFCM).
	ev.Title, ev.Body = clampPushText(ev.Title, ev.Body)
}

const (
	maxPushTitleRunes = 64
	maxPushBodyRunes  = 500
)

func clampPushText(title, body string) (string, string) {
	return truncatePushRunes(title, maxPushTitleRunes), truncatePushRunes(body, maxPushBodyRunes)
}

func truncatePushRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}

func dedupeKey(ev Event) string {
	switch ev.Kind {
	case KindAskUser:
		if ev.RequestID != "" {
			return "ask:" + ev.RequestID
		}
		// Some emitters omit request_id; fall back to chat+question so a
		// re-sent progress snapshot doesn't spam the phone.
		if ev.ChatID != "" {
			return "ask:" + ev.ChatID + ":" + ev.Body
		}
	case KindTurnDone:
		if ev.CollapseKey != "" {
			return "done:" + ev.CollapseKey
		}
	}
	return ""
}

func dedupeTTL(kind Kind) time.Duration {
	if kind == KindAskUser {
		return askUserDedupe
	}
	return turnDoneDedupe
}

func (n *Notifier) claim(key string, ttl time.Duration) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if until, ok := n.dedupe[key]; ok && until.After(now) {
		return false
	}
	n.dedupe[key] = now.Add(ttl)
	if len(n.dedupe) > 4096 {
		for k, until := range n.dedupe {
			if until.Before(now) {
				delete(n.dedupe, k)
			}
		}
	}
	return true
}

func (n *Notifier) allow(targetID int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	kept := n.rate[targetID][:0]
	for _, ts := range n.rate[targetID] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= perTargetPerMin {
		n.rate[targetID] = kept
		return false
	}
	n.rate[targetID] = append(kept, now)
	return true
}

func (n *Notifier) isPresent(provider, address string) bool {
	key := provider + ":" + address
	n.mu.Lock()
	defer n.mu.Unlock()
	until, ok := n.presence[key]
	if !ok {
		return false
	}
	if until.Before(time.Now()) {
		delete(n.presence, key)
		return false
	}
	return true
}
