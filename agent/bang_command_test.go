package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/config"
	"xbot/tools"
)

// REPRO —— 本机（none 沙箱）首次运行时工作区目录不存在 → `!cmd` 直接失败。
//
// 现场：`xbot-cli --local -p '!echo hi'` 报
// `exit: fork/exec /bin/bash: no such file or directory`（bash 明明存在），
// 插桩显示 exec 的 dir=/root/.xbot/users/cli_user/workspace 并不存在；
// 手动 mkdir 后同一条命令立刻成功。
//
// 根因：ensureWorkspace 把 "none"（本机）与 remote/docker 一样直接跳过 —— 而
// 本机的工作区是一个真实路径，需要建；bang 的 exec Dir 恰好回落到它。
func TestEnsureWorkspace_LocalSandboxCreatesWorkspace(t *testing.T) {
	prev := tools.GetSandbox()
	tools.SetSandbox(&tools.NoneSandbox{})
	t.Cleanup(func() { tools.SetSandbox(prev) })

	dir := filepath.Join(t.TempDir(), "users", "cli_user", "workspace") // 不存在
	// ⚠️ a.sandbox 必须设置：sandboxNameForUser 只有在 a.sandbox 非 nil 时才
	// 返回 "none"（否则返回 ""，会绕过 skip 分支走 os.MkdirAll —— 那样旧的
	// 跳过逻辑也能"通过"测试，回归守卫就失效了）。
	a := &Agent{sandbox: &tools.NoneSandbox{}}
	if err := a.ensureWorkspace(context.Background(), dir, "cli_user"); err != nil {
		t.Fatalf("ensureWorkspace: %v", err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("本机工作区未创建: dir=%s stat=%v err=%v", dir, st, err)
	}

	// 端到端：工作区刚建好时 bang 必须能跑（回归：以前 exec 直接失败）
	out, err := a.executeBangCommand(context.Background(), "echo bang_workspace_probe", dir, "cli:chat-test", "cli_user", dir)
	if err != nil {
		t.Fatalf("executeBangCommand 失败（工作区不存在时 !cmd 不可用）: %v", err)
	}
	if !strings.Contains(out, "bang_workspace_probe") {
		t.Fatalf("bang 输出 = %q，未见探针字符串", out)
	}
}

// SandboxRouter itself deliberately rejects filesystem operations because it
// has no session identity at that boundary. ensureWorkspace must therefore
// resolve the concrete per-session sandbox before creating the local directory.
func TestEnsureWorkspace_RouterResolvesSessionBeforeCreatingWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "users", "web-user", "workspace")
	router := tools.NewSandboxRouter(config.SandboxConfig{}, t.TempDir())
	a := &Agent{sandbox: router}

	if err := a.ensureWorkspace(context.Background(), dir, "web:chat-test"); err != nil {
		t.Fatalf("ensureWorkspace through SandboxRouter: %v", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("本机会话工作区未创建: dir=%s stat=%v err=%v", dir, st, err)
	}
}

func TestIsBangCommand(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantOK  bool
	}{
		{"!ls -la", "ls -la", true},
		{"!pwd", "pwd", true},
		{"! echo hello", "echo hello", true},
		{"!  echo hello", "echo hello", true}, // multiple spaces after !
		{"!cat /etc/os-release", "cat /etc/os-release", true},
		{"!", "", false},        // just `!`, no command
		{"!  ", "", false},      // `!` followed by whitespace only
		{"hello", "", false},    // normal message
		{"/version", "", false}, // slash command
		{"", "", false},         // empty
		{"  !ls", "ls", true},   // leading whitespace
		{"!!ls", "!ls", true},   // double bang (passes through, shell handles it)
		// Markdown images are NOT bang commands — a pasted screenshot in the
		// composer starts with `![image.png](/api/files/download?...)` and
		// must flow through the normal chat pipeline (turn_id allocation),
		// not be executed as a shell command (regression: image messages
		// failed with "message accepted without a turn_id").
		{"![image.png](/api/files/download?key=uploads%2F1%2Fa.png&inline=1)", "", false},
		{"![chart](https://example.com/x.png) 这个图", "", false},
		{"![alt](viewimg://abc.png)\n\nmore text", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cmd, ok := isBangCommand(tt.input)
			if ok != tt.wantOK {
				t.Errorf("isBangCommand(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if cmd != tt.wantCmd {
				t.Errorf("isBangCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCmd)
			}
		})
	}
}

func TestFormatBangOutput(t *testing.T) {
	tests := []struct {
		name    string
		command string
		output  string
		err     error
		want    string
	}{
		{
			name:    "success with output",
			command: "ls",
			output:  "file1\nfile2",
			err:     nil,
			want:    "```\nfile1\nfile2\n```",
		},
		{
			name:    "success no output",
			command: "mkdir test",
			output:  "",
			err:     nil,
			want:    "`OK (no output)`",
		},
		{
			name:    "error with output",
			command: "cat missing",
			output:  "cat: missing: No such file or directory",
			err:     fmt.Errorf("exit status 1"),
			want:    "```\ncat: missing: No such file or directory\n```\n`exit: exit status 1`",
		},
		{
			name:    "error no output",
			command: "false",
			output:  "",
			err:     fmt.Errorf("exit status 1"),
			want:    "`exit: exit status 1`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBangOutput(tt.command, tt.output, tt.err)
			if got != tt.want {
				t.Errorf("formatBangOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteBangOutputFile(t *testing.T) {
	tmpDir := t.TempDir()

	command := "find / -type f"
	output := strings.Repeat("line\n", 1000)

	a := &Agent{} // no sandbox, uses os.WriteFile directly
	filePath, err := a.writeBangOutputFile(context.Background(), tmpDir, command, output, nil, "test-user")
	if err != nil {
		t.Fatalf("writeBangOutputFile() error = %v", err)
	}

	// Check file exists
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("output file not found: %v", err)
	}

	// Check file is in the workspace dir
	if !strings.HasPrefix(filePath, tmpDir) {
		t.Errorf("file path %q not under workspace %q", filePath, tmpDir)
	}

	// Check file extension
	if filepath.Ext(filePath) != ".md" {
		t.Errorf("file extension = %q, want .md", filepath.Ext(filePath))
	}

	// Check content contains code block
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "```") {
		t.Error("output file should contain code block markers")
	}
	if !strings.Contains(content, command) {
		t.Error("output file should contain the command")
	}
}

func TestWriteBangOutputFileWithError(t *testing.T) {
	tmpDir := t.TempDir()

	command := "cat missing"
	output := "cat: missing: No such file or directory"
	execErr := fmt.Errorf("exit status 1")

	a := &Agent{} // no sandbox, uses os.WriteFile directly
	filePath, err := a.writeBangOutputFile(context.Background(), tmpDir, command, output, execErr, "test-user")
	if err != nil {
		t.Fatalf("writeBangOutputFile() error = %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "exit status 1") {
		t.Error("output file should contain exit status")
	}
}
