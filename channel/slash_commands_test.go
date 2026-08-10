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
