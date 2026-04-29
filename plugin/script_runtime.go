package plugin

import (
	"context"
	"fmt"
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

	pctx PluginContext // captured in Activate for UpdateWidget
}

func (p *scriptPlugin) Manifest() PluginManifest {
	return p.manifest
}

func (p *scriptPlugin) Activate(ctx PluginContext) error {
	p.pctx = ctx

	// Register UI widgets declared in the manifest
	for _, ui := range p.manifest.Contributes.UI {
		if err := ctx.ContributeUI(ui.ID, ui.Slot, p, ui.Priority); err != nil {
			return fmt.Errorf("contribute widget %q: %w", ui.ID, err)
		}
	}

	// Start periodic refresh loop
	interval := 30 * time.Second // default
	if len(p.manifest.Contributes.UI) > 0 && p.manifest.Contributes.UI[0].RefreshInterval != "" {
		if d, err := time.ParseDuration(p.manifest.Contributes.UI[0].RefreshInterval); err == nil && d > 0 {
			interval = d
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
			text = output
		}
	}

	if text == "" {
		return []WidgetSpan{{Text: "", Style: StyleDim}}
	}
	return parseScriptOutput(text)
}

// OnWorkDirChanged triggers an immediate script re-run when the session CWD changes.
// The output is stored per-workDir in the outputs map, and Render() reads from
// pctx.WorkingDir() which is set per-RPC-call — no cross-session mixing.
func (p *scriptPlugin) OnWorkDirChanged(dir string) {
	select {
	case p.triggerCh <- struct{}{}:
	default:
		// channel full — a run is already queued, skip
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
	// Capture the workDir ONCE before running — concurrent RPCs may change
	// pctx.WorkingDir() between runScript() and output storage. Using the
	// same wd for both ensures output goes to the correct key.
	var wd string
	if p.pctx != nil {
		wd = p.pctx.WorkingDir()
	}
	output, err := p.runScript(wd)
	if err != nil {
		log.Info(fmt.Sprintf("Script plugin %s execution failed: %v", p.manifest.ID, err))
		return
	}
	// Store output per workDir so each CLI window sees its own result
	if wd != "" {
		p.outputMu.Lock()
		if p.outputs == nil {
			p.outputs = make(map[string]string)
		}
		p.outputs[wd] = output
		p.outputMu.Unlock()
	}

	// Push update to TUI via PluginContext
	if p.pctx != nil {
		for _, ui := range p.manifest.Contributes.UI {
			_ = p.pctx.UpdateWidget(ui.ID)
		}
	}
}

// subscribeTrigger parses a trigger string ("EventName:Matcher") and subscribes
// to the corresponding hook. When the hook fires, it signals triggerCh to
// run the script immediately.
func (p *scriptPlugin) subscribeTrigger(ctx PluginContext, trigger string) error {
	parts := strings.SplitN(trigger, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid trigger format %q (expected EventName:Matcher)", trigger)
	}
	event, matcher := parts[0], parts[1]

	switch event {
	case "PostToolUse":
		return ctx.OnPostToolUse(matcher, func(_ context.Context, _ *HookPayload) (*HookResult, error) {
			select {
			case p.triggerCh <- struct{}{}:
			default:
				// channel full — skip this trigger (rate limiting)
			}
			return nil, nil
		})
	default:
		return fmt.Errorf("unsupported trigger event %q (supported: PostToolUse)", event)
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
		cmd.Dir = workDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("script %q: %w", p.manifest.Entry, err)
	}

	// Use first line as widget content
	lines := strings.SplitN(string(out), "\n", 2)
	text := strings.TrimSpace(lines[0])
	return text, nil
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
