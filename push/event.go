// Package push is the unified, provider-agnostic notification layer.
//
// A semantic Event (ask_user / turn_done / test) is delivered to Targets
// (devices, browsers, IM chats) by a Provider. Adding a transport means adding
// a Provider + a config section — the Notifier and the HTTP handlers never
// learn the provider's name.
package push

import (
	"errors"
	"fmt"
)

// Kind is the semantic meaning of a push; providers only decide how to render it.
type Kind string

const (
	KindAskUser  Kind = "ask_user"
	KindTurnDone Kind = "turn_done"
	KindTest     Kind = "test"
)

// Event is transport-independent. Providers read Title/Body and the metadata
// needed for deep links; they never inspect ChatID semantics.
type Event struct {
	Kind Kind
	// Action is set only for session-state events (busy/idle/...). The Notifier
	// turns a busy→idle transition into a KindTurnDone event.
	Action       string
	Channel      string
	ChatID       string
	SenderID     string
	Title        string
	Body         string
	RequestID    string // ask_user dedupe key
	HighPriority bool
	CollapseKey  string
	TS           int64
}

// ErrorKind tells the Notifier how to react to a delivery failure.
type ErrorKind int

const (
	// Permanent: the target is dead (unregistered/invalid) — delete it.
	Permanent ErrorKind = iota
	// Transient: retry with backoff.
	Transient
	// Auth: the provider credentials expired — refresh and retry once.
	Auth
)

// DeliveryError classifies a provider failure.
type DeliveryError struct {
	Kind ErrorKind
	Err  error
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("push delivery (%d): %v", e.Kind, e.Err)
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// PermanentError marks a target as dead.
func PermanentError(err error) error { return &DeliveryError{Kind: Permanent, Err: err} }

// TransientError marks a retryable failure.
func TransientError(err error) error { return &DeliveryError{Kind: Transient, Err: err} }

// AuthError marks an expired provider credential.
func AuthError(err error) error { return &DeliveryError{Kind: Auth, Err: err} }

// ErrorKindOf reports the classification of err; unknown errors are Permanent
// (never retry blindly).
func ErrorKindOf(err error) ErrorKind {
	var de *DeliveryError
	if errors.As(err, &de) {
		return de.Kind
	}
	return Permanent
}
