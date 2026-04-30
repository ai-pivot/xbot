package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/plugin"
)

// TestPluginEnricherMiddleware_Process verifies that the middleware correctly
// runs enrichers and injects their output into SystemParts.
func TestPluginEnricherMiddleware_Process(t *testing.T) {
	registry := plugin.NewEnricherRegistry()
	registry.Register("test-plugin", "weather", func(ctx context.Context) (string, error) {
		return "Current weather: sunny, 22°C", nil
	}, 100)

	mw := newPluginEnricherMiddleware(registry)
	mc := newMC()

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	got, ok := mc.SystemParts["plugin_enrichers"]
	if !ok {
		t.Fatal("expected SystemParts to contain 'plugin_enrichers' key")
	}
	if !strings.Contains(got, "weather") {
		t.Errorf("expected enricher output to contain 'weather', got %q", got)
	}
	if !strings.Contains(got, "test-plugin") {
		t.Errorf("expected enricher output to contain 'test-plugin', got %q", got)
	}
}

// TestPluginEnricherMiddleware_Empty verifies that the middleware is a no-op
// when the registry has no enrichers.
func TestPluginEnricherMiddleware_Empty(t *testing.T) {
	registry := plugin.NewEnricherRegistry()
	mw := newPluginEnricherMiddleware(registry)
	mc := newMC()

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if _, ok := mc.SystemParts["plugin_enrichers"]; ok {
		t.Error("expected no 'plugin_enrichers' key when registry is empty")
	}
}

// TestPluginEnricherMiddleware_NilRegistry verifies that the middleware handles
// a nil registry gracefully.
func TestPluginEnricherMiddleware_NilRegistry(t *testing.T) {
	mw := newPluginEnricherMiddleware(nil)
	mc := newMC()

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if _, ok := mc.SystemParts["plugin_enrichers"]; ok {
		t.Error("expected no 'plugin_enrichers' key when registry is nil")
	}
}

// TestPluginEnricherMiddleware_NameAndPriority verifies the middleware metadata.
func TestPluginEnricherMiddleware_NameAndPriority(t *testing.T) {
	mw := newPluginEnricherMiddleware(nil)

	if mw.Name() != "plugin_enrichers" {
		t.Errorf("expected name 'plugin_enrichers', got %q", mw.Name())
	}
	if mw.Priority() != 150 {
		t.Errorf("expected priority 150, got %d", mw.Priority())
	}
}

// TestPluginEnricherMiddleware_MultipleEnrichers verifies that multiple
// enrichers are concatenated in priority order.
func TestPluginEnricherMiddleware_MultipleEnrichers(t *testing.T) {
	registry := plugin.NewEnricherRegistry()
	registry.Register("p1", "high-priority", func(ctx context.Context) (string, error) {
		return "First enricher", nil
	}, 10)
	registry.Register("p2", "low-priority", func(ctx context.Context) (string, error) {
		return "Second enricher", nil
	}, 100)

	mw := newPluginEnricherMiddleware(registry)
	mc := newMC()

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	got := mc.SystemParts["plugin_enrichers"]
	idx1 := strings.Index(got, "First")
	idx2 := strings.Index(got, "Second")
	if idx1 == -1 || idx2 == -1 {
		t.Fatalf("expected both enrichers in output, got %q", got)
	}
	if idx1 > idx2 {
		t.Errorf("expected first enricher (priority 10) before second (priority 100)")
	}
}
