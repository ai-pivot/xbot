package runnerclient

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xbot/internal/runnerproto"
)

// skipOnWindows guards Unix-only shell tests: DetectShell's $SHELL precedence
// and the sh-based bg-task execution only apply on Unix runners (Windows
// DetectShell returns powershell.exe/cmd.exe before the $SHELL check, and
// there is no "sh" to exec).
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only: $SHELL precedence / sh execution do not exist on Windows")
	}
}

// ============================================================================
// DetectShell — must match the LOCAL sandbox's defaultShell() precedence
// (2026-09-22 P0: "remote task failed with exit code 2" on runner 1101).
//
// The local (none) sandbox resolves the shell via $SHELL first; the runner's
// DetectShell used to probe /bin/bash directly. On machines where $SHELL points
// at the user's real login shell, the runner must report the SAME shell the
// local path would use — otherwise the server builds a login-shell command for
// a shell the user never configured (e.g. /bin/sh=dash on PAI DSW images whose
// /etc/profile.d uses bash-only process substitution → dash syntax error →
// exit 2, the command never runs).
// ============================================================================

func TestDetectShell_PrefersShellEnv(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	zsh := filepath.Join(dir, "zsh")
	if err := os.WriteFile(zsh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", zsh)
	if got := DetectShell(false, nil); got != zsh {
		t.Fatalf("DetectShell = %q, want $SHELL %q (local defaultShell parity)", got, zsh)
	}
}

func TestDetectShell_FallsBackToBashWithoutEnv(t *testing.T) {
	t.Setenv("SHELL", "")
	// /bin/bash exists on every Linux test runner; if it somehow doesn't,
	// the probe chain still must return a non-empty shell.
	got := DetectShell(false, nil)
	if got == "" {
		t.Fatal("DetectShell must never return an empty shell")
	}
}

// ============================================================================
// bgTaskManager.runNative — work-dir fallback parity with NativeExecutor.Exec
// (2026-09-22 audit: "make every runner behave exactly like local").
//
// NativeExecutor.Exec resolves a USABLE work dir (requested → workspace →
// home → nearest existing ancestor) and NEVER fails a command because the
// requested dir is missing. The bg-task path (used by the Shell tool via
// bg_exec) hard-set cmd.Dir with no fallback: a session CWD that exists on the
// server but not on the runner made chdir fail → the command never started →
// "remote task failed with exit code -1".
// ============================================================================

func TestBgTaskRunNative_MissingWorkDirFallsBack(t *testing.T) {
	skipOnWindows(t)
	ws := t.TempDir()
	m := newBgTaskManager(false, false, ws, nil)

	task := &bgTask{
		id:      "t1",
		command: "echo hi",
		req:     newBgExecRequest("/nonexistent/definitely/missing/dir", "echo hi"),
		status:  "running",
	}

	exitCode, status := task.runNative(m)
	if status != "completed" {
		t.Fatalf("status = %q, want completed (dir fallback must not fail the command); exit=%d", status, exitCode)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if out := task.stdout.String(); out != "hi\n" {
		t.Fatalf("stdout = %q, want %q", out, "hi\n")
	}
}

func TestBgTaskRunNative_UsesRequestedDirWhenPresent(t *testing.T) {
	skipOnWindows(t)
	ws := t.TempDir()
	m := newBgTaskManager(false, false, ws, nil)

	task := &bgTask{
		id:      "t2",
		command: "pwd",
		req:     newBgExecRequest(ws, "pwd"),
		status:  "running",
	}

	exitCode, status := task.runNative(m)
	if status != "completed" || exitCode != 0 {
		t.Fatalf("status=%q exit=%d, want completed/0", status, exitCode)
	}
	if got := filepath.Clean(strings.TrimSpace(task.stdout.String())); got != filepath.Clean(ws) {
		t.Fatalf("pwd = %q, want the requested dir %q", got, ws)
	}
}

// newBgExecRequest builds a shell-mode BgExecRequest for tests.
func newBgExecRequest(dir, command string) (req runnerproto.BgExecRequest) {
	req.Dir = dir
	req.Command = command
	req.Shell = true
	return req
}
