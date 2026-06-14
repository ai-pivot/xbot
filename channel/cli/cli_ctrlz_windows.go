//go:build windows

package cli

import (
	"os"
)

// Windows uses Ctrl+Z for EOF (not SIGTSTP signal suspension).
func setupCtrlZSuspend(_ *CLIChannel, _ *os.File, _ *os.File) {
	// No-op: Windows doesn't have SIGTSTP.
}
