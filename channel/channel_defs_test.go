package channel

import "testing"

// The built-in channel schema is the single source of truth for BOTH the CLI
// settings panel and the Web channels panel, so a missing/renamed field shows
// up as an uneditable channel in two places at once.
func TestBuiltinChannelSchema(t *testing.T) {
	for _, name := range BuiltinChannelNames {
		schema := BuiltinChannelSchema(name)
		if len(schema) == 0 {
			t.Fatalf("built-in channel %q has no schema", name)
		}
		keys := map[string]bool{}
		for _, def := range schema {
			if def.Key == "" {
				t.Errorf("%s: field with empty key", name)
			}
			if def.Type == "" {
				t.Errorf("%s.%s: empty type", name, def.Key)
			}
			keys[def.Key] = true
		}
		// Every channel is switchable and must therefore expose `enabled`
		// (the panel renders it as the toggle; set_channel_config keys on it).
		if !keys["enabled"] {
			t.Errorf("%s: schema has no `enabled` field", name)
		}
	}
}

func TestBuiltinChannelSchema_UnknownIsNil(t *testing.T) {
	if schema := BuiltinChannelSchema("telegram"); schema != nil {
		t.Errorf("plugin channel names must not resolve to a built-in schema, got %v", schema)
	}
}

func TestBuiltinChannelNames_MatchesKnownChannels(t *testing.T) {
	want := []string{"web", "feishu", "qq", "napcat"}
	if len(BuiltinChannelNames) != len(want) {
		t.Fatalf("BuiltinChannelNames: got %v, want %v", BuiltinChannelNames, want)
	}
	for i, name := range want {
		if BuiltinChannelNames[i] != name {
			t.Errorf("BuiltinChannelNames[%d]: got %q, want %q", i, BuiltinChannelNames[i], name)
		}
	}
}
