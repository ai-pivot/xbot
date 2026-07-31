package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"xbot/internal/runnerproto"
)

func (rs *RemoteSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	rc, err := rs.getRunner(spec.UserID)
	if err != nil {
		return nil, err
	}

	timeout := spec.Timeout
	if timeout == 0 {
		timeout = RemoteSandboxExecTimeout
	}

	reqBody, err := json.Marshal(ExecRequest{
		Command: spec.Command,
		Args:    spec.Args,
		Shell:   spec.Shell,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Stdin:   spec.Stdin,
		Timeout: int(timeout / time.Second),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   ProtoExec,
		UserID: spec.UserID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, timeout+5*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "exec")
	}

	var result ExecResultResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal exec result: %w", err)
	}

	return &ExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
		TimedOut: result.TimedOut,
	}, nil
}

// ExecBg starts a background command on the runner.
// Returns immediately with the task ID — the command runs asynchronously on the runner.
func (rs *RemoteSandbox) ExecBg(ctx context.Context, spec ExecSpec, taskID string) error {
	rc, err := rs.getRunner(spec.UserID)
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(runnerproto.BgExecRequest{
		TaskID:  taskID,
		Command: spec.Command,
		Args:    spec.Args,
		Shell:   spec.Shell,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Stdin:   spec.Stdin,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoBgExec,
		UserID: spec.UserID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}

	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "bg_exec")
	}

	return nil
}

// KillBg kills a background task on the runner.
func (rs *RemoteSandbox) KillBg(ctx context.Context, userID, taskID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(runnerproto.BgKillRequest{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoBgKill,
		UserID: userID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}

	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "bg_kill")
	}

	return nil
}

// StatusBg queries the current status and output of a background task on the runner.
func (rs *RemoteSandbox) StatusBg(ctx context.Context, userID, taskID string) (*RemoteBgTaskStatus, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(runnerproto.BgStatusRequest{TaskID: taskID})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoBgStatus,
		UserID: userID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "bg_status")
	}

	var result runnerproto.BgOutputResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal bg_status result: %w", err)
	}

	return &RemoteBgTaskStatus{
		TaskID:   result.TaskID,
		Status:   result.Status,
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
	}, nil
}

// RemoteBgTaskStatus holds the status snapshot of a remote background task.
type RemoteBgTaskStatus struct {
	TaskID   string
	Status   string // "running", "completed", "failed", "killed"
	ExitCode int
	Stdout   string
	Stderr   string
}
