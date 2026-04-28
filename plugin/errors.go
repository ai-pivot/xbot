package plugin

import (
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors — use with errors.Is()
// ---------------------------------------------------------------------------

// ErrPluginNotFound indicates a plugin was not found.
var ErrPluginNotFound = errors.New("plugin: not found")

// ErrPluginAlreadyRegistered indicates a plugin ID conflict.
var ErrPluginAlreadyRegistered = errors.New("plugin: already registered")

// ErrPluginNotActive indicates an operation on an inactive plugin.
// Reserved for future use when per-state operation guards are added.
var ErrPluginNotActive = errors.New("plugin: not active")

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

// ErrRateLimitExceeded is returned when a plugin exceeds its rate limit.
// Reserved for future use when Allow() is refactored to return error.
type ErrRateLimitExceeded struct {
	PluginID   string
	RetryAfter time.Duration
}

func (e *ErrRateLimitExceeded) Error() string {
	return fmt.Sprintf("plugin %s: rate limit exceeded, retry after %v", e.PluginID, e.RetryAfter)
}

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
