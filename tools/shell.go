package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"xbot/llm"

	log "xbot/logger"
)

// ShellTool 执行命令工具
type ShellTool struct{}

func (t *ShellTool) Name() string {
	return "Shell"
}

func (t *ShellTool) Description() string {
	return `Execute a command and return its output.
The command will be executed in the agent's working directory.
IMPORTANT: Commands are executed non-interactively with a timeout. Do NOT run interactive commands (e.g. vim, top, htop) or commands that require manual input. For commands that might prompt for input, use non-interactive flags (e.g. "apt-get -y", "yes |", "ssh -o BatchMode=yes"). For sudo, use NOPASSWD or "echo password | sudo -S".

PROCESS CLEANUP: Non-background commands are killed (including all child processes) when they return. Do NOT use nohup, disown, or trailing & — they create orphaned processes that waste resources and cause confusion. If a command needs to outlive the tool call, use "background": true instead.

BACKGROUND MODE: Set "background": true to run long-running commands (dev servers, build processes) without blocking. Returns a task ID immediately. The agent continues working while the command runs in the background. When the command finishes, its output is automatically injected into the conversation. To check progress, use task_status. If status is "running", use task_wait to block until completion, or continue with other work.

AUTO-BACKGROUND: If a command times out, it is automatically converted to a background task so no work is lost. The agent receives the task ID and can continue. Use task_wait to wait for completion, or continue with other work.

Example — poll a health endpoint until ready:
  {"command": "for i in $(seq 1 60); do curl -sf http://localhost:8080/health && exit 0; sleep 2; done; exit 1", "background": true}
Then use task_wait to block until the endpoint is up.

Parameters (JSON):
  - command: string, the command to execute
  - timeout: number (optional), timeout in seconds (default: 120, max: 600)
  - background: boolean (optional), run in background mode

Environment Variables:
- Commands run in a login shell (detected from container's /etc/passwd), which automatically sources /etc/profile, ~/.bash_profile, ~/.bashrc, etc.`
}

func (t *ShellTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "command", Type: "string", Description: "The command to execute", Required: true},
		{Name: "timeout", Type: "number", Description: "Timeout in seconds (default: 120, max: 600)", Required: false},
		{Name: "background", Type: "boolean", Description: "Run command in background (for long-running tasks like dev servers). Returns task ID immediately.", Required: false},
		{Name: "run_as", Type: "string", Description: "OS username to execute as. Requires permission control to be enabled. Only effective in none sandbox mode.", Required: false},
		{Name: "reason", Type: "string", Description: "Optional human-readable reason shown in approval requests when approval is required.", Required: false},
	}
}

func (t *ShellTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Command    string  `json:"command"`
		Timeout    float64 `json:"timeout"`
		Background bool    `json:"background"`
		RunAs      string  `json:"run_as"`
		Reason     string  `json:"reason"`
	}](input)
	if err != nil {
		return nil, err
	}

	if params.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	// When permission control is disabled, ignore any stale run_as/reason
	// the LLM might send from cached context.
	if !isPermControlActiveFromCtx(toolCtx.Ctx) {
		params.RunAs = ""
		params.Reason = ""
	}

	if err := validateRunAsReason(params.RunAs, params.Reason); err != nil {
		return nil, err
	}

	// 检测命令中的控制字符和 null bytes
	if strings.ContainsAny(params.Command, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f") {
		return nil, fmt.Errorf("command contains control characters (null bytes or other non-printable characters)")
	}

	timeout := DefaultShellTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
		if timeout > MaxShellTimeout {
			log.WithFields(log.Fields{
				"requested": timeout,
				"max":       MaxShellTimeout,
			}).Warn("Shell timeout exceeds maximum, capping")
			timeout = MaxShellTimeout
		}
	}

	// 使用传入的 context 作为父 context，支持外部取消（如用户 stop）
	parentCtx := context.Background()
	if toolCtx != nil && toolCtx.Ctx != nil {
		parentCtx = toolCtx.Ctx
	}

	userID := ""
	workspaceRoot := ""
	execDir := ""
	if toolCtx != nil {
		workspaceRoot = toolCtx.WorkspaceRoot
		if toolCtx.CurrentDir != "" {
			execDir = toolCtx.CurrentDir
		} else if toolCtx.WorkspaceRoot != "" {
			execDir = toolCtx.WorkspaceRoot
		} else {
			execDir = toolCtx.WorkingDir
		}
		userID = toolCtx.OriginUserID
		if userID == "" {
			userID = toolCtx.SenderID // fallback
		}
	}

	// 沙箱模式：workspace 必须用宿主机路径（用于 bind mount / 容器查找），
	// 不能用容器内路径（CurrentDir），否则会导致容器 mount 校验失败并重建。
	sandboxWorkspace := workspaceRoot
	if sandboxWorkspace == "" {
		sandboxWorkspace = execDir
	}

	// 使用 ToolContext 中的沙箱实例（由 SandboxRouter 按用户路由注入）
	sandbox := toolCtx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}

	// 获取容器默认 shell 并使用 login shell 执行命令
	shell, err := sandbox.GetShell(userID, sandboxWorkspace)
	if err != nil {
		return nil, fmt.Errorf("failed to get shell: %w", err)
	}

	// 构建登录 shell 命令
	shellCmd := params.Command

	// 审计日志：记录每次 shell 执行
	log.WithFields(log.Fields{
		"command":    params.Command,
		"timeout":    timeout,
		"background": params.Background,
	}).Debug("Shell command executing")

	// Build ExecSpec based on sandbox mode
	buildSpec := func() ExecSpec {
		switch sandbox.Name() {
		case "docker":
			dir := ""
			if toolCtx != nil && toolCtx.CurrentDir != "" {
				dir = toolCtx.CurrentDir
			} else if toolCtx != nil && toolCtx.Sandbox != nil && toolCtx.Sandbox.Name() != "none" {
				dir = toolCtx.Sandbox.Workspace(toolCtx.OriginUserID)
			}
			return ExecSpec{
				Command:   shell,
				Args:      []string{shell, "-l", "-c", shellCmd},
				Shell:     false,
				Dir:       dir,
				Timeout:   timeout,
				Workspace: sandboxWorkspace,
				UserID:    userID,
			}
		case "remote":
			remoteDir := ""
			if toolCtx != nil && toolCtx.CurrentDir != "" {
				remoteDir = toolCtx.CurrentDir
			} else if rs, ok := sandbox.(*RemoteSandbox); ok {
				remoteDir = rs.Workspace(userID)
			}
			return ExecSpec{
				Command: shell,
				Args:    []string{shell, "-l", "-c", shellCmd},
				Shell:   false,
				Dir:     remoteDir,
				Timeout: timeout,
				UserID:  userID,
			}
		default:
			// None sandbox: use platform-aware shell args.
			// Unix: bash -l -c "command" (login shell, loads profile)
			// Windows: powershell.exe -Command "command" (loads profile by default)
			args := LoginShellArgs(shell, shellCmd)
			return ExecSpec{
				Command:   shell,
				Args:      args,
				Shell:     false,
				Dir:       execDir,
				Timeout:   timeout,
				UserID:    userID,
				RunAsUser: params.RunAs,
			}
		}
	}

	// Background mode: launch task in goroutine, return task ID immediately
	if params.Background {
		return t.executeBackground(toolCtx, shellCmd, sandbox, buildSpec)
	}

	// Foreground mode: synchronous execution with auto-promote on timeout
	return t.executeForeground(toolCtx, shellCmd, sandbox, parentCtx, timeout, buildSpec)
}

// executeBackground launches a command as a background task.
func (t *ShellTool) executeBackground(
	toolCtx *ToolContext,
	command string,
	sandbox Sandbox,
	buildSpec func() ExecSpec,
) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported (BgTaskManager not configured)")
	}

	sessionKey := toolCtx.BgSessionKey
	if sessionKey == "" {
		sessionKey = toolCtx.Channel + ":" + toolCtx.ChatID
	}
	senderID := toolCtx.OriginUserID

	task := toolCtx.BgTaskManager.Start(sessionKey, senderID, command,
		func(ctx context.Context, outputBuf func(string)) (int, error) {
			spec := buildSpec()
			spec.Timeout = 0 // no timeout for background
			return sandboxExecAsync(ctx, sandbox, spec, outputBuf)
		},
	)

	result := fmt.Sprintf(
		"Background task started [task_id: %q]\nCommand: %s\n\n"+
			"The task is running in the background. You can continue working.\n"+
			"When it completes, the output will be automatically injected into the conversation.\n"+
			"- Use task_wait (task_id=[%q]) to block until completion, or task_status (task_id=[%q]) to check progress\n"+
			"- Use task_kill (task_id=[%q]) to terminate the task\n"+
			"Note: for multiple tasks, pass all IDs in one array — task_wait(task_id=[\"id1\",\"id2\"], mode=\"any\")",
		task.ID, task.Command, task.ID, task.ID, task.ID,
	)

	return NewResultWithTips(result, fmt.Sprintf("Background task running, use task_wait (task_id=[%q]) to wait for it", task.ID)), nil
}

// executeForeground runs a command synchronously with promote-to-background
// support. The execution itself runs in a streaming goroutine (same core as
// background tasks — sandboxExecAsync); the foreground wait selects on:
//   - completion      → normal tool result
//   - timeout         → auto-promote to a background task (no re-exec, the
//     already-running process is adopted)
//   - user promote    → manual promote (web UI button), same adoption path
//   - tool ctx cancel → kill the process (user stop), return error
//
// The exec context is independent of the tool ctx so a promoted process
// keeps running after the tool call returns.
func (t *ShellTool) executeForeground(
	toolCtx *ToolContext,
	command string,
	sandbox Sandbox,
	parentCtx context.Context,
	timeout time.Duration,
	buildSpec func() ExecSpec,
) (*ToolResult, error) {
	spec := buildSpec()
	// Lifetime is managed by the select below (timeout → promote, not kill);
	// sandboxExecAsync's per-sandbox cores all treat Timeout=0 as unlimited.
	spec.Timeout = 0

	// Shared output buffer: written by the streaming goroutine, snapshotted
	// by the foreground wait and the background task (after promote).
	var outMu sync.Mutex
	var outBuf strings.Builder
	snapshotOutput := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return outBuf.String()
	}

	// Independent execution context: survives promote (the process keeps
	// running under BgTaskManager after this tool call returns). On every
	// non-promote exit path the deferred cancel kills the process group.
	execCtx, cancelExec := context.WithCancel(context.Background())
	promoted := false
	defer func() {
		if !promoted {
			cancelExec()
		}
	}()

	// Adoption handle: created up-front so the streaming output closure can
	// fan output deltas into the background task's SSE push (set on promote).
	execHandle := &RunningExecHandle{
		Output: snapshotOutput,
		Cancel: cancelExec,
	}

	type fgExecResult struct {
		exitCode int
		err      error
	}
	execDone := make(chan fgExecResult, 1)
	execDoneSig := make(chan struct{})
	execHandle.Done = execDoneSig
	execHandle.Result = func() (int, error) {
		r := <-execDone
		return r.exitCode, r.err
	}

	outputBuf := func(s string) {
		if s == "" {
			return
		}
		outMu.Lock()
		outBuf.WriteString(s)
		// Tail-trim at write time: a promoted (background) command can run
		// indefinitely (tail -f / training logs) and the buffer previously only
		// got truncated at task END — unbounded growth while running (CR: 输出
		// 缓冲无上限). Keep the newest maxBgOutputSize bytes, same semantics as
		// the background task output cap.
		if outBuf.Len() > maxBgOutputSize {
			b := []byte(outBuf.String())
			trimmed := string(b[len(b)-maxBgOutputSize:])
			outBuf.Reset()
			outBuf.WriteString(trimmed)
		}
		outMu.Unlock()
		execHandle.fireDelta(s)
	}

	go func() {
		code, err := sandboxExecAsync(execCtx, sandbox, spec, outputBuf)
		execDone <- fgExecResult{exitCode: code, err: err}
		close(execDoneSig)
	}()

	// Registry entry: web promote_shell RPC finds the running shell by
	// (sessionKey, tool call id) and fires the signal. Only registered when
	// a BgTaskManager exists (promote/timeout adoption needs it).
	var mgr *BackgroundTaskManager
	var sessionKey string
	var senderID string
	if toolCtx != nil {
		mgr = toolCtx.BgTaskManager
		sessionKey = toolCtx.BgSessionKey
		if sessionKey == "" {
			sessionKey = toolCtx.Channel + ":" + toolCtx.ChatID
		}
		senderID = toolCtx.OriginUserID
	}
	var fgHandle *ForegroundShellHandle
	var promoteCh <-chan struct{}
	if mgr != nil {
		fgHandle = registerForegroundShell(sessionKey, toolCallIDOf(toolCtx), command)
		defer unregisterForegroundShell(fgHandle)
		promoteCh = fgHandle.promoteCh
	}
	startedAt := time.Now()

	// promote adopts the running execution as a background task and builds
	// the tool result for the LLM. manual=true is the user action, false is
	// the timeout auto-promote.
	promote := func(manual bool) (*ToolResult, error) {
		promoted = true
		task := mgr.AdoptRunning(sessionKey, senderID, command, startedAt, execHandle)
		// Stream subsequent output to the web task panel (bg_task_output SSE).
		taskID := task.ID
		execHandle.SetOnDelta(func(delta string) {
			mgr.fireOutput(sessionKey, taskID, delta)
		})
		if fgHandle != nil {
			if manual {
				notifyPromoteResult(fgHandle, task.ID, nil)
			}
			logPromote(sessionKey, fgHandle.CallID, task.ID, manual)
		}

		output := snapshotOutput()
		var headline, tips string
		if manual {
			headline = fmt.Sprintf(
				"[PROMOTED to background by user] Command moved to the background [task_id: %q]\n"+
					"Partial output so far:\n%s",
				task.ID, output)
		} else {
			headline = fmt.Sprintf(
				"[TIMEOUT after %s] Command timed out. Auto-promoted to background task [task_id: %q]\n"+
					"Partial output before timeout:\n%s",
				timeout, task.ID, output)
		}
		tips = fmt.Sprintf("Promoted to background task, use task_wait (task_id=[%q]) to wait for it", task.ID)
		body := fmt.Sprintf(
			"%s\n\nThe command continues running in the background. Its output will be injected when done.\n"+
				"- Use task_wait (task_id=[%q]) to block until completion, or task_status (task_id=[%q]) to check progress\n"+
				"- Use task_kill (task_id=[%q]) to terminate\n"+
				"Note: for multiple tasks, pass all IDs in one array — task_wait(task_id=[\"id1\",\"id2\"], mode=\"any\")",
			headline, task.ID, task.ID, task.ID)
		return NewResultWithTips(body, tips), nil
	}

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	select {
	case r := <-execDone:
		// Normal completion (or user-stop kill: execCtx cancelled by the
		// tool ctx branch below — that branch waits on execDone itself, so
		// this case is always a natural completion or a stray cancel race
		// where output is still the best we have).
		if r.err != nil && execCtx.Err() != nil {
			// The only cancel source outside promote is the tool ctx branch,
			// which returns before waiting here. A stray cancel (safety
			// sandbox) surfaces as an error — mirror the old sandbox.Exec
			// error semantics.
			return nil, fmt.Errorf("sandbox exec: %w", r.err)
		}
		output := strings.TrimSpace(snapshotOutput())
		if r.err != nil {
			// Execution error (start failure, remote kill, …).
			if output != "" {
				return nil, fmt.Errorf("sandbox exec: %w\n%s", r.err, output)
			}
			return nil, fmt.Errorf("sandbox exec: %w", r.err)
		}
		if r.exitCode != 0 {
			errMsg := fmt.Sprintf("[EXIT %d] %s", r.exitCode, command)
			if output != "" {
				errMsg += "\n" + output
			}
			log.WithFields(log.Fields{
				"command":  command,
				"exitCode": r.exitCode,
			}).Warn("Shell command failed")
			return NewErrorResult(errMsg), nil
		}
		if output == "" {
			return NewResult("Command executed successfully (no output)"), nil
		}
		res := NewResult(output)
		if tip := detectCdTip(command); tip != "" {
			res = res.WithTips(tip)
		}
		return res, nil

	case <-parentCtx.Done():
		// User stop: kill the process group, wait for the kill to settle,
		// then surface the cancel (same semantics as the old sandbox.Exec
		// returning a context error on stop).
		cancelExec()
		<-execDone
		return nil, fmt.Errorf("sandbox exec: %w", parentCtx.Err())

	case <-promoteCh:
		// User promote from the web UI — adopt the running execution.
		return promote(true)

	case <-timeoutTimer.C:
		// Timeout — auto-promote when possible (no re-exec: the process is
		// adopted in place), else the old timeout error.
		if mgr == nil {
			output := snapshotOutput()
			timeoutErr := fmt.Sprintf("[TIMEOUT after %s] Command timed out", timeout)
			if output != "" {
				timeoutErr = fmt.Sprintf("[TIMEOUT after %s] Partial output:\n%s", timeout, output)
			}
			cancelExec()
			<-execDone
			log.WithFields(log.Fields{
				"command": command,
				"timeout": timeout,
				"output":  output,
			}).Warn("Shell command timed out")
			return NewErrorResult(timeoutErr), nil
		}
		return promote(false)
	}
}

// toolCallIDOf returns the tool call id from the context ("" when absent —
// e.g. internal invocations without an LLM tool call).
func toolCallIDOf(toolCtx *ToolContext) string {
	if toolCtx == nil {
		return ""
	}
	return toolCtx.ToolCallID
}

// sandboxExecAsync runs a sandbox command asynchronously, streaming output via outputBuf.
func sandboxExecAsync(
	ctx context.Context,
	sandbox Sandbox,
	spec ExecSpec,
	outputBuf func(string),
) (int, error) {
	switch sandbox.Name() {
	case "none":
		return noneSandboxExecAsync(ctx, spec, outputBuf)
	case "remote":
		return remoteSandboxExecAsync(ctx, sandbox, spec, outputBuf)
	default:
		// Docker: synchronous fallback (timeout=0 means no timeout)
		result, err := sandbox.Exec(ctx, spec)
		if outputBuf != nil && result != nil {
			if result.Stdout != "" {
				outputBuf(result.Stdout)
			}
			if result.Stderr != "" {
				outputBuf("[stderr] " + result.Stderr)
			}
		}
		if err != nil {
			if result != nil {
				return result.ExitCode, err
			}
			return -1, err
		}
		return result.ExitCode, nil
	}
}

// remoteSandboxExecAsync runs a command on a remote runner asynchronously.
// It starts the command via bg_exec protocol, then polls status until completion.
func remoteSandboxExecAsync(
	ctx context.Context,
	sandbox Sandbox,
	spec ExecSpec,
	outputBuf func(string),
) (int, error) {
	rs, ok := sandbox.(*RemoteSandbox)
	if !ok {
		return -1, fmt.Errorf("remote sandbox type assertion failed")
	}

	// Generate a unique task ID for the runner.
	taskID := "remote-" + generateID()

	// Start the background task on the runner.
	if err := rs.ExecBg(ctx, spec, taskID); err != nil {
		return -1, fmt.Errorf("remote bg_exec: %w", err)
	}

	// Poll until the task completes or context is cancelled.
	const pollInterval = 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			// Try to kill the task on the runner before returning.
			killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			rs.KillBg(killCtx, spec.UserID, taskID)
			cancel()
			return -1, ctx.Err()
		case <-time.After(pollInterval):
		}

		status, err := rs.StatusBg(ctx, spec.UserID, taskID)
		if err != nil {
			return -1, fmt.Errorf("remote bg_status: %w", err)
		}

		// Stream any new output.
		if outputBuf != nil {
			if status.Stdout != "" {
				outputBuf(status.Stdout)
			}
			if status.Stderr != "" {
				outputBuf("[stderr] " + status.Stderr)
			}
		}

		switch status.Status {
		case "completed":
			return status.ExitCode, nil
		case "failed":
			return status.ExitCode, fmt.Errorf("remote task failed with exit code %d", status.ExitCode)
		case "killed":
			return -1, fmt.Errorf("remote task was killed")
		case "running":
			// Continue polling.
		default:
			return -1, fmt.Errorf("unknown remote task status: %s", status.Status)
		}
	}
}

// cdPattern detects standalone cd commands (not inside subshells, comments, or strings).
// Matches: "cd foo", "cd /path", "cd ..", "cd ~", as well as "cd foo && ls" etc.
var cdPattern = regexp.MustCompile(`(?:^|&&|\|\||;)\s*cd\s+`)

// detectCdTip returns a tip string if the command contains a cd that won't persist.
func detectCdTip(command string) string {
	if !cdPattern.MatchString(command) {
		return ""
	}
	return `NOTE: "cd" inside Shell only affects this single command — the working directory resets on the next tool call. Use the Cd tool to persistently change directory.`
}
