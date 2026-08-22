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
		if n, ok := params["limit"].(float64); ok && n > 0 && n <= 50 {
			limit = int(n)
		}
		return gitLog(cwd, limit)
	case "diff":
		path, _ := params["path"].(string)
		if path == "" {
			return rpcErr("path is required"), nil
		}
		return gitDiff(cwd, path)
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

func gitLog(cwd string, limit int) (*protocol.WebPluginRPCResult, error) {
	out, err := runGit(cwd, "log", fmt.Sprintf("-%d", limit), "--pretty=%h|%an|%ar|%s")
	if err != nil {
		return rpcOK(map[string]any{"commits": []commit{}}), nil
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
	return rpcOK(map[string]any{"commits": commits}), nil
}

// diffLine is one line of a parsed unified diff.
type diffLine struct {
	Kind    string `json:"kind"`     // "hunk" | "add" | "del" | "ctx" | "meta"
	OldLine int    `json:"old_line"` // 0 for add/meta
	NewLine int    `json:"new_line"` // 0 for del/meta
	Text    string `json:"text"`
}

func gitDiff(cwd, path string) (*protocol.WebPluginRPCResult, error) {
	// Untracked files have no HEAD diff — render the whole file as additions
	// (VSC behavior). Detect via `git ls-files --error-unmatch`.
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
			}), nil
		}
	}
	out, err := runGit(cwd, "diff", "--no-color", "--", path)
	if err != nil {
		return nil, err
	}
	return rpcOK(map[string]any{
		"path":    path,
		"content": out,
		"lines":   parseUnifiedDiff(out),
	}), nil
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
