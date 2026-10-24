package push

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"xbot/config"
	"xbot/storage"
)

// WebPushProvider delivers to browsers via the Web Push protocol (VAPID).
type WebPushProvider struct {
	subject      string
	vapidPublic  string
	vapidPrivate string
	allowlist    []string
	client       *http.Client
}

// NewWebPush validates the VAPID configuration.
func NewWebPush(cfg config.WebPushConfig) (*WebPushProvider, error) {
	if cfg.VAPIDPublic == "" || cfg.VAPIDPrivate == "" {
		return nil, fmt.Errorf("webpush: vapid_public and vapid_private are required")
	}
	subject := cfg.Subject
	if subject == "" {
		return nil, fmt.Errorf("webpush: subject is required (mailto:...)")
	}
	if len(cfg.EndpointAllowlist) == 0 {
		return nil, fmt.Errorf("webpush: endpoint_allowlist is required (SSRF guard)")
	}
	return &WebPushProvider{
		subject:      subject,
		vapidPublic:  cfg.VAPIDPublic,
		vapidPrivate: cfg.VAPIDPrivate,
		allowlist:    cfg.EndpointAllowlist,
		client:       &http.Client{Timeout: 5 * time.Second},
	}, nil
}

func (p *WebPushProvider) Name() string     { return "webpush" }
func (p *WebPushProvider) Platform() string { return "web" }

func (p *WebPushProvider) Validate(t storage.PushTarget) error {
	if t.Platform != "" && t.Platform != "web" {
		return fmt.Errorf("webpush: platform %q is not web", t.Platform)
	}
	u, err := url.Parse(t.Address)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("webpush: endpoint must be an https URL")
	}
	if !p.hostAllowed(u.Hostname()) {
		return fmt.Errorf("webpush: endpoint host %q is not allowlisted", u.Hostname())
	}
	if t.Metadata["p256dh"] == "" || t.Metadata["auth"] == "" {
		return fmt.Errorf("webpush: subscription keys p256dh/auth are required")
	}
	return nil
}

func (p *WebPushProvider) hostAllowed(host string) bool {
	for _, allowed := range p.allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// webPushTopic maps the business collapse key (e.g. "web:chat_x:1729") to a
// valid RFC 8030 §5.4 Topic: at most 32 base64url characters. Passing the raw
// key makes compliant push services reject every completion notification with
// 400, which the notifier would otherwise misread as a dead subscription.
func webPushTopic(collapseKey string) string {
	if collapseKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(collapseKey))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

func (p *WebPushProvider) Send(ctx context.Context, t storage.PushTarget, ev Event) error {
	payload, err := json.Marshal(map[string]string{
		"kind":       string(ev.Kind),
		"chat_id":    ev.ChatID,
		"title":      ev.Title,
		"body":       ev.Body,
		"request_id": ev.RequestID,
		"ts":         strconv.FormatInt(ev.TS, 10),
	})
	if err != nil {
		return PermanentError(err)
	}
	sub := &webpush.Subscription{
		Endpoint: t.Address,
		Keys: webpush.Keys{
			P256dh: t.Metadata["p256dh"],
			Auth:   t.Metadata["auth"],
		},
	}
	opts := &webpush.Options{
		Subscriber:      p.subject,
		VAPIDPublicKey:  p.vapidPublic,
		VAPIDPrivateKey: p.vapidPrivate,
		TTL:             3600,
		HTTPClient:      p.client,
		Topic:           webPushTopic(ev.CollapseKey),
	}
	if ev.HighPriority {
		opts.Urgency = webpush.UrgencyHigh
	} else {
		opts.Urgency = webpush.UrgencyNormal
	}

	resp, err := webpush.SendNotificationWithContext(ctx, payload, sub, opts)
	if err != nil {
		return TransientError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	err = fmt.Errorf("webpush http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return PermanentError(err) // subscription expired
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return TransientError(err)
	default:
		// 400/401/403 are configuration/endpoint errors — retrying won't help.
		return PermanentError(err)
	}
}
