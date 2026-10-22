package clipanic

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoverWritesLogAndRepanics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()

	defer func() {
		r := recover()
		if r != "boom" {
			t.Fatalf("expected repanic value boom, got %v", r)
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read panic log: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "where=main.main") {
			t.Fatalf("expected where in panic log, got: %s", content)
		}
		if !strings.Contains(content, "panic=boom") {
			t.Fatalf("expected panic value in panic log, got: %s", content)
		}
	}()

	func() {
		defer Recover("main.main", nil, true)
		panic("boom")
	}()
}

func TestRecoverWritesMessageType(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()

	type progressMsg struct{}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected repanic")
			}
		}()
		func() {
			defer Recover("channel.cliModel.Update", progressMsg{}, true)
			panic("boom")
		}()
	}()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read panic log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "msg=clipanic.progressMsg") {
		t.Fatalf("expected msg type in panic log, got: %s", content)
	}
}

func TestGoWritesLogAndSwallowsPanic(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()

	Go("worker.loop", func() {
		panic("worker boom")
	})

	// Go() launches a goroutine with defer Recover(...).
	// Recover writes the log synchronously under writeMu before the goroutine exits.
	// We can't observe goroutine completion directly, so poll for the log file.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		data, err := os.ReadFile(logPath)
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.Contains(content, "where=worker.loop") {
			t.Fatalf("expected worker where in panic log, got: %s", content)
		}
		if !strings.Contains(content, "panic=worker boom") {
			t.Fatalf("expected worker panic in panic log, got: %s", content)
		}
		return // success
	}
	// Try one final read for a better error message
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("timed out: panic log file not created: %v", err)
	}
	t.Fatalf("timed out: log file exists but missing expected content: %s", string(data))
}

// ---------------------------------------------------------------------------
// Crash output: unrecovered panic in a NON-main goroutine.
//
// 2026-10-22 05:26 生产事故：xbot-server 以 exit status 2 崩掉，supervisor 只留下
// 一行 "exited"，logs/cli-panic.log **从未生成** —— 因为 EnableFileLogging 只记
// 了一个路径，而 defer clipanic.Recover("main.main", ...) 只能兜 main goroutine，
// 其它 goroutine 的 panic 直接打到（被 supervisor 丢弃的）stderr。
//
// 本用例在**子进程**里故意 panic 一个后台 goroutine，断言堆栈落进 cli-panic.log。
// 子进程自己必然崩溃（exit 2），这正是被测的现实；断言对象是文件内容。
// ---------------------------------------------------------------------------
func TestEnableFileLogging_CapturesNonMainGoroutinePanic(t *testing.T) {
	const childEnv = "CLIPANIC_CRASH_CHILD_DIR"
	if dir := os.Getenv(childEnv); dir != "" {
		// ---- 子进程：启用文件日志 → 后台 goroutine panic ----
		EnableFileLogging(filepath.Join(dir, "cli-panic.log"))
		go func() {
			panic("background-goroutine-sentinel-9f3c")
		}()
		// 等运行时把崩溃输出写完（进程会自己退出，这里只是不提前返回）。
		time.Sleep(10 * time.Second)
		return
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestEnableFileLogging_CapturesNonMainGoroutinePanic")
	cmd.Env = append(os.Environ(), childEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child process did not crash (want panic exit); output:\n%s", out)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
		t.Fatalf("child exit = %v, want exit code 2 (Go panic); output:\n%s", err, out)
	}

	logPath := filepath.Join(dir, "cli-panic.log")
	data, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("cli-panic.log was NOT created by a background-goroutine panic: %v\nchild output:\n%s", readErr, out)
	}
	content := string(data)
	if !strings.Contains(content, "background-goroutine-sentinel-9f3c") {
		t.Errorf("panic value missing from cli-panic.log:\n%s", content)
	}
	if !strings.Contains(content, "panic:") {
		t.Errorf("no 'panic:' header in cli-panic.log:\n%s", content)
	}
	if !strings.Contains(content, "goroutine ") {
		t.Errorf("no goroutine stack in cli-panic.log:\n%s", content)
	}
	// 堆栈必须能指回 panic 现场（同一文件 + 行号），否则等于没有堆栈。
	if !strings.Contains(content, "clipanic_test.go:") {
		t.Errorf("stack does not point back at the panicking frame:\n%s", content)
	}
	// 0600：堆栈会带出环境变量/URL/token 片段。
	if fi, statErr := os.Stat(logPath); statErr == nil {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("cli-panic.log mode = %o, want 600", perm)
		}
	}

	// 把捕获到的堆栈打进测试输出 —— 取证用（t.TempDir 随后会被清理）。
	head := content
	if lines := strings.SplitN(content, "\n", 16); len(lines) > 15 {
		head = strings.Join(lines[:15], "\n") + "\n..."
	}
	t.Logf("cli-panic.log 捕获成功（%d bytes），前 15 行：\n%s", len(data), head)
}

// TestEnableFileLogging_IsIdempotent 钉死"只打开一次"：重复启用同一路径不得
// 重建文件（也就不可能丢已写入的记录），且 Report 与 crash output 共用句柄。
func TestEnableFileLogging_IsIdempotent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nested", "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()
	first := currentCrashFile()
	if first == nil {
		t.Fatal("EnableFileLogging did not open a crash file")
	}

	EnableFileLogging(logPath) // 重复启用
	if second := currentCrashFile(); second != first {
		t.Fatal("EnableFileLogging reopened the file for the same path (must open exactly once)")
	}
	if got := currentLogPath(); got != logPath {
		t.Errorf("currentLogPath() = %q, want %q", got, logPath)
	}

	ReportWithStack("test.where", nil, "recovered-boom", []byte("stack-line"))
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read panic log: %v", err)
	}
	if !strings.Contains(string(data), "recovered-boom") {
		t.Errorf("ReportWithStack did not append through the shared handle: %s", data)
	}
}
