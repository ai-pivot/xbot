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
//     restart <unit>` — the supervisor revives the process.
//   - launchd (macOS): `launchctl kickstart -k gui/<uid>/com.xbot.server`.
//   - supervisord / docker / any other manager: we send SIGTERM to ourselves —
//     the graceful shutdown path runs (WAL checkpoint, pending-resume stamps).
//     Whether the process comes back depends on the manager's restart policy
//     (supervisord autorestart, docker --restart, pm2, …) — we deliberately do
//     NOT assume either way; the UI copy stays neutral and tells the user to
//     rely on their own service management.
//
// Detection is best-effort and only used to pick the restart mechanism and to
// make the UI hint more accurate — an undetected manager (or a truly manual
// start) both fall through to the SIGTERM path, which is correct for both.

// DetectServiceManager reports how the current process is supervised:
// "systemd", "launchd", "supervisord", "docker", or "none" (unknown — could be
// a manual start OR an unrecognized manager; the UI must not assume the
// process will or will not come back).
func DetectServiceManager() string {
	// systemd sets INVOCATION_ID for every service unit (user + system).
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	// supervisord injects SUPERVISOR_ENABLED / SUPERVISOR_PROCESS_NAME into
	// every child it spawns.
	if os.Getenv("SUPERVISOR_ENABLED") != "" || os.Getenv("SUPERVISOR_PROCESS_NAME") != "" {
		return "supervisord"
	}
	// Docker containers have /.dockerenv; some runtimes also set $container.
	if _, err := os.Stat("/.dockerenv"); err == nil || os.Getenv("container") != "" {
		return "docker"
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
//
// For systemd/launchd we ask the manager to restart us explicitly. For
// everything else (supervisord, docker, pm2, a manual start, …) we SIGTERM
// ourselves: the graceful shutdown path runs (WAL checkpoint, pending-resume
// stamps — see serverapp.Run's signal handling) and the process exits. Whether
// it comes back is up to the user's service management (supervisord
// autorestart, docker --restart, …) — the UI copy stays neutral and does not
// assume either way.
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
		// supervisord / docker / pm2 / manual start: graceful self-termination.
		// The manager's restart policy (if any) decides whether we come back;
		// the UI has already shown the neutral restart notice.
		go func() {
			time.Sleep(800 * time.Millisecond)
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}()
		return nil
	}
}
