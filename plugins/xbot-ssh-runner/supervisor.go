package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// SSH-pipe supervisor (VS Code Remote model)
//
// The runner is NOT a resident service on the managed machine. Each connection:
//
//	ssh [-R tunnel] host '<kill stale runner>; exec xbot-runner --server … '
//
// i.e. the runner runs in the FOREGROUND of the SSH session, so its lifetime is
// the pipe's lifetime. A per-target supervisor keeps the invariant:
//
//   - exclusive: every (re)connect first kills the previous runner (locally by
//     terminating the ssh child, remotely by pkill on the runner name), so two
//     runners can never race for the same registry slot;
//   - self-healing: if the pipe drops, the supervisor reconnects (which again
//     kills-then-starts);
//   - observable: pipe output is captured for `logs`, and state/restart count
//     for `status`.
//
// Two transport modes (explicit — no silent fallback):
//
//	tunnel (default): ssh -R 127.0.0.1:<remotePort>:127.0.0.1:<serverPort>, and
//	                  the runner dials 127.0.0.1:<remotePort>. The managed
//	                  machine therefore needs NO network path to the server — only
//	                  we need to reach it over SSH (exactly like VS Code Remote).
//	direct:           the runner dials the server URL as given.
// ============================================================================

const (
	// connModeTunnel routes the runner's connection to the server through the SSH
	// session (reverse forward). Default: works behind NAT on both sides.
	connModeTunnel = "tunnel"
	// connModeDirect has the runner dial the server URL itself.
	connModeDirect = "direct"

	// supervisorOutputLines is how much pipe output we retain per target.
	supervisorOutputLines = 400
	// supervisorBackoff* bound the reconnect backoff.
	supervisorBackoffMin = 1 * time.Second
	supervisorBackoffMax = 15 * time.Second
	// portProbeRangeSize is how many candidate remote ports we probe per target.
	portProbeRangeSize = 64
	// portRangeBase/Size pick the remote listen-port window used for tunnels.
	portRangeBase = 39000
	portRangeSize = 900
)

// supervisorStatus is the observable state of one target's pipe.
type supervisorStatus struct {
	Connected   bool   `json:"connected"`
	Mode        string `json:"mode"`
	RemotePort  int    `json:"remote_port,omitempty"`
	Restarts    int    `json:"restarts"`
	ConnectedAt string `json:"connected_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// targetSpec is everything needed to (re)establish one pipe.
type targetSpec struct {
	SSHField    string // user-supplied ssh prefix, e.g. "ssh user@host -p 2222"
	Name        string // runner name (registry identity + kill pattern)
	ConnectCmd  string // "--server ws://… --token … --name …" produced by the server
	InstallDir  string // where xbot-runner lives on the remote
	ConnMode    string // connModeTunnel | connModeDirect
	ServerHost  string // parsed from ConnectCmd (host:port of our server)
	ServerPort  int
	ServerPath  string // e.g. "/ws"
	ServerQuery string
}

// spawnFunc starts one long-lived ssh session and returns its combined output
// stream plus a wait function. Injectable so the supervisor can be tested
// without spawning a real ssh process.
type spawnFunc func(ctx context.Context, argv []string) (io.ReadCloser, func() error, error)

// defaultSpawn runs the real ssh process: one stream (stderr folded into
// stdout) because the pipe IS the runner's output.
func defaultSpawn(ctx context.Context, argv []string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, cmd.Wait, nil
}

// supervisor owns one target's pipe and keeps it alive.
type supervisor struct {
	spec  targetSpec
	exec  execFunc  // one-shot remote executor (port probe, remote pkill)
	spawn spawnFunc // long-lived ssh session starter

	mu          sync.Mutex
	want        bool // should the pipe be up?
	cancel      context.CancelFunc
	connected   bool
	remotePort  int
	restarts    int
	connectedAt time.Time
	lastErr     string
	output      []string // ring of recent pipe output lines
}

// supervisorManager holds one supervisor per runner name.
type supervisorManager struct {
	mu    sync.Mutex
	sups  map[string]*supervisor
	exec  execFunc  // remote one-shot executor (probes, pkill, install steps)
	spawn spawnFunc // long-lived ssh session starter (defaults to a real ssh)
}

func newSupervisorManager(exec execFunc) *supervisorManager {
	return &supervisorManager{sups: map[string]*supervisor{}, exec: exec, spawn: defaultSpawn}
}

func (m *supervisorManager) get(name string) (*supervisor, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sups[name]
	return s, ok
}

func (m *supervisorManager) put(s *supervisor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sups[s.spec.Name] = s
}

func (m *supervisorManager) remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sups, name)
}

// StopAll tears down every supervised pipe. Used when the host deactivates us:
// killing the local ssh children is what actually ends the remote runners.
func (m *supervisorManager) StopAll(ctx context.Context) {
	m.mu.Lock()
	names := make([]string, 0, len(m.sups))
	for n := range m.sups {
		names = append(names, n)
	}
	m.mu.Unlock()
	sort.Strings(names)
	for _, n := range names {
		m.Stop(ctx, n)
	}
}

// StatusOf reports the pipe state for a target (zero value when never connected).
func (m *supervisorManager) StatusOf(name string) supervisorStatus {
	s, ok := m.get(name)
	if !ok {
		return supervisorStatus{}
	}
	return s.Status()
}

// Connect (re)establishes the pipe for a target, kill-old-first.
//
// It returns as soon as the supervisor is armed: the SSH session itself is
// long-lived, and blocking an RPC on it would exceed the 30s plugin call
// timeout (which also kills this process — see plugin/runtime.go).
func (m *supervisorManager) Connect(ctx context.Context, spec targetSpec) (supervisorStatus, error) {
	if err := validateTargetName(spec.Name); err != nil {
		return supervisorStatus{}, err
	}
	if strings.TrimSpace(spec.SSHField) == "" {
		return supervisorStatus{}, errors.New("ssh command is required")
	}
	if spec.ConnMode == "" {
		spec.ConnMode = connModeTunnel
	}
	if spec.ConnMode != connModeTunnel && spec.ConnMode != connModeDirect {
		return supervisorStatus{}, fmt.Errorf("invalid connection mode %q (want %s or %s)",
			spec.ConnMode, connModeTunnel, connModeDirect)
	}
	// 解析 --server：**host 可空、端口必需**（见 parseServerFromConnectCmd 的注释）。
	// 隧道模式下 host 与铸命令无关（runner 连的是 ssh -R 暴露在远端的
	// 127.0.0.1:<remotePort>，我们只需要**转发目标端口** ServerPort）；直连模式才要求
	// host 真实可达。
	host, port, path, query, perr := parseServerFromConnectCmd(spec.ConnectCmd)
	if perr != nil {
		return supervisorStatus{}, perr
	}
	if spec.ConnMode == connModeTunnel {
		spec.ServerHost, spec.ServerPort, spec.ServerPath, spec.ServerQuery = "127.0.0.1", port, path, query
	} else if host == "" {
		return supervisorStatus{}, errors.New(
			"direct mode needs a reachable --server host (set sandbox.public_url, or use tunnel mode)")
	}

	// Stop any existing pipe for this name first: the invariant is that at most
	// one runner per name exists, and a reconnect must replace it.
	m.Stop(ctx, spec.Name)

	s := &supervisor{spec: spec, want: true, exec: m.exec, spawn: m.spawn}
	if s.spawn == nil {
		s.spawn = defaultSpawn
	}
	m.put(s)
	runCtx, cancel := context.WithCancel(context.Background()) // survives the RPC
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	go s.loop(runCtx)
	return s.Status(), nil
}

// Stop tears the pipe down: cancel the supervisor and kill the remote runner.
func (m *supervisorManager) Stop(ctx context.Context, name string) {
	s, ok := m.get(name)
	if !ok {
		// No supervisor in this process, but a runner may survive from a previous
		// one (e.g. after a plugin restart) — still kill it remotely.
		if killCtx, cancel := context.WithTimeout(ctx, 20*time.Second); cancel != nil {
			defer cancel()
			if sshField := m.sshFieldHint(name); sshField != "" {
				_, _ = m.exec(killCtx, sshField, 15*time.Second, killRunnerScript(name))
			}
		}
		return
	}
	s.mu.Lock()
	s.want = false
	cancel := s.cancel
	sshField := s.spec.SSHField
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Remote kill: the local ssh child dying does not guarantee the remote
	// process is gone (it may have been started outside this process).
	killCtx, cancelKill := context.WithTimeout(ctx, 20*time.Second)
	defer cancelKill()
	_, _ = m.exec(killCtx, sshField, 15*time.Second, killRunnerScript(name))
	m.remove(name)
}

// sshFieldHint remembers an ssh prefix per target so Stop can still kill a
// runner left behind by a previous plugin process. Cleared with the supervisor.
var sshFieldHints sync.Map // name → ssh field

func (m *supervisorManager) sshFieldHint(name string) string {
	if v, ok := sshFieldHints.Load(name); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return ""
}

// killRunnerScript kills any process whose command line matches this runner.
//
// The pattern is anchored on the runner's --name value (validated to a safe
// character set), so it cannot match unrelated processes.
func killRunnerScript(name string) string {
	return fmt.Sprintf(
		"pkill -f %s >/dev/null 2>&1; sleep 0.3; pkill -9 -f %s >/dev/null 2>&1; exit 0",
		shellQuote(xbotRunnerKillPattern(name)),
		shellQuote(xbotRunnerKillPattern(name)),
	)
}

// xbotRunnerKillPattern matches only xbot-runner processes bound to this name.
func xbotRunnerKillPattern(name string) string {
	return fmt.Sprintf("xbot-runner.*--name[= ]%s([[:space:]]|$)", regexp.QuoteMeta(name))
}

// loop keeps the pipe up until cancelled.
func (s *supervisor) loop(ctx context.Context) {
	backoff := supervisorBackoffMin
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return // stopped on purpose
		}

		s.mu.Lock()
		s.connected = false
		s.restarts++
		if err != nil {
			s.lastErr = err.Error()
		}
		s.mu.Unlock()

		// Reconnect (this re-runs kill-old-then-start inside runOnce).
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < supervisorBackoffMax {
			backoff *= 2
			if backoff > supervisorBackoffMax {
				backoff = supervisorBackoffMax
			}
		}
	}
}

// runOnce establishes one SSH session and blocks until it ends.
func (s *supervisor) runOnce(ctx context.Context) error {
	remotePort := 0
	if s.spec.ConnMode == connModeTunnel {
		port, err := s.pickRemotePort(ctx)
		if err != nil {
			return fmt.Errorf("pick remote port: %w", err)
		}
		remotePort = port
		s.mu.Lock()
		s.remotePort = port
		s.mu.Unlock()
	}

	argv, _, err := s.buildPipeCommand(remotePort)
	if err != nil {
		return err
	}

	out, wait, err := s.spawn(ctx, argv)
	if err != nil {
		return fmt.Errorf("start ssh session: %w", err)
	}
	s.mu.Lock()
	s.connected = true
	s.connectedAt = time.Now()
	s.lastErr = ""
	s.mu.Unlock()
	sshFieldHints.Store(s.spec.Name, s.spec.SSHField)

	go s.drain(out)

	err = wait()
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ssh session ended: %w", err)
	}
	return errors.New("ssh session ended (runner exited)")
}

// buildPipeCommand is the pure part of a (re)connect: it returns the ssh argv
// and the remote script.
//
// remotePort is only meaningful in tunnel mode (the free port we forward on the
// remote side); it is ignored in direct mode.
func (s *supervisor) buildPipeCommand(remotePort int) ([]string, string, error) {
	serverArg := connectCmdServerValue(s.spec.ConnectCmd)
	forward := ""
	if s.spec.ConnMode == connModeTunnel {
		if remotePort <= 0 || s.spec.ServerPort <= 0 {
			return nil, "", errors.New("tunnel mode needs both a remote port and the server port")
		}
		forward = fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", remotePort, s.spec.ServerPort)
		serverArg = fmt.Sprintf("ws://127.0.0.1:%d%s%s", remotePort, s.spec.ServerPath, s.spec.ServerQuery)
	}

	script := s.buildRemoteScript(serverArg)
	argv, err := buildSSHArgv(s.spec.SSHField, script)
	if err != nil {
		return nil, "", err
	}
	if forward != "" {
		// Insert the reverse forward as plain ssh options (before the user's
		// args/host), exactly like the injected defaults.
		//
		// ExitOnForwardFailure is essential: without it ssh only *warns* when the
		// remote port is taken and carries on WITHOUT the forward — the runner
		// would then dial a port where nothing (or something else) listens. With
		// it, the session fails loudly, the supervisor backs off and retries, and
		// the retry re-probes a free port.
		argv = append([]string{argv[0], "-o", "ExitOnForwardFailure=yes", "-R", forward}, argv[1:]...)
	}
	return argv, script, nil
}

// buildRemoteScript kills any previous runner and then runs the runner in the
// foreground so the pipe owns its lifetime.
//
// It preflights the binary first: without this, a missing/uninstalled runner
// would surface as an opaque "exit status 127" from a retry loop instead of an
// actionable message in status.last_error.
func (s *supervisor) buildRemoteScript(serverArg string) string {
	bin := strings.TrimSuffix(strings.TrimSpace(s.spec.InstallDir)+"/xbot-runner", "/")
	arg := strings.TrimSpace(s.spec.ConnectCmd)
	// Replace the --server value with the (possibly tunnelled) endpoint.
	arg = replaceConnectCmdServer(arg, serverArg)
	quotedBin := shellQuote(bin)
	preflight := fmt.Sprintf(
		`if [ ! -x %s ]; then echo "xbot-runner not found at %s — run provision first" >&2; exit 1; fi`,
		quotedBin, bin)
	return fmt.Sprintf("%s; %s; exec %s %s",
		preflight,
		strings.TrimSuffix(killRunnerScript(s.spec.Name), "; exit 0"),
		quotedBin, arg)
}

// pickRemotePort finds a free TCP port on the remote inside this target's window.
func (s *supervisor) pickRemotePort(ctx context.Context) (int, error) {
	base := portRangeBase + int(hash32(s.spec.Name))%portRangeSize
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := s.execProbe(probeCtx, fmt.Sprintf(
		"for p in $(seq %d %d); do if command -v ss >/dev/null 2>&1; then "+
			"ss -ltn 2>/dev/null | grep -q \"[:.]$p \" || { echo $p; exit 0; }; "+
			"else netstat -an 2>/dev/null | grep -q \"[:.]$p \" || { echo $p; exit 0; }; fi; done; exit 1",
		base, base+portProbeRangeSize-1))
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(firstLine(out)))
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("no free remote port in %d..%d", base, base+portProbeRangeSize-1)
	}
	return port, nil
}

// execProbe runs a read-only remote command (shares the service's ssh executor).
func (s *supervisor) execProbe(ctx context.Context, script string) (string, error) {
	if s.exec == nil {
		return "", errors.New("supervisor: remote executor not configured")
	}
	return s.exec(ctx, s.spec.SSHField, 20*time.Second, script)
}

// drain captures pipe output into a bounded ring.
func (s *supervisor) drain(r io.Reader) {
	buf := make([]byte, 4096)
	var partial string
	for {
		n, err := r.Read(buf)
		if n > 0 {
			partial += string(buf[:n])
			for {
				idx := strings.IndexByte(partial, '\n')
				if idx < 0 {
					break
				}
				line := strings.TrimRight(partial[:idx], "\r")
				partial = partial[idx+1:]
				s.appendLine(line)
			}
			if len(partial) > 8192 { // never grow unbounded on a \n-less stream
				s.appendLine(partial)
				partial = ""
			}
		}
		if err != nil {
			if strings.TrimSpace(partial) != "" {
				s.appendLine(partial)
			}
			return
		}
	}
}

func (s *supervisor) appendLine(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output = append(s.output, line)
	if len(s.output) > supervisorOutputLines {
		s.output = s.output[len(s.output)-supervisorOutputLines:]
	}
}

// Status snapshots the supervisor state.
func (s *supervisor) Status() supervisorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := supervisorStatus{
		Connected:  s.connected,
		Mode:       s.spec.ConnMode,
		RemotePort: s.remotePort,
		Restarts:   s.restarts,
		LastError:  s.lastErr,
	}
	if !s.connectedAt.IsZero() {
		st.ConnectedAt = s.connectedAt.UTC().Format(time.RFC3339)
	}
	return st
}

// Tail returns the last n captured lines.
func (s *supervisor) Tail(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.output) {
		n = len(s.output)
	}
	out := make([]string, n)
	copy(out, s.output[len(s.output)-n:])
	return out
}

// ============================================================================
// Helpers
// ============================================================================

// parseServerFromConnectCmd extracts the server endpoint from
// "--server ws://host:port/path?query …".
//
// ⛔ **host 允许为空**（2026-09-18 用户实机 P1）。默认的**隧道模式**下，runner 连的是
// `ssh -R` 暴露在**远端**的 `127.0.0.1:<remotePort>`（见 buildPipeCommand），铸命令里的
// host 只是服务端 `server.host`（常为空 = 绑定所有网卡、无需任何公网入口）——要求它存在
// 会把默认隧道模式直接卡死（报 `--server "ws://:8089/ws" must include host:port`）。
//
// **端口是必需的**：隧道用它做 `-R …:127.0.0.1:<ServerPort>` 的转发目标，直连用它拨号。
// 直连模式对 host 的要求由调用方显式校验（那里 host 才真的必须可达）。
func parseServerFromConnectCmd(connectCmd string) (host string, port int, path, query string, err error) {
	raw := connectCmdServerValue(connectCmd)
	if raw == "" {
		return "", 0, "", "", errors.New("connect command has no --server value")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("parse --server %q: %w", raw, err)
	}
	host = u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		return "", 0, "", "", fmt.Errorf("--server %q must include a port", raw)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return "", 0, "", "", fmt.Errorf("--server %q has an invalid port", raw)
	}
	path = u.Path
	if path == "" {
		path = "/ws"
	}
	if u.RawQuery != "" {
		query = "?" + u.RawQuery
	}
	return host, port, path, query, nil
}

// connectCmdServerValue returns the raw value following --server.
func connectCmdServerValue(connectCmd string) string {
	fields := strings.Fields(connectCmd)
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == "--server" && i+1 < len(fields):
			return fields[i+1]
		case strings.HasPrefix(fields[i], "--server="):
			return strings.TrimPrefix(fields[i], "--server=")
		}
	}
	return ""
}

// replaceConnectCmdServer swaps the --server value, preserving the rest.
func replaceConnectCmdServer(connectCmd, serverArg string) string {
	fields := strings.Fields(connectCmd)
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == "--server" && i+1 < len(fields):
			fields[i+1] = serverArg
			return strings.Join(fields, " ")
		case strings.HasPrefix(fields[i], "--server="):
			fields[i] = "--server=" + serverArg
			return strings.Join(fields, " ")
		}
	}
	// No --server (should not happen: the server always produces one).
	return strings.TrimSpace(connectCmd + " --server " + serverArg)
}

// hash32 gives a stable per-name port window (FNV-1a via sha256 prefix).
func hash32(s string) uint32 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint32(sum[:4])
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// sortedRunnerNames is a small helper used by tests and diagnostics.
func sortedRunnerNames(m map[string]*supervisor) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
