package tools

import (
	"context"

	"xbot/protocol"
)

// contextKey is an unexported type for context keys defined in this package.
type contextKey string

const permUsersKey contextKey = "perm_users"
const workingDirKey contextKey = "working_dir"

// PermUsersFromContext retrieves the permission control user config from context.
func PermUsersFromContext(ctx context.Context) (defaultUser, privilegedUser string) {
	config, ok := ctx.Value(permUsersKey).(*PermUsersPair)
	if !ok || config == nil {
		return "", ""
	}
	return config.DefaultUser, config.PrivilegedUser
}

// PermUsersPair holds the permission control user pair for context injection.
type PermUsersPair struct {
	DefaultUser    string
	PrivilegedUser string
}

// isPermControlActiveFromCtx checks if permission control is active from context.
// Returns false when no perm users are configured (both empty).
func isPermControlActiveFromCtx(ctx context.Context) bool {
	defaultUser, privilegedUser := PermUsersFromContext(ctx)
	return defaultUser != "" || privilegedUser != ""
}

// WithPermUsers injects the permission control user config into the context.
func WithPermUsers(ctx context.Context, defaultUser, privilegedUser string) context.Context {
	return context.WithValue(ctx, permUsersKey, &PermUsersPair{
		DefaultUser:    defaultUser,
		PrivilegedUser: privilegedUser,
	})
}

// WithWorkingDir injects the agent's working directory into context.
// Used by checkpoint hook to resolve relative file paths to absolute.
func WithWorkingDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, workingDirKey, dir)
}

// WorkingDirFromContext retrieves the working directory from context.
func WorkingDirFromContext(ctx context.Context) string {
	if dir, ok := ctx.Value(workingDirKey).(string); ok {
		return dir
	}
	return ""
}

// ApprovalRequest represents a pending user approval for a tool execution.
type ApprovalRequest = protocol.ApprovalRequest

// ApprovalResult is the user's decision.
type ApprovalResult = protocol.ApprovalResult

// ApprovalHandler is the channel-agnostic interface for user approval.
// Each channel (CLI, Web) provides its own implementation.
type ApprovalHandler interface {
	// RequestApproval sends an approval request and waits for the user's response.
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error)
}
