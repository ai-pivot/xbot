package runner

import "errors"

// Sentinel errors for runner operations.
var (
	ErrNotFound      = errors.New("runner not found")
	ErrAlreadyExists = errors.New("runner already exists")
	ErrNotConnected  = errors.New("runner not connected")
	ErrLocalOnly     = errors.New("operation only supported on local runner")
)
