package agent

import (
	"context"
	"errors"
)

// Sentinel errors for service availability checks.
var (
	ErrSettingsUnavailable      = errors.New("settings service not available")
	ErrBgTasksUnavailable       = errors.New("background tasks not available")
	ErrSubscriptionsUnavailable = errors.New("subscription service not available")
	ErrNoSessionManager         = errors.New("no session manager")
)

// AgentRunner manages the Agent's lifecycle (start, run, stop).
type AgentRunner interface {
	Start(ctx context.Context) error
	Stop()
	Run(ctx context.Context) error
}

// BgTaskJSON is a JSON-serializable background task summary.
type BgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

// TenantInfo is a JSON-serializable tenant summary.
type TenantInfo struct {
	ID           int64  `json:"id"`
	Channel      string `json:"channel"`
	ChatID       string `json:"chat_id"`
	Label        string `json:"label,omitempty"`
	Model        string `json:"model,omitempty"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}
