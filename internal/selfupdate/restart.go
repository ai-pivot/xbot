package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Service manager detection + restart.
//
// The restart contract (user-facing, web About panel):
//   - systemd user service (install.sh MODE=server-client): `systemctl --user
//     restart xbot-server` — the supervisor revives the process. This is the
//     "conventional" start method; restart is fully automatic.
//   - launchd (macOS): `launchctl kickstart -k gui/<uid>/com.xbot.server`.
//   - anything else (manual `xbot-cli serve`, nohup, docker, IDE): we send
//     SIGTERM to ourselves — the graceful shutdown path runs (WAL checkpoint,
//     pending-resume stamps) and the process exits. NOTHING revives it, so
//     the web UI must warn the user beforehand that they may need to restart
//     the process by hand.

// DetectServiceManager reports how the current process is supervised:
// "systemd", "launchd", or "none" (started by hand — restart will NOT
// auto-revive; the UI must warn).
func DetectServiceManager() string {
	// systemd sets INVOCATION_ID for every service unit (user + system).
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	if runtime.GOOS == "darwin" {
		// launchd: our plist label is com.xbot.server (install.sh). Being
		// listed means the service is loaded (KeepAlive may revive it).
		if out, err := exec.Command("launchctl", "list").Output(); err == nil {
			if strings.Contains(string(out), "com.xbot.server") {
				return "launchd"
			}
		}
	}
	return "none"
}

// systemdUnitName resolves the user-service unit name of the current process
// from /proc/self/cgroup (cgroup v2 user services carry the unit path). Falls
// back to the canonical "xbot-server" installed by scripts/install.sh.
func systemdUnitName() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			// Line shape: 0::/user.slice/user-1000.slice/user@1000.service/app.slice/xbot-server.service
			if i := strings.Index(line, ".service"); i >= 0 {
				for _, part := range strings.Split(line, "/") {
					if strings.HasSuffix(part, ".service") {
						return part
					}
				}
			}
		}
	}
	return "xbot-server"
}

// Restart triggers a service restart and returns immediately (the actual
// restart happens after a short delay so the HTTP response flushes first).
// The caller MUST have warned the user when managedBy == "none".
//
// For systemd/launchd the supervisor revives the process automatically. For
// "none" we SIGTERM ourselves: the graceful shutdown path runs (WAL
// checkpoint, pending-resume stamps — see serverapp.Run's signal handling)
// and the process exits; the user restarts it by hand.
func Restart() error {
	switch DetectServiceManager() {
	case "systemd":
		unit := systemdUnitName()
		go func() {
			time.Sleep(800 * time.Millisecond)
			// systemctl restart sends SIGTERM (graceful) then starts a fresh
			// instance. Errors are invisible to the caller (response already
			// sent) — the user sees the reconnect state in the UI.
			if err := exec.Command("systemctl", "--user", "restart", unit).Run(); err != nil {
				// Supervisor restart failed (renamed unit, D-Bus session gone).
				// Fall back to SIGTERM so at least the graceful path runs.
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			}
		}()
		return nil
	case "launchd":
		go func() {
			time.Sleep(800 * time.Millisecond)
			label := fmt.Sprintf("gui/%d/com.xbot.server", os.Getuid())
			if err := exec.Command("launchctl", "kickstart", "-k", label).Run(); err != nil {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			}
		}()
		return nil
	default:
		// No supervisor: graceful self-termination. The web UI has already
		// warned the user they may need to restart the process manually.
		go func() {
			time.Sleep(800 * time.Millisecond)
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}()
		return nil
	}
}
