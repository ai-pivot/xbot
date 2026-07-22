package runnerclient

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"xbot/internal/runnerproto"
)

// ptyProcess represents one interactive PTY running on the runner.
type ptyProcess struct {
	ptmx *os.File  // PTY master (read stdout + write stdin)
	cmd  *exec.Cmd // shell process
	done chan struct{}
}

// ptyManager manages the lifecycle of interactive PTY sessions.
// Mirrors stdioManager but uses a pseudo-terminal instead of pipes,
// enabling full terminal emulation (ANSI, cursor movement, interactive shells).
type ptyManager struct {
	mu    sync.Mutex
	procs map[string]*ptyProcess

	writeCh    chan<- WriteMsg
	writeDone  <-chan struct{}
	verbose    bool
	dockerMode bool
	executor   Executor
	logf       LogFunc
}

func newPtyManager(verbose, dockerMode bool, logf LogFunc) *ptyManager {
	return &ptyManager{
		procs:      make(map[string]*ptyProcess),
		verbose:    verbose,
		dockerMode: dockerMode,
		logf:       logf,
	}
}

// SetWriteChannels sets the write channels (called by ReadLoop at startup).
func (pm *ptyManager) SetWriteChannels(writeCh chan<- WriteMsg, writeDone <-chan struct{}) {
	pm.writeCh = writeCh
	pm.writeDone = writeDone
}

// HandleCreate handles pty_create requests (request/response).
func (pm *ptyManager) HandleCreate(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.PtyCreateRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid pty_create request: "+err.Error())
	}
	if req.StreamID == "" {
		return runnerproto.MakeError(msg.ID, "EINVAL", "stream_id is required")
	}

	pm.mu.Lock()
	if _, exists := pm.procs[req.StreamID]; exists {
		pm.mu.Unlock()
		return runnerproto.MakeError(msg.ID, "EEXIST", "pty stream already exists: "+req.StreamID)
	}
	pm.mu.Unlock()

	cmd, err := pm.buildCmd(req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "build command: "+err.Error())
	}

	// Set initial window size before starting.
	if req.Cols == 0 {
		req.Cols = 80
	}
	if req.Rows == 0 {
		req.Rows = 24
	}
	ws := &pty.Winsize{Cols: req.Cols, Rows: req.Rows}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "start pty: "+err.Error())
	}

	proc := &ptyProcess{
		ptmx: ptmx,
		cmd:  cmd,
		done: make(chan struct{}),
	}

	pm.mu.Lock()
	pm.procs[req.StreamID] = proc
	pm.mu.Unlock()

	// Read PTY output → push pty_data to server.
	go pm.forwardOutput(req.StreamID, proc)

	// Wait for process exit → push pty_exit to server.
	go pm.waitExit(req.StreamID, proc)

	callLogf(pm.logf, "  pty_create stream=%s cmd=%s size=%dx%d", req.StreamID, req.Command, req.Cols, req.Rows)
	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoOK, runnerproto.PtyCreateResponse{StreamID: req.StreamID})
}

// HandleStdin handles pty_stdin requests (fire-and-forget).
func (pm *ptyManager) HandleStdin(msg runnerproto.RunnerMessage) {
	var req runnerproto.PtyStdinRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return
	}

	pm.mu.Lock()
	proc, ok := pm.procs[req.StreamID]
	pm.mu.Unlock()
	if !ok {
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return
	}
	proc.ptmx.Write(data) //nolint:errcheck
}

// HandleResize handles pty_resize requests (fire-and-forget).
func (pm *ptyManager) HandleResize(msg runnerproto.RunnerMessage) {
	var req runnerproto.PtyResizeRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return
	}

	pm.mu.Lock()
	proc, ok := pm.procs[req.StreamID]
	pm.mu.Unlock()
	if !ok {
		return
	}

	ws := &pty.Winsize{Cols: req.Cols, Rows: req.Rows}
	pty.Setsize(proc.ptmx, ws) //nolint:errcheck
}

// HandleClose handles pty_close requests (request/response).
func (pm *ptyManager) HandleClose(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.PtyCloseRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid pty_close request: "+err.Error())
	}

	pm.mu.Lock()
	proc, ok := pm.procs[req.StreamID]
	delete(pm.procs, req.StreamID)
	pm.mu.Unlock()
	if !ok {
		return runnerproto.MakeError(msg.ID, "ENOENT", "pty stream not found: "+req.StreamID)
	}

	// Close PTY master → sends SIGHUP to the foreground process group.
	proc.ptmx.Close()

	// Give the process time to exit gracefully, then kill the group.
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		if proc.cmd.Process != nil {
			killProcessTree(proc.cmd.Process.Pid)
		}
		select {
		case <-proc.done:
		case <-time.After(3 * time.Second):
			proc.cmd.Process.Kill() //nolint:errcheck
		}
	}

	callLogf(pm.logf, "  pty_close stream=%s", req.StreamID)
	return runnerproto.MakeOK(msg.ID)
}

// buildCmd constructs the exec.Cmd for a PTY session.
// In docker mode, the shell runs inside the container via docker exec -it.
func (pm *ptyManager) buildCmd(req runnerproto.PtyCreateRequest) (*exec.Cmd, error) {
	if pm.dockerMode {
		de, ok := pm.executor.(*DockerExecutor)
		if !ok {
			return nil, fmt.Errorf("docker executor not available")
		}

		args := []string{"exec", "-it"}
		dir := req.Dir
		if dir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(de.CtrWorkspace, dir)
		}
		if dir == "" {
			dir = de.CtrWorkspace
		}
		if dir != "" {
			args = append(args, "-w", dir)
		}

		hasPath := false
		for _, e := range req.Env {
			args = append(args, "-e", e)
			if strings.HasPrefix(e, "PATH=") {
				hasPath = true
			}
		}
		if !hasPath {
			args = append(args, "-e", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
		}

		shellCmd := req.Command
		if shellCmd == "" {
			shellCmd = "sh"
		}
		args = append(args, de.ContainerName, shellCmd)
		return exec.Command("docker", args...), nil
	}

	// Native mode: start the shell directly.
	shell := req.Command
	if shell == "" {
		shell = "bash"
	}
	cmd := exec.Command(shell, req.Args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	// Only pass safe environment variables — don't leak runner secrets into PTY.
	env := []string{
		"TERM=xterm-256color",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"SHELL=" + os.Getenv("SHELL"),
		"LANG=" + os.Getenv("LANG"),
	}
	env = append(env, req.Env...)
	cmd.Env = env
	return cmd, nil
}

// forwardOutput reads PTY master and pushes pty_data messages to the server.
func (pm *ptyManager) forwardOutput(streamID string, proc *ptyProcess) {
	defer close(proc.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := proc.ptmx.Read(buf)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			pushMsg := &runnerproto.RunnerMessage{
				Type: runnerproto.ProtoPtyData,
				Body: mustMarshal(runnerproto.PtyDataMessage{
					StreamID: streamID,
					Data:     encoded,
				}),
			}
			data, mErr := json.Marshal(pushMsg)
			if mErr != nil {
				return
			}
			select {
			case pm.writeCh <- WriteMsg{Data: data}:
			case <-pm.writeDone:
				return
			default:
				// Drop frame under extreme backpressure to prevent PTY read freeze.
			}
		}
		if err != nil {
			return
		}
	}
}

// waitExit waits for the shell process to exit, then pushes pty_exit.
func (pm *ptyManager) waitExit(streamID string, proc *ptyProcess) {
	<-proc.done

	exitCode := 0
	errMsg := ""
	if err := proc.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			errMsg = err.Error()
		}
	}

	pushMsg := &runnerproto.RunnerMessage{
		Type: runnerproto.ProtoPtyExit,
		Body: mustMarshal(runnerproto.PtyExitMessage{
			StreamID: streamID,
			ExitCode: exitCode,
			Error:    errMsg,
		}),
	}
	data, mErr := json.Marshal(pushMsg)
	if mErr != nil {
		return
	}
	select {
	case pm.writeCh <- WriteMsg{Data: data}:
	case <-pm.writeDone:
	}

	pm.mu.Lock()
	delete(pm.procs, streamID)
	pm.mu.Unlock()

	callLogf(pm.logf, "  pty_exit stream=%s exit=%d", streamID, exitCode)
}

// Cleanup kills all active PTY sessions (called on session disconnect).
func (pm *ptyManager) Cleanup() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for id, proc := range pm.procs {
		proc.ptmx.Close()
		if proc.cmd.Process != nil {
			killProcessTree(proc.cmd.Process.Pid)
		}
		callLogf(pm.logf, "  pty cleanup stream=%s", id)
	}
	pm.procs = make(map[string]*ptyProcess)
}
