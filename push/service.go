package push

import (
	"context"
	"fmt"
	"time"

	"xbot/config"
	log "xbot/logger"
	"xbot/storage"
)

// Service owns the registry + notifier and exposes the operations the web
// channel needs. It also implements SessionResolver over storage.Storage.
type Service struct {
	Registry         *Registry
	notifier         *Notifier
	store            storage.Storage
	webPushPublicKey string
}

// NewService builds the enabled providers and starts the notifier. A disabled
// or misconfigured push layer yields an empty registry (Register returns an
// error, Notify is a no-op).
func NewService(cfg config.PushConfig, store storage.Storage) *Service {
	reg, warnings := BuildRegistry(cfg)
	s := &Service{Registry: reg, store: store}
	if cfg.WebPush != nil && cfg.WebPush.Enable {
		s.webPushPublicKey = cfg.WebPush.VAPIDPublic
	}
	if !reg.Empty() && store != nil {
		s.notifier = NewNotifier(Options{
			Targets:        store.Config(),
			Sessions:       s,
			Registry:       reg,
			IncludePreview: cfg.IncludePreview,
		})
	}
	log.WithFields(log.Fields{
		"providers": Summary(reg),
		"warnings":  len(warnings),
	}).Info("push: unified notification layer initialized")
	return s
}

// Notify publishes a semantic event; it is non-blocking.
func (s *Service) Notify(ev Event) {
	if s == nil || s.notifier == nil {
		return
	}
	s.notifier.Notify(ev)
}

// Register validates and stores one of the caller's delivery targets.
func (s *Service) Register(senderID string, userID int64, t storage.PushTarget) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("push is not enabled")
	}
	p, ok := s.Registry.Get(t.Provider)
	if !ok {
		return fmt.Errorf("push provider %q is not enabled", t.Provider)
	}
	if t.Platform == "" {
		t.Platform = p.Platform()
	}
	if err := p.Validate(t); err != nil {
		return err
	}
	t.UserID = userID
	t.SenderID = senderID
	if err := s.store.Config().UpsertPushTarget(&t); err != nil {
		return fmt.Errorf("store push target: %w", err)
	}
	log.WithFields(log.Fields{
		"provider": t.Provider, "platform": t.Platform, "sender_id": senderID,
	}).Info("push: target registered")
	return nil
}

// Unregister removes one of the caller's own targets.
func (s *Service) Unregister(senderID string, userID int64, provider, address string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("push is not enabled")
	}
	if err := s.store.Config().DeletePushTarget(userID, provider, address); err != nil {
		return fmt.Errorf("delete push target: %w", err)
	}
	log.WithFields(log.Fields{"provider": provider, "sender_id": senderID}).
		Info("push: target unregistered")
	return nil
}

// Presence marks a target as visible/backgrounded.
func (s *Service) Presence(provider, address string, active bool) {
	if s == nil || s.notifier == nil {
		return
	}
	s.notifier.MarkPresence(provider, address, active)
}

// Test sends a synchronous test notification to every target of senderID.
func (s *Service) Test(senderID string) (int, error) {
	if s == nil || s.store == nil {
		return 0, fmt.Errorf("push is not enabled")
	}
	targets, err := s.store.Config().ListPushTargetsBySender(senderID)
	if err != nil {
		return 0, fmt.Errorf("list push targets: %w", err)
	}
	if len(targets) == 0 {
		return 0, fmt.Errorf("没有注册的推送目标")
	}
	ev := Event{
		Kind: KindTest, Channel: "web", SenderID: senderID,
		Title: "xbot 测试通知", Body: "如果你看到这条消息，推送通道正常。",
		HighPriority: true, TS: time.Now().Unix(),
	}
	sent := 0
	var firstErr error
	for _, t := range targets {
		p, ok := s.Registry.Get(t.Provider)
		if !ok {
			continue
		}
		if err := p.Send(context.Background(), t, ev); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sent++
	}
	if sent == 0 && firstErr != nil {
		return 0, firstErr
	}
	if firstErr != nil {
		log.WithError(firstErr).WithField("sender_id", senderID).
			Warn("push: test partially failed")
	}
	return sent, nil
}

// Close stops the notifier.
func (s *Service) Close() {
	if s == nil || s.notifier == nil {
		return
	}
	s.notifier.Close()
}

// PushConfig exposes the public web-push configuration (VAPID public key).
// A disabled/misconfigured WebPush provider answers enabled=false.
func (s *Service) PushConfig() map[string]any {
	wp := map[string]any{"enabled": false}
	if s != nil && s.webPushPublicKey != "" && s.Registry != nil {
		if _, ok := s.Registry.Get("webpush"); ok {
			wp = map[string]any{"enabled": true, "public_key": s.webPushPublicKey}
		}
	}
	return map[string]any{"webpush": wp}
}

// ─── SessionResolver ───

func (s *Service) GetSenderForChat(channel, chatID string) (string, error) {
	return s.store.Tenant().GetSenderForChat(channel, chatID)
}

func (s *Service) GetTenantIDByChannelChatID(channel, chatID string) (int64, error) {
	return s.store.Tenant().GetTenantIDByChannelChatID(channel, chatID)
}

func (s *Service) GetLastAssistantPreview(tenantID int64, maxRunes int) (string, error) {
	sess, err := s.store.Session(tenantID)
	if err != nil {
		return "", err
	}
	return sess.GetLastAssistantPreview(tenantID, maxRunes)
}
