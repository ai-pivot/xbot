package common

import (
	"errors"
	"time"
)

// ─── Structured messages (report / ask_user / answer / decision / inbox) ───

const (
	StructuredKindText     = "text"
	StructuredKindAskUser  = "ask_user"
	StructuredKindAnswer   = "answer"
	StructuredKindReport   = "report"
	StructuredKindDecision = "decision"
	StructuredKindCommand  = "command"
	StructuredKindEvent    = "event"
)

// StructuredMessageKinds is the kind whitelist (API validation + docs).
var StructuredMessageKinds = map[string]bool{
	StructuredKindText:     true,
	StructuredKindAskUser:  true,
	StructuredKindAnswer:   true,
	StructuredKindReport:   true,
	StructuredKindDecision: true,
	StructuredKindCommand:  true,
	StructuredKindEvent:    true,
}

// Closed-loop domain errors (handlers map them to HTTP status codes).
var (
	ErrStructuredNotFound         = errors.New("structured message not found")
	ErrAskUserAnswered            = errors.New("target already answered")
	ErrAskUserCancelled           = errors.New("target already cancelled")
	ErrDecisionTargetNotDecidable = errors.New("decision target is not a report or ask_user")
	ErrDecisionSelfApproval       = errors.New("a department must not approve its own report")
	ErrIdempotencyConflict        = errors.New("idempotency key conflict")
)

// StructuredMessage is the full shape of one structured message row.
type StructuredMessage struct {
	ID             int64
	ChatID         string
	Kind           string
	CorrelationID  string
	ReplyTo        string
	IdempotencyKey string
	StructuredJSON string
	Direction      string // in | out
	Answered       bool
	Cancelled      bool

	Role       string
	Content    string
	SenderType string
	SenderID   string
	SenderName string

	CreatedAt time.Time
}

// StructuredRole decides the row role by kind: ask/report are assistant
// output; answer/decision are user input; the rest default to user unless the
// caller sets Role explicitly.
func StructuredRole(m StructuredMessage) string {
	if m.Role != "" {
		return m.Role
	}
	switch m.Kind {
	case StructuredKindAskUser, StructuredKindReport:
		return "assistant"
	default:
		return "user"
	}
}
