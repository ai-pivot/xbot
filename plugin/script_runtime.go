package plugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	// Validate that at least one entry point is defined.
	// Platform-specific entries (entry_windows, etc.) are optional overrides;
	// the generic "entry" field is the fallback.
	if manifest.Entry == "" && manifest.EntryWindows == "" &&
		manifest.EntryDarwin == "" && manifest.EntryLinux == "" {
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

	// Per-workDir per-widget output cache — each CLI window (different workDir)
	// sees its own git branch, not the branch of whichever window last refreshed.
	// Map structure: workDir → widgetID → last script output.
	outputMu sync.RWMutex
	outputs  map[string]map[string]string // workDir → widgetID → output

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

	// Synchronous trigger support: when a UISlotContribution has Sync=true,
	// the hook trigger runs the script synchronously and stores output here.
	// The engine reads this immediately after the hook fires.
	hintMu      sync.RWMutex
	hintContent string   // last output from synchronous trigger
	syncWidgets []string // widget IDs that require synchronous execution

	// All widget IDs declared in the manifest — used by runAndUpdate to run
	// the script once per workDir and cache output for each widget.
	widgetIDs []string
}

// widgetAdapter wraps a scriptPlugin for a specific widget ID.
// Each widget declared in the manifest gets its own adapter so that
// Render/RenderForWorkDir can pass the widgetID to the script via
// XBOT_WIDGET_ID environment variable.
type widgetAdapter struct {
	plugin   *scriptPlugin
	widgetID string
}

// Ensure widgetAdapter implements UIWidget and WorkDirRenderer.
var _ UIWidget = (*widgetAdapter)(nil)
var _ WorkDirRenderer = (*widgetAdapter)(nil)

func (a *widgetAdapter) Render(width int) []WidgetSpan {
	return a.plugin.renderForWidget(a.widgetID, width, "")
}

func (a *widgetAdapter) RenderForWorkDir(width int, workDir string) []WidgetSpan {
	return a.plugin.renderByWidgetAndWorkDir(a.widgetID, width, workDir)
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

	// Register UI widgets declared in the manifest — each widget gets its own
	// widgetAdapter so Render/RenderForWorkDir knows which widgetID to pass
	// to runScript via XBOT_WIDGET_ID.
	for _, ui := range p.manifest.Contributes.UI {
		adapter := &widgetAdapter{plugin: p, widgetID: ui.ID}
		if err := ctx.ContributeUI(ui.ID, ui.Slot, adapter, ui.Priority); err != nil {
			return fmt.Errorf("contribute widget %q: %w", ui.ID, err)
		}
		p.widgetIDs = append(p.widgetIDs, ui.ID)
		if ui.Sync {
			p.syncWidgets = append(p.syncWidgets, ui.ID)
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

// renderForWidget returns the cached script output for the given widgetID,
// using the PluginContext's current working directory.
// If no cached output exists for this workDir+widgetID, runs the script
// synchronously with XBOT_WIDGET_ID set, so the script can produce
// widget-specific output.
func (p *scriptPlugin) renderForWidget(widgetID string, width int, fallbackWorkDir string) []WidgetSpan {
	p.outputMu.RLock()
	var wd string
	if p.pctx != nil {
		wd = p.pctx.WorkingDir()
	}
	if wd == "" {
		wd = fallbackWorkDir
	}
	widgetOutputs := p.outputs[wd]
	text := ""
	if widgetOutputs != nil {
		text = widgetOutputs[widgetID]
	}
	p.outputMu.RUnlock()

	// Cache miss — run script synchronously for this workDir+widgetID
	if text == "" && wd != "" {
		if output, err := p.runScript(wd, widgetID); err == nil && output != "" {
			p.outputMu.Lock()
			if p.outputs == nil {
				p.outputs = make(map[string]map[string]string)
			}
			if p.outputs[wd] == nil {
				p.outputs[wd] = make(map[string]string)
			}
			p.outputs[wd][widgetID] = output
			p.outputMu.Unlock()
			log.Debugf("[plugin:%s] output[%s][%s]=%q", p.manifest.ID, wd, widgetID, output)
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

// renderByWidgetAndWorkDir renders widget content for a specific workDir WITHOUT
// modifying the shared PluginContext. This prevents cross-session races.
func (p *scriptPlugin) renderByWidgetAndWorkDir(widgetID string, width int, workDir string) []WidgetSpan {
	if workDir == "" {
		return p.renderForWidget(widgetID, width, "")
	}
	p.outputMu.RLock()
	widgetOutputs := p.outputs[workDir]
	text := ""
	if widgetOutputs != nil {
		text = widgetOutputs[widgetID]
	}
	p.outputMu.RUnlock()

	// Cache miss — run script synchronously for this workDir+widgetID
	if text == "" {
		if output, err := p.runScript(workDir, widgetID); err == nil && output != "" {
			p.outputMu.Lock()
			if p.outputs == nil {
				p.outputs = make(map[string]map[string]string)
			}
			if p.outputs[workDir] == nil {
				p.outputs[workDir] = make(map[string]string)
			}
			p.outputs[workDir][widgetID] = output
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
	log.Debugf("[plugin:%s] runAndUpdate: workDirs=%v widgetIDs=%v", p.manifest.ID, workDirs, p.widgetIDs)

	// Snapshot current outputs for change detection (before eviction + re-run).
	// Deep-copy the two-level map: workDir → widgetID → output.
	p.outputMu.RLock()
	prevOutputs := make(map[string]map[string]string, len(p.outputs))
	for wd, wm := range p.outputs {
		prevWm := make(map[string]string, len(wm))
		for wid, v := range wm {
			prevWm[wid] = v
		}
		prevOutputs[wd] = prevWm
	}
	p.outputMu.RUnlock()

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

	// Determine the primary widgetID (first declared, or empty if single-widget).
	// For backward compatibility, when a plugin has a single widget, we run the
	// script once per workDir and cache the same output for that widget.
	// For multi-widget plugins, we also run once per workDir but with XBOT_WIDGET_ID
	// set to the primary widget so the script output is consistent.
	// The per-widget render paths (renderForWidget/renderByWidgetAndWorkDir) will
	// call runScript with the specific widgetID for cache misses.
	primaryWidgetID := ""
	if len(p.widgetIDs) > 0 {
		primaryWidgetID = p.widgetIDs[0]
	}

	// Run script for each workDir and update per-workDir output cache.
	for _, wd := range workDirs {
		output, err := p.runScript(wd, primaryWidgetID)
		if err != nil {
			log.Info(fmt.Sprintf("Script plugin %s execution failed for %s: %v", p.manifest.ID, wd, err))
			continue
		}
		p.outputMu.Lock()
		if p.outputs == nil {
			p.outputs = make(map[string]map[string]string)
		}
		if p.outputs[wd] == nil {
			p.outputs[wd] = make(map[string]string)
		}
		// Store the same output for ALL widgets of this plugin.
		// The per-widget render paths will re-run with specific widgetID
		// if the script needs different output per widget.
		for _, wid := range p.widgetIDs {
			p.outputs[wd][wid] = output
		}
		p.outputMu.Unlock()
	}

	// Change detection: only notify if any output actually changed.
	// Compares per-workDir, per-widget outputs against the snapshot.
	changed := false
	p.outputMu.RLock()
	for _, wd := range workDirs {
		curWm := p.outputs[wd]
		prevWm := prevOutputs[wd]
		if curWm == nil && prevWm == nil {
			continue
		}
		if (curWm == nil) != (prevWm == nil) || len(curWm) != len(prevWm) {
			changed = true
			break
		}
		for wid, v := range curWm {
			if prevWm[wid] != v {
				changed = true
				break
			}
		}
		if changed {
			break
		}
	}
	// Also check for evicted entries
	if !changed && len(p.outputs) != len(prevOutputs) {
		changed = true
	}
	p.outputMu.RUnlock()

	if changed {
		if p.widgetReg != nil {
			p.widgetReg.NotifyUpdated()
		}
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
			log.Debugf("[plugin:%s] trigger fired: tool=%s", p.manifest.ID, hp.ToolName)
		}

		if len(p.syncWidgets) > 0 {
			// Synchronous execution: run script inline so the engine can read
			// hint content immediately after the PostToolUse hook returns.
			wd := ""
			if p.pctx != nil {
				wd = p.pctx.WorkingDir()
			}
			// Use the first sync widget ID as primary for the hint content.
			primaryWidgetID := p.syncWidgets[0]
			output, err := p.runScript(wd, primaryWidgetID)
			log.Debugf("[plugin:%s] hint sync: wd=%s widget=%s len=%d", p.manifest.ID, wd, primaryWidgetID, len(output))
			if err == nil && output != "" {
				p.outputMu.Lock()
				if p.outputs == nil {
					p.outputs = make(map[string]map[string]string)
				}
				if p.outputs[wd] == nil {
					p.outputs[wd] = make(map[string]string)
				}
				// Store for all sync widgets
				for _, wid := range p.syncWidgets {
					p.outputs[wd][wid] = output
				}
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
	// Script plugin triggers are session-agnostic: they manage per-workDir output
	// caches and produce per-session content via RenderForWorkDir. Register them
	// as global hooks so bridge.Dispatch doesn't filter them by session.
	case "PreToolUse":
		return registerGlobalHook(ctx, HookPreToolUse, matcher, triggerFn)
	case "PostToolUse":
		return registerGlobalHook(ctx, HookPostToolUse, matcher, triggerFn)
	case "PostToolUseFailure":
		return registerGlobalHook(ctx, HookPostToolUseError, matcher, triggerFn)
	case "UserPromptSubmit":
		return registerGlobalHook(ctx, HookUserPromptSubmit, "", triggerFn)
	case "AgentStop":
		return registerGlobalHook(ctx, HookAgentStop, "", triggerFn)
	case "SessionStart":
		return registerGlobalHook(ctx, HookSessionStart, "", triggerFn)
	case "SessionEnd":
		return registerGlobalHook(ctx, HookSessionEnd, "", triggerFn)
	case "SubAgentStart":
		return registerGlobalHook(ctx, HookSubAgentStart, "", triggerFn)
	case "SubAgentStop":
		return registerGlobalHook(ctx, HookSubAgentStop, "", triggerFn)
	case "PreCompact":
		return registerGlobalHook(ctx, HookPreCompact, "", triggerFn)
	case "PostCompact":
		return registerGlobalHook(ctx, HookPostCompact, "", triggerFn)
	case "CronFired":
		return registerGlobalHook(ctx, HookCronFired, "", triggerFn)
	case "WebhookReceived":
		return registerGlobalHook(ctx, HookWebhookReceived, "", triggerFn)
	default:
		return fmt.Errorf("unsupported trigger event %q", event)
	}
}

// registerGlobalHook registers a script plugin trigger hook as session-agnostic.
// Script plugin triggers are global because they manage per-workDir output caches
// and produce per-session content via RenderForWorkDir — they don't need session
// isolation and would break if filtered by chatID (multi-session remote CLI).
func registerGlobalHook(ctx PluginContext, event HookEvent, matcher string, handler HookHandler) error {
	if impl, ok := ctx.(*pluginContextImpl); ok {
		return impl.onEvent(event, matcher, handler, true)
	}
	// Fallback: non-impl context (shouldn't happen for script plugins)
	return ctx.OnEvent(event, matcher, handler)
}

// ---------------------------------------------------------------------------
// resolvedEntry returns the platform-appropriate entry command.
// Platform-specific fields (entry_windows, entry_darwin, entry_linux)
// take precedence over the generic entry field.
func (p *scriptPlugin) resolvedEntry() string {
	switch runtime.GOOS {
	case "windows":
		if p.manifest.EntryWindows != "" {
			return p.manifest.EntryWindows
		}
	case "darwin":
		if p.manifest.EntryDarwin != "" {
			return p.manifest.EntryDarwin
		}
	case "linux":
		if p.manifest.EntryLinux != "" {
			return p.manifest.EntryLinux
		}
	}
	return p.manifest.Entry
}

func (p *scriptPlugin) runScript(workDir, widgetID string) (string, error) {
	// Resolve platform-specific entry command
	entry := p.resolvedEntry()

	// Split entry into command and args (safe shell-free splitting)
	parts := strings.Fields(entry)
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
	// Scripts can use XBOT_WIDGET_ID, XBOT_TOOL_NAME, XBOT_TOOL_OUTPUT, XBOT_TOOL_INPUT,
	// XBOT_WORK_DIR, XBOT_MODEL, XBOT_MAX_CONTEXT, XBOT_TOKEN_USAGE.
	p.lastHookMu.RLock()
	hp := p.lastHook
	p.lastHookMu.RUnlock()
	env := os.Environ()
	env = append(env, "XBOT_WORK_DIR="+workDir)
	if widgetID != "" {
		env = append(env, "XBOT_WIDGET_ID="+widgetID)
	}
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
		// Session context from Extra — available on all hook events
		if hp.Extra != nil {
			if model, ok := hp.Extra["model"].(string); ok && model != "" {
				env = append(env, "XBOT_MODEL="+model)
			}
			if maxCtx, ok := hp.Extra["max_context"].(int64); ok && maxCtx > 0 {
				env = append(env, "XBOT_MAX_CONTEXT="+strconv.FormatInt(maxCtx, 10))
			}
			// Token usage — JSON for structured access
			if pt, okPt := hp.Extra["prompt_tokens"].(int64); okPt && pt > 0 {
				ct := int64(0)
				if c, ok := hp.Extra["comp_tokens"].(int64); ok {
					ct = c
				}
				env = append(env, fmt.Sprintf("XBOT_TOKEN_USAGE=%d/%d", pt, ct))
				env = append(env, "XBOT_PROMPT_TOKENS="+strconv.FormatInt(pt, 10))
				env = append(env, "XBOT_COMP_TOKENS="+strconv.FormatInt(ct, 10))
			}
		}
	}
	cmd.Env = env

	out, err := cmd.Output()
	if err != nil {
		log.Infof("[plugin:%s] runScript(%s) failed: %v", p.manifest.ID, workDir, err)
		return "", fmt.Errorf("script %q: %w", entry, err)
	}
	log.Debugf("[plugin:%s] runScript(%s) output: %s", p.manifest.ID, workDir, strings.TrimSpace(string(out)))

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
