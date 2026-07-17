package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/internal/cmdbuilder"

	log "xbot/logger"
)

// NoneSandbox implements Sandbox with direct os.* calls (no containerization).
type NoneSandbox struct{}

// maxNoneDownloadSize is the maximum download size for NoneSandbox (100MB).
const maxNoneDownloadSize = 100 * 1024 * 1024

// noneDownloadHTTPClient is a dedicated HTTP client for NoneSandbox downloads.
var noneDownloadHTTPClient = &http.Client{Timeout: 0} // use context timeout

func (s *NoneSandbox) Name() string              { return "none" }
func (s *NoneSandbox) Workspace(_ string) string { return "" }

func (s *NoneSandbox) Close() error                        { return nil }
func (s *NoneSandbox) CloseForUser(userID string) error    { return nil }
func (s *NoneSandbox) IsExporting(userID string) bool      { return false }
func (s *NoneSandbox) ExportAndImport(userID string) error { return nil }

func (s *NoneSandbox) GetShell(userID string, workspace string) (string, error) {
	return defaultShell(), nil
}

func (s *NoneSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	// Apply timeout to context before creating the command (avoid duplicate cmd creation).
	if spec.Timeout > 0 && !spec.KeepAlive {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	// KeepAlive uses unmanaged cmd (exec.Command) so context cancel doesn't kill the process.
	cmd, err := buildCmdFromSpec(ctx, spec, !spec.KeepAlive)
	if err != nil {
		return nil, err
	}

	// Always use process group so we can kill the entire tree on cancel.
	setProcessAttrs(cmd)

	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	} else {
		// Ensure stdin is never nil — prevents commands (e.g. sudo) from
		// opening /dev/tty and blocking the terminal in none-sandbox mode.
		// In docker/remote sandboxes the process is isolated so this isn't needed.
		cmd.Stdin = bytes.NewReader(nil)
	}

	// KeepAlive mode: use pipes so we can detach on timeout without killing the process.
	if spec.KeepAlive {
		return s.execKeepAlive(ctx, cmd, spec.Timeout)
	}

	// Use pipe-based execution (same pattern as execKeepAlive) to avoid cmd.Run()
	// blocking on pipe EOF when child processes inherit FDs. cmd.Run() uses cmd.Wait()
	// internally, which blocks until all IO copying completes. If a child process
	// forks background processes that inherit stdout/stderr pipe FDs, io.Copy never
	// gets EOF and cmd.Run() hangs forever — even after context timeout kills the
	// main process. Using cmd.Process.Wait() + separate pipe capture goroutines
	// avoids this: we wait only for the direct child, then close pipes to unblock
	// the capture goroutines.
	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		return nil, err
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(&stdoutBuf, stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		io.Copy(&stderrBuf, stderrPipe)
	}()

	// Wait for the direct child process to exit.
	// Use cmd.Process.Wait() (not cmd.Wait()) to avoid blocking on IO completion.
	state, waitErr := cmd.Process.Wait()
	// Kill the process group first to release pipe FDs held by background
	// children, then let capture goroutines drain already-written output.
	killProcessGroup(cmd.Process)
	waitForPipeCapture(&wg, stdoutPipe, stderrPipe)

	result := &ExecResult{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	if waitErr == nil && state != nil {
		result.ExitCode = extractExitCodeFromState(state)
	} else if waitErr != nil {
		result.ExitCode = -1
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	} else if waitErr != nil && result.ExitCode == -1 && ctx.Err() == nil {
		// Non-timeout, non-exit-code error (e.g. signal kill from process group)
		return nil, waitErr
	}

	return result, nil
}

func waitForPipeCapture(wg *sync.WaitGroup, stdoutPipe, stderrPipe io.Closer) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		stdoutPipe.Close()
		stderrPipe.Close()
		<-done
	}
}

// execKeepAlive runs a command with streaming output via pipes.
// On timeout, the process is NOT killed — it continues running and
// the caller takes ownership via ExecResult.Process.
func (s *NoneSandbox) execKeepAlive(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (*ExecResult, error) {
	// Setpgid so we can kill the process group independently
	setProcessAttrs(cmd)

	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		return nil, err
	}

	// Collect output from pipes
	var stdoutBuf, stderrBuf bytes.Buffer
	var pipesClosed bool   // guards against double pipe close
	var pipesMu sync.Mutex // protects pipesClosed
	var wg sync.WaitGroup
	wg.Add(2)

	closePipes := func() {
		pipesMu.Lock()
		defer pipesMu.Unlock()
		if pipesClosed {
			return
		}
		pipesClosed = true
		stdoutPipe.Close()
		stderrPipe.Close()
	}

	capture := func(dst *bytes.Buffer, r io.Reader) {
		defer wg.Done()
		if _, err := io.Copy(dst, r); err != nil {
			log.Req(ctx, log.CatTool).WithError(err).Debug("sandbox: stdout/stderr capture incomplete")
		}
	}
	go capture(&stdoutBuf, stdoutPipe)
	go capture(&stderrBuf, stderrPipe)

	// Wait for the command to finish or timeout/cancel.
	// NOTE: Use cmd.Process.Wait() instead of cmd.Wait() because cmd.Wait()
	// blocks until all IO copying completes. If child processes (e.g. from
	// login shell profile sourcing) inherit pipe FDs, io.Copy never gets EOF
	// and cmd.Wait() hangs forever. cmd.Process.Wait() only waits for the
	// direct child process to exit, then we explicitly close pipes to
	// unblock the IO goroutines.
	waitCh := make(chan int, 1)
	go func() {
		state, err := cmd.Process.Wait()
		code := -1
		if err == nil && state != nil {
			code = extractExitCodeFromState(state)
		}
		// Close pipes to unblock IO goroutines (even if grandchildren hold FDs)
		closePipes()
		wg.Wait()
		waitCh <- code
	}()

	// Build cancellation channel from context
	cancelCh := ctx.Done()

	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case exitCode := <-waitCh:
			// Command finished before timeout
			result := &ExecResult{
				Stdout:   stdoutBuf.String(),
				Stderr:   stderrBuf.String(),
				ExitCode: exitCode,
			}
			return result, nil

		case <-timer.C:
			// Timeout — do NOT kill the process. Return it to the caller.
			// The background goroutine is still running (cmd.Process.Wait()).
			// Capture goroutines continue writing to stdoutBuf/stderrBuf.
			// OngoingOutput lets the caller (Adopt) read the final full output
			// once the process exits and all capture goroutines complete.
			exitCodeCh := make(chan int, 1)
			go func() {
				exitCodeCh <- <-waitCh
			}()
			ongoingOutput := func() string {
				wg.Wait() // ensure capture goroutines have finished writing
				var sb strings.Builder
				if stdoutBuf.Len() > 0 {
					sb.Write(stdoutBuf.Bytes())
				}
				if stderrBuf.Len() > 0 {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.Write(stderrBuf.Bytes())
				}
				return sb.String()
			}
			result := &ExecResult{
				Stdout:        stdoutBuf.String(),
				Stderr:        stderrBuf.String(),
				ExitCode:      -1,
				TimedOut:      true,
				Process:       cmd.Process,
				ExitCodeCh:    exitCodeCh,
				OngoingOutput: ongoingOutput,
			}
			return result, nil

		case <-cancelCh:
			// Context canceled (e.g. Ctrl+C) — kill entire process group immediately
			killProcessGroup(cmd.Process)
			closePipes()
			exitCode := <-waitCh
			result := &ExecResult{
				Stdout:   stdoutBuf.String(),
				Stderr:   stderrBuf.String(),
				ExitCode: exitCode,
			}
			return result, nil
		}
	}

	// No timeout — wait for completion or context cancel
	select {
	case exitCode := <-waitCh:
		result := &ExecResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
		}
		return result, nil

	case <-cancelCh:
		// Context canceled (e.g. Ctrl+C) — kill entire process group immediately
		killProcessGroup(cmd.Process)
		closePipes()
		exitCode := <-waitCh
		result := &ExecResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
		}
		return result, nil
	}
}

// killProcessGroup sends a kill signal to the entire process tree.
// This is a legacy wrapper — callers should use killProcessTree directly.
func killProcessGroup(proc *os.Process) {
	killProcessTree(proc)
}

func (s *NoneSandbox) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxSandboxFileSize {
		return nil, fmt.Errorf("file exceeds maximum size of %d bytes (actual: %d)", MaxSandboxFileSize, info.Size())
	}
	return os.ReadFile(path)
}

func (s *NoneSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	if int64(len(data)) > MaxSandboxFileSize {
		return fmt.Errorf("data exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	return os.WriteFile(path, data, perm)
}

func (s *NoneSandbox) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &SandboxFileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (s *NoneSandbox) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]DirEntry, len(entries))
	for i, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		result[i] = DirEntry{
			Name:  e.Name(),
			IsDir: info.IsDir(),
			Size:  info.Size(),
		}
	}
	return result, nil
}

func (s *NoneSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	return os.MkdirAll(path, perm)
}

func (s *NoneSandbox) Remove(ctx context.Context, path string, userID string) error {
	return os.Remove(path)
}

func (s *NoneSandbox) RemoveAll(ctx context.Context, path string, userID string) error {
	return os.RemoveAll(path)
}

func (s *NoneSandbox) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := noneDownloadHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, io.LimitReader(resp.Body, maxNoneDownloadSize))
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if written >= maxNoneDownloadSize {
		return fmt.Errorf("downloaded file exceeds maximum size (%d bytes)", maxNoneDownloadSize)
	}

	log.Req(ctx, log.CatTool).WithFields(log.Fields{"url": url, "output_path": outputPath, "size": written}).Info("File downloaded (none sandbox)")
	return nil
}

// noneSandboxExecAsync runs a command asynchronously with streaming output.
// Uses Setpgid to ensure all child processes are killed on context cancel.
func noneSandboxExecAsync(ctx context.Context, spec ExecSpec, outputBuf func(string)) (int, error) {
	cmd, err := buildCmdFromSpec(ctx, spec, true)
	if err != nil {
		return -1, err
	}
	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	} else {
		cmd.Stdin = bytes.NewReader(nil)
	}

	// Setpgid: create new process group so kill kills all children
	setProcessAttrs(cmd)

	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		return -1, err
	}

	// Stream stdout and stderr concurrently
	var wg sync.WaitGroup
	wg.Add(2)

	stream := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 && outputBuf != nil {
				outputBuf(string(buf[:n]))
			}
			if readErr != nil {
				return
			}
		}
	}

	go stream(stdoutPipe)
	go stream(stderrPipe)

	// Watch for context cancellation: exec.CommandContext only kills the direct
	// child process (the shell), not its entire process tree. When the context
	// is cancelled (e.g. by task_kill), we must kill the whole process group
	// that setProcessAttrs created, so all child processes terminate.
	var killOnce sync.Once
	go func() {
		<-ctx.Done()
		killOnce.Do(func() {
			if cmd.Process != nil {
				killProcessTree(cmd.Process)
			}
		})
	}()

	// Wait for process exit. Use cmd.Process.Wait() instead of cmd.Wait()
	// because cmd.Wait() blocks until all IO copying completes. If login shell
	// profile sourcing spawns background children that inherit pipe FDs,
	// io.Copy never gets EOF and cmd.Wait() hangs forever.
	state, _ := cmd.Process.Wait()
	// Wait for stream goroutines to finish reading before closing pipes
	wg.Wait()
	stdoutPipe.Close()
	stderrPipe.Close()

	exitCode := extractExitCodeFromState(state)

	if ctx.Err() != nil {
		return exitCode, ctx.Err()
	}
	return exitCode, nil
}

// --- Shared helpers for command execution ---

// buildCmdFromSpec creates an *exec.Cmd from an ExecSpec.
// If managedCtx is true, uses exec.CommandContext (context cancel kills the process).
// If false, uses exec.Command (caller manages process lifecycle manually, e.g. KeepAlive).
func buildCmdFromSpec(ctx context.Context, spec ExecSpec, managedCtx bool) (*exec.Cmd, error) {
	// When managedCtx=false, pass nil context to get exec.Command (no context-based kill)
	buildCtx := ctx
	if !managedCtx {
		buildCtx = nil
	}
	cmd, err := cmdbuilder.Build(buildCtx, spec.Shell, spec.Command, spec.Args,
		spec.Dir, spec.Env, cmdbuilder.Config{RunAsUser: spec.RunAsUser})
	if err != nil {
		return nil, err
	}
	return cmd, nil
}

// setupPipes creates stdout and stderr pipes for a command, then starts it.
func setupPipes(cmd *exec.Cmd) (stdoutPipe, stderrPipe io.ReadCloser, err error) {
	stdoutPipe, err = cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err = cmd.StderrPipe()
	if err != nil {
		stdoutPipe.Close()
		return nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	return stdoutPipe, stderrPipe, cmd.Start()
}

// extractExitCodeFromState returns the exit code from an os.ProcessState.
func extractExitCodeFromState(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if state.Success() {
		return 0
	}
	return state.ExitCode()
}
