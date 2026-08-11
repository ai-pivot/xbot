package channel

import "testing"

func TestTUICommandListHasCompleteMetadataAndReturnsCopy(t *testing.T) {
	commands := TUICommandList()
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		if command.Name == "" || command.Usage == "" || command.Description == "" {
			t.Fatalf("incomplete TUI command metadata: %#v", command)
		}
		if _, exists := seen[command.Name]; exists {
			t.Fatalf("duplicate TUI command %q", command.Name)
		}
		seen[command.Name] = struct{}{}
	}
	for _, name := range append(append([]string(nil), TUISlashCommands...), additionalTUICommands...) {
		if _, ok := seen[name]; !ok {
			t.Errorf("TUICommandList is missing compatible command %q", name)
		}
	}
	if _, stale := seen["/model"]; stale {
		t.Error("removed /model command must not be advertised")
	}

	commands[0].Name = "/mutated"
	if TUICommandList()[0].Name == "/mutated" {
		t.Fatal("TUICommandList returned mutable catalog storage")
	}
}

// TestTUICommandMetadataHasNoOrphanEntries guards against dead metadata:
// every key in tuiCommandMetadata must be reachable from the command name
// lists. An orphan entry is silently dead — TUICommandList never emits it —
// and signals a name-list/metadata drift (e.g. a command renamed in one place
// but not the other).
func TestTUICommandMetadataHasNoOrphanEntries(t *testing.T) {
	reachable := make(map[string]struct{})
	for _, name := range append(append([]string(nil), TUISlashCommands...), additionalTUICommands...) {
		reachable[name] = struct{}{}
	}
	for name := range tuiCommandMetadata {
		if _, ok := reachable[name]; !ok {
			t.Errorf("tuiCommandMetadata has orphan entry %q not in TUISlashCommands/additionalTUICommands", name)
		}
	}
}
