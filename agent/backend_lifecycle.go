package agent

import "context"

// Lifecycle groups methods for backend lifecycle management.
type Lifecycle interface {
	Start(ctx context.Context) error
	Stop()
	Close() error
	Run(ctx context.Context) error
}
