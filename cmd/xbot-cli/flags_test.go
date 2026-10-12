package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/version"
)

// main() 不能被单测直接调用（它会拉起 TUI / 会话），所以这里**构建真实二进制并执行**
// —— 这正是该 bug 的本质（2026-10-12 用户报告：`xbot-cli --version` 会**启动 xbot**，
// 而不是打印版本；根因是未知 flag 在 default 里被静默吞掉，随后照常走默认路径起会话）。
// 只有真正跑进程才能证明"没有起来会话"。
var (
	cliBuildOnce sync.Once
	cliBuildPath string
	cliBuildErr  error
)

func cliBinary(t *testing.T) string {
	t.Helper()
	cliBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "xbot-cli-flagtest")
		if err != nil {
			cliBuildErr = err
			return
		}
		bin := filepath.Join(dir, "xbot-cli")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
		if err != nil {
			cliBuildErr = errors.New(string(out))
			return
		}
		cliBuildPath = bin
	})
	if cliBuildErr != nil {
		t.Skipf("无法构建 CLI 二进制（跳过 flag 回归）: %v", cliBuildErr)
	}
	return cliBuildPath
}

func runCLI(t *testing.T, args ...string) (stdout string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBinary(t), args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("`xbot-cli %s` 超时未退出 —— 说明它**启动了会话**而不是打印后退出（正是被报告的 bug）", strings.Join(args, " "))
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		exitCode = 0
	case errors.As(err, &ee):
		exitCode = ee.ExitCode()
	default:
		t.Fatalf("执行 `xbot-cli %s` 失败: %v", strings.Join(args, " "), err)
	}
	return string(out), exitCode
}

func TestCLI_VersionFlagPrintsVersionAndExits(t *testing.T) {
	for _, flag := range []string{"--version", "-v", "-V"} {
		out, code := runCLI(t, flag)
		if code != 0 {
			t.Fatalf("`%s` 必须 exit 0（打印后退出），got %d\n%s", flag, code, out)
		}
		// stdout 必须**恰好**是版本号（不带 banner、不带 "Mode: resuming …"）
		if got := strings.TrimSpace(out); got != version.Version {
			t.Fatalf("`%s` 的输出必须只有版本号 %q，got %q", flag, version.Version, got)
		}
	}
}

func TestCLI_HelpFlagPrintsUsageAndExits(t *testing.T) {
	out, code := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("`--help` 必须 exit 0，got %d\n%s", code, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "Usage: xbot-cli") {
		t.Fatalf("`--help` 必须打印用法且不带 banner，got %q", out)
	}
}

func TestCLI_UnknownFlagFailsFastInsteadOfStartingSession(t *testing.T) {
	// 拼错的 flag：必须**报错退出**（exit 2），绝不静默吞掉后照常启动会话。
	out, code := runCLI(t, "--versoin")
	if code != 2 {
		t.Fatalf("未知 flag 必须以 exit 2 退出，got %d\n%s", code, out)
	}
	if !strings.Contains(out, "unknown flag") {
		t.Fatalf("未知 flag 必须给出明确错误，got %q", out)
	}
}
