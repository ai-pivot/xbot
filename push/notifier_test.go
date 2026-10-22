package push

import (
	"context"
	"sync"
	"testing"
	"time"

	"xbot/storage"
)

type fakeTargets struct {
	mu      sync.Mutex
	targets []storage.PushTarget
	deleted []int64
	touched int
}

func (f *fakeTargets) ListPushTargetsBySender(string) ([]storage.PushTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]storage.PushTarget, len(f.targets))
	copy(out, f.targets)
	return out, nil
}

func (f *fakeTargets) DeletePushTargetByID(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeTargets) TouchPushTargets(ids []int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched += len(ids)
	return nil
}

type fakeSessions struct {
	sender   string
	tenantID int64
	preview  string
}

func (f fakeSessions) GetSenderForChat(string, string) (string, error) { return f.sender, nil }
func (f fakeSessions) GetTenantIDByChannelChatID(string, string) (int64, error) {
	return f.tenantID, nil
}
func (f fakeSessions) GetLastAssistantPreview(int64, int) (string, error) {
	return f.preview, nil
}

type fakeProvider struct {
	name string
	mu   sync.Mutex
	sent []Event
	err  error
	ch   chan Event
}

func (p *fakeProvider) Name() string                      { return p.name }
func (p *fakeProvider) Platform() string                  { return "android" }
func (p *fakeProvider) Validate(storage.PushTarget) error { return nil }
func (p *fakeProvider) Send(_ context.Context, _ storage.PushTarget, ev Event) error {
	p.mu.Lock()
	p.sent = append(p.sent, ev)
	p.mu.Unlock()
	if p.ch != nil {
		p.ch <- ev
	}
	return p.err
}

func (p *fakeProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

func newTestNotifier(t *testing.T, targets *fakeTargets, sessions fakeSessions, p Provider, includePreview bool) *Notifier {
	t.Helper()
	n := NewNotifier(Options{
		Targets:        targets,
		Sessions:       sessions,
		Registry:       NewRegistry(p),
		IncludePreview: includePreview,
		Workers:        1,
	})
	t.Cleanup(n.Close)
	return n
}

func TestNotifierAskUserDedupe(t *testing.T) {
	targets := &fakeTargets{targets: []storage.PushTarget{{ID: 1, Provider: "fake", Address: "a", Platform: "android"}}}
	p := &fakeProvider{name: "fake"}
	n := newTestNotifier(t, targets, fakeSessions{sender: "web-1"}, p, false)

	ev := Event{Kind: KindAskUser, Channel: "web", ChatID: "c1", SenderID: "web-1", RequestID: "req-1"}
	n.deliver(ev)
	n.deliver(ev)
	if got := p.count(); got != 1 {
		t.Fatalf("ask_user delivered %d times, want 1 (request_id dedupe)", got)
	}
}

func TestNotifierBusyIdle(t *testing.T) {
	targets := &fakeTargets{targets: []storage.PushTarget{{ID: 1, Provider: "fake", Address: "a", Platform: "android"}}}
	p := &fakeProvider{name: "fake", ch: make(chan Event, 4)}
	n := newTestNotifier(t, targets, fakeSessions{sender: "web-1", tenantID: 1, preview: "done text"}, p, true)

	// idle without a preceding busy must not notify.
	n.Notify(Event{Action: "idle", Channel: "web", ChatID: "c1"})
	select {
	case ev := <-p.ch:
		t.Fatalf("idle alone produced %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	n.Notify(Event{Action: "busy", Channel: "web", ChatID: "c1"})
	n.Notify(Event{Action: "idle", Channel: "web", ChatID: "c1"})
	select {
	case ev := <-p.ch:
		if ev.Kind != KindTurnDone {
			t.Fatalf("kind = %q, want turn_done", ev.Kind)
		}
		if ev.Body != "done text" {
			t.Fatalf("body = %q, want preview", ev.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("busy→idle did not produce a notification")
	}
}

func TestNotifierPresenceSkipsTarget(t *testing.T) {
	targets := &fakeTargets{targets: []storage.PushTarget{{ID: 1, Provider: "fake", Address: "a", Platform: "android"}}}
	p := &fakeProvider{name: "fake"}
	n := newTestNotifier(t, targets, fakeSessions{sender: "web-1"}, p, false)

	n.MarkPresence("fake", "a", true)
	n.deliver(Event{Kind: KindTest, Channel: "web", ChatID: "c1", SenderID: "web-1"})
	if got := p.count(); got != 0 {
		t.Fatalf("present target received %d pushes, want 0", got)
	}

	n.MarkPresence("fake", "a", false)
	n.deliver(Event{Kind: KindTest, Channel: "web", ChatID: "c1", SenderID: "web-1"})
	if got := p.count(); got != 1 {
		t.Fatalf("after presence cleared, got %d, want 1", got)
	}
}

func TestNotifierPermanentErrorDeletesTarget(t *testing.T) {
	targets := &fakeTargets{targets: []storage.PushTarget{{ID: 42, Provider: "fake", Address: "a", Platform: "android"}}}
	p := &fakeProvider{name: "fake", err: PermanentError(context.DeadlineExceeded)}
	n := newTestNotifier(t, targets, fakeSessions{sender: "web-1"}, p, false)

	n.deliver(Event{Kind: KindTest, Channel: "web", ChatID: "c1", SenderID: "web-1"})
	targets.mu.Lock()
	defer targets.mu.Unlock()
	if len(targets.deleted) != 1 || targets.deleted[0] != 42 {
		t.Fatalf("deleted = %v, want [42]", targets.deleted)
	}
}

func TestNotifierOwnerResolutionFailureDrops(t *testing.T) {
	targets := &fakeTargets{targets: []storage.PushTarget{{ID: 1, Provider: "fake", Address: "a"}}}
	p := &fakeProvider{name: "fake"}
	n := newTestNotifier(t, targets, fakeSessions{sender: ""}, p, false)

	n.deliver(Event{Kind: KindTest, Channel: "web", ChatID: "c1"})
	if got := p.count(); got != 0 {
		t.Fatalf("ownerless session delivered %d pushes, want 0", got)
	}
}
