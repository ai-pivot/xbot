package plugin

import (
	"errors"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Sentinel errors — use with errors.Is()
// ---------------------------------------------------------------------------

// ErrPluginNotFound indicates a plugin was not found.
var ErrPluginNotFound = errors.New("plugin: not found")

// ErrPluginAlreadyRegistered indicates a plugin ID conflict.
var ErrPluginAlreadyRegistered = errors.New("plugin: already registered")

// ---------------------------------------------------------------------------
// Structured error types — use with errors.As()
// ---------------------------------------------------------------------------

// ErrPluginActivationFailed is returned when plugin activation fails
// (timeout, panic, or Activate() returning an error).
type ErrPluginActivationFailed struct {
	PluginID string
	Err      error
}

func (e *ErrPluginActivationFailed) Error() string {
	return fmt.Sprintf("plugin %s: activation failed: %v", e.PluginID, e.Err)
}

func (e *ErrPluginActivationFailed) Unwrap() error { return e.Err }

// ---------------------------------------------------------------------------
// PermissionError — migrated from context.go for centralized error definitions
// ---------------------------------------------------------------------------

// PermissionError is returned when a plugin attempts an unauthorized action.
type PermissionError struct {
	PluginID   string
	Permission string
	Action     string
}

func (e *PermissionError) Error() string {
	return "plugin " + e.PluginID + ": permission denied for '" + e.Permission + "' (action: " + e.Action + ")"
}

// ---------------------------------------------------------------------------
// Dependency Errors — circular and missing dependency detection
// ---------------------------------------------------------------------------

// ErrCircularDependency indicates a cycle was detected in the plugin dependency graph.
type ErrCircularDependency struct {
	Cycle []string // plugin IDs involved in the dependency cycle
}

func (e *ErrCircularDependency) Error() string {
	return fmt.Sprintf("plugin: circular dependency detected among: %s", strings.Join(e.Cycle, " → "))
}

// ErrMissingDependency indicates a plugin depends on another plugin that is not installed.
type ErrMissingDependency struct {
	PluginID string // the plugin declaring the dependency
	Missing  string // the dependency that is not available
}

func (e *ErrMissingDependency) Error() string {
	return fmt.Sprintf("plugin %s: missing dependency %s", e.PluginID, e.Missing)
}
