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
	xbotHome    string                  // for per-plugin log file redirection

	mu   sync.Mutex
	conn *agent.ChannelPluginTransport
}

var _ channel.ChannelProvider = (*stdioChannelPluginProvider)(nil)

// NewStdioChannelPluginProvider creates a stdioChannelPluginProvider with the
// given declaration, RPC dispatch table, and tool registry. Used by both CLI
// and server modes. registry may be nil if channel tool registration is not
// needed.
func NewStdioChannelPluginProvider(decl *plugin.ChannelProviderDecl, rpcTable RPCTableDispatcher, registry *tools.Registry) *stdioChannelPluginProvider {
	return &stdioChannelPluginProvider{
		decl: decl,
		rpcDisp: func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
			return rpcTable.Dispatch(ctx, method, payload)
		},
		getRegistry: func() *tools.Registry { return registry },
	}
}

func (p *stdioChannelPluginProvider) Name() string {
	return p.decl.Name
}

func (p *stdioChannelPluginProvider) CreateChannel(cfg map[string]string, msgBus *bus.MessageBus) (channel.Channel, error) {
	p.msgBus = msgBus

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

	transport := agent.NewChannelPluginTransport(agent.ChannelPluginTransportConfig{
		Name:     p.decl.Name,
		Stdin:    proc.stdinPipe,
		Stdout:   proc.stdoutPipe,
		Dispatch: p.rpcDisp,
		EventCh:  eventCh,
		Registry: reg,
	})

	p.mu.Lock()
	p.conn = transport
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

// ---------------------------------------------------------------------------
// channelProcess — manages the lifecycle of a channel plugin process.
// ---------------------------------------------------------------------------

type channelProcess struct {
	cmd        *exec.Cmd
	stdinPipe  io.WriteCloser
	stdoutPipe io.Reader
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
		cmd:        cmd,
		stdinPipe:  stdinPipe,
		stdoutPipe: stdoutPipe,
	}, nil
}

// openPluginStderrWriter creates (or opens) a log file for the channel plugin
// process's stderr. The file is at <xbotHome>/plugins/<channelName>/logs/stderr.log.
// Returns an *os.File that the caller assigns to cmd.Stderr. The OS will close
// the file when the process exits.
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
