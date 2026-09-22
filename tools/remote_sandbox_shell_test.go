package tools

import (
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// GetShell session-scoped resolution (2026-09-22 P0: "remote task failed with
// exit code 2" on runner 1101).
//
// Root cause chain (production evidence, machine 8.222.11.182:1101):
//  1. RemoteSandbox.GetShell resolved the runner with an EMPTY session key.
//  2. With 2+ runners connected (b300-4 + 1101) that resolution fails → GetShell
//     silently fell back to the hardcoded "/bin/sh".
//  3. The server sent ["/bin/sh", "-l", "-c", cmd]. On 1101 /bin/sh is DASH;
//     the login shell sourced /etc/profile → /etc/profile.d/dsw_runtime_env.sh
//     uses bash-only process substitution → dash syntax error → exit 2, the
//     command NEVER RAN (only the login banner printed).
//
// The session's own runner (1101) reported /bin/bash — the fallback ignored it.
// ============================================================================

// A session bound to a runner must get THAT runner's shell, even when other
// runners are connected. (Pre-fix: GetShell used an empty session key, failed
// with 2 runners online, and returned the "/bin/sh" fallback.)
func TestRemoteSandbox_GetShell_ResolvesSessionRunner(t *testing.T) {
	rs := &RemoteSandbox{
		runners:  map[string]*runnerConnection{},
		versions: map[string]string{},
	}
	rs.runners["m1"] = &runnerConnection{runnerName: "m1", workspace: "/w1", shell: "/bin/bash"}
	rs.runners["1101"] = &runnerConnection{runnerName: "1101", workspace: "/root", shell: "/bin/bash"}
	// A runner whose shell differs, to prove the RIGHT connection is picked.
	rs.runners["zshbox"] = &runnerConnection{runnerName: "zshbox", workspace: "/wz", shell: "/bin/zsh"}

	sessionRunners := &sync.Map{}
	sessionRunners.Store("web:chat_1", "zshbox")
	rs.sessionRunners = sessionRunners

	got, err := rs.GetShell("web:chat_1", "")
	if err != nil {
		t.Fatalf("GetShell(session) failed: %v", err)
	}
	if got != "/bin/zsh" {
		t.Fatalf("GetShell(session) = %q, want the bound runner's shell /bin/zsh (got fallback? pre-fix behavior: /bin/sh)", got)
	}
}

// Unbound session + multiple runners: GetShell must fail loudly instead of
// silently returning "/bin/sh" (the shell of a machine the session never
// uses). A wrong shell is worse than an error — the caller cannot tell.
func TestRemoteSandbox_GetShell_UnboundMultipleRunnersFailsLoudly(t *testing.T) {
	rs := &RemoteSandbox{
		runners:  map[string]*runnerConnection{},
		versions: map[string]string{},
	}
	rs.runners["m1"] = &runnerConnection{runnerName: "m1", workspace: "/w1", shell: "/bin/bash"}
	rs.runners["m2"] = &runnerConnection{runnerName: "m2", workspace: "/w2", shell: "/bin/bash"}
	rs.sessionRunners = &sync.Map{}

	_, err := rs.GetShell("web:unbound", "")
	if err == nil {
		t.Fatal("GetShell on an unbound session with 2+ runners connected must return an error, not a silent /bin/sh fallback (pre-fix behavior)")
	}
}

// Unbound session + exactly one runner: the single runner is used (same
// resolution rule as getRunnerForSession / ExecBg routing).
func TestRemoteSandbox_GetShell_UnboundSingleRunnerUsesIt(t *testing.T) {
	rs := &RemoteSandbox{
		runners:  map[string]*runnerConnection{},
		versions: map[string]string{},
	}
	rs.runners["only"] = &runnerConnection{runnerName: "only", workspace: "/w", shell: "/bin/bash"}
	rs.sessionRunners = &sync.Map{}

	got, err := rs.GetShell("web:unbound", "")
	if err != nil {
		t.Fatalf("single-runner GetShell should succeed: %v", err)
	}
	if got != "/bin/bash" {
		t.Fatalf("GetShell = %q, want /bin/bash", got)
	}
}

// ============================================================================
// remoteTaskOutcome — exit-code semantics aligned with the LOCAL sandbox.
//
// Local (noneSandboxExecAsync) returns (exitCode, nil) for ANY exit code: a
// command that ran and exited non-zero is a normal completion; the caller
// (executeForeground) formats it as "[EXIT N] <cmd>\n<output>". The remote path
// used to return a Go error for the same situation ("remote task failed with
// exit code N"), producing a different tool result shape than local.
// ============================================================================

func TestRemoteTaskOutcome_NonZeroExitIsCompletion(t *testing.T) {
	code, err := remoteTaskOutcome("failed", 2)
	if err != nil {
		t.Fatalf("a command that RAN and exited 2 must be a normal completion (local parity), got error: %v", err)
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRemoteTaskOutcome_CompletedKeepsExitCode(t *testing.T) {
	code, err := remoteTaskOutcome("completed", 0)
	if err != nil {
		t.Fatalf("completed: unexpected error %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
}

func TestRemoteTaskOutcome_NegativeExitIsExecutionError(t *testing.T) {
	// Negative exit code = the command never ran (start/chdir failure on the
	// runner reports -1). That IS an execution error, not a command exit.
	_, err := remoteTaskOutcome("failed", -1)
	if err == nil {
		t.Fatal("failed with negative exit code must be an execution error")
	}
}

func TestRemoteTaskOutcome_KilledIsError(t *testing.T) {
	_, err := remoteTaskOutcome("killed", 0)
	if err == nil {
		t.Fatal("killed must be an error")
	}
}

func TestRemoteTaskOutcome_UnknownStatusIsError(t *testing.T) {
	_, err := remoteTaskOutcome("weird", 0)
	if err == nil {
		t.Fatal("unknown status must be an error")
	}
	if !strings.Contains(err.Error(), "weird") {
		t.Fatalf("error should name the unknown status, got: %v", err)
	}
}
