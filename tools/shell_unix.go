//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// defaultShell returns the user's login shell from /etc/passwd.
// Falls back to /bin/sh if lookup fails.
func defaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	// Fallback
	return "/bin/sh"
}

// LoginShellArgs returns the command-line arguments for executing a command
// in a shell that loads the user's environment (PATH, aliases, etc.).
//
// Shell source order differs:
//
//	bash -l -c   → .bash_profile → .bashrc (login sources rc via profile chain)
//	zsh  -l -c   → .zshenv + .zprofile, but NOT .zshrc (rc is interactive-only)
//	zsh  -c      → .zshenv only
//
// User PATH config (go, cuda, nvm, etc.) typically lives in .zshrc / .bashrc.
// For zsh we explicitly source .zshrc so the user's environment is available
// in non-interactive mode without the overhead of -i (prompts, completion, etc.).
func LoginShellArgs(shell, command string) []string {
	name := filepath.Base(shell)
	switch name {
	case "zsh":
		return []string{shell, "-c", "source ~/.zshrc 2>/dev/null; " + command}
	default:
		// bash, sh, dash, etc.: -l login shell sources profile → rc chain.
		return []string{shell, "-l", "-c", command}
	}
}

// setProcessAttrs 设置 Unix 平台的进程属性
// 使用进程组，超时时可以杀掉整棵进程树
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess 杀掉进程组
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		killProcessTree(cmd.Process)
	}
}

// killProcessTree kills a process and its ENTIRE process tree on Unix.
//
// 两路覆盖，缺一不可（2026-10-22 实测根因）：
//  1. kill(-pid)：整个进程组（setProcessAttrs 的 Setpgid 让子进程默认同组）；
//  2. 逐个 SIGKILL 通过 /proc 的 ppid 关系收集到的**后代**。`setsid`/daemonize()
//     这类孙进程会自建 pgid 脱离进程组，kill(-pid) 打不到它；而它继承了
//     stdout/stderr 管道的写端 ⇒ 管道永无 EOF ⇒ 采集 goroutine 永久阻塞在
//     Read（goroutine dump: none_sandbox.go:532 wg.Wait ← sandboxExecAsync ←
//     ShellTool.executeForeground）。用户看到的就是"cancel 了但 shell 卡住"。
//
// 后代必须在**杀之前**收集：父进程一死，子孙立即被 reparent 到 1，ppid 链断了就
// 再也找不回来。整个函数是 O(#进程) 的一次 /proc 扫描，实测 <5ms。
func killProcessTree(proc *os.Process) {
	if proc == nil || proc.Pid == 0 {
		return
	}
	// Already reaped (the common "command finished normally" path): its children
	// have been reparented, so there is no descendant graph left to walk. Skip the
	// /proc scan to keep ordinary Shell calls free of that overhead.
	if _, err := os.Stat("/proc/" + strconv.Itoa(proc.Pid)); err != nil {
		_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
		proc.Kill()
		return
	}
	// Collect descendants BEFORE sending any signal (see doc comment).
	victims := descendantPids(proc.Pid)
	// Process group first: covers children that are still grouped even when the
	// group leader (proc) has already exited.
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		// ESRCH here just means the group is already empty — still try the
		// per-pid kills below, and the direct kill as a last resort.
		proc.Kill()
	}
	for _, pid := range victims {
		if pid == proc.Pid {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// descendantPids returns root plus every live descendant of root, ordered
// breadth-first from root. Zombies are skipped (nothing left to kill).
//
// Reads /proc/<pid>/stat for every numeric entry — the ppid field is the only
// reliable parent link after a process has been double-forked away from its
// original process group.
func descendantPids(root int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return []int{root}
	}
	children := make(map[int][]int, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, ok := procStatPPID(pid)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	out := make([]int, 0, 8)
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		out = append(out, cur)
		for _, child := range children[cur] {
			if seen[child] {
				continue
			}
			seen[child] = true
			queue = append(queue, child)
		}
	}
	return out
}

// procStatPPID parses the parent pid out of /proc/<pid>/stat.
// The comm field can contain spaces and parentheses, so fields are split after
// the LAST ')': state, ppid, pgrp, session, ...
func procStatPPID(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 > len(s) {
		return 0, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// isProcessAlive checks whether a process with the given PID is still running.
// Uses Signal(0) on Unix (doesn't actually send a signal, just checks existence).
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// SetProcessAttrs 是 setProcessAttrs 的导出版本，供其他包使用
func SetProcessAttrs(cmd *exec.Cmd) { setProcessAttrs(cmd) }

// KillProcess 是 killProcess 的导出版本，供其他包使用
func KillProcess(cmd *exec.Cmd) { killProcess(cmd) }
