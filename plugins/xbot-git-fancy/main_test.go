package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-pivot/xbot/plugin/protocol"
)

// helper: run the handler against a temp git repo.
func tempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := runGitDir(dir, args...)
		if cmd != "" {
			t.Fatalf("git %v failed: %s", args, cmd)
		}
		return ""
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644)
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	return dir
}

func runGitDir(dir string, args ...string) string {
	out, err := runGit(dir, args...)
	if err != nil {
		return err.Error() + ": " + out
	}
	return ""
}

func call(t *testing.T, cwd, method string, params map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	p := &protocol.WebPluginRPCParams{Method: method, Params: raw}
	resp, err := handleWebPluginRPC(p)
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("method %s error: %s", method, resp.Error)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.Result), &out); err != nil {
		t.Fatalf("bad result JSON: %v", err)
	}
	return out
}

func TestStatus(t *testing.T) {
	dir := tempGitRepo(t)
	// create an untracked file + a modified file
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\nmodified\n"), 0o644)

	out := call(t, dir, "status", map[string]any{"cwd": dir})
	if out["repo"] != true {
		t.Fatalf("repo flag wrong: %v", out)
	}
	if out["branch"] != "main" {
		t.Fatalf("branch wrong: %v", out["branch"])
	}
	if out["clean"] != false {
		t.Fatalf("clean should be false: %v", out)
	}
	changes := out["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %v", len(changes), out)
	}
	found := map[string]string{}
	for _, c := range changes {
		m := c.(map[string]any)
		found[m["path"].(string)] = m["status"].(string)
	}
	if found["a.txt"] != "M" {
		t.Errorf("a.txt status = %v, want M", found["a.txt"])
	}
	if found["new.txt"] != "U" {
		t.Errorf("new.txt status = %v, want U", found["new.txt"])
	}
	// a.txt should have +1 (1 added line: "modified")
	for _, c := range changes {
		m := c.(map[string]any)
		if m["path"] == "a.txt" && m["added"].(float64) != 1 {
			t.Errorf("a.txt added = %v, want 1", m["added"])
		}
	}
}

func TestStatusNotRepo(t *testing.T) {
	dir := t.TempDir()
	out := call(t, dir, "status", map[string]any{"cwd": dir})
	if out["repo"] != false {
		t.Fatalf("repo flag should be false for non-repo, got %v", out)
	}
}

func TestLog(t *testing.T) {
	dir := tempGitRepo(t)
	out := call(t, dir, "log", map[string]any{"cwd": dir, "limit": 10})
	commits := out["commits"].([]any)
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	c := commits[0].(map[string]any)
	if !strings.HasPrefix(c["hash"].(string), "a") && c["hash"].(string) == "" {
		t.Fatalf("bad hash: %v", c["hash"])
	}
	if c["subject"] != "init" {
		t.Errorf("subject = %v, want init", c["subject"])
	}
}

func TestDiff(t *testing.T) {
	dir := tempGitRepo(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld modified\n"), 0o644)
	out := call(t, dir, "diff", map[string]any{"cwd": dir, "path": "a.txt"})
	content := out["content"].(string)
	if !strings.Contains(content, "world modified") && !strings.Contains(content, "-world") {
		t.Errorf("diff content missing changes: %q", content)
	}
	lines := out["lines"].([]any)
	hasAdd, hasDel, hasCtx := false, false, false
	for _, l := range lines {
		kind := l.(map[string]any)["kind"].(string)
		switch kind {
		case "add":
			hasAdd = true
		case "del":
			hasDel = true
		case "ctx":
			hasCtx = true
		}
	}
	if !hasAdd || !hasDel || !hasCtx {
		t.Errorf("diff lines missing kinds: add=%v del=%v ctx=%v\n%v", hasAdd, hasDel, hasCtx, lines)
	}
}

func TestDiffUntracked(t *testing.T) {
	dir := tempGitRepo(t)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("line1\nline2\n"), 0o644)
	out := call(t, dir, "diff", map[string]any{"cwd": dir, "path": "new.txt"})
	if out["untracked"] != true {
		t.Fatalf("expected untracked flag, got %v", out)
	}
	lines := out["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines for 2-line untracked file, got %d", len(lines))
	}
	for _, l := range lines {
		if l.(map[string]any)["kind"] != "add" {
			t.Errorf("untracked line kind = %v, want add", l.(map[string]any)["kind"])
		}
	}
}

func TestBranches(t *testing.T) {
	dir := tempGitRepo(t)
	out := call(t, dir, "branches", map[string]any{"cwd": dir})
	if out["current"] != "main" {
		t.Fatalf("current = %v, want main", out["current"])
	}
}

func TestUnknownMethod(t *testing.T) {
	resp, _ := handleWebPluginRPC(&protocol.WebPluginRPCParams{Method: "nope"})
	if resp.Error == "" {
		t.Fatal("expected error for unknown method")
	}
}

func TestParseHunkStart(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"-3,5", 3},
		{"+3", 3},
		{"-1", 1},
	}
	for _, c := range cases {
		if got, _ := parseHunkStart(c.in); got != c.want {
			t.Errorf("parseHunkStart(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
