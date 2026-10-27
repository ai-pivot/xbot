package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xbot/protocol"
	"xbot/tools"
)

// TestCheckpointCallback_NilStoreDoesNotPanic guards the regression where the
// checkpoint hook was registered globally but only CLI sessions ever get a
// CheckpointStore (agent.ensureCheckpointStore returns early for non-cli
// channels). Every FileCreate/FileReplace in a web/qq session therefore hit
// cs.Store() == nil and panicked inside the hook — logged as
// `hooks: handler "builtin:checkpoint" panicked ... nil pointer dereference`.
//
// A session without a store simply has no rewind support; the hook must skip
// silently rather than panic.
func TestCheckpointCallback_NilStoreDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(abs, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	cs := protocol.NewCheckpointState(nil)
	if cs.Store() != nil {
		t.Fatal("precondition: expected nil store")
	}
	cb := CheckpointCallback(cs)
	if cb == nil || cb.Fn == nil {
		t.Fatal("expected non-nil callback")
	}

	input := map[string]any{"path": abs}

	// Pre must not panic with a nil store.
	if _, err := cb.Fn(context.Background(), &PreToolUseEvent{
		ToolName_:  "FileReplace",
		ToolInput_: input,
	}); err != nil {
		t.Fatalf("PreToolUse returned error: %v", err)
	}

	// Post must not panic. This is where the original nil dereference lived:
	// cs.Store().Write(snap) on a nil interface.
	if _, err := cb.Fn(context.Background(), &PostToolUseEvent{
		ToolName_:   "FileReplace",
		ToolInput_:  input,
		ToolOutput_: "ok",
	}); err != nil {
		t.Fatalf("PostToolUse returned error: %v", err)
	}

	// Nothing should have been left pending: with no store there is no rewind,
	// so the hook must not accumulate snapshots for the whole session lifetime.
	if _, found := cs.GetAndDeletePending(abs); found {
		t.Error("nil store must not leave a pending snapshot behind")
	}
}

// TestCheckpointCallback_WithStoreStillWorks ensures the guard did not disable
// the feature for CLI sessions, which do have a store.
func TestCheckpointCallback_WithStoreStillWorks(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(abs, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := tools.NewCheckpointStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	cs := protocol.NewCheckpointState(store)
	cs.SetTurnIdx(7)
	cb := CheckpointCallback(cs)

	input := map[string]any{"path": abs}
	if _, err := cb.Fn(context.Background(), &PreToolUseEvent{
		ToolName_:  "FileReplace",
		ToolInput_: input,
	}); err != nil {
		t.Fatalf("PreToolUse returned error: %v", err)
	}
	if _, found := cs.GetAndDeletePending(abs); !found {
		t.Fatal("expected a pending snapshot when a store is present")
	}

	// Re-stage then run Post to exercise the write path.
	if _, err := cb.Fn(context.Background(), &PreToolUseEvent{
		ToolName_:  "FileReplace",
		ToolInput_: input,
	}); err != nil {
		t.Fatalf("PreToolUse returned error: %v", err)
	}
	if _, err := cb.Fn(context.Background(), &PostToolUseEvent{
		ToolName_:   "FileReplace",
		ToolInput_:  input,
		ToolOutput_: "ok",
	}); err != nil {
		t.Fatalf("PostToolUse returned error: %v", err)
	}
}
