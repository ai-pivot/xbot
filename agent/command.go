package agent

import (
	"context"
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

// Register adds a command to the registry. Safe for concurrent use.
func (r *CommandRegistry) Register(cmd Command) {
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
