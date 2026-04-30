package plugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// ScriptRuntime — language-agnostic plugin via external scripts
// ---------------------------------------------------------------------------

type scriptRuntimeFactory struct{}

// NewScriptRuntime returns a RuntimeFactory for script-based plugins.
func NewScriptRuntime() RuntimeFactory {
	return &scriptRuntimeFactory{}
}

func (f *scriptRuntimeFactory) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	if manifest.Entry == "" {
		return nil, fmt.Errorf("script plugin %s: entry command is required", manifest.ID)
	}
	if len(manifest.Contributes.UI) == 0 {
		return nil, fmt.Errorf("script plugin %s: at least one ui contribution required", manifest.ID)
	}
	return &scriptPlugin{
		manifest: *manifest,
		dir:      dir,
	}, nil
}

// scriptPlugin implements Plugin for external scripts.
type scriptPlugin struct {
	manifest PluginManifest
	dir      string

	cancel    context.CancelFunc // stops the periodic refresh loop
	triggerCh chan struct{}      // signals hook-triggered instant runs

	// Per-workDir output cache — each CLI window (different workDir) sees
	// its own git branch, not the branch of whichever window last refreshed.
	outputMu sync.RWMutex
	outputs  map[string]string // workDir → last script output

	// Pending workDirs from OnWorkDirChanged that haven't been processed yet.
	// Prevents multi-session races where session B's Cd overwrites pctx before
	// session A's trigger is processed.
	pendingMu   sync.Mutex
	pendingDirs map[string]struct{} // workDirs to refresh on next runAndUpdate

	// Last hook payload data — stored by triggerFn for env injection in runScript.
	lastHookMu sync.RWMutex
	lastHook   *HookPayload // may be nil if not triggered by a hook
	// NOTE: rapid triggers overwrite lastHook — script only sees the latest event.

	pctx      PluginContext   // captured in Activate for UpdateWidget
	widgetReg *WidgetRegistry // captured in Activate for NotifyUpdated (no runtime type assertion)

	// Synchronous hint content: when a plugin contributes to the "toolHint" zone,
	// the hook trigger runs the script synchronously and stores the markdown output.
	// The engine reads this immediately after the PostToolUse hook fires.
	hintMu       sync.RWMutex
	hintContent  string // last hint output from synchronous trigger
	isHintPlugin bool   // true if plugin has any toolHint zone widget
}

func (p *scriptPlugin) Manifest() PluginManifest {
	return p.manifest
}

// GetHintContent returns the last hint output from a synchronous trigger.
// Used by the engine to include markdown hints in ToolProgress.
func (p *scriptPlugin) GetHintContent() string {
	p.hintMu.RLock()
	defer p.hintMu.RUnlock()
	return p.hintContent
}

func (p *scriptPlugin) Activate(ctx PluginContext) error {
	p.pctx = ctx

	// Capture WidgetRegistry at activation time to avoid runtime type assertion.
	if impl, ok := ctx.(*pluginContextImpl); ok {
		p.widgetReg = impl.getWidgetRegistry()
	}

	// Register UI widgets declared in the manifest
	for _, ui := range p.manifest.Contributes.UI {
		if err := ctx.ContributeUI(ui.ID, ui.Slot, p, ui.Priority); err != nil {
			return fmt.Errorf("contribute widget %q: %w", ui.ID, err)
		}
		if ui.Slot == "toolHint" {
			p.isHintPlugin = true
		}
	}

	// Start periodic refresh loop — use the shortest interval across all widgets
	interval := 30 * time.Second // default
	for _, ui := range p.manifest.Contributes.UI {
		if ui.RefreshInterval != "" {
			if d, err := time.ParseDuration(ui.RefreshInterval); err == nil && d > 0 {
				if d < interval {
					interval = d
				}
			} else if err != nil {
				log.Info(fmt.Sprintf("Script plugin %s: invalid refreshInterval %q for widget %s: %v",
					p.manifest.ID, ui.RefreshInterval, ui.ID, err))
			}
		}
	}

	// Subscribe to hook triggers declared in ui contributions
	// Format: "PostToolUse:Shell*" → hook fires → script runs instantly
	for _, ui := range p.manifest.Contributes.UI {
		for _, trigger := range ui.Triggers {
			if err := p.subscribeTrigger(ctx, trigger); err != nil {
				log.Info(fmt.Sprintf("Script plugin %s: trigger %q subscribe failed: %v", p.manifest.ID, trigger, err))
			}
		}
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.triggerCh = make(chan struct{}, 8) // buffered for multiple rapid triggers

	go p.refreshLoop(bgCtx, interval)

	log.Info(fmt.Sprintf("Script plugin %s started (interval=%s)", p.manifest.ID, interval))
	return nil
}

func (p *scriptPlugin) Deactivate(ctx PluginContext) error {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	ctx.Logger().Info(fmt.Sprintf("Script plugin %s deactivated", p.manifest.ID))
	return nil
}

// ---------------------------------------------------------------------------
// UIWidget — returns the last script output as widget spans
// ---------------------------------------------------------------------------

// Render returns the cached script output as widget content for the
// PluginContext's current working directory. Each session sees its own
// output because ~pctx.WorkingDir()~ is per-session (set by RefreshWorkDir
// in the RPC handler).
// If no cached output exists for the current workDir (e.g. after Cd or
// initial remote connect), runs the script synchronously to populate it.
func (p *scriptPlugin) Render(width int) []WidgetSpan {
	p.outputMu.RLock()
	var wd string
	if p.pctx != nil {
		wd = p.pctx.WorkingDir()
	}
	text := p.outputs[wd]
	p.outputMu.RUnlock()

	// Cache miss — run script synchronously for this workDir
	if text == "" && wd != "" {
		if output, err := p.runScript(wd); err == nil && output != "" {
			p.outputMu.Lock()
			if p.outputs == nil {
				p.outputs = make(map[string]string)
			}
			p.outputs[wd] = output
			p.outputMu.Unlock()
			log.Debugf("[plugin:%s] output[%s]=%q", p.manifest.ID, wd, output)
			text = output
		}
	}

	if text == "" {
		return []WidgetSpan{{Text: "", Style: StyleDim}}
	}
	return parseScriptOutput(text)
}

// OnWorkDirChanged triggers an immediate script re-run when the session CWD changes.
// The dir is stored in a pending set so runAndUpdate can process it even if
// pctx.WorkingDir() is overwritten by another session's Cd before the trigger fires.
func (p *scriptPlugin) OnWorkDirChanged(dir string) {
	if dir != "" {
		p.pendingMu.Lock()
		if p.pendingDirs == nil {
			p.pendingDirs = make(map[string]struct{})
		}
		p.pendingDirs[dir] = struct{}{}
		p.pendingMu.Unlock()
	}
	select {
	case p.triggerCh <- struct{}{}:
	default:
		// channel full — a run is already queued, dir is in pendingDirs for next run
	}
}

// RenderForWorkDir renders widget content for a specific workDir WITHOUT
// modifying the shared PluginContext. This prevents cross-session races.
func (p *scriptPlugin) RenderForWorkDir(width int, workDir string) []WidgetSpan {
	if workDir == "" {
		return p.Render(width)
	}
	p.outputMu.RLock()
	text := p.outputs[workDir]
	p.outputMu.RUnlock()

	// Cache miss — run script synchronously for this workDir
	if text == "" {
		if output, err := p.runScript(workDir); err == nil && output != "" {
			p.outputMu.Lock()
			if p.outputs == nil {
				p.outputs = make(map[string]string)
			}
			p.outputs[workDir] = output
			p.outputMu.Unlock()
			text = output
		}
	}

	if text == "" {
		return []WidgetSpan{{Text: "", Style: StyleDim}}
	}
	return parseScriptOutput(text)
}

// ---------------------------------------------------------------------------
// refresh loop
// ---------------------------------------------------------------------------

func (p *scriptPlugin) refreshLoop(ctx context.Context, interval time.Duration) {
	// Run immediately on start
	p.runAndUpdate()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.runAndUpdate()
		case <-p.triggerCh:
			p.runAndUpdate()
		}
	}
}

func (p *scriptPlugin) runAndUpdate() {
	// Collect ALL known workDirs from three sources:
	// 1. Existing outputs map (previously refreshed workDirs)
	// 2. Pending dirs from OnWorkDirChanged (new sessions that haven't been processed yet)
	// 3. Current pctx workDir (fallback for the active session)
	workDirSet := make(map[string]struct{})

	p.outputMu.RLock()
	for wd := range p.outputs {
		workDirSet[wd] = struct{}{}
	}
	p.outputMu.RUnlock()

	// Drain pending dirs from OnWorkDirChanged
	p.pendingMu.Lock()
	for wd := range p.pendingDirs {
		workDirSet[wd] = struct{}{}
	}
	p.pendingDirs = nil // clear after consuming
	p.pendingMu.Unlock()

	// Also include current pctx workDir
	if p.pctx != nil {
		if cur := p.pctx.WorkingDir(); cur != "" {
			workDirSet[cur] = struct{}{}
		}
	}

	// Flatten to slice
	workDirs := make([]string, 0, len(workDirSet))
	for wd := range workDirSet {
		workDirs = append(workDirs, wd)
	}
	log.Debugf("[plugin:%s] runAndUpdate: workDirs=%v", p.manifest.ID, workDirs)

	// Evict stale entries: remove outputs for directories that no longer exist.
	// Prevents unbounded map growth when users Cd through temp dirs.
	// os.Stat is cheap and only runs every refresh tick (default 30s).
	p.outputMu.Lock()
	for wd := range p.outputs {
		if _, err := os.Stat(wd); err != nil && os.IsNotExist(err) {
			delete(p.outputs, wd)
		}
	}
	p.outputMu.Unlock()

	// Run script for each workDir and update per-workDir output cache.
	for _, wd := range workDirs {
		output, err := p.runScript(wd)
		if err != nil {
			log.Info(fmt.Sprintf("Script plugin %s execution failed for %s: %v", p.manifest.ID, wd, err))
			continue
		}
		p.outputMu.Lock()
		if p.outputs == nil {
			p.outputs = make(map[string]string)
		}
		p.outputs[wd] = output
		p.outputMu.Unlock()
	}

	// Trigger WidgetRegistry notification WITHOUT writing to the global slot cache.
	// The global cache is session-agnostic and causes cross-session overwrites.
	// Instead, the push path and RPC path use RenderZoneForWorkDir which reads
	// from the per-workDir output cache directly.
	if p.widgetReg != nil {
		p.widgetReg.NotifyUpdated()
	}
}

// subscribeTrigger parses a trigger string ("EventName:Matcher") and subscribes
// to the corresponding hook. When the hook fires, it signals triggerCh to
// run the script immediately.
//
// Supported events: PreToolUse, PostToolUse, PostToolUseFailure, UserPromptSubmit,
// AgentStop, SessionStart, SessionEnd, SubAgentStart, SubAgentStop, PreCompact,
// PostCompact, CronFired, WebhookReceived.
func (p *scriptPlugin) subscribeTrigger(ctx PluginContext, trigger string) error {
	parts := strings.SplitN(trigger, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid trigger format %q (expected EventName:Matcher)", trigger)
	}
	event, matcher := parts[0], parts[1]

	triggerFn := func(_ context.Context, hp *HookPayload) (*HookResult, error) {
		// Store payload data so runScript can inject it as env vars
		if hp != nil {
			p.lastHookMu.Lock()
			p.lastHook = hp
			p.lastHookMu.Unlock()
			log.Infof("[plugin:%s] trigger fired: tool=%s", p.manifest.ID, hp.ToolName)
		}

		if p.isHintPlugin {
			// Synchronous execution: run script inline so the engine can read
			// hint content immediately after the PostToolUse hook returns.
			wd := ""
			if p.pctx != nil {
				wd = p.pctx.WorkingDir()
			}
			output, err := p.runScript(wd)
			log.Infof("[plugin:%s] hint sync: wd=%s len=%d", p.manifest.ID, wd, len(output))
			if err == nil && output != "" {
				p.outputMu.Lock()
				if p.outputs == nil {
					p.outputs = make(map[string]string)
				}
				p.outputs[wd] = output
				p.outputMu.Unlock()
				// Strip "md|" prefix for clean markdown text
				hintText := output
				if strings.HasPrefix(hintText, "md|") {
					hintText = hintText[3:]
				} else if strings.HasPrefix(hintText, "diff|") {
					hintText = hintText[5:]
				}
				p.hintMu.Lock()
				p.hintContent = hintText
				p.hintMu.Unlock()
			}
		} else {
			select {
			case p.triggerCh <- struct{}{}:
			default:
				// channel full — skip this trigger (rate limiting)
			}
		}
		return nil, nil
	}

	switch event {
	case "PreToolUse":
		return ctx.OnPreToolUse(matcher, triggerFn)
	case "PostToolUse":
		return ctx.OnPostToolUse(matcher, triggerFn)
	case "PostToolUseFailure":
		return ctx.OnEvent(HookPostToolUseError, matcher, triggerFn)
	case "UserPromptSubmit":
		return ctx.OnEvent(HookUserPromptSubmit, "", triggerFn)
	case "AgentStop":
		return ctx.OnAgentStop(triggerFn)
	case "SessionStart":
		return ctx.OnSessionStart(triggerFn)
	case "SessionEnd":
		return ctx.OnSessionEnd(triggerFn)
	case "SubAgentStart":
		return ctx.OnEvent(HookSubAgentStart, "", triggerFn)
	case "SubAgentStop":
		return ctx.OnEvent(HookSubAgentStop, "", triggerFn)
	case "PreCompact":
		return ctx.OnEvent(HookPreCompact, "", triggerFn)
	case "PostCompact":
		return ctx.OnEvent(HookPostCompact, "", triggerFn)
	case "CronFired":
		return ctx.OnEvent(HookCronFired, "", triggerFn)
	case "WebhookReceived":
		return ctx.OnEvent(HookWebhookReceived, "", triggerFn)
	default:
		return fmt.Errorf("unsupported trigger event %q", event)
	}
}

// ---------------------------------------------------------------------------
func (p *scriptPlugin) runScript(workDir string) (string, error) {
	// Split entry into command and args (safe shell-free splitting)
	parts := strings.Fields(p.manifest.Entry)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty entry command")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	// Resolve the script path relative to the plugin directory so it can be found.
	if len(parts) > 1 && !filepath.IsAbs(parts[1]) {
		parts[1] = filepath.Join(p.dir, parts[1])
		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	}
	// Use the captured workDir — concurrent RPCs cannot corrupt it.
	if workDir != "" {
		if _, err := os.Stat(workDir); err == nil {
			cmd.Dir = workDir
		}
		// If workDir doesn't exist (e.g. temp dir cleaned up by a
		// parallel test on Windows), skip setting cmd.Dir and let
		// the script run in the plugin's own directory instead.
	}

	// Inject hook payload data as environment variables.
	// Scripts can use XBOT_TOOL_NAME, XBOT_TOOL_OUTPUT, XBOT_TOOL_INPUT, XBOT_WORK_DIR.
	p.lastHookMu.RLock()
	hp := p.lastHook
	p.lastHookMu.RUnlock()
	env := os.Environ()
	env = append(env, "XBOT_WORK_DIR="+workDir)
	if hp != nil {
		if hp.ToolName != "" {
			env = append(env, "XBOT_TOOL_NAME="+hp.ToolName)
		}
		if hp.ToolOutput != "" {
			env = append(env, "XBOT_TOOL_OUTPUT="+hp.ToolOutput)
		}
		if hp.ToolInput != "" {
			env = append(env, "XBOT_TOOL_INPUT="+hp.ToolInput)
		}
	}
	cmd.Env = env

	out, err := cmd.Output()
	if err != nil {
		log.Infof("[plugin:%s] runScript(%s) failed: %v", p.manifest.ID, workDir, err)
		return "", fmt.Errorf("script %q: %w", p.manifest.Entry, err)
	}
	log.Infof("[plugin:%s] runScript(%s) output: %s", p.manifest.ID, workDir, strings.TrimSpace(string(out)))

	trimmed := strings.TrimSpace(string(out))
	// For "md|" and "diff|" prefixes, preserve full multi-line output
	// (markdown content or unified diff).  All other prefixes are single-line.
	if strings.HasPrefix(trimmed, "md|") || strings.HasPrefix(trimmed, "diff|") {
		return trimmed, nil
	}
	// Default: use first line as widget content
	lines := strings.SplitN(trimmed, "\n", 2)
	return lines[0], nil
}

// ---------------------------------------------------------------------------
// parseScriptOutput — converts script output to WidgetSpan
// ---------------------------------------------------------------------------

// parseScriptOutput interprets a simple format:
//
//	"text"              → StyleNormal
//	"dim|text"          → StyleDim
//	"ok|text"           → StyleSuccess
//	"warn|text"         → StyleWarning
//	"err|text"          → StyleError
//	"info|text"         → StyleInfo
//	"accent|text"       → StyleAccent
//	"diff|<multiline>"  → StyleRaw (full multi-line unified diff, preserves ANSI)
//
// The part before the first "|" is the style hint, the rest is the text.
func parseScriptOutput(text string) []WidgetSpan {
	if text == "" {
		return nil
	}

	// Check for style prefix: "style|text"
	parts := strings.SplitN(text, "|", 2)
	if len(parts) == 2 {
		style, content := strings.TrimSpace(parts[0]), parts[1]
		// "diff|" prefix: multi-line raw content (unified diff with ANSI colors)
		if style == "diff" {
			return []WidgetSpan{{Text: content, Style: StyleRaw}}
		}
		sc := parseStyleHint(style)
		return []WidgetSpan{{Text: content, Style: sc}}
	}

	return []WidgetSpan{{Text: text, Style: StyleNormal}}
}

func parseStyleHint(hint string) StyleClass {
	switch hint {
	case "dim":
		return StyleDim
	case "ok":
		return StyleSuccess
	case "warn":
		return StyleWarning
	case "err":
		return StyleError
	case "info":
		return StyleInfo
	case "accent":
		return StyleAccent
	default:
		return StyleNormal
	}
}
