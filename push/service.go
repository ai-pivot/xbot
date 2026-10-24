package push

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"xbot/config"
	"xbot/crypto"
	log "xbot/logger"
	"xbot/storage"
)

// runtimeState is one immutable snapshot of the push layer. Settings changes
// build a new snapshot and swap it under Service.mu, so in-flight deliveries
// finish on the old notifier while new events use the new one.
type runtimeState struct {
	registry         *Registry
	notifier         *Notifier
	webPushPublicKey string
}

// Service owns the current runtime and persists the push settings. The
// settings live in storage (push_settings) after the first start; config.push is
// only the initial seed. It also implements SessionResolver over storage.
type Service struct {
	store storage.Storage

	mu      sync.RWMutex
	cfg     config.PushConfig
	runtime *runtimeState
}

// NewService loads push_settings from storage (seeding it from cfg on the first
// start) and builds the providers + notifier.
func NewService(cfg config.PushConfig, store storage.Storage) *Service {
	s := &Service{store: store}

	if store != nil {
		blob, err := store.Config().GetPushSettings()
		switch {
		case err != nil:
			log.WithError(err).Warn("push: failed to read push_settings; using config.push")
		case blob == "":
			// First start: persist config.push so the UI can edit it afterwards.
			if err := saveSettings(store, cfg); err != nil {
				log.WithError(err).Warn("push: failed to seed push_settings; config.push remains the source")
			}
		default:
			loaded, err := decodeSettings(blob)
			if err != nil {
				log.WithError(err).Warn("push: failed to decode push_settings; using config.push")
			} else {
				cfg = loaded
			}
		}
	}

	s.cfg = cfg
	s.runtime = s.buildRuntime(cfg)
	log.WithFields(log.Fields{
		"providers": Summary(s.runtime.registry),
		"enabled":   !s.runtime.registry.Empty(),
	}).Info("push: unified notification layer initialized")
	return s
}

func (s *Service) buildRuntime(cfg config.PushConfig) *runtimeState {
	reg, _ := BuildRegistry(cfg)
	st := &runtimeState{registry: reg}
	if cfg.WebPush != nil && cfg.WebPush.Enable {
		st.webPushPublicKey = cfg.WebPush.VAPIDPublic
	}
	if !reg.Empty() && s.store != nil {
		st.notifier = NewNotifier(Options{
			Targets:        s.store.Config(),
			Sessions:       s,
			Registry:       reg,
			IncludePreview: cfg.IncludePreview,
		})
	}
	return st
}

func (s *Service) snapshot() *runtimeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtime
}

// Enabled reports whether at least one provider is active.
func (s *Service) Enabled() bool {
	st := s.snapshot()
	return st != nil && st.registry != nil && !st.registry.Empty()
}

// Notify publishes a semantic event; it is non-blocking.
func (s *Service) Notify(ev Event) {
	st := s.snapshot()
	if st == nil || st.notifier == nil {
		return
	}
	st.notifier.Notify(ev)
}

// Register validates and stores one of the caller's delivery targets.
func (s *Service) Register(senderID string, userID int64, t storage.PushTarget) error {
	st := s.snapshot()
	if s.store == nil || st == nil || st.registry == nil || st.registry.Empty() {
		return fmt.Errorf("push is not enabled")
	}
	p, ok := st.registry.Get(t.Provider)
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
	st := s.snapshot()
	if st == nil || st.notifier == nil {
		return
	}
	st.notifier.MarkPresence(provider, address, active)
}

// Test sends a synchronous test notification to every target of senderID.
func (s *Service) Test(senderID string) (int, error) {
	st := s.snapshot()
	if s.store == nil || st == nil || st.registry == nil || st.registry.Empty() {
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
		p, ok := st.registry.Get(t.Provider)
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

// Close stops the current notifier.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	st := s.runtime
	s.runtime = nil
	s.mu.Unlock()
	if st != nil && st.notifier != nil {
		st.notifier.Close()
	}
}

// PushConfig exposes the public web-push configuration (VAPID public key).
// A disabled/misconfigured WebPush provider answers enabled=false.
func (s *Service) PushConfig() map[string]any {
	wp := map[string]any{"enabled": false}
	st := s.snapshot()
	if st != nil && st.webPushPublicKey != "" && st.registry != nil {
		if _, ok := st.registry.Get("webpush"); ok {
			wp = map[string]any{"enabled": true, "public_key": st.webPushPublicKey}
		}
	}
	return map[string]any{"webpush": wp}
}

// SettingsView returns the persisted settings with the VAPID private key
// masked. has_vapid_private tells the UI whether leaving the field empty will
// keep the current key.
func (s *Service) SettingsView() map[string]any {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	hasPrivate := cfg.WebPush != nil && cfg.WebPush.VAPIDPrivate != ""
	if cfg.WebPush != nil {
		copyCfg := *cfg.WebPush
		copyCfg.VAPIDPrivate = ""
		cfg.WebPush = &copyCfg
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return map[string]any{"enable": cfg.Enable, "has_vapid_private": hasPrivate}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"enable": cfg.Enable, "has_vapid_private": hasPrivate}
	}
	out["has_vapid_private"] = hasPrivate
	return out
}

// SaveSettingsJSON parses the UI payload, keeps the existing VAPID private key
// when the field is left empty, and applies the new configuration.
func (s *Service) SaveSettingsJSON(raw json.RawMessage) error {
	var incoming config.PushConfig
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return fmt.Errorf("invalid push settings: %w", err)
	}
	s.mu.RLock()
	current := s.cfg
	s.mu.RUnlock()
	if incoming.WebPush != nil && incoming.WebPush.VAPIDPrivate == "" {
		if current.WebPush != nil {
			incoming.WebPush.VAPIDPrivate = current.WebPush.VAPIDPrivate
		}
	}
	return s.ApplySettings(incoming)
}

// ApplySettings validates, persists and hot-swaps the push layer. It does not
// touch config.json; storage is authoritative after the first start.
func (s *Service) ApplySettings(cfg config.PushConfig) error {
	cfg = withPushDefaults(cfg)

	if err := validatePushSettings(cfg); err != nil {
		return err
	}

	next := s.buildRuntime(cfg)
	keep := false
	defer func() {
		if !keep && next.notifier != nil {
			next.notifier.Close()
		}
	}()
	if cfg.Enable {
		if cfg.FCM != nil && cfg.FCM.Enable {
			if _, ok := next.registry.Get("fcm"); !ok {
				return fmt.Errorf("FCM provider failed to initialize (check credentials_file and project_id)")
			}
		}
		if cfg.WebPush != nil && cfg.WebPush.Enable {
			if _, ok := next.registry.Get("webpush"); !ok {
				return fmt.Errorf("WebPush provider failed to initialize (check VAPID keys, subject and endpoint_allowlist)")
			}
		}
	}

	if s.store != nil {
		if err := saveSettings(s.store, cfg); err != nil {
			return err
		}
	}

	s.mu.Lock()
	old := s.runtime
	s.runtime = next
	s.cfg = cfg
	s.mu.Unlock()
	keep = true

	if old != nil && old.notifier != nil {
		old.notifier.Close()
	}
	log.WithFields(log.Fields{
		"providers": Summary(next.registry),
		"enabled":   !next.registry.Empty(),
	}).Info("push: settings applied")
	return nil
}

func withPushDefaults(cfg config.PushConfig) config.PushConfig {
	if cfg.FCM != nil {
		if cfg.FCM.Mode == "" {
			cfg.FCM.Mode = "data"
		}
		if cfg.FCM.CredentialsFile == "" {
			cfg.FCM.CredentialsFile = filepath.Join(config.XbotHome(), "fcm-service-account.json")
		}
	}
	if cfg.APNs != nil && cfg.APNs.Env == "" {
		cfg.APNs.Env = "production"
	}
	return cfg
}

func validatePushSettings(cfg config.PushConfig) error {
	if !cfg.Enable {
		return nil
	}
	if cfg.FCM != nil && cfg.FCM.Enable && cfg.FCM.CredentialsFile == "" {
		return fmt.Errorf("fcm.credentials_file is required")
	}
	if cfg.WebPush != nil && cfg.WebPush.Enable {
		if cfg.WebPush.VAPIDPublic == "" || cfg.WebPush.VAPIDPrivate == "" {
			return fmt.Errorf("webpush VAPID public/private keys are required")
		}
		if cfg.WebPush.Subject == "" {
			return fmt.Errorf("webpush.subject is required")
		}
		if len(cfg.WebPush.EndpointAllowlist) == 0 {
			return fmt.Errorf("webpush.endpoint_allowlist is required")
		}
	}
	if cfg.APNs != nil && cfg.APNs.Enable {
		return fmt.Errorf("apns provider is not implemented yet")
	}
	if cfg.Feishu != nil && cfg.Feishu.Enable {
		return fmt.Errorf("feishu provider is not implemented yet")
	}
	return nil
}

func saveSettings(store storage.Storage, cfg config.PushConfig) error {
	if store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal push settings: %w", err)
	}
	enc, err := crypto.Encrypt(string(raw))
	if err != nil {
		return fmt.Errorf("encrypt push settings: %w", err)
	}
	if err := store.Config().SavePushSettings(enc); err != nil {
		return fmt.Errorf("save push settings: %w", err)
	}
	return nil
}

func decodeSettings(blob string) (config.PushConfig, error) {
	plain, err := crypto.Decrypt(blob)
	if err != nil {
		return config.PushConfig{}, fmt.Errorf("decrypt push settings: %w", err)
	}
	var cfg config.PushConfig
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
		return config.PushConfig{}, fmt.Errorf("parse push settings: %w", err)
	}
	return cfg, nil
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
