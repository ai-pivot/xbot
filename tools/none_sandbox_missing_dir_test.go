//go:build !windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNoneSandboxExec_MissingWorkingDir reproduces issue #457: when the session
// working directory is deleted (dev machine migration, worktree cleanup, tmp
// reclamation), every Shell call failed with
//
//	sandbox exec: fork/exec /usr/bin/zsh: no such file or directory
//
// which blames the shell binary while the real cause is the missing cwd. The
// exec layer must report the working directory and the Cd recovery path.
func TestNoneSandboxExec_MissingWorkingDir(t *testing.T) {
	gone := removedDir(t)

	s := &NoneSandbox{}
	_, err := s.Exec(context.Background(), ExecSpec{
		Shell:   true,
		Command: "echo ok",
		Dir:     gone,
		Timeout: 10 * time.Second,
	})
	assertMissingWorkDirError(t, err, gone)
}

// TestNoneSandboxExecAsync_MissingWorkingDir covers the background / streaming
// path (Sandbox background shell and auto-promote use noneSandboxExecAsync).
func TestNoneSandboxExecAsync_MissingWorkingDir(t *testing.T) {
	gone := removedDir(t)

	_, err := noneSandboxExecAsync(context.Background(), ExecSpec{
		Shell:   true,
		Command: "echo ok",
		Dir:     gone,
	}, nil)
	assertMissingWorkDirError(t, err, gone)
}

// TestNoneSandboxExec_ExistingWorkingDirStillWorks guards against an
// over-eager validation breaking the normal path.
func TestNoneSandboxExec_ExistingWorkingDirStillWorks(t *testing.T) {
	dir := t.TempDir()

	s := &NoneSandbox{}
	res, err := s.Exec(context.Background(), ExecSpec{
		Shell:   true,
		Command: "echo ok",
		Dir:     dir,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec in existing dir: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" {
		t.Fatalf("unexpected result: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// TestShellTool_MissingWorkingDir covers the end-to-end tool surface: the
// model must see the cwd cause + Cd recovery hint, never a missing-shell error.
func TestShellTool_MissingWorkingDir(t *testing.T) {
	gone := removedDir(t)

	tool := &ShellTool{}
	ctx := &ToolContext{
		Ctx:        context.Background(),
		CurrentDir: gone,
		Sandbox:    &NoneSandbox{},
	}
	_, err := tool.Execute(ctx, `{"command":"echo ok"}`)
	assertMissingWorkDirError(t, err, gone)
}

// removedDir creates a directory and deletes it again, returning the now
// non-existent path.
func removedDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gone-workdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	return dir
}

// assertMissingWorkDirError asserts the error names the working directory,
// points at the Cd tool, and no longer masquerades as a missing shell binary.
func assertMissingWorkDirError(t *testing.T, err error, dir string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error for missing working dir %q, got nil", dir)
	}
	msg := err.Error()
	if !strings.Contains(msg, dir) {
		t.Errorf("error must name the missing working directory %q, got: %s", dir, msg)
	}
	if !strings.Contains(msg, "Cd") {
		t.Errorf("error must point at the Cd recovery path, got: %s", msg)
	}
	if strings.Contains(msg, "fork/exec") {
		t.Errorf("error must not masquerade as a missing shell binary, got: %s", msg)
	}
	if !strings.Contains(msg, "no such file or directory") && !strings.Contains(msg, "not exist") {
		t.Errorf("error must retain the underlying chdir/stat cause, got: %s", msg)
	}
}
