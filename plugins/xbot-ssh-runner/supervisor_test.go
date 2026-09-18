package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// Fakes
// ============================================================================

type fakeRemote struct {
	mu        sync.Mutex
	scripts   []string
	probePort int
}

func newFakeRemote(probePort int) *fakeRemote {
	return &fakeRemote{probePort: probePort}
}

func (f *fakeRemote) exec(_ context.Context, _ string, _ time.Duration, script string) (string, error) {
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	port := f.probePort
	f.mu.Unlock()
	// Port probe: the only remote command whose output we consume.
	if strings.Contains(script, "seq ") {
		return fmt.Sprintf("%d\n", port), nil
	}
	return "", nil
}

func (f *fakeRemote) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.scripts {
		n += strings.Count(s, substr)
	}
	return n
}

// blockingSpawn records the argv of every session and keeps it "up" until the
// context is cancelled (i.e. until the supervisor is stopped or restarted).
func blockingSpawn(started chan []string) spawnFunc {
	return func(ctx context.Context, argv []string) (io.ReadCloser, func() error, error) {
		pr, pw := io.Pipe()
		select {
		case started <- argv:
		default:
		}
		go func() {
			<-ctx.Done()
			_ = pw.Close()
		}()
		return pr, func() error { <-ctx.Done(); return ctx.Err() }, nil
	}
}

func tunnelSpec() targetSpec {
	return targetSpec{
		SSHField:   "ssh dev@10.0.0.9 -p 2222",
		Name:       "m1",
		ConnectCmd: "--server ws://xbot.example:8082/ws --token s3cr3t --name m1",
		InstallDir: "/home/dev/.local/bin",
		ConnMode:   connModeTunnel,
		ServerPort: 8082,
		ServerPath: "/ws",
	}
}

// ============================================================================
// Pipe command construction
// ============================================================================

// Tunnel mode: the managed machine must NOT need a route to our server, so the
// runner is pointed at a local port that the SSH session forwards back to us.
func TestBuildPipeCommand_TunnelForwardsAndRewritesServer(t *testing.T) {
	s := &supervisor{spec: tunnelSpec()}
	argv, script, err := s.buildPipeCommand(39042)
	if err != nil {
		t.Fatalf("buildPipeCommand: %v", err)
	}

	if !containsSeq(argv, "-R") || !containsSeq(argv, "127.0.0.1:39042:127.0.0.1:8082") {
		t.Fatalf("reverse forward missing from argv: %v", argv)
	}
	if !strings.Contains(script, "--server ws://127.0.0.1:39042/ws") {
		t.Fatalf("runner must dial the tunnel endpoint, got: %s", script)
	}
	if strings.Contains(script, "xbot.example:8082") {
		t.Fatalf("the original server address must be replaced in tunnel mode: %s", script)
	}
	if !strings.Contains(script, "--token s3cr3t") {
		t.Fatalf("token must be preserved verbatim: %s", script)
	}
	// The pipe must own the runner: foreground exec, no nohup/&.
	if !strings.Contains(script, "exec ") || strings.Contains(script, "nohup") {
		t.Fatalf("runner must run in the foreground of the pipe: %s", script)
	}
}

// Direct mode is explicit: no forward, the runner dials the server itself.
func TestBuildPipeCommand_DirectKeepsServerAddress(t *testing.T) {
	spec := tunnelSpec()
	spec.ConnMode = connModeDirect
	s := &supervisor{spec: spec}
	argv, script, err := s.buildPipeCommand(0)
	if err != nil {
		t.Fatalf("buildPipeCommand: %v", err)
	}
	if containsSeq(argv, "-R") {
		t.Fatalf("direct mode must not set up a tunnel: %v", argv)
	}
	if !strings.Contains(script, "--server ws://xbot.example:8082/ws") {
		t.Fatalf("direct mode must keep the server address: %s", script)
	}
}

// Every (re)connect kills the previous runner BEFORE starting a new one.
func TestBuildPipeCommand_KillsOldRunnerFirst(t *testing.T) {
	s := &supervisor{spec: tunnelSpec()}
	_, script, _ := s.buildPipeCommand(39042)
	kill := strings.Index(script, "pkill")
	execAt := strings.Index(script, "exec ")
	if kill < 0 || execAt < 0 || kill > execAt {
		t.Fatalf("kill-old must precede start: %s", script)
	}
}

// The kill pattern must match this runner and only this runner: "m1" must not
// take out "m10" or an unrelated xbot-runner.
func TestXbotRunnerKillPattern_IsNameExact(t *testing.T) {
	pat := xbotRunnerKillPattern("m1")
	if !strings.Contains(pat, "--name[= ]m1([[:space:]]|$)") {
		t.Fatalf("pattern must anchor the name: %q", pat)
	}
	// A regex-metachar name must be escaped so it cannot match broadly.
	pat = xbotRunnerKillPattern("a.b+c")
	if strings.Contains(pat, "a.b+c") && !strings.Contains(pat, `a\.b\+c`) {
		t.Fatalf("regex metacharacters must be escaped: %q", pat)
	}
}

// ============================================================================
// Supervisor lifecycle
// ============================================================================

// Connect is exclusive and idempotent: a second Connect for the same name
// replaces the previous pipe (and kills the previous runner remotely).
func TestConnect_IsExclusiveAndKillsPrevious(t *testing.T) {
	remote := newFakeRemote(39042)
	m := newSupervisorManager(remote.exec)
	started := make(chan []string, 4)
	m.spawn = blockingSpawn(started)

	ctx := context.Background()
	if _, err := m.Connect(ctx, tunnelSpec()); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	waitFor(t, func() bool { return len(started) == 1 })

	if _, err := m.Connect(ctx, tunnelSpec()); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	waitFor(t, func() bool { return len(started) == 2 })

	// Exactly one supervisor survives for the name.
	m.mu.Lock()
	n := len(m.sups)
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("supervisors for one name = %d, want 1", n)
	}
	// Each connect killed the old runner remotely.
	if got := remote.count("pkill"); got < 2 {
		t.Fatalf("expected a remote pkill per connect, got %d", got)
	}
}

// Stop tears the pipe down and kills the runner remotely, so a later Connect
// never faces a stray process.
func TestStop_KillsRemoteRunnerAndForgetsTarget(t *testing.T) {
	remote := newFakeRemote(39042)
	m := newSupervisorManager(remote.exec)
	started := make(chan []string, 2)
	m.spawn = blockingSpawn(started)

	ctx := context.Background()
	if _, err := m.Connect(ctx, tunnelSpec()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitFor(t, func() bool { return len(started) == 1 })

	before := remote.count("pkill")
	m.Stop(ctx, "m1")
	if after := remote.count("pkill"); after <= before {
		t.Fatalf("stop must kill the remote runner (pkill before=%d after=%d)", before, after)
	}
	if _, ok := m.get("m1"); ok {
		t.Fatal("stop must forget the supervisor")
	}
	if st := m.StatusOf("m1"); st.Mode != "" {
		t.Fatalf("status after stop = %+v, want zero value", st)
	}
}

// A dropped pipe is retried (the runner's lifetime is the pipe's lifetime, so a
// disconnect must not silently end the target's connection).
func TestSupervisor_ReconnectsAfterPipeDrop(t *testing.T) {
	remote := newFakeRemote(39042)
	m := newSupervisorManager(remote.exec)

	var mu sync.Mutex
	sessions := 0
	m.spawn = func(_ context.Context, _ []string) (io.ReadCloser, func() error, error) {
		mu.Lock()
		sessions++
		mu.Unlock()
		pr, pw := io.Pipe()
		_ = pw.Close() // session ends immediately → supervisor must retry
		return pr, func() error { return nil }, nil
	}

	ctx := context.Background()
	if _, err := m.Connect(ctx, tunnelSpec()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return sessions >= 2
	})
	m.Stop(ctx, "m1")
}

// The pipe's output is what `logs` serves, and it stays bounded.
func TestSupervisor_TailIsBoundedAndOrdered(t *testing.T) {
	s := &supervisor{spec: tunnelSpec()}
	for i := 1; i <= supervisorOutputLines+50; i++ {
		s.appendLine(fmt.Sprintf("line-%d", i))
	}
	tail := s.Tail(3)
	wantLast := fmt.Sprintf("line-%d", supervisorOutputLines+50)
	if len(tail) != 3 || tail[2] != wantLast {
		t.Fatalf("tail = %v, want last three lines ending in %s", tail, wantLast)
	}
	if got := len(s.Tail(0)); got != supervisorOutputLines {
		t.Fatalf("ring size = %d, want %d", got, supervisorOutputLines)
	}
}

// ============================================================================
// Connect validation & server parsing
// ============================================================================

func TestConnect_RejectsBadInput(t *testing.T) {
	m := newSupervisorManager(newFakeRemote(1).exec)
	ctx := context.Background()

	bad := tunnelSpec()
	bad.Name = "in valid"
	if _, err := m.Connect(ctx, bad); err == nil {
		t.Error("invalid runner name must be rejected")
	}

	bad = tunnelSpec()
	bad.SSHField = "   "
	if _, err := m.Connect(ctx, bad); err == nil {
		t.Error("empty ssh command must be rejected")
	}

	bad = tunnelSpec()
	bad.ConnMode = "carrier-pigeon"
	if _, err := m.Connect(ctx, bad); err == nil {
		t.Error("unknown connection mode must be rejected (no silent fallback)")
	}

	bad = tunnelSpec()
	bad.ConnectCmd = "--token t" // no --server
	if _, err := m.Connect(ctx, bad); err == nil {
		t.Error("tunnel mode without a server endpoint must be rejected")
	}
}

func TestParseServerFromConnectCmd(t *testing.T) {
	host, port, path, query, err := parseServerFromConnectCmd(
		"--server wss://xbot.example:8443/ws?tenant=1 --token t --name m1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if host != "xbot.example" || port != 8443 || path != "/ws" || query != "?tenant=1" {
		t.Fatalf("parsed (%q,%d,%q,%q)", host, port, path, query)
	}

	// --server=URL spelling and the default path.
	_, port, path, _, err = parseServerFromConnectCmd("--server=ws://h:9000 --token t")
	if err != nil || port != 9000 || path != "/ws" {
		t.Fatalf("spelling/default path: port=%d path=%q err=%v", port, path, err)
	}

	if _, _, _, _, err := parseServerFromConnectCmd("--token t"); err == nil {
		t.Error("missing --server must error")
	}
	if _, _, _, _, err := parseServerFromConnectCmd("--server ws://nohost --token t"); err == nil {
		t.Error("missing port must error")
	}

	// 空 host + 有端口 ⇒ **必须可解析**：默认隧道模式下 runner 连的是 ssh -R 暴露在
	// 远端的 127.0.0.1:<remotePort>，铸命令里的 host 只是服务端 bind 地址（未配置时为空）。
	// 2026-09-18 用户实机 P1：`--server "ws://:8089/ws"` 曾报 "must include host:port"，
	// 把默认（且唯一无需公网的）隧道模式直接卡死。
	h, p, ph, _, err := parseServerFromConnectCmd(`--server ws://:8089/ws --token t --name b300-4`)
	if err != nil {
		t.Errorf("host-less --server must parse (tunnel mode needs only the port): %v", err)
	} else if h != "" || p != 8089 || ph != "/ws" {
		t.Errorf("host-less parse = (%q,%d,%q), want (\"\", 8089, \"/ws\")", h, p, ph)
	}
}

func TestReplaceConnectCmdServer_PreservesOtherArgs(t *testing.T) {
	got := replaceConnectCmdServer(
		"--server ws://old:1/ws --token tk --name m1 --workspace /w",
		"ws://127.0.0.1:39042/ws")
	want := "--server ws://127.0.0.1:39042/ws --token tk --name m1 --workspace /w"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// ============================================================================
// Helpers
// ============================================================================

func containsSeq(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// A missing runner binary must fail with an actionable message, not an opaque
// "exit status 127" from the retry loop.
func TestBuildRemoteScript_PreflightsMissingBinary(t *testing.T) {
	s := &supervisor{spec: tunnelSpec()}
	_, script, _ := s.buildPipeCommand(39042)
	if !strings.Contains(script, "run provision first") {
		t.Fatalf("script must explain the missing binary: %s", script)
	}
	pre := strings.Index(script, "run provision first")
	execAt := strings.Index(script, "exec ")
	if pre < 0 || execAt < 0 || pre > execAt {
		t.Fatalf("preflight must run before exec: %s", script)
	}
}

// Deactivation must close every pipe (and kill the remote runners), otherwise
// orphaned ssh children keep runners alive after we are unloaded.
func TestStopAll_TearsDownEveryPipe(t *testing.T) {
	remote := newFakeRemote(39042)
	m := newSupervisorManager(remote.exec)
	started := make(chan []string, 4)
	m.spawn = blockingSpawn(started)

	ctx := context.Background()
	for _, n := range []string{"m1", "m2"} {
		spec := tunnelSpec()
		spec.Name = n
		if _, err := m.Connect(ctx, spec); err != nil {
			t.Fatalf("connect %s: %v", n, err)
		}
	}
	waitFor(t, func() bool { return len(started) == 2 })

	before := remote.count("pkill")
	m.StopAll(ctx)

	m.mu.Lock()
	left := len(m.sups)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("supervisors after StopAll = %d, want 0", left)
	}
	if got := remote.count("pkill"); got < before+2 {
		t.Fatalf("StopAll must kill each remote runner (pkill %d → %d)", before, got)
	}
}

// A taken remote port must fail loudly instead of silently skipping the forward.
func TestBuildPipeCommand_TunnelFailsLoudlyOnForwardError(t *testing.T) {
	s := &supervisor{spec: tunnelSpec()}
	argv, _, err := s.buildPipeCommand(39042)
	if err != nil {
		t.Fatalf("buildPipeCommand: %v", err)
	}
	if !containsSeq(argv, "ExitOnForwardFailure=yes") {
		t.Fatalf("tunnel mode must set ExitOnForwardFailure: %v", argv)
	}
	// Direct mode has no forward ⇒ no need for the option.
	spec := tunnelSpec()
	spec.ConnMode = connModeDirect
	d := &supervisor{spec: spec}
	argv, _, _ = d.buildPipeCommand(0)
	if containsSeq(argv, "ExitOnForwardFailure=yes") {
		t.Fatalf("direct mode should not carry forward options: %v", argv)
	}
}
