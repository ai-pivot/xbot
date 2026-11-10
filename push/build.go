package push

import (
	"fmt"

	"xbot/config"
	log "xbot/logger"
)

// BuildRegistry constructs the enabled providers from config. A provider whose
// config is invalid is skipped with a warning (the server still starts; the
// other channels keep working).
func BuildRegistry(cfg config.PushConfig) (*Registry, []string) {
	// The master switch wins: a configured provider is inert while
	// push.enable is false.
	if !cfg.Enable {
		return NewRegistry(), nil
	}
	var providers []Provider
	var warnings []string

	if cfg.FCM != nil && cfg.FCM.Enable {
		p, err := NewFCM(*cfg.FCM)
		if err != nil {
			warnings = append(warnings, err.Error())
		} else {
			providers = append(providers, p)
		}
	}
	if cfg.WebPush != nil && cfg.WebPush.Enable {
		p, err := NewWebPush(*cfg.WebPush)
		if err != nil {
			warnings = append(warnings, err.Error())
		} else {
			providers = append(providers, p)
		}
	}
	if cfg.APNs != nil && cfg.APNs.Enable {
		warnings = append(warnings, "push: apns provider is not implemented yet")
	}
	if cfg.Feishu != nil && cfg.Feishu.Enable {
		warnings = append(warnings, "push: feishu provider is not implemented yet")
	}

	for _, w := range warnings {
		log.Warn(w)
	}
	return NewRegistry(providers...), warnings
}

// Summary is a log-safe description of the registry (no secrets).
func Summary(r *Registry) string {
	if r == nil || r.Empty() {
		return "（未启用）"
	}
	return fmt.Sprintf("%v", r.Names())
}
