package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"xbot/bus"
	"xbot/channel"
)

// Command defines the interface for slash commands and other quick commands
// that bypass the LLM pipeline and execute directly.
type Command interface {
	// Name returns the primary command name (e.g., "/new", "/help").
	// For bang commands, this returns "!".
	Name() string

	// Aliases returns alternative names that also trigger this command.
	// For example, a command might respond to both "/version" and "/v".
	Aliases() []string

	// Match checks if the given message content should be handled by this command.
	// This allows commands with prefix matching (e.g., "/prompt <query>", "/set-llm <args>")
	// or special patterns (e.g., "!" prefix for bang commands).
	// Returns true if this command should handle the message.
	Match(content string) bool

	// Execute runs the command and returns an outbound message (or nil to suppress reply).
	// The command receives the full Agent context to access sessions, tools, etc.
	Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error)

	// Concurrent reports whether this command can safely run concurrently with
	// normal message processing. Stateless commands (e.g., /version, /help)
	// return true and are dispatched in an independent goroutine. Commands that
	// mutate session state (e.g., /new, /compress) return false and are
	// serialized through the normal message queue to avoid data races.
	Concurrent() bool
}

// CommandRegistry holds registered commands and provides lookup.
// Thread-safe: Register and Match can be called concurrently.
type CommandRegistry struct {
	mu       sync.RWMutex
	commands []Command
}

// NewCommandRegistry creates an empty command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{}
}

// Register adds a command with display metadata to the registry. Safe for
// concurrent use.
func (r *CommandRegistry) Register(cmd Command, info CommandInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd = &commandWithInfo{Command: cmd, info: info}
	r.commands = append(r.commands, cmd)
}

// RegisterCommand adds a command that describes itself via CommandInfoProvider
// or falls back to its Name().
func (r *CommandRegistry) RegisterCommand(cmd Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, cmd)
}

// Match finds the first command that matches the given message content.
// Returns nil if no command matches. Safe for concurrent use.
func (r *CommandRegistry) Match(content string) Command {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, cmd := range r.commands {
		if cmd.Match(trimmed) {
			return cmd
		}
	}
	return nil
}

// IsCommand returns true if the message content matches any registered command.
func (r *CommandRegistry) IsCommand(content string) bool {
	return r.Match(content) != nil
}

// Commands returns all registered commands (for /help generation, etc.).
func (r *CommandRegistry) Commands() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Command, len(r.commands))
	copy(result, r.commands)
	return result
}

// DescribedCommand is an optional interface for commands that expose a
// human-readable description. Built-in commands and pluginCmdAdapter implement
// it; commands without a description simply omit the method.
type DescribedCommand interface {
	Description() string
}

// CommandInfo is a lightweight, JSON-friendly description of a registered
// command, suitable for external consumers such as the web UI Tab-completion.
type CommandInfo struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Usage       string   `json:"usage,omitempty"`
	Description string   `json:"description,omitempty"`
	Hidden      bool     `json:"-"`
}

// CommandInfoProvider is implemented by commands that can describe themselves.
type CommandInfoProvider interface {
	CommandInfo() CommandInfo
}

// CommandList returns metadata for every registered command (built-in + plugin).
// Commands whose type does not implement DescribedCommand get an empty
// description — callers should treat an empty description as "no description".
func (r *CommandRegistry) CommandList() []CommandInfo {
	cmds := r.Commands()
	result := make([]CommandInfo, 0, len(cmds))
	for _, cmd := range cmds {
		info := commandInfoFor(cmd)
		if info.Hidden {
			continue
		}
		result = append(result, info)
	}
	return result
}

// CommandNames returns the primary names of visible registered commands.
func (r *CommandRegistry) CommandNames() []string {
	infos := r.CommandList()
	result := make([]string, 0, len(infos))
	seen := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		name := info.Name
		if name == "" && info.Usage != "" {
			fields := strings.Fields(info.Usage)
			if len(fields) > 0 {
				name = fields[0]
			}
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

// HelpText renders the registered command list. Commands are shown in
// registration order, which is also the match priority order.
func (r *CommandRegistry) HelpText() string {
	infos := r.CommandList()
	var b strings.Builder
	b.WriteString("xbot 命令:\n")
	for _, info := range infos {
		usage := info.Usage
		if usage == "" {
			usage = info.Name
		}
		if info.Description == "" {
			fmt.Fprintf(&b, "%s\n", usage)
			continue
		}
		fmt.Fprintf(&b, "%s — %s\n", usage, info.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

type commandWithInfo struct {
	Command
	info CommandInfo
}

func (c *commandWithInfo) Description() string {
	return c.CommandInfo().Description
}

func (c *commandWithInfo) CommandInfo() CommandInfo {
	info := c.info
	if info.Name == "" {
		info.Name = c.Name()
	}
	if info.Usage == "" {
		info.Usage = c.Name()
	}
	if len(info.Aliases) == 0 {
		info.Aliases = c.Aliases()
	}
	return info
}

func commandInfoFor(cmd Command) CommandInfo {
	if provider, ok := cmd.(CommandInfoProvider); ok {
		info := provider.CommandInfo()
		if info.Name == "" {
			info.Name = cmd.Name()
		}
		if info.Usage == "" {
			info.Usage = cmd.Name()
		}
		if len(info.Aliases) == 0 {
			info.Aliases = cmd.Aliases()
		}
		return info
	}
	info := CommandInfo{
		Name:    cmd.Name(),
		Usage:   cmd.Name(),
		Aliases: cmd.Aliases(),
	}
	if dc, ok := cmd.(DescribedCommand); ok {
		info.Description = dc.Description()
	}
	return info
}
