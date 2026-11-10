package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// readPidFile polls a pid file written by the spawned shell.
func readPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("shell never wrote its pid to %s", path)
	return 0
}

// processAlive reports whether pid still exists and is not a zombie.
func processAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 > len(s) {
		return true
	}
	fields := strings.Fields(s[i+1:])
	return len(fields) > 0 && fields[0] != "Z"
}

// TestCancelBlocksUntilTurnReallyStops 验证 /cancel 的**阻塞语义**基础：
// ActiveRunDone 只有在整轮收尾（processMessage 返回 + cancel 注册摘除）之后
// 才关闭，且此刻正在跑的慢工具（sleep 5）必须已经**真的**被杀掉。
//
// 这是 POST /api/cancel 阻塞等待的权威信号（用户 2026-10-22：「cancel 只要 api
// 返回了一定必须已经取消成功，它是阻塞的」）。
//
// 变异判别：
//   - 若取消只发信号不杀工具（killProcessTree 退化为空操作）⇒ sleep 5 活到超时，
//     run-done 迟迟不关，测试在 3s 上限处变红；
//   - 若 ActiveRunDone 在"有活跃轮次"时被提前关闭（例如把信号当完成）⇒ 后续
//     "进程已消失"断言变红。
func TestCancelBlocksUntilTurnReallyStops(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("linux-only test (reads /proc)")
	}
	a := &Agent{bus: bus.NewMessageBus(), channelFinder: nil}
	cancelKey := "cli:blocking"
	cancelCh := make(chan struct{}, 1)
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	a.registerActiveCancelState(cancelKey, cancelCh, reqCancel)

	runDone, active := a.ActiveRunDone("cli", "blocking")
	if !active || runDone == nil {
		t.Fatal("ActiveRunDone must report the active turn and hand out its completion channel")
	}
	select {
	case <-runDone:
		t.Fatal("run-done closed while the turn is still running")
	default:
	}

	pidFile := filepath.Join(t.TempDir(), "shell.pid")
	turnEnded := make(chan struct{})
	go func() {
		defer close(turnEnded)
		// cancel listener — same shape as chatProcessLoop's goroutine.
		go func() {
			for {
				select {
				case <-cancelCh:
					reqCancel()
				case <-reqCtx.Done():
					return
				}
			}
		}()
		// The "slow tool": a real shell running `sleep 5`.
		s := &tools.NoneSandbox{}
		// Args carry the executable (cmdbuilder uses shell=false ⇒ args[0] is the
		// program), same shape as ShellTool's own ExecSpec.
		if _, err := s.Exec(reqCtx, tools.ExecSpec{
			Command: "/bin/bash",
			Args:    []string{"/bin/bash", "-c", "echo $$ > " + pidFile + "; exec sleep 5"},
			Dir:     filepath.Dir(pidFile),
		}); err != nil && reqCtx.Err() == nil {
			t.Errorf("shell exec failed: %v", err)
		}
		// Turn teardown — what chatProcessLoop's defer does.
		a.finishActiveCancelState(cancelKey, reqCtx, reqCancel)
	}()

	pid := readPidFile(t, pidFile)

	start := time.Now()
	a.interceptCancel(bus.InboundMessage{Channel: "cli", ChatID: "blocking", SenderID: "u1"})

	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("run-done did not close within 3s of /cancel (exec waiting at %v) — cancellation did not take effect", pid)
	}
	waited := time.Since(start)

	<-turnEnded
	if waited > time.Second {
		t.Fatalf("turn took %v to stop after /cancel — the `sleep 5` tool was not killed", waited)
	}
	if processAlive(pid) {
		t.Fatalf("shell pid %d is STILL ALIVE at the moment run-done closed — the API would have lied about the cancellation", pid)
	}
	t.Logf("cancel→turn-end=%v (shell pid %d gone)", waited, pid)
}

// TestActiveRunDone_NoActiveTurn — 没有活跃轮次时必须报 ok=false，让
// POST /api/cancel 立即返回成功（保持原行为，不得白等 5s）。
func TestActiveRunDone_NoActiveTurn(t *testing.T) {
	a := &Agent{}
	if _, ok := a.ActiveRunDone("cli", "nothing-running"); ok {
		t.Fatal("ActiveRunDone reported an active turn for an idle session")
	}
	if _, ok := a.ActiveRunDone("", ""); ok {
		t.Fatal("ActiveRunDone reported an active turn for an empty session key")
	}
}

// TestActiveRunDone_FreshChannelPerTurn — 每一轮必须有**新的**完成通道：
// 复用上一轮（已 close 的）通道会让等待方立刻返回并谎报取消成功。
func TestActiveRunDone_FreshChannelPerTurn(t *testing.T) {
	a := &Agent{}
	cancelKey := "cli:reuse"

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	a.registerActiveCancelState(cancelKey, make(chan struct{}, 1), cancel1)
	done1, ok := a.ActiveRunDone("cli", "reuse")
	if !ok {
		t.Fatal("turn 1 not active")
	}
	a.finishActiveCancelState(cancelKey, ctx1, cancel1)
	select {
	case <-done1:
	case <-time.After(time.Second):
		t.Fatal("turn 1 done channel not closed by teardown")
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	a.registerActiveCancelState(cancelKey, make(chan struct{}, 1), cancel2)
	done2, ok := a.ActiveRunDone("cli", "reuse")
	if !ok {
		t.Fatal("turn 2 not active")
	}
	select {
	case <-done2:
		t.Fatal("turn 2's completion channel was already closed — it must be a FRESH channel")
	default:
	}
	a.finishActiveCancelState(cancelKey, ctx2, cancel2)
}
