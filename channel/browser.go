package channel

import (
	"os/exec"
	"runtime"
)

// OpenBrowser opens the given URL in the user's default browser.
// Exported version for use by sub-packages (cli, web).
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
