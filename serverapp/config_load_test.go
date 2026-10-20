package serverapp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xbot/config"
)

func TestLoadServerConfigExplicitPathDefaults(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"llm_retry_attempts":0,"llm_retry_delay":"0s","llm_retry_max_delay":"0s","llm_retry_timeout":"0s"},"llm":{"model":"selected-file"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "selected-file" {
		t.Fatalf("model = %q", cfg.LLM.Model)
	}
	if cfg.Agent.LLMRetryAttempts != 5 || cfg.Agent.LLMRetryDelay != config.Duration(time.Second) || cfg.Agent.LLMRetryMaxDelay != config.Duration(30*time.Second) || cfg.Agent.LLMRetryTimeout != config.Duration(120*time.Second) {
		t.Fatalf("explicit path bypassed retry defaults: attempts=%d delay=%s max_delay=%s timeout=%s", cfg.Agent.LLMRetryAttempts, time.Duration(cfg.Agent.LLMRetryDelay), time.Duration(cfg.Agent.LLMRetryMaxDelay), time.Duration(cfg.Agent.LLMRetryTimeout))
	}
}

func TestLoadServerConfigExplicitPathEnvironment(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())
	t.Setenv("LLM_RETRY_ATTEMPTS", "3")
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"llm_retry_attempts":7}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.LLMRetryAttempts != 3 {
		t.Fatalf("attempts = %d, want environment override 3", cfg.Agent.LLMRetryAttempts)
	}
}

func TestLoadServerConfigMissingExplicitFile(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())
	if _, err := loadServerConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing explicit config must fail")
	}
}
