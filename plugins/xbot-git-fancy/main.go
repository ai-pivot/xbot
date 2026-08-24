// Command git-fancy implements the xbot.git-fancy stdio plugin backend.
//
// It is a pure stdio IPC plugin: xbot spawns this binary and drives it via
// JSON-over-stdio (protocol.Run). The frontend Git Fancy view calls
// ctx.rpc.call('xbot.git-fancy.status', ...) which xbot routes to this
// process's WebPluginRPC handler. All git commands execute in the session CWD
// (injected by the server as params.cwd); the plugin itself is stateless.
//
// Supported methods (all read-only):
//   - status    — branch, clean, changes (path/status/±lines), ahead/behind
//   - log       — recent commits (hash/author/relative time/subject)
//   - diff      — unified diff for one path, line-level +/-/ context
//   - branches  — current branch + local branch list
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-pivot/xbot/plugin/protocol"
)

func main() {
	protocol.Run(&protocol.Handler{
		Activate: func(params *protocol.ActivateParams) (*protocol.ActivateResult, error) {
			return &protocol.ActivateResult{Result: "ok"}, nil
		},
		WebPluginRPC: handleWebPluginRPC,
		Deactivate:   func() {},
	})
}

// handleWebPluginRPC dispatches frontend view RPCs to git commands.
func handleWebPluginRPC(p *protocol.WebPluginRPCParams) (*protocol.WebPluginRPCResult, error) {
	if p.Method == "" {
		return rpcErr("method is required"), nil
	}
	// params.cwd is injected by the server (web_plugin_rpc handler) from the
	// session's current directory. Fall back to process CWD for manual testing.
	var params map[string]any
	if len(p.Params) > 0 {
		_ = json.Unmarshal(p.Params, &params)
	}
	cwd, _ := params["cwd"].(string)
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	switch p.Method {
	case "status":
		return gitStatus(cwd)
	case "log":
		limit := 10
		if n, ok := params["limit"].(float64); ok && n > 0 && n <= 100 {
			limit = int(n)
		}
		skip := 0
		if n, ok := params["skip"].(float64); ok && n > 0 {
			skip = int(n)
		}
		return gitLog(cwd, limit, skip)
	case "commit":
		hash, _ := params["hash"].(string)
		if hash == "" {
			return rpcErr("hash is required"), nil
		}
		return gitCommit(cwd, hash)
	case "diff":
		path, _ := params["path"].(string)
		if path == "" {
			return rpcErr("path is required"), nil
		}
		// commit 可选：指定时渲染该 commit 内此文件的 diff（git show），
		// 缺省渲染工作区 diff（HEAD vs working tree）。
		commit, _ := params["commit"].(string)
		return gitDiff(cwd, path, commit)
	case "branches":
		return gitBranches(cwd)
	default:
		return rpcErr(fmt.Sprintf("unknown method: %s", p.Method)), nil
	}
}

func rpcErr(msg string) *protocol.WebPluginRPCResult {
	return &protocol.WebPluginRPCResult{Error: msg}
}

func rpcOK(v any) *protocol.WebPluginRPCResult {
	data, err := json.Marshal(v)
	if err != nil {
		return &protocol.WebPluginRPCResult{Error: err.Error()}
	}
	return &protocol.WebPluginRPCResult{Result: string(data)}
}

// runGit executes a read-only git command in cwd. Returns stdout.
func runGit(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if e, ok := err.(*exec.ExitError); ok {
			ee = e
		} else {
			ee = &exec.ExitError{}
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
	}
	return string(out), nil
}

// isGitRepo reports whether cwd is inside a git work tree.
func isGitRepo(cwd string) bool {
	_, err := runGit(cwd, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// change describes one working-tree change (from porcelain status).
type change struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

func gitStatus(cwd string) (*protocol.WebPluginRPCResult, error) {
	if !isGitRepo(cwd) {
		return rpcOK(map[string]any{
			"repo":    false,
			"error":   "not a git repository",
			"changes": []change{},
		}), nil
	}
	res := struct {
		Repo    bool     `json:"repo"`
		Branch  string   `json:"branch"`
		Clean   bool     `json:"clean"`
		Changes []change `json:"changes"`
		Behind  int      `json:"behind"`
		Ahead   int      `json:"ahead"`
		Error   string   `json:"error,omitempty"`
	}{
		Repo:    true,
		Changes: []change{},
	}
	if branch, err := runGit(cwd, "branch", "--show-current"); err == nil {
		res.Branch = strings.TrimSpace(branch)
	}
	if ab, err := runGit(cwd, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		parts := strings.Fields(ab)
		if len(parts) == 2 {
			res.Ahead, _ = strconv.Atoi(parts[0])
			res.Behind, _ = strconv.Atoi(parts[1])
		}
	}
	out, err := runGit(cwd, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		res.Error = err.Error()
		return rpcOK(res), nil
	}
	res.Clean = strings.TrimSpace(out) == ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		path := strings.TrimSpace(line[3:])
		// Rename entries carry "old -> new" — keep only the new path.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		var c change
		c.Path = path
		switch code {
		case "??":
			c.Status = "U" // untracked
		case " M", "M ":
			c.Status = "M"
		case " A", "A ":
			c.Status = "A"
		case " D", "D ":
			c.Status = "D"
		case "R ", " R":
			c.Status = "R"
		case "C ", " C":
			c.Status = "C"
		default:
			c.Status = strings.TrimSpace(code)
		}
		res.Changes = append(res.Changes, c)
	}
	// Count ± lines per changed path (numstat). Only tracked files produce
	// numstat rows; untracked files have no diff stats.
	if stat, err := runGit(cwd, "diff", "--numstat"); err == nil {
		stats := map[string][2]int{}
		for _, line := range strings.Split(stat, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			added, aErr := strconv.Atoi(fields[0])
			deleted, dErr := strconv.Atoi(fields[1])
			if aErr != nil || dErr != nil {
				continue
			}
			path := strings.Join(fields[2:], " ")
			stats[path] = [2]int{added, deleted}
		}
		for i := range res.Changes {
			if s, ok := stats[res.Changes[i].Path]; ok {
				res.Changes[i].Added = s[0]
				res.Changes[i].Deleted = s[1]
			}
		}
	}
	return rpcOK(res), nil
}

type commit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	When    string `json:"when"`
	Subject string `json:"subject"`
}

// gitLog returns one page of recent commits (skip/limit) plus the total
// commit count so the frontend can offer "load more" (dynamic pagination).
func gitLog(cwd string, limit, skip int) (*protocol.WebPluginRPCResult, error) {
	args := []string{"log", fmt.Sprintf("-%d", limit)}
	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}
	args = append(args, "--pretty=%h|%an|%ar|%s")
	out, err := runGit(cwd, args...)
	if err != nil {
		return rpcOK(map[string]any{"commits": []commit{}, "total": 0}), nil
	}
	commits := []commit{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		commits = append(commits, commit{Hash: parts[0], Author: parts[1], When: parts[2], Subject: parts[3]})
	}
	// Total commit count（HEAD 可达历史总数）驱动前端"加载更多"。
	total := 0
	if n, err := runGit(cwd, "rev-list", "--count", "HEAD"); err == nil {
		total, _ = strconv.Atoi(strings.TrimSpace(n))
	}
	return rpcOK(map[string]any{"commits": commits, "total": total}), nil
}

// commitFile describes one file touched by a commit.
type commitFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// gitCommit returns a commit's detail (hash/author/date/message) plus the
// files it touched (path/status/±lines) — the "commit detail" view data.
func gitCommit(cwd, hash string) (*protocol.WebPluginRPCResult, error) {
	// Detail: %H full hash | %an author | %ae email | %aI ISO date | %B body.
	out, err := runGit(cwd, "show", "-s", "--format=%H%n%an%n%ae%n%aI%n%B", hash)
	if err != nil {
		return rpcOK(map[string]any{"error": err.Error(), "files": []commitFile{}}), nil
	}
	lines := strings.SplitN(strings.TrimRight(out, "\n"), "\n", 5)
	for len(lines) < 5 {
		lines = append(lines, "")
	}
	fullHash, author, email, date, message := lines[0], lines[1], lines[2], lines[3], strings.TrimSpace(lines[4])

	files := []commitFile{}
	// name-status gives per-file status letters (M/A/D/R/C).
	statusByPath := map[string]string{}
	if ns, err := runGit(cwd, "diff-tree", "--no-commit-id", "--name-status", "-r", hash); err == nil {
		for _, line := range strings.Split(ns, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			status := fields[0]
			path := fields[len(fields)-1]
			// Rename/copy rows carry both paths (R100\told\tnew) — keep the
			// new path, same convention as status.
			statusByPath[path] = string(status[0])
		}
	}
	// numstat gives ± line counts per file.
	if stat, err := runGit(cwd, "show", "--numstat", "--format=", hash); err == nil {
		for _, line := range strings.Split(stat, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			added, aErr := strconv.Atoi(fields[0])
			deleted, dErr := strconv.Atoi(fields[1])
			if aErr != nil || dErr != nil {
				continue // binary files report "-"
			}
			path := strings.Join(fields[2:], " ")
			status := statusByPath[path]
			if status == "" {
				status = "M"
			}
			files = append(files, commitFile{Path: path, Status: status, Added: added, Deleted: deleted})
		}
	}
	return rpcOK(map[string]any{
		"hash":    fullHash,
		"short":   fullHash[:min(7, len(fullHash))],
		"author":  author,
		"email":   email,
		"date":    date,
		"message": message,
		"files":   files,
	}), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// diffLine is one line of a parsed unified diff.
type diffLine struct {
	Kind    string `json:"kind"`     // "hunk" | "add" | "del" | "ctx" | "meta"
	OldLine int    `json:"old_line"` // 0 for add/meta
	NewLine int    `json:"new_line"` // 0 for del/meta
	Text    string `json:"text"`
}

func gitDiff(cwd, path, commitHash string) (*protocol.WebPluginRPCResult, error) {
	// Commit-scoped diff: original = parent version, modified = commit version.
	// Either side may be missing (added/deleted file, root commit) → empty string.
	if commitHash != "" {
		modified := ""
		if out, err := runGit(cwd, "show", fmt.Sprintf("%s:%s", commitHash, path)); err == nil {
			modified = out
		}
		original := ""
		if out, err := runGit(cwd, "show", fmt.Sprintf("%s^:%s", commitHash, path)); err == nil {
			original = out
		}
		out, err := runGit(cwd, "show", "--no-color", commitHash, "--", path)
		if err != nil {
			return nil, err
		}
		lines := parseUnifiedDiff(out)
		return rpcOK(map[string]any{
			"path":     path,
			"commit":   commitHash,
			"content":  out,
			"lines":    lines,
			"original": original,
			"modified": modified,
			"adds":     countKind(lines, "add"),
			"dels":     countKind(lines, "del"),
		}), nil
	}
	// Untracked files: original = empty (all additions), modified = file content.
	if _, err := runGit(cwd, "ls-files", "--error-unmatch", "--", path); err != nil {
		if content, rErr := os.ReadFile(filepath.Join(cwd, path)); rErr == nil {
			lines := []diffLine{}
			text := string(content)
			for i, l := range strings.Split(text, "\n") {
				if i == strings.Count(text, "\n") && l == "" {
					continue // trailing newline — skip phantom empty line
				}
				lines = append(lines, diffLine{Kind: "add", NewLine: i + 1, Text: "+" + l})
			}
			return rpcOK(map[string]any{
				"path":      path,
				"content":   text,
				"lines":     lines,
				"untracked": true,
				"original":  "",
				"modified":  text,
				"adds":      len(lines),
				"dels":      0,
			}), nil
		}
	}
	// Tracked working-tree diff: original = HEAD version, modified = file content.
	original := ""
	if out, err := runGit(cwd, "show", "HEAD:"+path); err == nil {
		original = out
	}
	modified := ""
	if content, rErr := os.ReadFile(filepath.Join(cwd, path)); rErr == nil {
		modified = string(content)
	}
	out, err := runGit(cwd, "diff", "--no-color", "--", path)
	if err != nil {
		return nil, err
	}
	lines := parseUnifiedDiff(out)
	return rpcOK(map[string]any{
		"path":     path,
		"content":  out,
		"lines":    lines,
		"original": original,
		"modified": modified,
		"adds":     countKind(lines, "add"),
		"dels":     countKind(lines, "del"),
	}), nil
}

// countKind counts diff lines of a given kind (add/del) for ± statistics.
func countKind(lines []diffLine, kind string) int {
	n := 0
	for _, l := range lines {
		if l.Kind == kind {
			n++
		}
	}
	return n
}

// parseUnifiedDiff splits a `git diff` output into line-level entries with
// old/new line numbers so the frontend can render VSC-style +/- coloring.
func parseUnifiedDiff(diff string) []diffLine {
	lines := []diffLine{}
	if diff == "" {
		return lines
	}
	oldLine, newLine := 0, 0
	for _, raw := range strings.Split(diff, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "@@"):
			// @@ -oldStart,oldCount +newStart,newCount @@
			parts := strings.Split(line, " ")
			if len(parts) >= 3 {
				oldLine, _ = parseHunkStart(parts[1])
				newLine, _ = parseHunkStart(parts[2])
			}
			lines = append(lines, diffLine{Kind: "hunk", Text: line})
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "new file") || strings.HasPrefix(line, "deleted file"):
			lines = append(lines, diffLine{Kind: "meta", Text: line})
		case strings.HasPrefix(line, "+"):
			lines = append(lines, diffLine{Kind: "add", NewLine: newLine, Text: line})
			newLine++
		case strings.HasPrefix(line, "-"):
			lines = append(lines, diffLine{Kind: "del", OldLine: oldLine, Text: line})
			oldLine++
		case strings.HasPrefix(line, "\\"):
			lines = append(lines, diffLine{Kind: "meta", Text: line})
		default:
			if oldLine > 0 {
				oldLine++
			}
			if newLine > 0 {
				newLine++
			}
			lines = append(lines, diffLine{Kind: "ctx", OldLine: oldLine, NewLine: newLine, Text: line})
		}
	}
	return lines
}

// parseHunkStart parses "-3,5" or "+3" → starting line number.
func parseHunkStart(s string) (int, bool) {
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "+")
	if i := strings.Index(s, ","); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func gitBranches(cwd string) (*protocol.WebPluginRPCResult, error) {
	current, _ := runGit(cwd, "branch", "--show-current")
	out, err := runGit(cwd, "branch", "--no-color")
	if err != nil {
		return rpcOK(map[string]any{"current": "", "branches": []string{}}), nil
	}
	branches := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		if line != "" {
			branches = append(branches, line)
		}
	}
	sort.Strings(branches)
	return rpcOK(map[string]any{"current": strings.TrimSpace(current), "branches": branches}), nil
}

// keep time import for potential future use (compile-time stable).
var _ = time.Now
