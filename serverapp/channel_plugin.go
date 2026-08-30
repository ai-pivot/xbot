package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/plugin"
	"xbot/tools"

	log "xbot/logger"
	"xbot/protocol"
)

// RPCTableDispatcher is the interface needed by the channel provider to
// dispatch RPC calls from plugin→xbot. Satisfied by *RPCTable.
type RPCTableDispatcher interface {
	Dispatch(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
}

// ---------------------------------------------------------------------------
// stdioChannelPluginProvider — channel.ChannelProvider backed by a separate
// plugin process communicating via bidirectional JSON-RPC over stdin/stdout.
// ---------------------------------------------------------------------------

type stdioChannelPluginProvider struct {
	decl        *plugin.ChannelProviderDecl
	msgBus      *bus.MessageBus
	rpcDisp     func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
	getRegistry func() *tools.Registry // lazy registry getter (resolved after agent init)
	agentGetter func() *agent.Agent    // lazy agent getter (for prompt registration)
	xbotHome    string                 // for per-plugin log file redirection

	mu   sync.Mutex
	conn *agent.ChannelPluginTransport
	proc *channelProcess // child process backing conn; killed on channel replace
}

var _ channel.ChannelProvider = (*stdioChannelPluginProvider)(nil)

// NewStdioChannelPluginProvider creates a stdioChannelPluginProvider with the
// given declaration, RPC dispatch table, tool registry, and agent getter.
// Used by both CLI and server modes. registry may be nil if channel tool
// registration is not needed. getAgent may be nil if agent is not yet available
// (use SetAgentGetter later).
func NewStdioChannelPluginProvider(decl *plugin.ChannelProviderDecl, rpcTable RPCTableDispatcher, registry *tools.Registry, getAgent func() *agent.Agent) *stdioChannelPluginProvider {
	return &stdioChannelPluginProvider{
		decl: decl,
		rpcDisp: func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
			return rpcTable.Dispatch(ctx, method, payload)
		},
		getRegistry: func() *tools.Registry { return registry },
		agentGetter: getAgent,
	}
}

func (p *stdioChannelPluginProvider) Name() string {
	return p.decl.Name
}

func (p *stdioChannelPluginProvider) CreateChannel(cfg map[string]string, msgBus *bus.MessageBus) (channel.Channel, error) {
	p.msgBus = msgBus

	// Replace any previous channel process: a reload must REPLACE the child
	// process, not leak it. Without this, the stale process keeps running and
	// tool routing still bound to the old transport executes against the OLD
	// binary (2026-08-28: plugin reload spawned a second genui process while
	// the first kept serving execute_tool with stale code → false syntax
	// rejections on every large GenUI call).
	p.mu.Lock()
	oldProc, oldConn := p.proc, p.conn
	p.proc, p.conn = nil, nil
	p.mu.Unlock()
	p.replaceChannelProcess(oldProc, oldConn)

	// Spawn a dedicated process for the channel.
	proc, err := spawnChannelProcess(p.decl, p.xbotHome)
	if err != nil {
		return nil, fmt.Errorf("spawn channel process: %w", err)
	}

	// Create the bidirectional transport.
	eventCh := make(chan protocol.WSMessage, 256)
	// Resolve registry lazily (agent may not be available at factory creation time).
	var reg *tools.Registry
	if p.getRegistry != nil {
		reg = p.getRegistry()
	}

	// Set up the OnChannelPrompt callback to register with the Agent.
	var onChannelPrompt func(agent.ChannelPromptProvider)
	if p.agentGetter != nil {
		onChannelPrompt = func(provider agent.ChannelPromptProvider) {
			if ag := p.agentGetter(); ag != nil {
				ag.AddChannelPromptProvider(provider)
			}
		}
	}

	// Set up the OnChannelUI callback to store web UI components in the Agent's
	// WebUIRegistry (which pushes them to web clients via NotifyWidgetsUpdated).
	var onChannelUI func(decls []plugin.WebUIComponent)
	if p.agentGetter != nil {
		onChannelUI = func(decls []plugin.WebUIComponent) {
			if ag := p.agentGetter(); ag != nil {
				ag.RegisterChannelWebUI(p.decl.Name, decls)
			}
		}
	}

	transport := agent.NewChannelPluginTransport(agent.ChannelPluginTransportConfig{
		Name:            p.decl.Name,
		Stdin:           proc.stdinPipe,
		Stdout:          proc.stdoutPipe,
		Dispatch:        p.rpcDisp,
		EventCh:         eventCh,
		Registry:        reg,
		OnChannelPrompt: onChannelPrompt,
		OnChannelUI:     onChannelUI,
	})

	p.mu.Lock()
	p.conn = transport
	p.proc = proc
	p.mu.Unlock()

	// Send initial config to the plugin as an event.
	configMsg := protocol.WSMessage{
		Type: "channel_config",
	}
	if cfgBytes, err := json.Marshal(cfg); err == nil {
		configMsg.Metadata = map[string]string{"config": string(cfgBytes)}
	}
	if err := transport.PushEvent(configMsg); err != nil {
		log.WithError(err).WithField("channel", p.decl.Name).Warn("Failed to push initial config")
	}

	return transport, nil
}

// replaceChannelProcess tears down the previous channel plugin child process
// and its transport. Close the transport first (unblocks its read pump and any
// pending RPC callers), then kill the child. Kill errors are logged, not
// fatal — the freshly spawned process replaces them either way.
func (p *stdioChannelPluginProvider) replaceChannelProcess(proc *channelProcess, conn *agent.ChannelPluginTransport) {
	if conn != nil {
		_ = conn.Close()
	}
	if proc != nil && proc.cmd.Process != nil {
		if err := proc.cmd.Process.Kill(); err != nil {
			log.WithField("channel", p.decl.Name).WithError(err).Warn("Failed to kill previous channel plugin process")
		}
		// Reap the child (avoids a zombie) and close the parent-side stderr
		// fd. cmd.Stderr only dups the fd into the child — the parent's fd
		// would leak on every plugin reload without this close.
		w := proc.stderrWriter
		go func() {
			_ = proc.cmd.Wait()
			if w != nil {
				_ = w.Close()
			}
		}()
	}
}

func (p *stdioChannelPluginProvider) ConfigSchema() []channel.SettingDefinition {
	schema := make([]channel.SettingDefinition, 0, len(p.decl.ConfigSchema))
	for _, s := range p.decl.ConfigSchema {
		sd := channel.SettingDefinition{
			Key:          strVal(s["key"]),
			Label:        strVal(s["label"]),
			Description:  strVal(s["description"]),
			Type:         channel.SettingType(strVal(s["type"])),
			DefaultValue: strVal(s["default_value"]),
			Category:     strVal(s["category"]),
		}
		if v, ok := s["read_only"]; ok {
			sd.ReadOnly = boolVal(v)
		}
		if opts, ok := s["options"].([]any); ok {
			for _, o := range opts {
				if m, ok := o.(map[string]any); ok {
					sd.Options = append(sd.Options, channel.SettingOption{
						Label: strVal(m["label"]),
						Value: strVal(m["value"]),
					})
				}
			}
		}
		schema = append(schema, sd)
	}
	return schema
}

func (p *stdioChannelPluginProvider) IsEnabled(cfg map[string]string) bool {
	if cfg == nil {
		return false
	}
	return cfg["enabled"] == "true"
}

// GetTransport returns the active transport, if any.
func (p *stdioChannelPluginProvider) GetTransport() *agent.ChannelPluginTransport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn
}

// SetAgentGetter sets the lazy agent getter for prompt and other
// agent-dependent registrations. Should be called before CreateChannel.
func (p *stdioChannelPluginProvider) SetAgentGetter(getter func() *agent.Agent) {
	p.agentGetter = getter
}

// GetChannelPromptProvider returns the ChannelPromptProvider for this channel
// plugin, or nil if the transport is not yet created.
func (p *stdioChannelPluginProvider) GetChannelPromptProvider() agent.ChannelPromptProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	return p.conn.ChannelPromptProvider()
}

// ---------------------------------------------------------------------------
// channelProcess — manages the lifecycle of a channel plugin process.
// ---------------------------------------------------------------------------

type channelProcess struct {
	cmd        *exec.Cmd
	stdinPipe  io.WriteCloser
	stdoutPipe io.Reader
	// stderrWriter is the parent-side fd of the per-plugin stderr log file
	// (cmd.Stderr). It MUST be closed when the process is torn down — the OS
	// only closes the child's dup, not the parent's fd. Tracked here so
	// replaceChannelProcess can release it after killing the process.
	stderrWriter io.WriteCloser
}

func spawnChannelProcess(decl *plugin.ChannelProviderDecl, xbotHome string) (*channelProcess, error) {
	var cmd *exec.Cmd
	if decl.Executable != "" {
		cmd = exec.Command(decl.Executable, decl.Args...)
	} else {
		parts := strings.Fields(decl.Entry)
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty entry command for channel %s", decl.Name)
		}
		cmd = exec.Command(parts[0], parts[1:]...)
	}
	cmd.Dir = decl.Dir

	// Redirect stderr to per-plugin log file instead of os.Stderr.
	// This keeps channel plugin process output (DEBUG logs, HTTP traces, etc.)
	// out of the main xbot log — consistent with Go plugin log isolation.
	stderrWriter, err := openPluginStderrWriter(decl.Name, xbotHome)
	if err != nil {
		log.WithField("channel", decl.Name).WithError(err).
			Warn("Failed to open plugin log file for stderr, falling back to os.Stderr")
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = stderrWriter
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start channel process: %w", err)
	}

	log.WithField("channel", decl.Name).WithField("pid", cmd.Process.Pid).Info("Channel process spawned")

	return &channelProcess{
		cmd:          cmd,
		stdinPipe:    stdinPipe,
		stdoutPipe:   stdoutPipe,
		stderrWriter: stderrWriter,
	}, nil
}

// openPluginStderrWriter creates (or opens) a log file for the channel plugin
// process's stderr. The file is at <xbotHome>/plugins/<channelName>/logs/stderr.log.
// Returns an *os.File that the caller assigns to cmd.Stderr. The child gets a
// dup of the fd; the parent-side fd returned here MUST be closed explicitly
// when the process is torn down (replaceChannelProcess does this) — the OS
// does NOT close the parent's fd when the child exits.
// If xbotHome is empty, returns an error so the caller falls back to os.Stderr.
func openPluginStderrWriter(channelName, xbotHome string) (*os.File, error) {
	if xbotHome == "" {
		return nil, fmt.Errorf("xbotHome is empty")
	}
	dir := filepath.Join(xbotHome, "plugins", channelName, "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create plugin log dir: %w", err)
	}
	logPath := filepath.Join(dir, "stderr.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open plugin stderr log: %w", err)
	}
	return f, nil
}
