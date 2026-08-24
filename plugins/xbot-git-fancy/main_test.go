package main

import (
	"encoding/json"
	"fmt"
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
	// 原生 diff editor 需要两侧完整内容。
	if out["original"] != "hello\nworld\n" {
		t.Errorf("original = %q, want HEAD version", out["original"])
	}
	if out["modified"] != "hello\nworld modified\n" {
		t.Errorf("modified = %q, want working-tree content", out["modified"])
	}
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

// multiCommitRepo builds a repo with N commits (msg1..msgN) so pagination
// tests have real history to page through.
func multiCommitRepo(t *testing.T, n int) string {
	t.Helper()
	dir := tempGitRepo(t) // has 1 commit "init"
	for i := 1; i <= n; i++ {
		content := fmt.Sprintf("line %d\n", i)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte(content), 0o644)
		runGitDir(dir, "add", ".")
		runGitDir(dir, "commit", "-q", "-m", fmt.Sprintf("msg%d", i))
	}
	return dir
}

func TestLogPagination(t *testing.T) {
	dir := multiCommitRepo(t, 5) // init + 5 = 6 commits total

	// Page 1: first 2 commits (newest first) + total count.
	out := call(t, dir, "log", map[string]any{"cwd": dir, "limit": 2})
	commits := out["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	if out["total"].(float64) != 6 {
		t.Fatalf("total = %v, want 6", out["total"])
	}
	first := commits[0].(map[string]any)
	if first["subject"] != "msg5" {
		t.Errorf("first subject = %v, want msg5 (newest first)", first["subject"])
	}

	// Page 2: skip=2 returns msg3, msg2.
	out = call(t, dir, "log", map[string]any{"cwd": dir, "limit": 2, "skip": 2})
	commits = out["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits on page 2, got %d", len(commits))
	}
	if commits[0].(map[string]any)["subject"] != "msg3" {
		t.Errorf("page2 first subject = %v, want msg3", commits[0].(map[string]any)["subject"])
	}

	// Skip beyond the end returns an empty page.
	out = call(t, dir, "log", map[string]any{"cwd": dir, "limit": 2, "skip": 6})
	commits = out["commits"].([]any)
	if len(commits) != 0 {
		t.Errorf("expected 0 commits past end, got %d", len(commits))
	}
}

func TestCommitDetail(t *testing.T) {
	dir := multiCommitRepo(t, 1) // init + msg1 = 2 commits
	// Find the "msg1" hash from log.
	out := call(t, dir, "log", map[string]any{"cwd": dir, "limit": 1})
	hash := out["commits"].([]any)[0].(map[string]any)["hash"].(string)

	detail := call(t, dir, "commit", map[string]any{"cwd": dir, "hash": hash})
	if detail["subject"] != nil { // detail carries full message, not subject
		t.Fatalf("unexpected subject field in commit detail: %v", detail)
	}
	if detail["message"] != "msg1" {
		t.Errorf("message = %v, want msg1", detail["message"])
	}
	if detail["author"] != "t" {
		t.Errorf("author = %v, want t", detail["author"])
	}
	if detail["date"] == "" {
		t.Error("date should be non-empty ISO timestamp")
	}
	files := detail["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(files), detail["files"])
	}
	f := files[0].(map[string]any)
	if f["path"] != "f1.txt" {
		t.Errorf("file path = %v, want f1.txt", f["path"])
	}
	if f["status"] != "A" {
		t.Errorf("file status = %v, want A (added in this commit)", f["status"])
	}
	if f["added"].(float64) != 1 {
		t.Errorf("file added = %v, want 1", f["added"])
	}
}

func TestCommitDetailMissingHash(t *testing.T) {
	dir := tempGitRepo(t)
	resp, _ := handleWebPluginRPC(&protocol.WebPluginRPCParams{
		Method: "commit",
		Params: mustJSON(t, map[string]any{"cwd": dir}),
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing hash")
	}
}

func TestDiffWithCommit(t *testing.T) {
	dir := multiCommitRepo(t, 1)
	// Modify f1.txt AFTER msg1 — commit-scoped diff must show the committed
	// content, not the working-tree change.
	os.WriteFile(filepath.Join(dir, "f1.txt"), []byte("working tree change\n"), 0o644)

	out := call(t, dir, "log", map[string]any{"cwd": dir, "limit": 1})
	hash := out["commits"].([]any)[0].(map[string]any)["hash"].(string)

	got := call(t, dir, "diff", map[string]any{"cwd": dir, "path": "f1.txt", "commit": hash})
	content := got["content"].(string)
	if !strings.Contains(content, "line 1") {
		t.Errorf("commit diff should show committed content 'line 1': %q", content)
	}
	if strings.Contains(content, "working tree change") {
		t.Errorf("commit diff must NOT include working-tree changes: %q", content)
	}
	if got["commit"] != hash {
		t.Errorf("commit echo = %v, want %v", got["commit"], hash)
	}
	// Commit-scoped diff parses into line entries too.
	lines := got["lines"].([]any)
	if len(lines) == 0 {
		t.Error("commit diff lines should be non-empty")
	}
}

func TestDiffWithRootCommit(t *testing.T) {
	// Root commit has no parent — `git show <hash> -- path` still renders the
	// full-file addition diff against the empty tree.
	dir := tempGitRepo(t) // single "init" commit adding a.txt
	out := call(t, dir, "log", map[string]any{"cwd": dir, "limit": 1})
	hash := out["commits"].([]any)[0].(map[string]any)["hash"].(string)

	got := call(t, dir, "diff", map[string]any{"cwd": dir, "path": "a.txt", "commit": hash})
	lines := got["lines"].([]any)
	hasAdd := false
	for _, l := range lines {
		if l.(map[string]any)["kind"] == "add" {
			hasAdd = true
		}
	}
	if !hasAdd {
		t.Errorf("root-commit diff should contain add lines: %v", lines)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// TestManifestPermissions guards the frontend capability declaration.
// The web panel's openDiffTab/openCommitTab go through ctx.ui.openViewTab —
// a plugin that does NOT declare the "ui" permission gets ctx.ui = undefined
// at runtime (buildContext gates by permissions), making file clicks silently
// no-op. This regressed once; the test keeps the manifest honest.
func TestManifestPermissions(t *testing.T) {
	data, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var m struct {
		Permissions []string `json:"permissions"`
		Web         struct {
			Contributes []struct {
				ID      string `json:"id"`
				Dynamic bool   `json:"dynamic"`
			} `json:"contributes"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	perms := map[string]bool{}
	for _, p := range m.Permissions {
		perms[p] = true
	}
	if !perms["rpc"] {
		t.Errorf("permissions must contain \"rpc\" (backend data), got %v", m.Permissions)
	}
	if !perms["ui"] {
		t.Errorf("permissions must contain \"ui\" (openViewTab for diff/commit tabs), got %v", m.Permissions)
	}
	dynamic := map[string]bool{}
	for _, c := range m.Web.Contributes {
		if c.Dynamic {
			dynamic[c.ID] = true
		}
	}
	// diff 视图已移除（改为宿主原生 DiffEditor tab，ctx.ui.openDiffTab）；
	// commit 详情仍是插件 dynamic view。
	if !dynamic["xbot.git-fancy.commit"] {
		t.Errorf("web.contributes must declare dynamic view commit, got %v", m.Web.Contributes)
	}
	if dynamic["xbot.git-fancy.diff"] {
		t.Errorf("web.contributes must NOT declare diff view (native DiffEditor via openDiffTab now), got %v", m.Web.Contributes)
	}
}
