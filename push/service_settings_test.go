package push

import (
	"encoding/json"
	"strings"
	"testing"

	"xbot/config"
)

func testPushConfig() config.PushConfig {
	return config.PushConfig{
		Enable:         true,
		IncludePreview: true,
		WebPush: &config.WebPushConfig{
			Enable:            true,
			VAPIDPublic:       "pub-key",
			VAPIDPrivate:      "priv-key",
			Subject:           "mailto:test@example.com",
			EndpointAllowlist: []string{"fcm.googleapis.com"},
		},
	}
}

func TestSettingsViewMasksVAPIDPrivate(t *testing.T) {
	svc := NewService(testPushConfig(), nil)
	view := svc.SettingsView()
	if view["has_vapid_private"] != true {
		t.Fatalf("has_vapid_private = %#v, want true", view["has_vapid_private"])
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(raw), "priv-key") {
		t.Fatalf("SettingsView leaked the VAPID private key: %s", raw)
	}
	wp, _ := view["webpush"].(map[string]any)
	if wp["vapid_public"] != "pub-key" {
		t.Fatalf("webpush public key missing: %#v", wp)
	}
}

func TestSaveSettingsKeepsVAPIDPrivateWhenBlank(t *testing.T) {
	svc := NewService(testPushConfig(), nil)
	payload := json.RawMessage(`{
		"enable": true,
		"include_preview": false,
		"webpush": {
			"enable": true,
			"vapid_public": "pub-key",
			"subject": "mailto:test@example.com",
			"endpoint_allowlist": ["fcm.googleapis.com"]
		}
	}`)
	if err := svc.SaveSettingsJSON(payload); err != nil {
		t.Fatalf("SaveSettingsJSON: %v", err)
	}
	svc.mu.RLock()
	priv := svc.cfg.WebPush.VAPIDPrivate
	svc.mu.RUnlock()
	if priv != "priv-key" {
		t.Fatalf("VAPIDPrivate = %q, want the existing priv-key", priv)
	}
}

func TestSaveSettingsRejectsIncompleteWebPush(t *testing.T) {
	svc := NewService(config.PushConfig{Enable: true}, nil)
	payload := json.RawMessage(`{
		"enable": true,
		"webpush": {"enable": true, "vapid_public": "pub", "endpoint_allowlist": []}
	}`)
	if err := svc.SaveSettingsJSON(payload); err == nil {
		t.Fatal("expected an error for missing subject/private key/allowlist")
	}
}

func TestDisabledServiceDoesNotNotify(t *testing.T) {
	svc := NewService(config.PushConfig{Enable: false}, nil)
	if svc.Enabled() {
		t.Fatal("disabled push reports Enabled()")
	}
	svc.Notify(Event{Kind: KindTurnDone, ChatID: "c"})
	if got := svc.PushConfig(); got["webpush"].(map[string]any)["enabled"] != false {
		t.Fatalf("PushConfig = %#v, want webpush disabled", got)
	}
}
