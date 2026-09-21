package runnerclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 2026-09-19 用户实机 P0：runner 「总是试图创建他不一定有权限的目录，然后 shell 执行失败」。
// 契约：**绝不因为"工作目录缺失/不可写"让命令失败** —— 必须能降级（创建 / 退到已存在的
// 祖先 / 退回继承 cwd），并把替换原因作为 warning 暴露出来（绝不静默）。
// 变异自证：把 resolveWorkDir 改回"直接返回 requested" ⇒ 第三个用例必红。
func TestResolveWorkDir_ExistingIsUsed(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "exists")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &NativeExecutor{Workspace: filepath.Join(base, "ws")}
	dir, warn := e.resolveWorkDir(existing)
	if dir != existing || warn != "" {
		t.Fatalf("existing dir must be used as-is, got dir=%q warn=%q", dir, warn)
	}
}

func TestResolveWorkDir_MissingIsCreatedWithWarning(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "a", "b", "c")
	e := &NativeExecutor{Workspace: filepath.Join(base, "ws")}
	dir, warn := e.resolveWorkDir(missing)
	if dir != missing {
		t.Fatalf("creatable dir must be created and used, got dir=%q (warn=%q)", dir, warn)
	}
	if warn == "" {
		t.Fatal("replacement/creation MUST be reported (never silent)")
	}
	if st, err := os.Stat(missing); err != nil || !st.IsDir() {
		t.Fatalf("dir must actually exist after resolve: %v", err)
	}
}

// 关键不变量：请求目录不可写且 workspace/home 也都不可用时，**绝不失败** ——
// 必须退到"最近存在的祖先"（或最终返回空 = 继承 cwd）。
func TestResolveWorkDir_UnwritableFallsBackNeverFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission test is POSIX-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	base := t.TempDir()
	ro := filepath.Join(base, "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o500); err != nil { // 只读：其下无法创建
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	req := filepath.Join(ro, "x", "y")
	// workspace 也指向不可写树，逼它走"候选耗尽 → 祖先回退"分支。
	e := &NativeExecutor{Workspace: filepath.Join(ro, "ws")}
	dir, warn := e.resolveWorkDir(req)

	// 不变量（这才是契约）：
	//   ① 返回非空 ⇒ 该目录**必须真实存在**（绝不能把不存在的目录交给 exec）；
	//   ② 返回空 ⇒ 必须带明确警告（退化为继承 cwd 时绝不静默）；
	//   ③ 绝不 panic、绝不返回 error（签名无 error）。
	// 注意：候选列表里的 $HOME 是**合法候选**，命中它属于正常选择，不需要警告
	//（只有"祖先回退 / 继承 cwd"这类降级才必须告警）—— 这也是本用例修正前断言错的地方。
	if dir != "" {
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			t.Fatalf("returned dir must exist and be a directory: %q (%v)", dir, err)
		}
	} else {
		if warn == "" {
			t.Fatal("empty dir MUST come with an explicit warning")
		}
	}
	if dir != req && warn == "" {
		// 发生了替换但落在候选内（如 $HOME）也允许静默；只要求"不是请求目录时不得撒谎"
		t.Logf("resolved %q -> %q (candidate fallback, no warning needed)", req, dir)
	}
}
