package tools

import (
	"fmt"
	"strings"
	"time"
)

// defaultExecTimeout is the default timeout for sandbox exec operations.
const defaultExecTimeout = 30 * time.Second

// maxHTTPResponseBodySize limits HTTP response body reads (10MB).
const maxHTTPResponseBodySize = 10 * 1024 * 1024

// HTTP client timeout defaults for tool HTTP operations.
const (
	httpDefaultTimeout  = 30 * time.Second // Default HTTP client timeout
	httpDownloadTimeout = 60 * time.Second // Timeout for file downloads
)

// Sandbox backend type constants.
const (
	SandboxNone   = "none"
	SandboxDocker = "docker"
	SandboxRemote = "remote"
)

// RunInSandbox executes a command in the sandbox and returns the output.
// returns an error when sandbox is in none mode.
func RunInSandbox(ctx *ToolContext, command string, args ...string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	spec := ExecSpec{
		Command: command,
		Args:    append([]string{command}, args...),
		Shell:   false,
		Timeout: defaultExecTimeout,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResult(result), nil
}

// RunInSandboxWithShell executes a shell command in the sandbox and returns the output.
// uses login shell to auto-load environment config files.
func RunInSandboxWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	// Get default shell
	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = ctx.WorkingDir
	}
	shell, err := sandbox.GetShell(userID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	// RunInSandboxRawWithShell only runs in docker/remote sandbox (none returns early above).
	// These sandboxes are always Linux — use hardcoded -l -c to avoid LoginShellArgs
	// returning -Command when compiled on Windows.
	spec := ExecSpec{
		Command: shell,
		Args:    []string{shell, "-l", "-c", shellCmd},
		Shell:   false,
		Timeout: defaultExecTimeout,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResult(result), nil
}

// RunInSandboxRaw executes a command in the sandbox and returns raw output (no TrimSpace)。
// Suitable for scenarios that need to preserve the original file content (e.g. cat reading files).
func RunInSandboxRaw(ctx *ToolContext, command string, args ...string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	spec := ExecSpec{
		Command: command,
		Args:    append([]string{command}, args...),
		Shell:   false,
		Timeout: defaultExecTimeout,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResultRaw(result), nil
}

// RunInSandboxRawWithShell executes a shell command in the sandbox and returns raw output (no TrimSpace)。
func RunInSandboxRawWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = ctx.WorkingDir
	}
	shell, err := sandbox.GetShell(userID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	spec := ExecSpec{
		Command: shell,
		Args:    LoginShellArgs(shell, shellCmd),
		Shell:   false,
		Timeout: defaultExecTimeout,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResultRaw(result), nil
}

// setSandboxDir sets the Dir and Workspace fields of ExecSpec based on sandbox mode.
func setSandboxDir(ctx *ToolContext, sandbox Sandbox, spec *ExecSpec) {
	switch sandbox.Name() {
	case SandboxDocker:
		spec.Workspace = ctx.WorkspaceRoot
		spec.Dir = ctx.Sandbox.Workspace(ctx.OriginUserID)
	case SandboxRemote:
		// Remote: use Cd-set CurrentDir if available, otherwise runner defaults to its workspace
		if ctx != nil && ctx.CurrentDir != "" {
			spec.Dir = ctx.CurrentDir
		}
	case SandboxNone:
		spec.Dir = ctx.WorkspaceRoot
	}
}

// formatExecResult formats ExecResult as a TrimSpace'd string.
// Returns error on non-zero exit code.
func formatExecResult(result *ExecResult) string {
	output := strings.TrimSpace(result.Stdout)
	if result.Stderr != "" {
		if output != "" {
			output += "\n[stderr] " + result.Stderr
		} else {
			output = "[stderr] " + result.Stderr
		}
	}
	return output
}

// formatExecResultRaw formats ExecResult as a raw string (no TrimSpace).
func formatExecResultRaw(result *ExecResult) string {
	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n[stderr] " + result.Stderr
		} else {
			output = "[stderr] " + result.Stderr
		}
	}
	return output
}
