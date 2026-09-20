package tools

import (
	"context"
	"fmt"
	"os"
)

// OfflineRunnerSandbox refuses every operation because the runner a session is
// bound to is not connected.
//
// This replaces the old silent fallback to the local host. Binding a session to
// a remote machine is an explicit user decision; if that machine is unreachable
// the work must NOT quietly happen on the server instead — it fails loudly with
// a message that names the unreachable machine.
type OfflineRunnerSandbox struct {
	runnerName string
}

func (s *OfflineRunnerSandbox) err() error {
	name := s.runnerName
	if name == "" {
		name = "<unknown>"
	}
	return fmt.Errorf("⚠️ 目标机器 %q 当前离线，无法执行工具。\n\n"+
		"请启动该机器上的 xbot-runner（或在本机网络可达后重试）；"+
		"如需改用本机执行，请在会话面板切换回「本机」。", name)
}

func (s *OfflineRunnerSandbox) Name() string              { return "runner-offline" }
func (s *OfflineRunnerSandbox) Workspace(_ string) string { return "" }

func (s *OfflineRunnerSandbox) Close() error              { return nil }
func (s *OfflineRunnerSandbox) CloseForUser(string) error { return nil }

func (s *OfflineRunnerSandbox) GetShell(string, string) (string, error) { return "", s.err() }

func (s *OfflineRunnerSandbox) Exec(context.Context, ExecSpec) (*ExecResult, error) {
	return nil, s.err()
}

func (s *OfflineRunnerSandbox) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, s.err()
}

func (s *OfflineRunnerSandbox) WriteFile(context.Context, string, []byte, os.FileMode, string) error {
	return s.err()
}

func (s *OfflineRunnerSandbox) Stat(context.Context, string, string) (*SandboxFileInfo, error) {
	return nil, s.err()
}

func (s *OfflineRunnerSandbox) ReadDir(context.Context, string, string) ([]DirEntry, error) {
	return nil, s.err()
}

func (s *OfflineRunnerSandbox) MkdirAll(context.Context, string, os.FileMode, string) error {
	return s.err()
}

func (s *OfflineRunnerSandbox) Remove(context.Context, string, string) error {
	return s.err()
}

func (s *OfflineRunnerSandbox) RemoveAll(context.Context, string, string) error {
	return s.err()
}

func (s *OfflineRunnerSandbox) DownloadFile(context.Context, string, string, string) error {
	return s.err()
}
