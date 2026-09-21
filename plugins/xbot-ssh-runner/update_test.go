package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ============================================================================
// A. 自动更新（VSC 语义：后台暂存，下次启动生效）—— 判别性单测
//
// 变异自证：
//   - 把 needsUpdate 改成恒 false ⇒ TestNeedsUpdate 的"differ"/"missing" 用例红；
//   - 把 needsUpdate 改成恒 true ⇒ "equal"/"latest unknown" 用例红；
//   - 去掉 fetchChecksums 的状态码检查 ⇒ TestFetchChecksums 的 404 用例红。
// ============================================================================

func TestNeedsUpdate(t *testing.T) {
	cases := []struct {
		name              string
		installed, latest string
		want              bool
	}{
		{"拿不到期望值 ⇒ 绝不动文件（基础设施抖动不得乱改二进制）", "abc", "", false},
		{"未安装（远端 sha 为空）⇒ 需要", "", "abc", true},
		{"相同 ⇒ 无需更新", "abc123", "abc123", false},
		{"相同（大小写不敏感）⇒ 无需更新", "ABC123", "abc123", false},
		{"不同 ⇒ 需要更新", "abc123", "def456", true},
		{"两侧空白被忽略", "  abc123 \n", "abc123", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsUpdate(c.installed, c.latest); got != c.want {
				t.Fatalf("needsUpdate(%q,%q) = %v, want %v", c.installed, c.latest, got, c.want)
			}
		})
	}
}

func TestInstalledShaScript(t *testing.T) {
	script := installedShaScript("/home/ubuntu/.local/bin/xbot-runner")
	if !strings.Contains(script, "sha256sum") {
		t.Fatalf("must compute sha256, got: %s", script)
	}
	if !strings.Contains(script, "/home/ubuntu/.local/bin/xbot-runner") {
		t.Fatalf("must target the installed binary, got: %s", script)
	}
	// 缺失/不可读时**不得失败**（否则"未安装"会被当成检查错误而不是"需要安装"）。
	if !strings.Contains(script, "[ -f ") {
		t.Fatalf("must tolerate a missing binary (no hard failure), got: %s", script)
	}
}

func TestFetchChecksums(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runner/checksums.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("deadbeef  xbot-runner-linux-amd64\n"))
	}))
	defer srv.Close()

	body, err := fetchChecksums(context.Background(), srv.URL+"/runner")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(body, "xbot-runner-linux-amd64") {
		t.Fatalf("body = %q", body)
	}

	if _, err := fetchChecksums(context.Background(), srv.URL+"/nope"); err == nil {
		t.Fatal("HTTP 404 must be an error (never stage an update from a bogus source)")
	}
	if _, err := fetchChecksums(context.Background(), ""); err == nil {
		t.Fatal("empty download_base must be an error")
	}
}

// parseChecksums 是 provision 与自动更新共用的解析点：拿不到期望 sha 必须报错，绝不猜。
// 注意：真实 checksums.txt 里是 64 位 sha256，解析器会**严格校验长度/字符集**
// （因此伪造的短串必须被拒 —— 这正是下面第二条断言的价值）。
func TestParseChecksumsForUpdate(t *testing.T) {
	const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body := shaA + "  xbot-runner-linux-amd64\n" + shaB + "  xbot-runner-darwin-arm64\n"

	got, err := parseChecksums(body, "xbot-runner-linux-amd64")
	if err != nil || got != shaA {
		t.Fatalf("got (%q, %v), want (%q, nil)", got, err, shaA)
	}
	if _, err := parseChecksums(body, "xbot-runner-linux-arm64"); err == nil {
		t.Fatal("asset missing from checksums.txt must be an error (no guessing)")
	}
	// 非 sha256（长度/字符集不对）必须被拒 —— 自动更新宁可不动，也不能拿脏值替换二进制。
	if _, err := parseChecksums("aaaa  xbot-runner-linux-amd64\n", "xbot-runner-linux-amd64"); err == nil {
		t.Fatal("a malformed (too short) sha must be rejected")
	}
}
