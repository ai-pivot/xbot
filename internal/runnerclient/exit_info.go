// Package runnerclient provides the executor implementations for sandboxed code execution.
package runnerclient

import (
	"context"
	"os/exec"
)

// extractExitInfo examines the command execution error and context error to determine
// exit code, timeout status, and any unexpected error.
//
// Returns:
//   - exitCode: the process exit code (-1 for timeout, 0 for success)
//   - timedOut: true if the context deadline was exceeded
//   - rawErr: non-nil when the error is not a known exit/timeout case (should be returned to caller)
func extractExitInfo(err error, ctxErr error) (exitCode int, timedOut bool, rawErr error) {
	if ctxErr == context.DeadlineExceeded {
		return -1, true, nil
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), false, nil
		}
		return 0, false, err
	}
	return 0, false, nil
}
