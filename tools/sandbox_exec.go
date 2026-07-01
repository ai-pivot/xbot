package tools

import (
	"fmt"
	"strings"
	"time"
)

// resolveTimeout returns the first non-zero timeout from the variadic args,
// or SandboxCtxTimeout if none provided.
func resolveTimeout(timeout ...time.Duration) time.Duration {
	if len(timeout) > 0 && timeout[0] > 0 {
		return timeout[0]
	}
	return SandboxCtxTimeout
}

// RunInSandbox 在沙箱内执行命令并返回输出。
// 当沙箱为 none 模式时返回错误。
// 可选 timeout 参数覆盖默认 SandboxCtxTimeout。
func RunInSandbox(ctx *ToolContext, command string, args ...string) (string, error) {
	return runInSandbox(ctx, false, "", command, args, nil)
}

// RunInSandboxWithTimeout 在沙箱内执行命令并返回输出，使用指定的超时时间。
func RunInSandboxWithTimeout(ctx *ToolContext, timeout time.Duration, command string, args ...string) (string, error) {
	to := []time.Duration{timeout}
	return runInSandbox(ctx, false, "", command, args, to)
}

// RunInSandboxWithShell 在沙箱内执行 shell 命令并返回输出。
// 使用 login shell 自动加载环境变量配置文件。
// 可选 timeout 参数覆盖默认 SandboxCtxTimeout。
func RunInSandboxWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	return runInSandbox(ctx, true, shellCmd, "", nil, nil)
}

// RunInSandboxWithShellTimeout 在沙箱内执行 shell 命令并返回输出，使用指定的超时时间。
func RunInSandboxWithShellTimeout(ctx *ToolContext, shellCmd string, timeout time.Duration) (string, error) {
	to := []time.Duration{timeout}
	return runInSandbox(ctx, true, shellCmd, "", nil, to)
}

// RunInSandboxRaw 在沙箱内执行命令并返回原始输出（不做 TrimSpace）。
// 适用于需要保留文件原始内容的场景（如 cat 读取文件）。
func RunInSandboxRaw(ctx *ToolContext, command string, args ...string) (string, error) {
	return runInSandboxRaw(ctx, false, "", command, args, nil)
}

// RunInSandboxRawWithTimeout 在沙箱内执行命令并返回原始输出，使用指定的超时时间。
func RunInSandboxRawWithTimeout(ctx *ToolContext, timeout time.Duration, command string, args ...string) (string, error) {
	to := []time.Duration{timeout}
	return runInSandboxRaw(ctx, false, "", command, args, to)
}

// RunInSandboxRawWithShell 在沙箱内执行 shell 命令并返回原始输出（不做 TrimSpace）。
func RunInSandboxRawWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	return runInSandboxRaw(ctx, true, shellCmd, "", nil, nil)
}

// RunInSandboxRawWithShellTimeout 在沙箱内执行 shell 命令并返回原始输出，使用指定的超时时间。
func RunInSandboxRawWithShellTimeout(ctx *ToolContext, shellCmd string, timeout time.Duration) (string, error) {
	to := []time.Duration{timeout}
	return runInSandboxRaw(ctx, true, shellCmd, "", nil, to)
}

// runInSandbox is the shared implementation for formatted (TrimSpace) sandbox execution.
func runInSandbox(ctx *ToolContext, useShell bool, shellCmd, command string, args []string, timeout []time.Duration) (string, error) {
	result, err := execSandbox(ctx, useShell, shellCmd, command, args, timeout)
	if err != nil {
		return "", err
	}
	return formatExecResult(result), nil
}

// runInSandboxRaw is the shared implementation for raw (no TrimSpace) sandbox execution.
func runInSandboxRaw(ctx *ToolContext, useShell bool, shellCmd, command string, args []string, timeout []time.Duration) (string, error) {
	result, err := execSandbox(ctx, useShell, shellCmd, command, args, timeout)
	if err != nil {
		return "", err
	}
	return formatExecResultRaw(result), nil
}

// execSandbox is the core sandbox execution logic shared by all variants.
func execSandbox(ctx *ToolContext, useShell bool, shellCmd, command string, args []string, timeout []time.Duration) (*ExecResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return nil, fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	spec := ExecSpec{
		Timeout: resolveTimeout(timeout...),
		UserID:  userID,
	}

	if useShell {
		workspaceRoot := ctx.WorkspaceRoot
		if workspaceRoot == "" {
			workspaceRoot = ctx.WorkingDir
		}
		shell, err := sandbox.GetShell(userID, workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to get shell: %w", err)
		}
		if command == "" && shellCmd != "" {
			// RunInSandboxWithShell path: use hardcoded -l -c (sandbox is always Linux)
			spec.Command = shell
			spec.Args = []string{shell, "-l", "-c", shellCmd}
		} else {
			// RunInSandboxRawWithShell path: use LoginShellArgs
			spec.Command = shell
			spec.Args = LoginShellArgs(shell, shellCmd)
		}
	} else {
		spec.Command = command
		spec.Args = append([]string{command}, args...)
	}

	setSandboxDir(ctx, sandbox, &spec)
	return sandbox.Exec(ctx.Ctx, spec)
}

// setSandboxDir 根据 sandbox 模式设置 ExecSpec 的 Dir 和 Workspace 字段。
func setSandboxDir(ctx *ToolContext, sandbox Sandbox, spec *ExecSpec) {
	switch sandbox.Name() {
	case "docker":
		spec.Workspace = ctx.WorkspaceRoot
		spec.Dir = ctx.Sandbox.Workspace(ctx.OriginUserID)
	case "remote":
		// Remote: use Cd-set CurrentDir if available, otherwise runner defaults to its workspace
		if ctx != nil && ctx.CurrentDir != "" {
			spec.Dir = ctx.CurrentDir
		}
	case "none":
		spec.Dir = ctx.WorkspaceRoot
	}
}

// formatExecResult 格式化 ExecResult 为 TrimSpace 后的字符串。
// 非零退出码时返回 error。
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

// formatExecResultRaw 格式化 ExecResult 为原始字符串（不做 TrimSpace）。
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
