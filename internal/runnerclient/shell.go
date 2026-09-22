package runnerclient

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// DetectShell 检测最佳可用的 shell。
// Docker 模式：查询容器内的 /etc/passwd（与 DockerSandbox.detectShell 相同）。
// Native 模式：检查宿主机文件系统。
//
// ⛔ 2026-09-22 parity fix: $SHELL takes precedence — the LOCAL sandbox's
// defaultShell() resolves the user's login shell from $SHELL first, and the
// runner must report the SAME shell the local path would use. The runner
// process inherits $SHELL from the SSH session it was started in, so this is
// the user's real login shell (e.g. /bin/bash on PAI DSW images where /bin/sh
// is dash and /etc/profile.d uses bash-only syntax — probing /bin/bash first
// happened to work there, but $SHELL is the authoritative answer and also
// covers machines whose login shell is zsh/fish).
func DetectShell(dockerMode bool, executor Executor) string {
	if dockerMode {
		de, ok := executor.(*DockerExecutor)
		if ok {
			out, err := exec.Command("docker", "exec", "-i", de.ContainerName,
				"sh", "-c", "grep '^root:' /etc/passwd | cut -d: -f7").Output()
			if err == nil {
				shell := strings.TrimSpace(string(out))
				if shell != "" {
					return shell
				}
			}
		}
	}

	// Platform-specific fallback
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			return "powershell.exe"
		}
		return "cmd.exe"
	}

	// $SHELL first (local defaultShell parity — the user's login shell).
	if shell := os.Getenv("SHELL"); shell != "" {
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
	}

	// Unix fallback
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}
