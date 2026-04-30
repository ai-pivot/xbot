package gitprstat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/plugin"
)

// GitPRPlugin monitors git operations via hooks and displays
// branch / PR status in the status bar. This demonstrates the
// hook→UI bridge: when a hook detects a git command, the handler
// calls pluginContext.UpdateWidget() to push new content to the TUI.
type GitPRPlugin struct {
	mu       sync.Mutex
	pctx     plugin.PluginContext // captured in Activate()
	branch   string
	lastOp   string
	lastTime time.Time
}

// Manifest returns the plugin manifest.
func (p *GitPRPlugin) Manifest() plugin.PluginManifest {
	return plugin.PluginManifest{
		Name:        "git-pr-status",
		Version:     "0.1.0",
		Description: "Monitors git operations via hooks and displays branch/PR status in the status bar.",
		Author:      "xbot",
		Runtime:     plugin.RuntimeNative,
		Permissions: []string{
			plugin.PermHooksSubscribe,
			plugin.PermUIContribute,
		},
		Contributes: &plugin.PluginContributes{
			Hooks: []plugin.HookContribution{
				{
					Event:   "PostToolUse",
					Matcher: "Shell*",
				},
			},
			UI: []plugin.UISlotContribution{
				{
					ID:          "git-branch",
					Slot:        "statusBarRight",
					Priority:    10,
					Description: "Shows current git branch and last git operation",
				},
			},
		},
	}
}

// Activate saves the PluginContext and registers UI + hooks.
func (p *GitPRPlugin) Activate(ctx plugin.PluginContext) error {
	p.pctx = ctx

	if err := ctx.ContributeUI("git-branch", "statusBarRight", p, 10); err != nil {
		return fmt.Errorf("contribute UI: %w", err)
	}
	if err := ctx.OnPostToolUse("Shell*", p.onPostToolUse); err != nil {
		return fmt.Errorf("register hook: %w", err)
	}

	ctx.Logger().Info("git-pr-status activated with hook→UI bridge")
	return nil
}

func (p *GitPRPlugin) Deactivate(ctx plugin.PluginContext) error {
	ctx.Logger().Info("git-pr-status deactivated")
	return nil
}

// ---------------------------------------------------------------------------
// UIWidget implementation
// ---------------------------------------------------------------------------

// Render returns widget spans for the status bar.
func (p *GitPRPlugin) Render(width int) []plugin.WidgetSpan {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.branch == "" {
		return []plugin.WidgetSpan{
			{Text: "git: —", Style: plugin.StyleDim},
		}
	}

	spans := []plugin.WidgetSpan{
		{Text: "git:", Style: plugin.StyleDim},
		{Text: p.branch, Style: plugin.StyleAccent},
	}
	if p.lastOp != "" && time.Since(p.lastTime) < 5*time.Second {
		spans = append(spans,
			plugin.WidgetSpan{Text: " " + p.lastOp, Style: plugin.StyleInfo},
		)
	}
	return spans
}

// ---------------------------------------------------------------------------
// Hook handler — the hook→UI bridge
// ---------------------------------------------------------------------------

// onPostToolUse fires after every Shell tool execution. When it detects a git
// command, it updates the internal state and pushes a widget re-render via
// p.pctx.UpdateWidget().
func (p *GitPRPlugin) onPostToolUse(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
	cmd := extractShellCommand(payload.ToolInput)
	if cmd == "" || !isGitCommand(cmd) {
		return nil, nil
	}

	// Update internal state
	p.mu.Lock()
	p.lastTime = time.Now()
	p.lastOp = summarizeGitCommand(cmd)
	if branch := detectBranch(cmd, payload.Extra); branch != "" {
		p.branch = branch
	}
	p.mu.Unlock()

	// 🔑 KEY: push widget update to the TUI
	if p.pctx != nil {
		_ = p.pctx.UpdateWidget("git-branch") // ignore error if not yet rendered
	}

	return nil, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func extractShellCommand(input string) string {
	// Shell tool input: {"command": "git status"}
	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "git ") {
			return trimmed
		}
	}
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "git ") {
		return trimmed
	}
	return ""
}

func isGitCommand(cmd string) bool {
	parts := strings.Fields(cmd)
	return len(parts) >= 2 && parts[0] == "git"
}

func summarizeGitCommand(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) < 2 {
		return cmd
	}
	subCmd := parts[1]
	switch subCmd {
	case "status":
		return "status"
	case "add":
		return "stage"
	case "commit":
		return "commit"
	case "push":
		return "push"
	case "pull":
		return "pull"
	case "branch":
		return "branch"
	case "checkout":
		if len(parts) > 2 {
			return "switch→" + parts[2]
		}
		return "checkout"
	case "merge":
		return "merge"
	case "rebase":
		return "rebase"
	default:
		return subCmd
	}
}

// detectBranch tries to extract the current branch from tool output in Extra.
func detectBranch(cmd string, extra map[string]any) string {
	output, _ := extra["output"].(string)
	if output == "" {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "On branch ") {
			return strings.TrimPrefix(line, "On branch ")
		}
		if strings.HasPrefix(line, "* ") {
			return strings.TrimPrefix(line, "* ")
		}
	}
	return ""
}
