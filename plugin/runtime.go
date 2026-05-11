package plugin

import (
	"context"
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
	registry map[string]Plugin // pre-registered native plugins
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
// gRPC Runtime — external process plugins
// ---------------------------------------------------------------------------

// grpcPluginProcess manages an external plugin process communicating via
// JSON-over-stdin/stdout protocol. Full gRPC with protobuf will be added
// in a future iteration when the proto definition is finalized.
type grpcPluginProcess struct {
	cmd     *exec.Cmd
	stdin   *jsonLineWriter
	stdout  *jsonLineReader
	mu      sync.Mutex
	running bool
}

// NewGRPCRuntime creates a factory for gRPC plugin processes.
func NewGRPCRuntime() RuntimeFactory {
	return &grpcRuntimeFactory{}
}

type grpcRuntimeFactory struct{}

func (f *grpcRuntimeFactory) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	if manifest.Entry == "" {
		return nil, fmt.Errorf("grpc plugin %s: entry command is required", manifest.ID)
	}
	return &grpcPlugin{
		manifest: *manifest,
		dir:      dir,
	}, nil
}

// grpcPlugin implements Plugin for external gRPC/stdio processes.
type grpcPlugin struct {
	manifest PluginManifest
	dir      string
	process  *grpcPluginProcess
}

func (g *grpcPlugin) Manifest() PluginManifest {
	return g.manifest
}

func (g *grpcPlugin) Activate(ctx PluginContext) error {
	proc, err := startPluginProcess(g.manifest.Entry, g.manifest.Executable, g.manifest.Args, g.dir)
	if err != nil {
		return fmt.Errorf("start plugin process: %w", err)
	}
	g.process = proc

	// Send activate command to the external process
	req := &pluginRequest{
		Method: "activate",
		Params: map[string]any{
			"pluginId": g.manifest.ID,
		},
	}
	resp, err := proc.call(context.Background(), req)
	if err != nil {
		proc.stop()
		return fmt.Errorf("activate call failed: %w", err)
	}
	if resp.Error != "" {
		proc.stop()
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
			proc.stop()
			return fmt.Errorf("register remote tool %q: %w", t.Name, err)
		}
	}

	// Register hooks from the response
	for _, h := range resp.Hooks {
		handler := g.makeRemoteHookHandler(h.Event, h.Matcher)
		if err := ctx.OnEvent(HookEvent(h.Event), h.Matcher, handler); err != nil {
			proc.stop()
			return fmt.Errorf("register remote hook %q: %w", h.Event, err)
		}
	}

	// Register context enrichers from the response
	for _, e := range resp.Enrichers {
		enricher := g.makeRemoteEnricher(e.Name)
		if err := ctx.EnrichContext(e.Name, enricher); err != nil {
			proc.stop()
			return fmt.Errorf("register remote enricher %q: %w", e.Name, err)
		}
	}

	return nil
}

func (g *grpcPlugin) Deactivate(ctx PluginContext) error {
	if g.process == nil {
		return nil
	}
	req := &pluginRequest{Method: "deactivate"}
	if _, err := g.process.call(context.Background(), req); err != nil {
		log.WithField("plugin", g.manifest.ID).Warn("Deactivate call failed: ", err)
	}
	g.process.stop()
	g.process = nil
	return nil
}

func (g *grpcPlugin) makeRemoteHookHandler(event, matcher string) HookHandler {
	return func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		req := &pluginRequest{
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
		resp, err := g.process.call(ctx, req)
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
func (g *grpcPlugin) makeRemoteEnricher(name string) ContextEnricher {
	return func(ctx context.Context) (string, error) {
		req := &pluginRequest{
			Method: "enrich",
			Params: map[string]any{
				"enricherName": name,
			},
		}
		resp, err := g.process.call(ctx, req)
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
	process  *grpcPluginProcess
}

func (rt *remoteTool) Definition() ToolDef {
	return rt.def
}

func (rt *remoteTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	req := &pluginRequest{
		Method: "execute_tool",
		Params: map[string]any{
			"toolName": rt.def.Name,
			"input":    input,
		},
	}
	resp, err := rt.process.call(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return NewToolError(resp.Error), nil
	}
	return NewToolResult(resp.Result), nil
}

// ---------------------------------------------------------------------------
// JSON-over-stdio protocol types
// ---------------------------------------------------------------------------

type pluginRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type pluginResponse struct {
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Tools      []ToolDef     `json:"tools,omitempty"`
	Hooks      []hookReg     `json:"hooks,omitempty"`
	HookResult *HookResult   `json:"hook_result,omitempty"`
	Enrichers  []enricherReg `json:"enrichers,omitempty"`
}

type hookReg struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
}

type enricherReg struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// Process lifecycle
// ---------------------------------------------------------------------------

func startPluginProcess(entry, executable string, args []string, dir string) (*grpcPluginProcess, error) {
	var cmd *exec.Cmd
	if executable != "" {
		cmd = exec.Command(executable, args...)
	} else {
		// Fallback: safely split entry into command + args (no shell)
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

	return &grpcPluginProcess{
		cmd:     cmd,
		stdin:   &jsonLineWriter{w: stdinPipe},
		stdout:  newJSONLineReader(stdoutPipe),
		running: true,
	}, nil
}

const pluginCallTimeout = 30 * time.Second

func (p *grpcPluginProcess) call(ctx context.Context, req *pluginRequest) (*pluginResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil, fmt.Errorf("plugin process not running")
	}

	if err := p.stdin.write(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	done := make(chan *pluginResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp := &pluginResponse{}
		if err := p.stdout.read(resp); err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()

	select {
	case resp := <-done:
		return resp, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		// Kill the process on context cancellation to prevent goroutine leak.
		// The read goroutine will unblock when the process exits and stdout closes.
		p.stopLocked()
		return nil, ctx.Err()
	case <-time.After(pluginCallTimeout):
		// Kill the process on timeout to prevent goroutine leak.
		p.stopLocked()
		return nil, fmt.Errorf("plugin call timeout (%v)", pluginCallTimeout)
	}
}

func (p *grpcPluginProcess) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

// stopLocked kills the process without acquiring the lock.
// Caller must hold p.mu.
func (p *grpcPluginProcess) stopLocked() {
	if p.running {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		p.running = false
	}
}
