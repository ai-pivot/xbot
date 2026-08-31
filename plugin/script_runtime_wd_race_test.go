package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// P2 race regression: hook-trigger WorkDir snapshot vs shared-pctx overwrite
// ---------------------------------------------------------------------------

// hookTestContext builds a scriptPlugin activated with a PostToolUse hook
// contribution and returns the pctx (whose hooks registry holds the
// registered handler — same package, direct access).
func activateHookPlugin(t *testing.T, entry string) (*scriptPlugin, *pluginContextImpl) {
	t.Helper()
	dir := t.TempDir()
	m := PluginManifest{
		ID:          "com.test.hook-race",
		Name:        "hook-race",
		Version:     "1.0.0",
		Runtime:     RuntimeScript,
		Entry:       entry,
		Permissions: []string{PermUIContribute, PermHooksSubscribe},
		Contributes: &PluginContributes{
			UI: []UISlotContribution{
				{ID: "w1", Slot: "infoBar", Priority: 10, RefreshInterval: "1h"},
			},
			Hooks: []HookContribution{
				{Event: "PostToolUse", Matcher: ""},
			},
		},
	}
	p, err := NewScriptRuntime().Create(&m, dir)
	if err != nil {
		t.Fatal(err)
	}
	sp := p.(*scriptPlugin)
	pctx := newTestPluginContext(t, sp, t.TempDir())
	if err := sp.Activate(pctx); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	return sp, pctx
}

// TestScriptPlugin_HookWorkDirSnapshotBeatsPctxRace reproduces the documented
// cross-session race (script_runtime.go's own comments admitted it):
//
//	Session A triggers a hook (its cwd = dirA).
//	Session B's Cd/RefreshWorkDir overwrites the SHARED pctx.workingDir to dirB.
//	The async trigger is consumed → script runs in dirB (WRONG — session A's
//	plugin ran in session B's directory).
//
// The fix: the hook handler pins hp.WorkDir (the TRIGGER-TIME snapshot from
// hooks BasePayload.CWD via plugin_bridge) into pendingDirs, and the refresh
// loop consumes the pinned dir. This test drives the REAL registered handler
// (extracted from pctx.hooks) with a payload snapshot dirA while pctx holds
// dirB and asserts the script output lands under the dirA key.
func TestScriptPlugin_HookWorkDirSnapshotBeatsPctxRace(t *testing.T) {
	t.Parallel()

	dirA := t.TempDir() // session A's cwd (hook trigger-time snapshot)
	dirB := t.TempDir() // session B's cwd (pctx overwritten before consumption)

	// The script echoes a marker we can find in the outputs map.
	sp, pctx := activateHookPlugin(t, "echo race-marker")
	defer func() { _ = sp.Deactivate(pctx) }()

	// Extract the registered hook handler (same package: pctx.hooks).
	var handler HookHandler
	pctx.mu.Lock()
	for _, reg := range pctx.hooks {
		if reg.Event == "PostToolUse" {
			handler = reg.Handler
			break
		}
	}
	pctx.mu.Unlock()
	if handler == nil {
		t.Fatal("no PostToolUse hook handler registered (manifest Hooks contributes missing?)")
	}

	// Session B wins the race: the SHARED pctx now points at dirB.
	pctx.SetSessionMetadata(dirB, "test-channel", "session-B", 0)
	if got := pctx.WorkingDir(); got != dirB {
		t.Fatalf("precondition: pctx.WorkingDir() = %q, want dirB", got)
	}

	// Session A's hook fires — the payload carries the TRIGGER-TIME snapshot
	// (plugin_bridge extracts hooks BasePayload.CWD into WorkDir).
	hp := &HookPayload{Event: "PostToolUse", WorkDir: dirA, ToolName: "Shell"}
	if _, err := handler(context.Background(), hp); err != nil {
		t.Fatalf("hook handler failed: %v", err)
	}

	// The handler must have pinned the snapshot dirA into pendingDirs
	// (BEFORE any pctx read — that's the race fix).
	sp.pendingMu.Lock()
	_, pinned := sp.pendingDirs[dirA]
	nPending := len(sp.pendingDirs)
	sp.pendingMu.Unlock()
	if !pinned {
		t.Fatalf("hook handler must pin hp.WorkDir (dirA) into pendingDirs; pendingDirs(%d)=%v", nPending, sp.pendingDirs)
	}

	// Wait for the async refresh loop to consume the pinned dir and produce
	// output under the dirA key (NOT dirB).
	deadline := time.After(5 * time.Second)
	for {
		sp.outputMu.RLock()
		var markerInA bool
		for _, w := range sp.outputs[dirA] {
			if strings.Contains(w, "race-marker") {
				markerInA = true
			}
		}
		sp.outputMu.RUnlock()
		if markerInA {
			// Snapshot won: the script ran in session A's directory despite
			// pctx pointing at dirB. dirB may also run (the pctx fallback —
			// running it is harmless and order-independent: runAndUpdate
			// iterates a map, so dirB's output may appear BEFORE dirA's;
			// the snapshot pin is proven by markerInA arriving AT ALL).
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for the snapshot dir output; outputs keys=%v", func() []string {
				sp.outputMu.RLock()
				defer sp.outputMu.RUnlock()
				var ks []string
				for k := range sp.outputs {
					ks = append(ks, k)
				}
				return ks
			}())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestScriptPlugin_HookSnapshotMissingFallsBackToPctx verifies the fallback:
// a hook payload WITHOUT WorkDir (e.g. an engine path that didn't set cwd)
// still runs in the pctx directory — the snapshot is an ADDITIVE pin, not a
// replacement for the pctx path.
func TestScriptPlugin_HookSnapshotMissingFallsBackToPctx(t *testing.T) {
	t.Parallel()

	dirB := t.TempDir() // pctx dir (the only source — no snapshot)

	sp, pctx := activateHookPlugin(t, "echo fallback-marker")
	defer func() { _ = sp.Deactivate(pctx) }()

	var handler HookHandler
	pctx.mu.Lock()
	for _, reg := range pctx.hooks {
		if reg.Event == "PostToolUse" {
			handler = reg.Handler
			break
		}
	}
	pctx.mu.Unlock()
	if handler == nil {
		t.Fatal("no PostToolUse hook handler registered")
	}

	// Payload without WorkDir → nothing pinned.
	hp := &HookPayload{Event: "PostToolUse", ToolName: "Shell"}
	if _, err := handler(context.Background(), hp); err != nil {
		t.Fatalf("hook handler failed: %v", err)
	}

	sp.pendingMu.Lock()
	_, pinned := sp.pendingDirs[dirB]
	nPending := len(sp.pendingDirs)
	sp.pendingMu.Unlock()
	if pinned {
		t.Fatalf("payload without WorkDir must not pin anything (pctx dir ran via the refresh-loop fallback); pendingDirs(%d)=%v", nPending, sp.pendingDirs)
	}

	// Drive the refresh synchronously (the async trigger-loop path is
	// covered by TestScriptPlugin_HookWorkDirSnapshotBeatsPctxRace; here we
	// assert the pctx-fallback semantics deterministically).
	pctx.SetSessionMetadata(dirB, "test-channel", "session-B", 0)
	sp.runAndUpdate()

	deadline := time.After(5 * time.Second)
	for {
		sp.outputMu.RLock()
		var found bool
		for _, w := range sp.outputs[dirB] {
			if strings.Contains(w, "fallback-marker") {
				found = true
			}
		}
		sp.outputMu.RUnlock()
		if found {
			return
		}
		select {
		case <-deadline:
			sp.outputMu.RLock()
			keys := make([]string, 0)
			for k := range sp.outputs {
				keys = append(keys, k)
			}
			sp.outputMu.RUnlock()
			t.Fatalf("pctx fallback did not run the script in the pctx dir; outputs keys=%v pctx.WorkingDir()=%q", keys, pctx.WorkingDir())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestAddPendingDirSnapshotIsolatesSessions verifies the helper directly:
// hook snapshot dirs and RefreshWorkDir broadcast dirs accumulate in
// pendingDirs — each consumed exactly once by runAndUpdate.
func TestAddPendingDirSnapshotIsolatesSessions(t *testing.T) {
	t.Parallel()
	sp := newTestScriptPlugin(t, "echo x")

	sp.addPendingDir("/session/A")
	sp.addPendingDir("/session/B")
	// Duplicate pins collapse (map semantics — one run per dir).
	sp.addPendingDir("/session/A")

	sp.pendingMu.Lock()
	got := make([]string, 0, len(sp.pendingDirs))
	for d := range sp.pendingDirs {
		got = append(got, d)
	}
	sp.pendingMu.Unlock()

	if len(got) != 2 {
		t.Fatalf("pendingDirs must contain exactly 2 distinct dirs, got %v", got)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "/session/A") || !strings.Contains(joined, "/session/B") {
		t.Fatalf("pendingDirs must contain both session dirs, got %v", got)
	}
}

// TestHookPayloadWorkDirField pins the field contract: HookPayload.WorkDir
// is the TRIGGER-TIME cwd snapshot (extracted from hooks BasePayload.CWD
// "cwd" by agent/hooks/plugin_bridge.go). Serializes as work_dir.
func TestHookPayloadWorkDirField(t *testing.T) {
	hp := &HookPayload{Event: "PostToolUse", WorkDir: "/session/A"}
	if hp.WorkDir != "/session/A" {
		t.Fatalf("WorkDir roundtrip failed: %q", hp.WorkDir)
	}
	data, err := json.Marshal(hp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"work_dir":"/session/A"`) {
		t.Fatalf("serialized payload must carry work_dir, got: %s", data)
	}
}
