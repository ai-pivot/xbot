package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/channel"
	"xbot/protocol"
)

// mockProcessIO implements processIO for testing.
type mockProcessIO struct {
	mu          sync.Mutex
	written     []any
	readCh      chan json.RawMessage
	closed      bool
	writeNotify chan struct{} // signals after each stdinWrite, for synchronization
}

func newMockProcessIO() *mockProcessIO {
	return &mockProcessIO{
		readCh:      make(chan json.RawMessage, 10),
		writeNotify: make(chan struct{}, 1),
	}
}

func (m *mockProcessIO) stdinWrite(v any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, v)
	// Non-blocking signal: don't block if no one is listening.
	select {
	case m.writeNotify <- struct{}{}:
	default:
	}
	return nil
}

// waitWrite blocks until at least one write has occurred.
func (m *mockProcessIO) waitWrite(ctx context.Context) error {
	m.mu.Lock()
	if len(m.written) > 0 {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	select {
	case <-m.writeNotify:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *mockProcessIO) stdoutRead() (json.RawMessage, error) {
	select {
	case msg, ok := <-m.readCh:
		if !ok {
			return nil, fmt.Errorf("closed")
		}
		return msg, nil
	case <-time.After(2 * time.Second):
		return nil, fmt.Errorf("timeout")
	}
}

func (m *mockProcessIO) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	close(m.readCh)
	return nil
}

func (m *mockProcessIO) getWritten() []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]any, len(m.written))
	copy(result, m.written)
	return result
}

// TestChannelPluginTransport_Send verifies that Send pushes a WSMessage to the plugin.
func TestChannelPluginTransport_Send(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	_, err := transport.Send(channel.OutboundMsg{
		Channel: "test",
		ChatID:  "chat1",
		Content: "Hello from agent",
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	written := pio.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 written message, got %d", len(written))
	}

	wsMsg, ok := written[0].(protocol.WSMessage)
	if !ok {
		t.Fatalf("expected WSMessage, got %T", written[0])
	}
	if wsMsg.Type != protocol.MsgTypeText {
		t.Errorf("expected type %s, got %s", protocol.MsgTypeText, wsMsg.Type)
	}
	if wsMsg.Content != "Hello from agent" {
		t.Errorf("expected content 'Hello from agent', got %s", wsMsg.Content)
	}
}

// TestChannelPluginTransport_PushEvent verifies event pushing.
func TestChannelPluginTransport_PushEvent(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	err := transport.PushEvent(protocol.WSMessage{
		Type:   protocol.MsgTypeProgress,
		ChatID: "chat1",
		Progress: &protocol.ProgressEvent{
			Phase: "thinking",
		},
	})
	if err != nil {
		t.Fatalf("PushEvent failed: %v", err)
	}

	written := pio.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 written message, got %d", len(written))
	}
}

// TestChannelPluginTransport_SendProgress verifies the ProgressSender interface.
func TestChannelPluginTransport_SendProgress(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	transport.SendProgress("chat1", &protocol.ProgressEvent{
		Phase: "tool",
	})

	written := pio.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 written message, got %d", len(written))
	}
	wsMsg, ok := written[0].(protocol.WSMessage)
	if !ok {
		t.Fatalf("expected WSMessage, got %T", written[0])
	}
	if wsMsg.Type != protocol.MsgTypeProgress {
		t.Errorf("expected type %s, got %s", protocol.MsgTypeProgress, wsMsg.Type)
	}
}

// TestChannelPluginTransport_HandlePluginRPC verifies plugin→xbot RPC dispatch.
func TestChannelPluginTransport_HandlePluginRPC(t *testing.T) {
	pio := newMockProcessIO()
	type dispatchResult struct {
		method string
	}
	dispatchCh := make(chan dispatchResult, 1)
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		dispatchCh <- dispatchResult{method: method}
		return json.Marshal(map[string]string{"status": "ok"})
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	// Simulate plugin sending an RPC request.
	pluginRequest := map[string]any{
		"id":     "req-1",
		"method": "send_inbound",
		"params": map[string]any{
			"channel":   "test",
			"chat_id":   "chat1",
			"content":   "hello",
			"chat_type": "p2p",
		},
	}
	raw, _ := json.Marshal(pluginRequest)
	pio.readCh <- raw

	// Run readLoop in background.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	go transport.readLoop(ctx)

	// Wait for dispatch to be called.
	select {
	case dr := <-dispatchCh:
		if dr.method != "send_inbound" {
			t.Errorf("expected method 'send_inbound', got %s", dr.method)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for dispatch to be called")
	}

	// Wait for writeRPCResponse to complete (called after dispatch returns).
	if err := pio.waitWrite(ctx); err != nil {
		t.Fatal("timed out waiting for response write")
	}

	// Check that a response was written back.
	written := pio.getWritten()
	if len(written) == 0 {
		t.Fatal("expected at least 1 written message (RPC response)")
	}

	// The response should be a rpcResponse.
	resp, ok := written[0].(rpcResponse)
	if !ok {
		t.Fatalf("expected rpcResponse, got %T", written[0])
	}
	if resp.ID != "req-1" {
		t.Errorf("expected response ID 'req-1', got %s", resp.ID)
	}
	if resp.Error != "" {
		t.Errorf("expected no error, got %s", resp.Error)
	}
}

// TestChannelPluginTransport_HandlePluginResponse verifies response delivery to pending calls.
func TestChannelPluginTransport_HandlePluginResponse(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	// Start a Call in a goroutine.
	callDone := make(chan error, 1)
	go func() {
		_, err := transport.Call("test_method", json.RawMessage(`{}`))
		callDone <- err
	}()

	// Wait for the call to register.
	time.Sleep(100 * time.Millisecond)

	// Simulate plugin sending a response.
	response := map[string]any{
		"id":     "srv-1",
		"result": "plugin response",
	}
	raw, _ := json.Marshal(response)
	transport.handleIncoming(raw)

	// Check that the call completed.
	select {
	case err := <-callDone:
		if err != nil {
			t.Errorf("Call failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call timed out")
	}
}

// TestChannelPluginTransport_Close verifies cleanup.
func TestChannelPluginTransport_Close(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	err := transport.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	if !pio.closed {
		t.Error("expected processIO to be closed")
	}
}

// TestChannelPluginTransport_EventPushLoop verifies the event push loop.
func TestChannelPluginTransport_EventPushLoop(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	// Start the transport (starts event push loop).
	if err := transport.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Push an event via the event channel.
	eventCh <- protocol.WSMessage{
		Type:    protocol.MsgTypeStreamContent,
		ChatID:  "chat1",
		Content: "streaming text",
	}

	// Wait for the event to be pushed.
	time.Sleep(200 * time.Millisecond)

	written := pio.getWritten()
	found := false
	for _, w := range written {
		if wsMsg, ok := w.(protocol.WSMessage); ok && wsMsg.Type == protocol.MsgTypeStreamContent {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a stream_content WSMessage to be written")
	}

	// Clean up.
	transport.Stop()
}

// TestChannelPluginTransport_SessionState verifies SessionStateSender.
func TestChannelPluginTransport_SessionState(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	transport.SendSessionState(protocol.SessionEvent{
		Channel: "test",
		ChatID:  "chat1",
		Action:  "busy",
	})

	written := pio.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 written message, got %d", len(written))
	}
	wsMsg, ok := written[0].(protocol.WSMessage)
	if !ok {
		t.Fatalf("expected WSMessage, got %T", written[0])
	}
	if wsMsg.Type != protocol.MsgTypeSession {
		t.Errorf("expected type %s, got %s", protocol.MsgTypeSession, wsMsg.Type)
	}
}

// TestChannelPluginTransport_InjectUserMessage verifies UserMessageInjector.
func TestChannelPluginTransport_InjectUserMessage(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	transport.InjectUserMessage("chat1", "scheduled task done")

	written := pio.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 written message, got %d", len(written))
	}
	wsMsg, ok := written[0].(protocol.WSMessage)
	if !ok {
		t.Fatalf("expected WSMessage, got %T", written[0])
	}
	if wsMsg.Type != protocol.MsgTypeInjectUser {
		t.Errorf("expected type %s, got %s", protocol.MsgTypeInjectUser, wsMsg.Type)
	}
	if !strings.Contains(wsMsg.Content, "scheduled task done") {
		t.Errorf("expected content to contain 'scheduled task done', got %s", wsMsg.Content)
	}
}

// TestChannelPluginTransport_ReadLoopExitsOnContext verifies readLoop exits cleanly.
func TestChannelPluginTransport_ReadLoopExitsOnContext(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		transport.readLoop(ctx)
		close(done)
	}()

	// Cancel context after a short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Success — readLoop exited.
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not exit after context cancellation")
	}
}

// TestChannelPluginTransport_StreamContent verifies SendStreamContent.
func TestChannelPluginTransport_StreamContent(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	transport.SendStreamContent("chat1", "Hello ", "thinking about...")

	written := pio.getWritten()
	if len(written) != 1 {
		t.Fatalf("expected 1 written message, got %d", len(written))
	}
	wsMsg, ok := written[0].(protocol.WSMessage)
	if !ok {
		t.Fatalf("expected WSMessage, got %T", written[0])
	}
	if wsMsg.Type != protocol.MsgTypeStreamContent {
		t.Errorf("expected type %s, got %s", protocol.MsgTypeStreamContent, wsMsg.Type)
	}
	if wsMsg.Content != "Hello " {
		t.Errorf("expected content 'Hello ', got %s", wsMsg.Content)
	}
	if wsMsg.Progress == nil || wsMsg.Progress.ReasoningStreamContent != "thinking about..." {
		t.Error("expected reasoning stream content in progress")
	}
}

// TestChannelPluginTransport_CallOnClosedTransport verifies Call returns error on closed transport.
func TestChannelPluginTransport_CallOnClosedTransport(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	// Close the transport first.
	transport.Stop()

	// Call on closed transport should fail immediately.
	_, err := transport.Call("test_method", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error on closed transport")
	}
}

// TestChannelPluginTransport_ChannelInterface verifies all Channel interface methods.
func TestChannelPluginTransport_ChannelInterface(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)

	// Name
	if transport.Name() != "test" {
		t.Errorf("expected name 'test', got %s", transport.Name())
	}

	// Start
	if err := transport.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop (should not panic)
	transport.Stop()

	// Double Stop (should not panic)
	transport.Stop()
}

// TestChannelPluginTransport_HandleChannelPrompt verifies the channel_prompt message handling.
func TestChannelPluginTransport_HandleChannelPrompt(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	var capturedProvider ChannelPromptProvider
	transport := NewChannelPluginTransportWithIO("telegram", pio, dispatch, eventCh)
	transport.onChannelPrompt = func(provider ChannelPromptProvider) {
		capturedProvider = provider
	}
	defer transport.Stop()

	// Simulate receiving a channel_prompt message via handleChannelPrompt directly
	raw, _ := json.Marshal(map[string]interface{}{
		"type": "channel_prompt",
		"system_parts": map[string]string{
			"05_channel_telegram": "telegram specific rules",
		},
	})
	transport.handleChannelPrompt(json.RawMessage(raw))

	// Verify the callback was called
	if capturedProvider == nil {
		t.Fatal("expected OnChannelPrompt callback to be called")
	}
	if name := capturedProvider.ChannelPromptName(); name != "telegram" {
		t.Errorf("expected name 'telegram', got %q", name)
	}

	// Verify system parts are stored correctly
	parts := capturedProvider.ChannelSystemParts(context.Background(), "chat1", "user1")
	if got := parts["05_channel_telegram"]; got != "telegram specific rules" {
		t.Errorf("expected 'telegram specific rules', got %q", got)
	}
}

// TestChannelPluginTransport_HandleChannelPrompt_NoCallback verifies that
// channel_prompt works without OnChannelPrompt callback set (no panic).
func TestChannelPluginTransport_HandleChannelPrompt_NoCallback(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)
	defer transport.Stop()

	// Should not panic
	raw, _ := json.Marshal(map[string]interface{}{
		"type": "channel_prompt",
		"system_parts": map[string]string{
			"05_test": "test rules",
		},
	})
	transport.handleChannelPrompt(json.RawMessage(raw))

	// Verify the provider is accessible and has the parts
	provider := transport.ChannelPromptProvider()
	if provider == nil {
		t.Fatal("expected non-nil ChannelPromptProvider")
	}
	parts := provider.ChannelSystemParts(context.Background(), "chat1", "user1")
	if got := parts["05_test"]; got != "test rules" {
		t.Errorf("expected 'test rules', got %q", got)
	}
}

// TestChannelPluginTransport_ChannelPromptProvider_InitiallyEmpty verifies that
// ChannelPromptProvider returns nil parts before any channel_prompt message.
func TestChannelPluginTransport_ChannelPromptProvider_InitiallyEmpty(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)
	defer transport.Stop()

	provider := transport.ChannelPromptProvider()
	if provider == nil {
		t.Fatal("expected non-nil ChannelPromptProvider")
	}

	// Initially empty -> should return nil
	parts := provider.ChannelSystemParts(context.Background(), "chat1", "user1")
	if parts != nil {
		t.Errorf("expected nil parts initially, got %v", parts)
	}
}

// TestChannelPluginTransport_HandleChannelPrompt_EmptyParts verifies that
// sending an empty system_parts doesn't panic.
func TestChannelPluginTransport_HandleChannelPrompt_EmptyParts(t *testing.T) {
	pio := newMockProcessIO()
	dispatch := func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
		return json.Marshal("ok")
	}
	eventCh := make(chan protocol.WSMessage, 10)

	transport := NewChannelPluginTransportWithIO("test", pio, dispatch, eventCh)
	defer transport.Stop()

	// Empty system_parts
	raw, _ := json.Marshal(map[string]interface{}{
		"type":         "channel_prompt",
		"system_parts": map[string]string{},
	})
	transport.handleChannelPrompt(json.RawMessage(raw))

	provider := transport.ChannelPromptProvider()
	parts := provider.ChannelSystemParts(context.Background(), "chat1", "user1")
	if parts != nil {
		t.Errorf("expected nil parts for empty declaration, got %v", parts)
	}
}
