package main

import (
	"bytes"
	"strings"
	"testing"

	"xbot/version"
)

// ⛔ CLI 未知 flag 必须【报错退出】，绝不静默忽略后启动会话（用户报告，2026-10-12：
// `xbot-cli --version` 曾会**启动 xbot**）。根因是 flag 循环的 `default` 只把非 `-` 前缀
// 参数当 prompt、其余静默吞掉 ⇒ 没有对应 case 的 flag 被"当没看见"，随后照常走默认路径。
//
// 契约由 **纯函数** `cliEarlyExitFlag` + 它在 main() 里**早于任何会话初始化**的调用点共同
// 保证（main() 本身无法单测）。⚠️ 不采用「构建真实二进制再 exec」的测法：CI 冷缓存下首次
// exec 一个刚写完的 ~100MB 二进制会超时（本 PR 的 CI 实测 `--version` 67s 未退出），
// 那是**测试实现**不可靠，不是被测行为的问题。
func TestCLIEarlyExitFlag_VersionPrintsVersionOnly(t *testing.T) {
	for _, flag := range []string{"--version", "-v", "-V"} {
		var out bytes.Buffer
		handled, code := cliEarlyExitFlag([]string{flag}, &out)
		if !handled || code != 0 {
			t.Fatalf("`%s` 必须 handled=true、code=0（打印后退出），got handled=%v code=%d", flag, handled, code)
		}
		// stdout 必须**恰好**是版本号一行（不带 banner / 不带 "Mode: resuming …"）
		if got := strings.TrimSpace(out.String()); got != version.Version {
			t.Fatalf("`%s` 的输出必须只有版本号 %q，got %q", flag, version.Version, got)
		}
		if strings.Count(strings.TrimRight(out.String(), "\n"), "\n") != 0 {
			t.Fatalf("`%s` 只能输出一行，got %q", flag, out.String())
		}
	}
}

func TestCLIEarlyExitFlag_HelpPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	handled, code := cliEarlyExitFlag([]string{"--help"}, &out)
	if !handled || code != 0 {
		t.Fatalf("`--help` 必须 handled=true、code=0，got handled=%v code=%d", handled, code)
	}
	if !strings.HasPrefix(out.String(), "Usage: xbot-cli") {
		t.Fatalf("`--help` 必须打印用法（且不带 banner），got %q", out.String())
	}
	// 用法里必须能查到 --version（否则用户无从得知）
	if !strings.Contains(out.String(), "--version") {
		t.Fatalf("用法必须列出 --version，got %q", out.String())
	}
}

// 未知 flag / 普通参数 / 无参数 ⇒ **不得**被当作"打印即退出"（必须继续走正常路径：
// 未知 `-` flag 会在 flag 循环里以 exit 2 报错；其余是正常启动/单条 prompt）。
func TestCLIEarlyExitFlag_DoesNotSwallowOtherArgs(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{},
		{"--versoin"},       // 拼错：必须落到未知 flag 报错，而不是被这里吞掉
		{"--new"},           // 正常启动 flag
		{"do something"},    // 位置参数 = prompt
		{"setup", "--help"}, // 子命令帮助：不能在这里被拦
		{"serve"},
	} {
		var out bytes.Buffer
		handled, code := cliEarlyExitFlag(args, &out)
		if handled || code != 0 {
			t.Fatalf("args=%v 必须 handled=false（继续走正常路径），got handled=%v code=%d out=%q", args, handled, code, out.String())
		}
		if out.Len() != 0 {
			t.Fatalf("args=%v 必须无输出，got %q", args, out.String())
		}
	}
}
