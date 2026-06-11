package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type FileSnapshot struct {
	TurnIdx    int    `json:"turn_idx"`
	ToolName   string `json:"tool_name"`
	FilePath   string `json:"file_path"`
	Existed    bool   `json:"existed"`
	ContentB64 string `json:"content_b64,omitempty"`
}

type ApprovalRequest struct {
	ToolName    string `json:"tool_name"`
	ToolArgs    string `json:"tool_args"`
	RunAs       string `json:"run_as"`
	Reason      string `json:"reason,omitempty"`
	Command     string `json:"command,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	ArgsSummary string `json:"args_summary,omitempty"`
}

type ApprovalResult struct {
	Approved   bool   `json:"approved"`
	DenyReason string `json:"deny_reason,omitempty"`
}

type ApprovalHandler interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error)
}

type ApprovalState struct {
	mu      sync.RWMutex
	handler ApprovalHandler
	Timeout time.Duration `json:"timeout"`
}

// SetHandler replaces the approval handler at runtime.
func (s *ApprovalState) SetHandler(h ApprovalHandler) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

// GetHandler returns the current approval handler.
func (s *ApprovalState) GetHandler() ApprovalHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handler
}

type RewindResult struct {
	Restored   []string `json:"restored"`
	CreatedDel []string `json:"created_del"`
	Skipped    []string `json:"skipped"`
	Errors     []string `json:"errors"`
}

type CheckpointStore interface {
	Rewind(turnIdx int) (RewindResult, error)
	HasChanges(turnIdx int) bool
	CountChanges(turnIdx int) int
	Write(snap FileSnapshot) error
}

type CheckpointState struct {
	mu      sync.Mutex
	store   CheckpointStore
	turnIdx int
	pending map[string]FileSnapshot
}

// NewCheckpointState creates a CheckpointState backed by the given store.
func NewCheckpointState(store CheckpointStore) *CheckpointState {
	return &CheckpointState{
		store:   store,
		pending: make(map[string]FileSnapshot),
	}
}

// SetTurnIdx sets the current turn index. Should be called before each agent turn.
func (cs *CheckpointState) SetTurnIdx(idx int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.turnIdx = idx
}

// TurnIdx returns the current turn index.
func (cs *CheckpointState) TurnIdx() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.turnIdx
}

// Store returns the underlying CheckpointStore.
func (cs *CheckpointState) Store() CheckpointStore {
	return cs.store
}

// SetStore replaces the underlying CheckpointStore. Used when switching sessions
// to point the shared CheckpointState at a session-specific store.
// The old store (if any) is NOT closed — the caller manages store lifecycle.
func (cs *CheckpointState) SetStore(store CheckpointStore) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.store = store
}

// SetPending stores a file snapshot for the given path.
func (cs *CheckpointState) SetPending(filePath string, snap FileSnapshot) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.pending[filePath] = snap
}

// GetAndDeletePending retrieves and removes the snapshot for the given path.
func (cs *CheckpointState) GetAndDeletePending(filePath string) (FileSnapshot, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	snap, found := cs.pending[filePath]
	if found {
		delete(cs.pending, filePath)
	}
	return snap, found
}

// ExtractRunAsAndReason parses the "run_as" and "reason" fields from JSON tool arguments.
// Returns empty strings if not present or on parse error.
func ExtractRunAsAndReason(args string) (runAs, reason string) {
	var raw struct {
		RunAs  string `json:"run_as"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", ""
	}
	return raw.RunAs, raw.Reason
}

// TruncateApprovalText truncates s to at most max bytes, appending "..." if
// truncated. Whitespace is trimmed first.
func TruncateApprovalText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// PopulateApprovalDetails extracts human-readable details for the approval dialog.
func PopulateApprovalDetails(req *ApprovalRequest, toolName, args string) {
	const maxDisplayLen = 160

	switch toolName {
	case "Shell":
		var p struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.Command = TruncateApprovalText(p.Command, maxDisplayLen)
			req.ArgsSummary = req.Command
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = TruncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Execute command as %q", req.RunAs)
			}
		}
	case "FileCreate":
		var p struct {
			Path   string `json:"path"`
			RunAs  string `json:"run_as"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = TruncateApprovalText(p.Path, maxDisplayLen)
			req.ArgsSummary = req.FilePath
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = TruncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Create file as %q", req.RunAs)
			}
		}
	case "FileReplace":
		var p struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
			Reason    string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = TruncateApprovalText(p.Path, maxDisplayLen)
			req.ArgsSummary = fmt.Sprintf("old=%q new=%q", TruncateApprovalText(p.OldString, 40), TruncateApprovalText(p.NewString, 40))
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = TruncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Modify file as %q", req.RunAs)
			}
		}
	}
	if req.Reason == "" {
		req.Reason = fmt.Sprintf("Execute %s as %q", toolName, req.RunAs)
	}
}
