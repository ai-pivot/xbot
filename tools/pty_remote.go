package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"xbot/internal/runnerproto"
	log "xbot/logger"
)

// ptyStream tracks an active PTY stream on the server side.
// Unlike stdioStream (which uses io.Pipe for a single MCP reader),
// ptyStream uses callbacks so the web channel can broadcast output
// to multiple browser WebSocket clients simultaneously.
type ptyStream struct {
	mu       sync.Mutex
	exited   bool
	exitCode int

	// onData is called with decoded PTY output bytes (stdout+stderr combined).
	onData func(data []byte)
	// onExit is called when the PTY process exits.
	onExit func(exitCode int)
}

// registerPtyStream registers a PTY stream for push message routing.
func (rs *RemoteSandbox) registerPtyStream(streamID string, stream *ptyStream) {
	rs.ptyMu.Lock()
	defer rs.ptyMu.Unlock()
	if rs.ptyStreams == nil {
		rs.ptyStreams = make(map[string]*ptyStream)
	}
	rs.ptyStreams[streamID] = stream
}

// removePtyStream removes a PTY stream from push routing.
func (rs *RemoteSandbox) removePtyStream(streamID string) {
	rs.ptyMu.Lock()
	defer rs.ptyMu.Unlock()
	delete(rs.ptyStreams, streamID)
}

// handlePtyPush routes incoming PTY push messages from the runner.
// Returns true if the message was handled as a PTY push.
func (rs *RemoteSandbox) handlePtyPush(resp *RunnerMessage) bool {
	switch resp.Type {
	case runnerproto.ProtoPtyData:
		var data runnerproto.PtyDataMessage
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return true
		}
		rs.ptyMu.Lock()
		stream, ok := rs.ptyStreams[data.StreamID]
		rs.ptyMu.Unlock()
		if !ok {
			return true
		}
		decoded, err := base64.StdEncoding.DecodeString(data.Data)
		if err != nil {
			return true
		}
		stream.mu.Lock()
		cb := stream.onData
		stream.mu.Unlock()
		if cb != nil {
			cb(decoded)
		}
		return true

	case runnerproto.ProtoPtyExit:
		var exit runnerproto.PtyExitMessage
		if err := json.Unmarshal(resp.Body, &exit); err != nil {
			return true
		}
		rs.ptyMu.Lock()
		stream, ok := rs.ptyStreams[exit.StreamID]
		rs.ptyMu.Unlock()
		if !ok {
			return true
		}
		stream.mu.Lock()
		stream.exited = true
		stream.exitCode = exit.ExitCode
		cb := stream.onExit
		stream.mu.Unlock()
		if cb != nil {
			cb(exit.ExitCode)
		}
		log.WithFields(log.Fields{
			"stream_id": exit.StreamID,
			"exit_code": exit.ExitCode,
		}).Debug("Remote PTY process exited")
		return true
	}
	return false
}

// PtyCreate sends a pty_create request to the runner and registers the stream.
// The onData/onExit callbacks on the stream will be invoked for push messages.
func (rs *RemoteSandbox) PtyCreate(ctx context.Context, userID, streamID, shell, cwd string, cols, rows uint16) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return fmt.Errorf("no runner for user %q: %w", userID, err)
	}

	// Register stream so handlePtyPush can route push messages to it.
	rs.registerPtyStream(streamID, &ptyStream{})

	env := []string{"TERM=xterm-256color"}
	reqBody, _ := json.Marshal(runnerproto.PtyCreateRequest{
		StreamID: streamID,
		Command:  shell,
		Dir:      cwd,
		Env:      env,
		Cols:     cols,
		Rows:     rows,
	})
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoPtyCreate,
		UserID: userID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		rs.removePtyStream(streamID)
		return fmt.Errorf("pty_create: %w", err)
	}
	if resp.Type == ProtoError {
		rs.removePtyStream(streamID)
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return fmt.Errorf("pty_create: %s", e.Message)
	}
	return nil
}

// PtyStdin sends data to the PTY's stdin on the runner (fire-and-forget).
func (rs *RemoteSandbox) PtyStdin(userID, streamID string, data []byte) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return fmt.Errorf("runner disconnected: %w", err)
	}

	reqBody, _ := json.Marshal(runnerproto.PtyStdinRequest{
		StreamID: streamID,
		Data:     base64.StdEncoding.EncodeToString(data),
	})
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoPtyStdin,
		UserID: userID,
		Body:   reqBody,
	}
	return rs.sendOnly(rc, msg)
}

// PtyResize resizes the PTY on the runner (fire-and-forget).
func (rs *RemoteSandbox) PtyResize(userID, streamID string, cols, rows uint16) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return fmt.Errorf("runner disconnected: %w", err)
	}

	reqBody, _ := json.Marshal(runnerproto.PtyResizeRequest{
		StreamID: streamID,
		Cols:     cols,
		Rows:     rows,
	})
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoPtyResize,
		UserID: userID,
		Body:   reqBody,
	}
	return rs.sendOnly(rc, msg)
}

// PtyClose destroys the PTY on the runner (request/response).
func (rs *RemoteSandbox) PtyClose(ctx context.Context, userID, streamID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil // runner already gone
	}

	reqBody, _ := json.Marshal(runnerproto.PtyCloseRequest{StreamID: streamID})
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoPtyClose,
		UserID: userID,
		Body:   reqBody,
	}

	rs.sendRequest(ctx, rc, msg, defaultRequestTimeout) //nolint:errcheck
	rs.removePtyStream(streamID)
	return nil
}

// SetPtyHandlers sets the onData/onExit callbacks for a PTY stream.
func (rs *RemoteSandbox) SetPtyHandlers(streamID string, onData func([]byte), onExit func(int)) {
	rs.ptyMu.Lock()
	stream, ok := rs.ptyStreams[streamID]
	rs.ptyMu.Unlock()
	if !ok {
		return
	}
	stream.mu.Lock()
	stream.onData = onData
	stream.onExit = onExit
	stream.mu.Unlock()
}
