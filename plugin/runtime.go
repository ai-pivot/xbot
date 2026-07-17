package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Native Runtime — in-process Go plugins (registered via Register())
// ---------------------------------------------------------------------------

// NativeRuntime creates plugins that run in-process.
type NativeRuntime struct {
	registry map[string]Plugin
}

// NewNativeRuntime creates a native runtime.
func NewNativeRuntime() *NativeRuntime {
	return &NativeRuntime{
		registry: make(map[string]Plugin),
	}
}

// RegisterPlugin adds a Go plugin instance to the native runtime.
func (nr *NativeRuntime) RegisterPlugin(p Plugin) {
	nr.registry[p.Manifest().ID] = p
}

// Create returns the pre-registered plugin or an error.
func (nr *NativeRuntime) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	if p, ok := nr.registry[manifest.ID]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("native plugin %s not registered", manifest.ID)
}

// ---------------------------------------------------------------------------
// gRPC Runtime — external process plugins (Phase 2: bidirectional multiplexer)
// ---------------------------------------------------------------------------

// pendingCall tracks an in-flight request waiting for its response.
type pendingCall struct {
	done chan struct{}
	resp *PluginResponse
	err  error
}

// InboundHandler processes asynchronous messages pushed by the plugin process.
// Registered via StdioPluginProcess.SetInboundHandler.
type InboundHandler func(msg *PluginInbound)

// StdioPluginProcess manages an external plugin process communicating via
// JSON-over-stdin/stdout protocol with bidirectional multiplexing.
//
// Phase 2 architecture:
//   - A single readLoop goroutine reads all stdout lines from the plugin.
//   - Lines with "method" field are inbound messages → routed to InboundHandler.
//   - Lines with "result"/"error" (no "method") are responses → routed to pending Call().
//   - Call() writes a request and waits on a pendingCall channel; readLoop delivers.
//
// This allows plugins to push channel_inbound messages at any time without polling.
type StdioPluginProcess struct {
	cmd    *exec.Cmd
	stdin  *jsonLineWriter
	stdout *jsonLineReader

	mu      sync.Mutex
	running bool

	// Multiplexer state (protected by muxMu)
	muxMu          sync.Mutex
	pending        *pendingCall   // at most one in-flight call (request-response protocol)
	inboundHandler InboundHandler // called for inbound messages

}

// NewStdioRuntime creates a factory for gRPC plugin processes.
func NewStdioRuntime() RuntimeFactory {
	return &stdioRuntimeFactory{}
}

type stdioRuntimeFactory struct{}

func (f *stdioRuntimeFactory) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	if manifest.Entry == "" {
		return nil, fmt.Errorf("grpc plugin %s: entry command is required", manifest.ID)
	}
	return &stdioPlugin{
		manifest: *manifest,
		dir:      dir,
	}, nil
}

// stdioPlugin implements Plugin for external gRPC/stdio processes.
type stdioPlugin struct {
	manifest        PluginManifest
	dir             string
	process         *StdioPluginProcess
	ChannelProvider *ChannelProviderDecl
}

func (g *stdioPlugin) Manifest() PluginManifest {
	return g.manifest
}

func (g *stdioPlugin) Activate(ctx PluginContext) error {
	proc, err := startPluginProcess(g.manifest.Entry, g.manifest.Executable, g.manifest.Args, g.dir)
	if err != nil {
		return fmt.Errorf("start plugin process: %w", err)
	}
	g.process = proc

	// Start the bidirectional readLoop before making any calls.
	go proc.readLoop()

	// Send activate command to the external process
	req := &PluginRequest{
		Method: "activate",
		Params: map[string]any{
			"pluginId": g.manifest.ID,
		},
	}
	resp, err := proc.Call(context.Background(), req)
	if err != nil {
		proc.Stop()
		return fmt.Errorf("activate call failed: %w", err)
	}
	if resp.Error != "" {
		proc.Stop()
		return fmt.Errorf("plugin activate error: %s", resp.Error)
	}

	// Register tools from the response
	for _, t := range resp.Tools {
		tool := &remoteTool{
			def:      t,
			pluginID: g.manifest.ID,
			process:  g.process,
		}
		if err := ctx.RegisterTool(tool); err != nil {
			proc.Stop()
			return fmt.Errorf("register remote tool %q: %w", t.Name, err)
		}
	}

	// Register hooks from the response
	for _, h := range resp.Hooks {
		handler := g.makeRemoteHookHandler(h.Event, h.Matcher)
		if err := ctx.OnEvent(HookEvent(h.Event), h.Matcher, handler); err != nil {
			proc.Stop()
			return fmt.Errorf("register remote hook %q: %w", h.Event, err)
		}
	}

	// Register context enrichers from the response
	for _, e := range resp.Enrichers {
		enricher := g.makeRemoteEnricher(e.Name)
		if err := ctx.EnrichContext(e.Name, enricher); err != nil {
			proc.Stop()
			return fmt.Errorf("register remote enricher %q: %w", e.Name, err)
		}
	}

	// If the plugin declares a channel_provider, create a bridge and register it.
	if resp.ChannelProvider != nil {
		cp := resp.ChannelProvider
		if cp.Name == "" {
			proc.Stop()
			return fmt.Errorf("plugin %s: channel_provider missing name", g.manifest.ID)
		}
		g.ChannelProvider = cp

		// Populate entry info from manifest for spawning a separate channel process.
		cp.Entry = g.manifest.Entry
		cp.Executable = g.manifest.Executable
		cp.Args = g.manifest.Args
		cp.Dir = g.dir

		// Create a channel.ChannelProvider via the factory registered by serverapp.
		bridge, err := CreateChannelProvider(cp, proc)
		if err != nil {
			proc.Stop()
			return fmt.Errorf("create grpc channel bridge: %w", err)
		}

		if err := ctx.RegisterChannelProvider(bridge); err != nil {
			proc.Stop()
			return fmt.Errorf("register channel provider %q: %w", cp.Name, err)
		}
	}

	return nil
}

func (g *stdioPlugin) Deactivate(ctx PluginContext) error {
	if g.process == nil {
		return nil
	}
	req := &PluginRequest{Method: "deactivate"}
	if _, err := g.process.Call(context.Background(), req); err != nil {
		log.Glob(log.CatPlugin).WithField("plugin", g.manifest.ID).Warn("Deactivate call failed: ", err)
	}
	g.process.Stop()
	g.process = nil
	return nil
}

func (g *stdioPlugin) makeRemoteHookHandler(event, matcher string) HookHandler {
	return func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		req := &PluginRequest{
			Method: "hook",
			Params: map[string]any{
				"event":     string(payload.Event),
				"toolName":  payload.ToolName,
				"toolInput": payload.ToolInput,
				"sessionId": payload.SessionID,
				"channel":   payload.Channel,
				"chatId":    payload.ChatID,
			},
		}
		resp, err := g.process.Call(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp.HookResult != nil {
			return resp.HookResult, nil
		}
		return &HookResult{Decision: DecisionAllow}, nil
	}
}

// makeRemoteEnricher creates a ContextEnricher that calls the remote plugin process.
func (g *stdioPlugin) makeRemoteEnricher(name string) ContextEnricher {
	return func(ctx context.Context) (string, error) {
		req := &PluginRequest{
			Method: "enrich",
			Params: map[string]any{
				"enricherName": name,
			},
		}
		resp, err := g.process.Call(ctx, req)
		if err != nil {
			return "", err
		}
		if resp.Error != "" {
			return "", fmt.Errorf("enricher %q error: %s", name, resp.Error)
		}
		return resp.Result, nil
	}
}

// remoteTool implements PluginTool for remote process tools.
type remoteTool struct {
	def      ToolDef
	pluginID string
	process  *StdioPluginProcess
}

func (rt *remoteTool) Definition() ToolDef {
	return rt.def
}

func (rt *remoteTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	req := &PluginRequest{
		Method: "execute_tool",
		Params: map[string]any{
			"toolName": rt.def.Name,
			"input":    input,
		},
	}
	resp, err := rt.process.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return NewToolError(resp.Error), nil
	}
	return NewToolResult(resp.Result), nil
}

// ---------------------------------------------------------------------------
// JSON-over-stdio protocol types (exported for serverapp bridge)
// ---------------------------------------------------------------------------

// PluginRequest is a JSON-over-stdio request sent from xbot to the plugin process.
type PluginRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// PluginResponse is a JSON-over-stdio response from the plugin process.
// Response lines have "result" or "error" but NO "method" field.
type PluginResponse struct {
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Tools      []ToolDef     `json:"tools,omitempty"`
	Hooks      []hookReg     `json:"hooks,omitempty"`
	HookResult *HookResult   `json:"hook_result,omitempty"`
	Enrichers  []enricherReg `json:"enrichers,omitempty"`

	// ChannelProvider declares that this plugin provides a custom channel.
	ChannelProvider *ChannelProviderDecl `json:"channel_provider,omitempty"`
}

// PluginInbound represents an asynchronous message pushed by the plugin.
// Inbound lines have "method" field but NO "result" or "error".
// Currently supported methods:
//   - "channel_inbound": plugin pushes user messages to xbot agent.
type PluginInbound struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type hookReg struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
}

type enricherReg struct {
	Name string `json:"name"`
}

// ChannelProviderDecl is the declaration returned by the plugin in its
// activate response to signal that it provides a custom channel.
type ChannelProviderDecl struct {
	Name         string           `json:"name"`
	ConfigSchema []map[string]any `json:"config_schema,omitempty"`

	// Entry info for spawning a separate channel process.
	// Populated by xbot from the plugin manifest during activation.
	Entry      string   `json:"-"` // not serialized to plugin
	Executable string   `json:"-"`
	Args       []string `json:"-"`
	Dir        string   `json:"-"`
}

// GetChannelProviderDecl extracts the channel provider declaration from a stdioPlugin.
func GetChannelProviderDecl(p Plugin) *ChannelProviderDecl {
	gp, ok := p.(*stdioPlugin)
	if !ok {
		return nil
	}
	return gp.ChannelProvider
}

// GetProcess returns the StdioPluginProcess for a stdioPlugin, or nil.
func GetProcess(p Plugin) *StdioPluginProcess {
	if gp, ok := p.(*stdioPlugin); ok {
		return gp.process
	}
	return nil
}

// ---------------------------------------------------------------------------
// Process lifecycle
// ---------------------------------------------------------------------------

func startPluginProcess(entry, executable string, args []string, dir string) (*StdioPluginProcess, error) {
	var cmd *exec.Cmd
	if executable != "" {
		cmd = exec.Command(executable, args...)
	} else {
		parts := strings.Fields(entry)
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty entry command")
		}
		cmd = exec.Command(parts[0], parts[1:]...)
	}
	cmd.Dir = dir
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	return &StdioPluginProcess{
		cmd:     cmd,
		stdin:   &jsonLineWriter{w: stdinPipe},
		stdout:  newJSONLineReader(stdoutPipe),
		running: true,
	}, nil
}

const pluginCallTimeout = 30 * time.Second

// SetInboundHandler registers a callback for asynchronous inbound messages
// from the plugin (e.g., channel_inbound). Must be called before readLoop starts
// or while no readLoop is running (thread-safe via muxMu).
func (p *StdioPluginProcess) SetInboundHandler(handler InboundHandler) {
	p.muxMu.Lock()
	defer p.muxMu.Unlock()
	p.inboundHandler = handler
}

// Call sends a request to the plugin process and waits for the response.
// Thread-safe: only one Call can be in-flight at a time (stdin is sequential).
// The readLoop goroutine delivers the response.
func (p *StdioPluginProcess) Call(ctx context.Context, req *PluginRequest) (*PluginResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil, fmt.Errorf("plugin process not running")
	}

	// Write request to stdin (under p.mu to serialize writes)
	if err := p.stdin.write(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Register pending call for readLoop to deliver response
	pc := &pendingCall{done: make(chan struct{})}

	p.muxMu.Lock()
	p.pending = pc
	p.muxMu.Unlock()

	// Wait for response (delivered by readLoop) or timeout/cancel
	select {
	case <-pc.done:
		return pc.resp, pc.err
	case <-ctx.Done():
		p.clearPending()
		p.stopLocked()
		return nil, ctx.Err()
	case <-time.After(pluginCallTimeout):
		p.clearPending()
		p.stopLocked()
		return nil, fmt.Errorf("plugin call timeout (%v)", pluginCallTimeout)
	}
}

func (p *StdioPluginProcess) clearPending() {
	p.muxMu.Lock()
	p.pending = nil
	p.muxMu.Unlock()
}

// readLoop is the bidirectional multiplexer. It reads all lines from the
// plugin's stdout and routes them:
//   - Response (has "result"/"error", no "method") → deliver to pending Call()
//   - Inbound (has "method", no "result"/"error")  → deliver to InboundHandler
//
// Runs as a background goroutine, started by Activate().
func (p *StdioPluginProcess) readLoop() {
	for {
		// Read next line from stdout
		line, err := p.stdout.readLine()
		if err != nil {
			// Process exited or error — fail any pending call
			p.muxMu.Lock()
			if p.pending != nil {
				p.pending.err = fmt.Errorf("plugin stdout closed: %w", err)
				close(p.pending.done)
				p.pending = nil
			}
			p.muxMu.Unlock()

			p.mu.Lock()
			if p.running {
				log.Glob(log.CatPlugin).WithField("plugin", "stdio").Warn("Plugin stdout closed: ", err)
			}
			p.mu.Unlock()
			return
		}

		// Peek at the line to determine type: response vs inbound
		var peek struct {
			Method string `json:"method"`
			Result string `json:"result"`
			Error  string `json:"error"`
		}
		if jsonErr := json.Unmarshal(line, &peek); jsonErr != nil {
			log.Glob(log.CatPlugin).WithField("plugin", "stdio").Warn("Failed to parse plugin stdout line: ", jsonErr)
			continue
		}

		if peek.Method != "" && peek.Result == "" && peek.Error == "" {
			// Inbound message from plugin (e.g., "channel_inbound")
			var inbound PluginInbound
			if err := json.Unmarshal(line, &inbound); err != nil {
				log.Glob(log.CatPlugin).WithField("plugin", "stdio").Warn("Failed to parse inbound: ", err)
				continue
			}
			p.muxMu.Lock()
			handler := p.inboundHandler
			p.muxMu.Unlock()
			if handler != nil {
				handler(&inbound)
			}
		} else {
			// Response to a pending Call
			var resp PluginResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				// Still try to deliver as error
				p.muxMu.Lock()
				if p.pending != nil {
					p.pending.err = fmt.Errorf("parse response: %w", err)
					close(p.pending.done)
					p.pending = nil
				}
				p.muxMu.Unlock()
				continue
			}

			p.muxMu.Lock()
			pc := p.pending
			if pc != nil {
				pc.resp = &resp
				close(pc.done)
				p.pending = nil
			}
			p.muxMu.Unlock()
		}
	}
}

// Stop kills the plugin process.
func (p *StdioPluginProcess) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

// IsRunning returns whether the plugin process is still alive.
func (p *StdioPluginProcess) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// stopLocked kills the process without acquiring the lock.
// Caller must hold p.mu.
func (p *StdioPluginProcess) stopLocked() {
	if p.running {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		p.running = false
	}
}
