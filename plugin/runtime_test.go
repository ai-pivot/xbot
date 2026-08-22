package plugin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// startDeactivateAwareProcess starts a stdio plugin whose process exits on its
// own after handling "deactivate" (mirrors protocol.run's deactivate branch).
func startDeactivateAwareProcess(t *testing.T) *StdioPluginProcess {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "plugin.py")
	// Cooperative plugin: on "deactivate" exit 0; on "activate" respond. Python
	// with explicit flush avoids the pipe block-buffering that breaks shell echo.
	body := `#!/usr/bin/env python3
import sys, json
for line in sys.stdin:
   line = line.strip()
   if not line:
      continue
   try:
      req = json.loads(line)
   except Exception:
      continue
   if req.get("method") == "deactivate":
      sys.exit(0)
   if req.get("method") == "activate":
      print(json.dumps({"result": "{}"}))
      sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	proc, err := startPluginProcess(script, "", nil, dir)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	// startPluginProcess does NOT start readLoop — the real caller
	// (stdioPlugin.Activate) does. Tests must start it manually.
	go proc.readLoop()
	if _, err := proc.Call(context.Background(), &PluginRequest{Method: "activate"}); err != nil {
		t.Fatalf("activate call: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	return proc
}

func TestStdioPluginProcess_GracefulStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("graceful stop relies on Unix signal semantics + Python shebang script; not portable to Windows")
	}
	proc := startDeactivateAwareProcess(t)
	if !proc.running {
		t.Fatal("expected process running")
	}
	start := time.Now()
	proc.Stop()
	elapsed := time.Since(start)
	if proc.running {
		t.Error("expected process stopped after Stop")
	}
	// Cooperative exit must be fast — Stop returns after the process exits on
	// its own, well before gracefulStopTimeout.
	if elapsed > gracefulStopTimeout {
		t.Errorf("graceful stop took %v; expected < %v (should exit on its own, not timeout-kill)", elapsed, gracefulStopTimeout)
	}
}

func TestStdioPluginProcess_StopForcesKillOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("force-kill grace period relies on Unix signal semantics + Python shebang script; not portable to Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "plugin.py")
	// This plugin never exits on deactivate — it ignores the notification.
	body := `#!/usr/bin/env python3
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line:
       continue
    try:
       req = json.loads(line)
    except Exception:
       continue
    if req.get("method") == "activate":
       print(json.dumps({"result": "{}"}))
       sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	proc, err := startPluginProcess(script, "", nil, dir)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	go proc.readLoop()
	if _, err := proc.Call(context.Background(), &PluginRequest{Method: "activate"}); err != nil {
		t.Fatalf("activate call: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	proc.Stop()
	elapsed := time.Since(start)
	if proc.running {
		t.Error("expected process stopped after Stop (force-killed)")
	}
	// Stubborn plugin must be killed only after the grace period.
	if elapsed < gracefulStopTimeout-500*time.Millisecond {
		t.Errorf("force-kill happened too early: %v; expected >= ~%v (grace period)", elapsed, gracefulStopTimeout)
	}
}
