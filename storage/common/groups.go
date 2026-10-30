package common

import (
	"encoding/json"
	"errors"
	"time"
)

// ─── Groups (metadata) ───

var (
	ErrGroupNotFound  = errors.New("group not found")
	ErrGroupExists    = errors.New("group already exists")
	ErrNotGroupMember = errors.New("not a group member")
)

type GroupRow struct {
	ID          string
	Name        string
	Owner       string
	CreatedAt   string
	MemberCount int
}

type GroupMemberRow struct {
	GroupID    string
	MemberAddr string
	Status     string
	JoinedAt   string
	// Name/Role/Instance are derived from member_addr, not persisted.
	Name     string
	Role     string
	Instance string
}

// GroupMessageRow is one message carrying a group envelope.
type GroupMessageRow struct {
	ID            int64
	GroupID       string
	Kind          string
	CorrelationID string
	SenderType    string
	SenderID      string
	SenderName    string
	Role          string
	Content       string
	To            []string
	Payload       map[string]any
	CreatedAt     string
}

// GroupMessageEnvelope is written into record_data (same shape as the
// protocol envelope; storage does not import protocol).
type GroupMessageEnvelope struct {
	GroupID       string         `json:"-"`
	Kind          string         `json:"kind"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	To            []string       `json:"to,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	SenderType    string         `json:"-"`
	SenderID      string         `json:"-"`
	SenderName    string         `json:"-"`
}

// JSON serializes the persisted envelope fields.
func (e GroupMessageEnvelope) JSON() string {
	raw, _ := json.Marshal(struct {
		Kind          string         `json:"kind"`
		CorrelationID string         `json:"correlation_id,omitempty"`
		To            []string       `json:"to,omitempty"`
		Payload       map[string]any `json:"payload,omitempty"`
	}{e.Kind, e.CorrelationID, e.To, e.Payload})
	return string(raw)
}

// NormalizeGroupMessageTime ensures a message timestamp exists.
func NormalizeGroupMessageTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
