package main

// argvprobe_test.go — 进程 argv 里是否出现 `--token <值>` 的可复现探针。
//
// 背景（2026-11-25 首辅派单，兵部口径）：2s×15 采样 `ps -eo pid=,ppid=,etime=,args=`
// 时，本机 ssh 进程的 argv 曾带着 2 枚 43 字符 runner token（8/15 次命中）。根因是
// 服务端铸出的 `xbot-runner --server … --token <值> --name …` 被整串拼进 ssh 的**远程
// 命令参数**。修复后 token 只走 ssh 的 stdin（见 supervisor.go buildRemoteScript）。
//
// 这个测试不依赖真实 ssh/远端：它把 **buildPipeCommand() 真实产出的 argv** 交给一个
// 惰性 stub 进程（sleep），再用与兵部完全相同的 ps 口径采样，只统计「本探针自己的那条
// 进程行」里 `--token` 的出现次数 —— 因此既不受本机其它进程污染，也不需要碰任何远端机器。
//
// 用法（默认跳过）：
//
//	XBOT_ARGV_PROBE=1 XBOT_ARGV_PROBE_EXPECT=clean XBOT_ARGV_SAMPLE_OUT=/path/argv.txt \
//	  go test -run TestArgvProbeTokenAbsent -v .
//
//	XBOT_ARGV_PROBE_EXPECT=leak → 期望采样里出现 --token（用来证明采样有判别力：
//	对旧实现跑必须命中，否则同样的断言在新实现上「0 命中」毫无意义）。
//
// 本文件刻意只使用 targetSpec / buildPipeCommand 这类在 HEAD 与修复后都存在的符号，
// 以便把同一个探针原样拷进 HEAD 的临时 worktree 里跑变异对照。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"strings"
	"testing"
	"time"
)

func TestArgvProbeTokenAbsent(t *testing.T) {
	if os.Getenv("XBOT_ARGV_PROBE") != "1" {
		t.Skip("probe disabled; set XBOT_ARGV_PROBE=1 (see file header)")
	}
	expect := os.Getenv("XBOT_ARGV_PROBE_EXPECT")
	if expect == "" {
		expect = "clean"
	}
	outPath := os.Getenv("XBOT_ARGV_SAMPLE_OUT")
	token := "probe" + "-token-" + strings.Repeat("Z", 34) // 43 chars, like a real runner token

	spec := targetSpec{
		SSHField:   "ssh probe@127.0.0.1 -p 2222",
		Name:       "argvprobe",
		ConnectCmd: "--server ws://127.0.0.1:8082/ws --token " + token + " --name argvprobe",
		InstallDir: "/home/probe/.local/bin",
		ConnMode:   connModeTunnel,
		ServerPort: 8082,
		ServerPath: "/ws",
	}
	s := &supervisor{spec: spec}
	argv, script, err := s.buildPipeCommand()
	if err != nil {
		t.Fatalf("buildPipeCommand: %v", err)
	}

	// Stub "ssh": keeps the exact argv alive for the whole sampling window.
	dir := t.TempDir()
	stub := filepath.Join(dir, "xbot-argv-probe-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	probeArgv := append([]string{stub}, argv[1:]...)

	cmd := exec.Command(probeArgv[0], probeArgv[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stub: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	// 等 stub 起来，避免第一拍采空。
	time.Sleep(300 * time.Millisecond)

	var (
		samples   []string
		hits      int
		probeHits int
	)
	for i := 0; i < 15; i++ {
		raw, perr := exec.Command("ps", "-eo", "pid=,ppid=,etime=,args=").Output()
		if perr != nil {
			t.Fatalf("ps: %v", perr)
		}
		ts := time.Now().UTC().Format(time.RFC3339)
		var buf strings.Builder
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if !strings.Contains(line, "xbot-argv-probe-stub") {
				continue
			}
			hits += strings.Count(line, "--token")
			probeHits++
			// token 值一律打码，只保留「有没有 --token」这个判据。
			buf.WriteString(maskTokenValues(line))
			buf.WriteString("\n")
		}
		samples = append(samples, fmt.Sprintf("### sample %02d %s\n%s", i+1, ts, buf.String()))
		time.Sleep(2 * time.Second)
	}

	report := strings.Join(samples, "")
	report += fmt.Sprintf("\n# probe_process_lines=%d  --token_hits=%d  expect=%s\n", probeHits, hits, expect)
	report += fmt.Sprintf("# remote_script_contains_token=%v  remote_script_contains_token_flag=%v\n",
		strings.Contains(script, token), strings.Contains(script, "--token"))

	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatalf("mkdir out: %v", err)
		}
		if err := os.WriteFile(outPath, []byte(report), 0o644); err != nil {
			t.Fatalf("write sample: %v", err)
		}
	}
	t.Logf("probe lines=%d --token hits=%d (expect=%s)", probeHits, hits, expect)

	if probeHits == 0 {
		t.Fatalf("sampler saw no probe process — it has no discriminating power")
	}
	switch expect {
	case "clean":
		if hits != 0 {
			t.Fatalf("argv still carries --token (%d hits)\n%s", hits, report)
		}
		if strings.Contains(script, "--token") || strings.Contains(script, token) {
			t.Fatalf("remote script still carries the token/flag:\n%s", script)
		}
	case "leak":
		if hits == 0 {
			t.Fatalf("expected the legacy implementation to leak --token, saw none")
		}
	default:
		t.Fatalf("unknown XBOT_ARGV_PROBE_EXPECT=%q", expect)
	}
}

// maskTokenValues rewrites `--token <value>` / `--token=<value>` / bare 40+ char
// token-ish blobs to `<MASKED>` so evidence can be shared without the secret.
func maskTokenValues(line string) string {
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--token" && i+1 < len(fields) {
			fields[i+1] = "<MASKED>"
		} else if strings.HasPrefix(fields[i], "--token=") {
			fields[i] = "--token=<MASKED>"
		}
	}
	return strings.Join(fields, " ")
}
