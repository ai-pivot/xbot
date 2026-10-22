package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEffectiveConfig_InteractiveSessionTTL pins the config surface of
// agent.interactive_session_ttl:
//   - 缺省 → 30m（与硬编码时代一致，默认值不变）；
//   - config.json 显式设置 → 生效；
//   - 环境变量 INTERACTIVE_SESSION_TTL → 覆盖 config.json。
func TestEffectiveConfig_InteractiveSessionTTL(t *testing.T) {
	t.Run("default 30m", func(t *testing.T) {
		cfg := loadEffectiveConfig(&Config{})
		if got := time.Duration(cfg.Agent.InteractiveSessionTTL); got != 30*time.Minute {
			t.Fatalf("default interactive_session_ttl = %s, want 30m", got)
		}
	})

	t.Run("from json", func(t *testing.T) {
		cfg := &Config{}
		cfg.Agent.InteractiveSessionTTL = Duration(90 * time.Second)
		cfg = loadEffectiveConfig(cfg)
		if got := time.Duration(cfg.Agent.InteractiveSessionTTL); got != 90*time.Second {
			t.Fatalf("interactive_session_ttl = %s, want 90s", got)
		}
	})

	t.Run("env overrides json", func(t *testing.T) {
		t.Setenv("INTERACTIVE_SESSION_TTL", "1s")
		cfg := &Config{}
		cfg.Agent.InteractiveSessionTTL = Duration(90 * time.Second)
		cfg = loadEffectiveConfig(cfg)
		if got := time.Duration(cfg.Agent.InteractiveSessionTTL); got != time.Second {
			t.Fatalf("INTERACTIVE_SESSION_TTL override = %s, want 1s", got)
		}
	})

	t.Run("json roundtrip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"agent":{"interactive_session_ttl":"2m"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWithPath(path)
		if err != nil {
			t.Fatalf("LoadWithPath: %v", err)
		}
		if got := time.Duration(cfg.Agent.InteractiveSessionTTL); got != 2*time.Minute {
			t.Fatalf("interactive_session_ttl from json = %s, want 2m", got)
		}
	})
}
