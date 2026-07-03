// xbot CLI Channel unit tests
// Tests for CLIChannel and cliModel functionality

package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"xbot/protocol"

	"xbot/llm"

	"xbot/channel"

	tea "charm.land/bubbletea/v2"
)

// isTerminal returns true if stdout is connected to a terminal.
// bubbletea requires a real TTY; tests that call ch.Start() must skip in non-TTY.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// ---------------------------------------------------------------------------
// CLIChannel Basic Tests
// ---------------------------------------------------------------------------

func TestCLIChannelName(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	if got := ch.Name(); got != "cli" {
		t.Errorf("CLIChannel.Name() = %q, want %q", got, "cli")
	}
}

func TestCLIChannelStartStop(t *testing.T) {
	// Skip if not a real terminal — bubbletea.Start() blocks without TTY
	if !isTerminal() {
		t.Skip("Skipping - requires TTY")
	}

	ch := NewCLIChannel(&CLIChannelConfig{})

	// Start in goroutine since it blocks
	startErr := make(chan error, 1)
	go func() {
		startErr <- ch.Start()
	}()

	// Give it time to initialize
	time.Sleep(100 * time.Millisecond)

	// Stop should terminate the program
	ch.Stop()

	select {
	case err := <-startErr:
		// Start may return error in headless env, that's OK
		_ = err
	case <-time.After(2 * time.Second):
		t.Error("Start() did not return after Stop() within timeout")
	}
}

// ---------------------------------------------------------------------------
// CLIChannel Send Tests
// ---------------------------------------------------------------------------

func TestCLIChannelSend(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// Send without starting should still work (messages buffered)
	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Hello, CLI!",
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() returned empty message ID")
	}
}

func TestCLIChannelSendPartial(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// Send partial (streaming) message
	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Thinking...",
		IsPartial: true,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() partial returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() partial returned empty message ID")
	}
}

func TestCLIChannelSendComplete(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// Send complete message
	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Final response",
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() complete returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() complete returned empty message ID")
	}
}

func TestCLIChannelSendBufferOverflow(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// Send more messages than buffer size to test non-blocking behavior
	for i := 0; i < cliMsgBufSize+10; i++ {
		msg := channel.OutboundMsg{
			Content: "message",
		}
		_, err := ch.Send(msg)
		if err != nil {
			t.Errorf("Send() at iteration %d returned error: %v", i, err)
		}
	}
	// Should not block or panic
}

func TestCLIChannelSendProgress(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// SendProgress with nil payload should not panic
	ch.SendProgress("test_chat", nil)

	// SendProgress without program should not panic
	payload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}
	ch.SendProgress("test_chat", payload)
	// Should not panic
}

// ---------------------------------------------------------------------------
// CLIChannel Edge Cases
// ---------------------------------------------------------------------------

func TestCLIChannelSendEmptyMessage(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "", // empty content
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() empty message returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() empty message returned empty ID")
	}
}

func TestCLIChannelSendLongMessage(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// Create a very long message
	longContent := strings.Repeat("This is a long message. ", 1000)

	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   longContent,
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() long message returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() long message returned empty ID")
	}
}

func TestCLIChannelSendWithMetadata(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Message with metadata",
		Metadata:  map[string]string{"key": "value"},
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() with metadata returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() with metadata returned empty ID")
	}
}

func TestCLIChannelSendWithMedia(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	msg := channel.OutboundMsg{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Message with media",
		Media:     []string{"/path/to/file1.txt", "/path/to/file2.png"},
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() with media returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() with media returned empty ID")
	}
}

// ---------------------------------------------------------------------------
// cliModel Tests
// ---------------------------------------------------------------------------

func TestCLIModelInit(t *testing.T) {
	model := newCLIModel()

	cmd := model.Init()
	if cmd == nil {
		t.Error("Init() returned nil command")
	}
}

func TestCLIModelSendInboundFn(t *testing.T) {
	model := newCLIModel()
	called := false
	model.sendInboundFn = func(msg channel.InboundMsg) bool {
		called = true
		return true
	}

	if !model.sendInbound(channel.InboundMsg{Content: "test"}) {
		t.Error("sendInbound() returned false")
	}
	if !called {
		t.Error("sendInboundFn was not called")
	}
}

func TestCLIModelHandleResize(t *testing.T) {
	model := newCLIModel()

	// Test resize
	model.handleResize(120, 40)

	if model.width != 120 {
		t.Errorf("handleResize() width = %d, want 120", model.width)
	}
	if model.height != 40 {
		t.Errorf("handleResize() height = %d, want 40", model.height)
	}
	if !model.ready {
		t.Error("handleResize() should set ready to true")
	}
}

func TestCLIModelHandleResizeMinimum(t *testing.T) {
	model := newCLIModel()

	// Test very small resize
	model.handleResize(10, 10)

	if model.width != 10 {
		t.Errorf("handleResize() width = %d, want 10", model.width)
	}
	// Should not panic
}

func TestCLIModelHandleResizeWithProgress(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{
		Phase: "tool_exec",
		ActiveTools: []protocol.ToolProgress{
			{Name: "test", Label: "Testing"},
		},
	}

	model.handleResize(80, 30)

	if model.viewport.Height() <= 0 {
		t.Error("viewport height should be positive")
	}
}

func TestCLIModelViewNotReady(t *testing.T) {
	model := newCLIModel()
	model.ready = false

	view := model.View()
	viewStr := view.Content
	// §14 splash 画面优先于 "初始化中..." 展示
	if !strings.Contains(viewStr, "xbot") && !strings.Contains(viewStr, "初始化") {
		t.Errorf("View() when not ready should show splash or initializing message, got: %q", viewStr)
	}
}

func TestCLIModelViewReady(t *testing.T) {
	model := newCLIModel()
	model.splashState.done = true
	model.handleResize(80, 24)

	view := model.View()
	if view.Content == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithTyping(t *testing.T) {
	model := newCLIModel()
	model.splashState.done = true
	model.handleResize(80, 24)
	model.typing = true

	view := model.View()
	if view.Content == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithProgress(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}

	view := model.View()
	if view.Content == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithMessages(t *testing.T) {
	model := newCLIModel()
	model.splashState.done = true
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "Hello", timestamp: time.Now()},
		{role: "assistant", content: "Hi there!", timestamp: time.Now()},
	}

	view := model.View()
	if view.Content == "" {
		t.Error("View() returned empty string")
	}
}

// ---------------------------------------------------------------------------
// cliModel Handle Agent Message Tests
// ---------------------------------------------------------------------------

func TestCLIModelHandleAgentMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Test complete message
	msg := channel.OutboundMsg{
		Content:   "Hello from agent",
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
	if model.messages[0].role != "assistant" {
		t.Errorf("Message role = %q, want 'assistant'", model.messages[0].role)
	}
	if model.messages[0].content != "Hello from agent" {
		t.Errorf("Message content = %q, want 'Hello from agent'", model.messages[0].content)
	}
	if model.typing {
		t.Error("typing should be false after complete message")
	}
	if !model.inputReady {
		t.Error("inputReady should be true after complete message")
	}
}

func TestCLIModelHandleAgentMessagePartial(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// First partial message
	msg1 := channel.OutboundMsg{
		Content:   "Thinking...",
		IsPartial: true,
	}
	model.handleAgentMessage(msg1)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message after first partial, got %d", len(model.messages))
	}
	if !model.messages[0].isPartial {
		t.Error("Message should be partial")
	}

	// Second partial (update)
	msg2 := channel.OutboundMsg{
		Content:   "Still thinking...",
		IsPartial: true,
	}
	model.handleAgentMessage(msg2)

	// Should update same message
	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message after second partial, got %d", len(model.messages))
	}

	// Complete message
	msg3 := channel.OutboundMsg{
		Content:   "Final answer",
		IsPartial: false,
	}
	model.handleAgentMessage(msg3)

	if model.messages[0].isPartial {
		t.Error("Message should not be partial after complete")
	}
	if model.typing {
		t.Error("typing should be false after complete")
	}
	if model.streamingMsgIdx != -1 {
		t.Error("streamingMsgIdx should be -1 after complete")
	}
}

func TestCLIModelHandleAgentMessageMultiplePartials(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Multiple partial updates
	for i := 0; i < 5; i++ {
		msg := channel.OutboundMsg{
			Content:   "Partial content " + string(rune('A'+i)),
			IsPartial: true,
		}
		model.handleAgentMessage(msg)
	}

	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message after multiple partials, got %d", len(model.messages))
	}

	// Complete
	model.handleAgentMessage(channel.OutboundMsg{
		Content:   "Final",
		IsPartial: false,
	})

	if model.streamingMsgIdx != -1 {
		t.Error("streamingMsgIdx should be -1 after complete")
	}
}

func TestCLIModelHandleAgentMessageWithFeishuCard(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Test Feishu card conversion
	cardContent := `__FEISHU_CARD__:id:{"header":{"title":{"content":"Card Title"}},"elements":[]}`
	msg := channel.OutboundMsg{
		Content:   cardContent,
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
	// Content should be converted (contain "Card Title")
	if !strings.Contains(model.messages[0].content, "Card Title") {
		t.Errorf("Feishu card not converted, content: %q", model.messages[0].content)
	}
}

func TestCLIModelHandleAgentMessageFeishuCardWithElements(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	cardContent := `__FEISHU_CARD__:id:{"header":{"title":{"content":"Test"}},"elements":[{"tag":"markdown","content":"**bold** text"},{"tag":"div","text":"plain"}]}`
	msg := channel.OutboundMsg{
		Content:   cardContent,
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
}

// TestSessionResetClearsMessages verifies that when the agent responds with
// session_reset=true (after /new), the CLI clears all messages and resets state.
func TestSessionResetClearsMessages(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Simulate existing conversation
	model.messages = []cliMessage{
		{role: "user", content: "Hello", timestamp: time.Now(), dirty: true},
		{role: "assistant", content: "Hi there!", timestamp: time.Now(), dirty: true},
		{role: "user", content: "/new", timestamp: time.Now(), dirty: true},
	}

	// Agent responds with session_reset metadata
	msg := channel.OutboundMsg{
		Content:   "New session started",
		IsPartial: false,
		Metadata:  map[string]string{"session_reset": "true"},
	}
	model.handleAgentMessage(msg)

	// Verify ALL messages were cleared (including the session_reset response itself)
	if len(model.messages) != 0 {
		t.Fatalf("Expected 0 messages after session_reset, got %d", len(model.messages))
	}
	if model.streamingMsgIdx != -1 {
		t.Errorf("Expected streamingMsgIdx -1, got %d", model.streamingMsgIdx)
	}
	if model.lastTokenUsage != nil {
		t.Error("Expected lastTokenUsage to be nil after session_reset")
	}
}

func TestCLIModelHandleAgentMessageEmptyContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Simulate active progress state
	model.progressState.current = &protocol.ProgressEvent{Phase: "thinking"}
	model.typing = true

	msg := channel.OutboundMsg{
		Content:   "",
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	// Empty content with no tools/waiting: should clear progress, not add message
	if len(model.messages) != 0 {
		t.Fatalf("Expected 0 messages, got %d", len(model.messages))
	}
	if model.progressState.current != nil {
		t.Error("Expected progress to be cleared")
	}
	if model.typing {
		t.Error("Expected typing to be cleared")
	}
}

func TestCLIModelHandleAgentMessageMarkdownContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	markdownContent := "# Header\n\n**Bold** and *italic* text\n\n- List item 1\n- List item 2"
	msg := channel.OutboundMsg{
		Content:   markdownContent,
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
}

// ---------------------------------------------------------------------------
// cliModel Update Tests
// ---------------------------------------------------------------------------

func TestCLIModelUpdateCtrlCClearsInput(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.textarea.SetValue("some text")

	keyMsg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	_, cmd := model.Update(keyMsg)

	// When not typing, Ctrl+C clears input (no quit)
	if cmd != nil {
		t.Error("Update(CtrlC) when not typing should return nil cmd")
	}
	if model.textarea.Value() != "" {
		t.Errorf("textarea should be empty after CtrlC, got %q", model.textarea.Value())
	}
}

func TestCLIModelUpdateEscClearsInput(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.textarea.SetValue("some text")

	keyMsg := tea.KeyPressMsg{Code: tea.KeyEsc}
	_, cmd := model.Update(keyMsg)

	// When not typing, Esc clears input (no quit)
	if cmd != nil {
		t.Error("Update(Esc) when not typing should return nil cmd")
	}
	if model.textarea.Value() != "" {
		t.Errorf("textarea should be empty after Esc, got %q", model.textarea.Value())
	}
}

func TestCLIModelUpdateCtrlCWhileTyping(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.sendInboundFn = func(msg channel.InboundMsg) bool {
		return true
	}

	keyMsg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	_, _ = model.Update(keyMsg)

	// Should add cancel system message
	hasCancel := false
	for _, msg := range model.messages {
		if msg.role == "system" && (strings.Contains(msg.content, "取消") || strings.Contains(msg.content, "Cancel")) {
			hasCancel = true
		}
	}
	if !hasCancel {
		t.Error("CtrlC while typing should add cancel message")
	}
}

func TestCLIModelUpdateProgressMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"
	model.typing = true // simulate active agent turn

	// Send progress message
	progMsg := cliProgressMsg{
		payload: &protocol.ProgressEvent{
			Phase:     "thinking",
			Iteration: 1,
			ChatID:    "cli:/test",
		},
	}

	_, _ = model.Update(progMsg)

	if model.progressState.current == nil {
		t.Error("Progress should be set after cliProgressMsg")
	}
	if model.progressState.current.Phase != "thinking" {
		t.Errorf("Progress phase = %q, want 'thinking'", model.progressState.current.Phase)
	}
}

func TestCLIModelUpdateProgressDone(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// Set initial progress
	model.progressState.current = &protocol.ProgressEvent{Phase: "thinking", ChatID: "cli:/test"}

	// Send done progress
	progMsg := cliProgressMsg{
		payload: &protocol.ProgressEvent{
			Phase:  "done",
			ChatID: "cli:/test",
		},
	}

	_, _ = model.Update(progMsg)

	// After "done" phase: endAgentTurn is called but no longer clears
	// progressState.current (kept for flicker-free rendering between
	// PhaseDone and handleAgentMessage). typing should be false.
	if model.typing {
		t.Error("typing should be false after done phase")
	}
}

func TestCLIModelUpdateProgressNilPayload(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	progMsg := cliProgressMsg{payload: nil}
	_, _ = model.Update(progMsg)

	// Should not panic
}

func TestCLIModelStaleProgressIgnored(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// Scenario 1: After Ctrl+C (typing=false, turnCancelled=true), progress is ignored
	model.typing = false
	model.progressState.current = nil
	model.turnCancelled = true

	progMsg := cliProgressMsg{
		payload: &protocol.ProgressEvent{
			Phase:     "thinking",
			Iteration: 1,
			ChatID:    "cli:/test",
		},
	}
	model.chatID = "/test"
	model.channelName = "cli"
	model.handleProgressMsg(progMsg)

	if model.progressState.current != nil {
		t.Error("Progress after Ctrl+C should be ignored when turnCancelled=true")
	}

	// Scenario 2: Progress for a different session is ignored
	model2 := newCLIModel()
	model2.handleResize(80, 24)
	model2.typing = true
	model2.chatID = "/other"
	model2.channelName = "cli"

	model2.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			Phase:     "thinking",
			Iteration: 1,
			ChatID:    "cli:/different",
		},
	})

	if model2.progressState.current != nil {
		t.Error("Progress for a different session should be ignored")
	}

	// Scenario 3: First switch to running SubAgent (typing=false, turnCancelled=false)
	// → auto-start should fire
	model3 := newCLIModel()
	model3.handleResize(80, 24)
	model3.chatID = "/test"
	model3.channelName = "cli"
	// No saved state → restoreSession sets typing=false, turnCancelled=false
	model3.restoreSession()

	if model3.typing {
		t.Error("restoreSession with no saved state should set typing=false")
	}
	if model3.turnCancelled {
		t.Error("restoreSession with no saved state should set turnCancelled=false")
	}

	model3.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			Phase:     "tool_exec",
			Iteration: 1,
			ChatID:    "cli:/test",
		},
	})

	if !model3.typing {
		t.Error("Auto-start should fire when turnCancelled=false and typing=false")
	}
	if model3.progressState.current == nil {
		t.Error("Progress should be set after auto-start")
	}
}

func TestCLIModelUpdateWindowSizeMsg(t *testing.T) {
	model := newCLIModel()

	// Simulate window resize
	sizeMsg := tea.WindowSizeMsg{Width: 100, Height: 30}
	_, _ = model.Update(sizeMsg)

	if model.width != 100 || model.height != 30 {
		t.Errorf("Window size not updated: width=%d, height=%d", model.width, model.height)
	}
}

func TestCLIModelUpdateTickMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Global tick: just verify no panic in idle state
	tickMsg := cliTickMsg{}
	model.Update(tickMsg)

	// Global tick with typing active: should also not panic
	model.typing = true
	model.Update(tickMsg)
}

func TestGlobalTickUpdatesSpinnerAndProgress(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Simulate an active agent turn.
	model.typing = true
	model.progressState.current = &protocol.ProgressEvent{Phase: "thinking"}

	// cliTickMsg from the global goroutine should advance spinner
	// and NOT panic or return errors.
	model.Update(cliTickMsg{})
	if !model.typing {
		t.Fatal("tick should not change typing state")
	}
}

func TestGlobalTickAdvancesSplashAnimation(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Splash not done — tick should advance splashFrame.
	model.splashState.done = false
	model.Update(cliTickMsg{})
	if model.splashState.frame != 1 {
		t.Fatalf("expected splashFrame=1, got %d", model.splashState.frame)
	}
}

func TestStartAgentTurnAndTypingTransition(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// cliProcessingMsg sets typing=true. In the new global-ticker architecture,
	// no tickCmd is needed — the global goroutine handles ticks.
	model.typing = false
	model.Update(cliProcessingMsg{processing: true})
	if !model.typing {
		t.Fatal("cliProcessingMsg should set typing=true")
	}
}

func TestCLIModelUpdateOutboundMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	outMsg := cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:   "Test message",
			IsPartial: false,
		},
	}

	_, _ = model.Update(outMsg)

	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(model.messages))
	}
}

func TestCLIModelUpdateEnterKeyWithContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = true
	model.sendInboundFn = func(msg channel.InboundMsg) bool { return true }

	// Set textarea content
	model.textarea.SetValue("Hello world")

	// Simulate Enter key
	keyMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
	_, _ = model.Update(keyMsg)

	// Message should be added (user message + streaming assistant from startAgentTurn)
	if len(model.messages) != 2 {
		t.Errorf("Expected 2 messages after Enter, got %d", len(model.messages))
	}
}

func TestCLIModelUpdateEnterKeyEmptyContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = true

	// Empty textarea
	model.textarea.SetValue("   ")

	// Simulate Enter key
	keyMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
	_, _ = model.Update(keyMsg)

	// No message should be added
	if len(model.messages) != 0 {
		t.Errorf("Expected 0 messages for empty input, got %d", len(model.messages))
	}
}

func TestCLIModelUpdateEnterKeyInputNotReady(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = false

	// Set textarea content
	model.textarea.SetValue("Hello world")

	// Simulate Enter key
	keyMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
	_, _ = model.Update(keyMsg)

	// No message should be added (input not ready)
	if len(model.messages) != 0 {
		t.Errorf("Expected 0 messages when input not ready, got %d", len(model.messages))
	}
}

// ---------------------------------------------------------------------------
// Progress Rendering Tests
// ---------------------------------------------------------------------------

func TestCLIModelRenderProgressStatus(t *testing.T) {
	model := newCLIModel()
	model.locale = channel.GetLocale("en")

	tests := []struct {
		phase    string
		expected string
	}{
		{"thinking", "Thinking"},
		{"tool_exec", "#0"},
		{"compressing", "compressing"},
		{"retrying", "retrying"},
		{"done", "#0"},
		{"unknown", "#0"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			model.progressState.current = &protocol.ProgressEvent{Phase: tt.phase}
			result := model.renderProgressStatus()
			if !strings.Contains(result, tt.expected) {
				t.Errorf("renderProgressStatus(%s) should contain %q, got %q",
					tt.phase, tt.expected, result)
			}
		})
	}
}

func TestCLIModelRenderProgressStatusNil(t *testing.T) {
	model := newCLIModel()
	model.locale = channel.GetLocale("en")
	model.progressState.current = nil

	result := model.renderProgressStatus()
	if !strings.Contains(result, "Thinking") {
		t.Errorf("renderProgressStatus with nil progress should show a thinking verb, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithIteration(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 5,
	}

	result := model.renderProgressStatus()

	if !strings.Contains(result, "#5") {
		t.Errorf("renderProgressStatus should show iteration, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithActiveTools(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:       "tool_exec",
		Iteration:   1,
		ActiveTools: []protocol.ToolProgress{{Name: "read", Label: "Reading file", Elapsed: 100}},
	}

	result := model.renderProgressStatus()

	// Active tool name is NOT shown in status bar (rendered in progress block instead)
	// Verify it shows iteration and doesn't crash
	if !strings.Contains(result, "#1") {
		t.Errorf("renderProgressStatus should show iteration with active tools, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusToolWithoutLabel(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:       "tool_exec",
		Iteration:   1,
		ActiveTools: []protocol.ToolProgress{{Name: "read", Label: "", Elapsed: 0}},
	}

	result := model.renderProgressStatus()

	// Active tool name is NOT shown in status bar (rendered in progress block instead)
	if !strings.Contains(result, "#1") {
		t.Errorf("renderProgressStatus should show iteration without crash, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithElapsed(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{Phase: "thinking"}
	model.typingStartTime = time.Now().Add(-5 * time.Second)

	result := model.renderProgressStatus()
	if !strings.Contains(result, "s") {
		t.Errorf("renderProgressStatus should show elapsed time, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Progress Block (viewport) Rendering Tests
// ---------------------------------------------------------------------------

func TestCLIModelRenderProgressBlockEmpty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = false
	model.progressState.current = nil

	result := model.renderProgressBlock()
	if result != "" {
		t.Errorf("renderProgressBlock should be empty when not typing, got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockThinking(t *testing.T) {
	model := newCLIModel()
	model.locale = channel.GetLocale("en")
	model.handleResize(80, 24)
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Verify streaming assistant message exists from startAgentTurn
	if model.streamingMsgIdx < 0 {
		t.Error("streamingMsgIdx should be set after startAgentTurn")
	}
	if !model.typing {
		t.Error("typing should be true after startAgentTurn")
	}
	// Progress block always returns empty now (inline rendering)
	result := model.renderProgressBlock()
	if result != "" {
		t.Errorf("renderProgressBlock should be empty (inline rendering), got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockWithTools(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.startAgentTurn()
	model.typingStartTime = time.Now()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "read_file", Label: "Reading config.go", Status: "running", Elapsed: 1200},
		},
		CompletedTools: []protocol.ToolProgress{
			{Name: "grep", Label: "Searching imports", Status: "done", Elapsed: 300, Iteration: 1},
		},
	}

	// Progress block always empty now — tools rendered inline in streaming message
	result := model.renderProgressBlock()
	if result != "" {
		t.Errorf("renderProgressBlock should be empty (inline), got: %q", result)
	}
	// Verify streaming message exists and progress is set
	if model.streamingMsgIdx < 0 {
		t.Error("streamingMsgIdx should be set")
	}
	if model.progressState.current == nil {
		t.Error("progress should be set")
	}
}

func TestCLIModelRenderProgressBlockWithIterationHistory(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.startAgentTurn()
	model.typingStartTime = time.Now()
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 0,
			Content:   "Analyzing requirements",
			Tools: []protocol.ToolProgress{
				{Name: "read", Label: "Reading file", Status: "done", Elapsed: 500},
			},
		},
	}
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}

	// Progress block always empty now — iterations rendered inline in streaming message
	result := model.renderProgressBlock()
	if result != "" {
		t.Errorf("renderProgressBlock should be empty (inline), got: %q", result)
	}
	// Verify streaming message exists and iteration history is preserved
	if model.streamingMsgIdx < 0 {
		t.Error("streamingMsgIdx should be set")
	}
	if len(model.progressState.iterations) == 0 {
		t.Error("iterationHistory should have entries")
	}
}

func TestCLIModelRenderProgressBlockSubAgents(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.startAgentTurn()
	model.typingStartTime = time.Now()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 0,
		SubAgents: []protocol.SubAgentInfo{
			{Role: "code-reviewer", Status: "running", Desc: "Reviewing code"},
			{Role: "test-runner", Status: "done", Desc: "Tests passed"},
			{Role: "explore", Status: "error", Desc: "429 rate limited"},
		},
	}

	// Progress block always empty now — subagent tree rendered inline
	result := model.renderProgressBlock()
	if result != "" {
		t.Errorf("renderProgressBlock should be empty (inline), got: %q", result)
	}
	// Verify streaming message and subagent data exists
	if model.streamingMsgIdx < 0 {
		t.Error("streamingMsgIdx should be set")
	}
	if model.progressState.current == nil || len(model.progressState.current.SubAgents) != 3 {
		t.Error("progress should have subagent data")
	}
}

func TestCLIModelRenderProgressBlockSubAgentChildren(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 0,
		SubAgents: []protocol.SubAgentInfo{
			{
				Role:   "reviewer",
				Status: "running",
				Children: []protocol.SubAgentInfo{
					{Role: "child", Status: "done"},
				},
			},
		},
	}

	result := model.renderProgressBlock()
	// Done child sub-agents should be hidden from progress panel
	if strings.Contains(result, "child") {
		t.Errorf("renderProgressBlock should not show completed child subagent, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// cliModel UpdateViewportContent Tests
// ---------------------------------------------------------------------------

func TestCLIModelUpdateViewportContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "Hello", timestamp: time.Now()},
		{role: "assistant", content: "Hi there!", timestamp: time.Now(), isPartial: false},
	}

	model.updateViewportContent()

	// Viewport should have content
	if model.viewport.View() == "" {
		t.Error("updateViewportContent should set viewport content")
	}
}

func TestCLIModelUpdateViewportContentPartialMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "assistant", content: "Streaming...", timestamp: time.Now(), isPartial: true},
	}

	model.updateViewportContent()

	// Should contain streaming indicator
	content := model.viewport.View()
	if content == "" {
		t.Error("updateViewportContent should set viewport content")
	}
}

func TestCLIModelUpdateViewportContentWithMarkdown(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "assistant", content: "# Header\n\n**bold**", timestamp: time.Now()},
	}

	model.updateViewportContent()

	// Should render markdown without error
	if model.viewport.View() == "" {
		t.Error("updateViewportContent should set viewport content")
	}
}

func TestCLIModelUpdateViewportContentUserMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "User message", timestamp: time.Now()},
	}

	model.updateViewportContent()

	content := model.viewport.View()
	if !strings.Contains(content, "You") {
		t.Error("User message should contain 'You' label")
	}
}

func TestCLIModelUpdateViewportContentAssistantMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "assistant", content: "Assistant message", timestamp: time.Now()},
	}

	model.updateViewportContent()

	content := model.viewport.View()
	if !strings.Contains(content, "Assistant") {
		t.Error("Assistant message should contain 'Assistant' label")
	}
}

// ---------------------------------------------------------------------------
// cliModel SendMessage Tests
// ---------------------------------------------------------------------------

func TestCLIModelSendMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Capture inbound message via sendInboundFn
	received := make(chan channel.InboundMsg, 1)
	model.sendInboundFn = func(msg channel.InboundMsg) bool {
		received <- msg
		return true
	}

	model.sendMessage("Hello agent")

	select {
	case msg := <-received:
		if msg.Content != "Hello agent" {
			t.Errorf("Received content = %q, want 'Hello agent'", msg.Content)
		}
		if msg.Channel != "cli" {
			t.Errorf("Received channel = %q, want 'cli'", msg.Channel)
		}
		if !model.typing {
			t.Error("typing should be true after sending message")
		}
		if model.inputReady {
			t.Error("inputReady should be false while waiting for response")
		}
	case <-time.After(time.Second):
		t.Error("Message not received within timeout")
	}
}

func TestCLIModelSendMessageNoSendInboundFn(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	// sendInboundFn is nil

	model.sendMessage("Hello agent")

	// Should not panic, message added to history
	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message in history, got %d", len(model.messages))
	}
}

func TestCLIModelSendMessageEmpty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.sendInboundFn = func(msg channel.InboundMsg) bool { return true }

	model.sendMessage("")

	// Message should still be added (empty is valid, + streaming from startAgentTurn)
	if len(model.messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(model.messages))
	}
}

// ---------------------------------------------------------------------------
// Helper Function Tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// cliMessage Tests
// ---------------------------------------------------------------------------

func TestCLIMessageFields(t *testing.T) {
	now := time.Now()
	msg := cliMessage{
		role:      "user",
		content:   "Test content",
		timestamp: now,
		isPartial: false,
	}

	if msg.role != "user" {
		t.Errorf("role = %q, want 'user'", msg.role)
	}
	if msg.content != "Test content" {
		t.Errorf("content = %q, want 'Test content'", msg.content)
	}
	if !msg.timestamp.Equal(now) {
		t.Error("timestamp not set correctly")
	}
	if msg.isPartial {
		t.Error("isPartial should be false")
	}
}

// ---------------------------------------------------------------------------
// protocol.ProgressEvent Tests
// ---------------------------------------------------------------------------

func TestProgressEventFields(t *testing.T) {
	payload := protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 3,
		ActiveTools: []protocol.ToolProgress{
			{Name: "read", Label: "Reading", Status: "running", Elapsed: 100},
		},
		CompletedTools: []protocol.ToolProgress{
			{Name: "glob", Label: "Globbing", Status: "done", Elapsed: 50},
		},
		Content: "Analyzing...",
		SubAgents: []protocol.SubAgentInfo{
			{Role: "reviewer", Status: "running", Desc: "Code review"},
		},
	}

	if payload.Phase != "thinking" {
		t.Errorf("Phase = %q, want 'thinking'", payload.Phase)
	}
	if len(payload.ActiveTools) != 1 {
		t.Errorf("ActiveTools count = %d, want 1", len(payload.ActiveTools))
	}
	if len(payload.CompletedTools) != 1 {
		t.Errorf("CompletedTools count = %d, want 1", len(payload.CompletedTools))
	}
}

func TestToolProgressFields(t *testing.T) {
	tool := protocol.ToolProgress{
		Name:    "read",
		Label:   "Reading file",
		Status:  "running",
		Elapsed: 150,
	}

	if tool.Name != "read" {
		t.Errorf("Name = %q, want 'read'", tool.Name)
	}
	if tool.Elapsed != 150 {
		t.Errorf("Elapsed = %d, want 150", tool.Elapsed)
	}
}

func TestSubAgentInfoFields(t *testing.T) {
	subAgent := protocol.SubAgentInfo{
		Role:     "code-reviewer",
		Status:   "done",
		Desc:     "Completed review",
		Children: []protocol.SubAgentInfo{},
	}

	if subAgent.Role != "code-reviewer" {
		t.Errorf("Role = %q, want 'code-reviewer'", subAgent.Role)
	}
	if subAgent.Status != "done" {
		t.Errorf("Status = %q, want 'done'", subAgent.Status)
	}
}

// ---------------------------------------------------------------------------
// mergeSubAgentTrees Tests
// ---------------------------------------------------------------------------

func TestMergeSubAgentTrees_EmptyPrev(t *testing.T) {
	t.Parallel()
	new := []protocol.SubAgentInfo{{Role: "explore", Status: "running"}}
	result := mergeSubAgentTrees(nil, new)
	if len(result) != 1 || result[0].Role != "explore" {
		t.Fatalf("expected 1 agent, got %v", result)
	}
}

func TestMergeSubAgentTrees_EmptyNew(t *testing.T) {
	t.Parallel()
	// When new is empty, server stopped reporting → completed agents are pruned.
	prev := []protocol.SubAgentInfo{{Role: "explore", Status: "done"}}
	result := mergeSubAgentTrees(prev, nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 agents (done pruned), got %v", result)
	}
}

func TestMergeSubAgentTrees_BothEmpty(t *testing.T) {
	t.Parallel()
	result := mergeSubAgentTrees(nil, nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 agents, got %d", len(result))
	}
}

func TestMergeSubAgentTrees_MergeUpdates(t *testing.T) {
	t.Parallel()
	prev := []protocol.SubAgentInfo{
		{Role: "explore", Status: "running", Desc: "scanning code"},
		{Role: "reviewer", Status: "done", Desc: "completed"},
	}
	new := []protocol.SubAgentInfo{
		{Role: "explore", Status: "done", Desc: "finished scan"},
	}
	result := mergeSubAgentTrees(prev, new)

	// Should have 1 agent: explore (updated from new). Reviewer is done
	// and not in new, so it's pruned (no zombies).
	if len(result) != 1 {
		t.Fatalf("expected 1 agent, got %d: %v", len(result), result)
	}

	if result[0].Role != "explore" {
		t.Fatalf("expected explore agent, got %q", result[0].Role)
	}
	if result[0].Status != "done" {
		t.Errorf("explore status = %q, want 'done'", result[0].Status)
	}
	if result[0].Desc != "finished scan" {
		t.Errorf("explore desc = %q, want 'finished scan'", result[0].Desc)
	}
}

func TestMergeSubAgentTrees_NoZombieDuplicates(t *testing.T) {
	t.Parallel()
	// Simulate the exact zombie bug: prev has a completed SubAgent, new is empty.
	// New behavior: done agents are pruned immediately.
	prev := []protocol.SubAgentInfo{
		{Role: "ministry-works", Status: "done", Desc: "completed"},
	}

	// First merge: new is empty → done agents pruned
	result1 := mergeSubAgentTrees(prev, nil)
	if len(result1) != 0 {
		t.Fatalf("first merge: expected 0 (done pruned), got %d", len(result1))
	}

	// Second merge: empty prev, empty new → still 0
	result2 := mergeSubAgentTrees(result1, nil)
	if len(result2) != 0 {
		t.Fatalf("second merge: expected 0, got %d", len(result2))
	}
}

func TestMergeSubAgentTrees_NestedChildren(t *testing.T) {
	t.Parallel()
	prev := []protocol.SubAgentInfo{
		{
			Role:   "crown-prince",
			Status: "running",
			Children: []protocol.SubAgentInfo{
				{Role: "explore", Status: "done"},
				{Role: "secretariat", Status: "running"},
			},
		},
	}
	new := []protocol.SubAgentInfo{
		{
			Role:   "crown-prince",
			Status: "running",
			Children: []protocol.SubAgentInfo{
				{Role: "secretariat", Status: "done"},
			},
		},
	}

	result := mergeSubAgentTrees(prev, new)
	if len(result) != 1 {
		t.Fatalf("expected 1 top-level agent, got %d", len(result))
	}

	children := result[0].Children
	// Should have 1 child: secretariat (updated from new). Explore is done
	// and not in new's children, so it's pruned.
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d: %v", len(children), children)
	}

	// secretariat — should be "done" (updated from new)
	if children[0].Role != "secretariat" {
		t.Fatalf("expected secretariat, got %q", children[0].Role)
	}
	if children[0].Status != "done" {
		t.Errorf("secretariat status = %q, want 'done'", children[0].Status)
	}
}

// ---------------------------------------------------------------------------
// formatElapsed Tests
// ---------------------------------------------------------------------------

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		ms       int64
		expected string
	}{
		{0, "0ms"},
		{50, "50ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1500, "1.5s"},
		{12300, "12.3s"},
		{59999, "60.0s"},
		{60000, "1m0s"},
		{90000, "1m30s"},
		{125000, "2m5s"},
	}
	for _, tt := range tests {
		got := formatElapsed(tt.ms)
		if got != tt.expected {
			t.Errorf("formatElapsed(%d) = %q, want %q", tt.ms, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Iteration History Accumulation Tests
// ---------------------------------------------------------------------------

func TestCLIModelIterationAccumulation(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"
	model.typing = true
	model.typingStartTime = time.Now()

	// Iteration 0: thinking
	prog0 := cliProgressMsg{payload: &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 0,
		ChatID:    "cli:/test",
	}}
	model.Update(prog0)
	if len(model.progressState.iterations) != 0 {
		t.Errorf("Expected 0 history entries, got %d", len(model.progressState.iterations))
	}

	// Iteration 0: tool_exec with completed tools
	prog0b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 0,
		ChatID:    "cli:/test",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Reading", Status: "done", Elapsed: 100},
		},
	}}
	model.Update(prog0b)

	// Iteration 1: thinking — should snapshot iteration 0
	prog1 := cliProgressMsg{payload: &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		ChatID:    "cli:/test",
	}}
	model.Update(prog1)
	if len(model.progressState.iterations) != 1 {
		t.Fatalf("Expected 1 history entry after iteration change, got %d", len(model.progressState.iterations))
	}
	if model.progressState.iterations[0].Iteration != 0 {
		t.Errorf("History[0].Iteration = %d, want 0", model.progressState.iterations[0].Iteration)
	}
	if len(model.progressState.iterations[0].Tools) != 1 {
		t.Errorf("History[0].Tools count = %d, want 1", len(model.progressState.iterations[0].Tools))
	}
}

func TestCLIModelCollectAllTools(t *testing.T) {
	model := newCLIModel()
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 0, Tools: []protocol.ToolProgress{{Name: "a"}, {Name: "b"}}},
		{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "c"}}},
	}
	all := model.collectAllTools()
	if len(all) != 3 {
		t.Errorf("collectAllTools() = %d tools, want 3", len(all))
	}
}

func TestCLIModelResetProgressState(t *testing.T) {
	model := newCLIModel()
	model.progressState.iterations = []cliIterationSnapshot{{Iteration: 0}}
	model.progressState.lastIter = 5
	model.typingStartTime = time.Now().Add(-10 * time.Second)

	model.resetProgressState()

	if model.progressState.iterations != nil {
		t.Error("iterationHistory should be nil after reset")
	}
	if model.progressState.lastIter != 0 {
		t.Errorf("lastSeenIteration = %d, want 0", model.progressState.lastIter)
	}
	if model.typingStartTime.IsZero() {
		t.Error("typingStartTime should be set after reset")
	}
}

// ---------------------------------------------------------------------------
// Interface Compliance Test
// ---------------------------------------------------------------------------

func TestCLIChannelImplementsChannelInterface(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})

	// This will fail to compile if CLIChannel doesn't implement Channel
	var _ channel.Channel = ch
}

// ---------------------------------------------------------------------------
// CLIChannelConfig Tests
// ---------------------------------------------------------------------------

func TestCLIChannelConfigEmpty(t *testing.T) {
	cfg := CLIChannelConfig{}
	ch := NewCLIChannel(&cfg)

	if ch == nil {
		t.Error("NewCLIChannel with empty config should not return nil")
	}
}

// ---------------------------------------------------------------------------
// channel.ConvertMessagesToHistory tests
// ---------------------------------------------------------------------------

// iterSnapshot mirrors channel.iterSnapshot for test JSON construction.
type iterSnapshot struct {
	Iteration int            `json:"iteration"`
	Content   string         `json:"thinking,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
	Tools     []iterToolSnap `json:"tools"`
}

type iterToolSnap struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Summary   string `json:"summary,omitempty"`
}

func makeDetail(iterations []iterSnapshot) string {
	b, _ := json.Marshal(iterations)
	return string(b)
}

func TestConvert_NormalCompletedTurn(t *testing.T) {
	// A normal completed turn: user → assistant(tool_calls) → tool → assistant(Detail + content)
	detail := makeDetail([]iterSnapshot{
		{Iteration: 1, Content: "think1", Tools: []iterToolSnap{{Name: "Shell", Label: "Shell", Status: "done", ElapsedMS: 500}}},
		{Iteration: 2, Content: "think2", Tools: []iterToolSnap{{Name: "Read", Label: "Read file", Status: "done", ElapsedMS: 200}}},
	})
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", ToolArguments: "{}"},
		{Role: "assistant", Content: "done!", Detail: detail},
	}
	history := channel.ConvertMessagesToHistory(msgs)

	// Should be: user, assistant(content + iterations merged)
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(history), history)
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "assistant")

	if history[1].Iterations == nil || len(history[1].Iterations) != 2 {
		t.Fatalf("expected 2 iterations in assistant message, got %d", len(history[1].Iterations))
	}
	// Should come from Detail (has elapsed data), not from pending (elapsed=0)
	if history[1].Iterations[0].Tools[0].Elapsed != 500 {
		t.Errorf("expected elapsed=500 from Detail, got %d", history[1].Iterations[0].Tools[0].Elapsed)
	}
	// Content should also be on the same assistant message
	if history[1].Content != "done!" {
		t.Errorf("expected content='done!' on assistant, got %q", history[1].Content)
	}
}

func TestConvert_CancelledTurn(t *testing.T) {
	// Cancelled turn: user → assistant(tool_calls) → tool → assistant(tool_calls) → tool (no final assistant)
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", ToolArguments: "{}"},
	}
	history := channel.ConvertMessagesToHistory(msgs)

	// Should be: user, assistant (accumulated from both iterations)
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(history), history)
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "assistant")

	if len(history[1].Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(history[1].Iterations))
	}
	if history[1].Iterations[0].Tools[0].Name != "Shell" {
		t.Errorf("expected Shell, got %s", history[1].Iterations[0].Tools[0].Name)
	}
	if history[1].Iterations[1].Tools[0].Name != "Read" {
		t.Errorf("expected Read, got %s", history[1].Iterations[1].Tools[0].Name)
	}
}

func TestConvert_MultipleTurns(t *testing.T) {
	// Turn 1: completed normally. Turn 2: cancelled.
	detail := makeDetail([]iterSnapshot{
		{Iteration: 1, Tools: []iterToolSnap{{Name: "Shell", Status: "done", ElapsedMS: 100}}},
	})
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn1"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}"},
		{Role: "assistant", Content: "done1", Detail: detail},
		{Role: "user", Content: "turn2"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Grep", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Grep", ToolArguments: "{}"},
	}
	history := channel.ConvertMessagesToHistory(msgs)

	// Expected: user, assistant(Detail, 1 iter, content), user, assistant(pending, 1 iter)
	if len(history) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(history), history)
	}
	assertRole(t, history[0], "user")      // turn1 user
	assertRole(t, history[1], "assistant") // turn1 completed (has iterations + content)
	assertRole(t, history[2], "user")      // turn2 user
	assertRole(t, history[3], "assistant") // turn2 cancelled (has iterations, no content)

	// Turn 1 assistant should have elapsed=100 from Detail
	if history[1].Iterations[0].Tools[0].Elapsed != 100 {
		t.Errorf("turn1 expected elapsed=100, got %d", history[1].Iterations[0].Tools[0].Elapsed)
	}
	// Turn 1 assistant should also have content
	if history[1].Content != "done1" {
		t.Errorf("turn1 expected content='done1', got %q", history[1].Content)
	}
	// Turn 2 assistant should have elapsed=0 from pending
	if history[3].Iterations[0].Tools[0].Elapsed != 0 {
		t.Errorf("turn2 expected elapsed=0, got %d", history[3].Iterations[0].Tools[0].Elapsed)
	}
}

func TestConvert_NoToolCalls(t *testing.T) {
	// Simple conversation without tool calls
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi!"},
	}
	history := channel.ConvertMessagesToHistory(msgs)
	if len(history) != 2 {
		t.Fatalf("expected 2, got %d", len(history))
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "assistant")
}

func assertRole(t *testing.T, msg channel.HistoryMessage, want string) {
	t.Helper()
	if msg.Role != want {
		t.Errorf("expected role=%q, got %q", want, msg.Role)
	}
}

func TestConvert_CrashedTurn_ToolStatusFromToolMessages(t *testing.T) {
	// Server crashed mid-turn: intermediate assistant messages have ToolCalls
	// but NO Detail. Tool status must be inferred from tool result messages.
	// Tool messages with content starting "Error:" → status "error".
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "run tests"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "Shell", Arguments: "{}"},
			{ID: "c2", Name: "Read", Arguments: "{}"},
		}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}", Content: "Error: command failed with exit code 1\n\nDo NOT retry..."},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", ToolArguments: "{}", Content: "file content here"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "c3", Name: "Grep", Arguments: "{}"},
		}},
		{Role: "tool", ToolCallID: "c3", ToolName: "Grep", ToolArguments: "{}", Content: "Error: pattern not found"},
	}
	history := channel.ConvertMessagesToHistory(msgs)

	if len(history) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(history))
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "assistant")

	if len(history[1].Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(history[1].Iterations))
	}

	// Iteration 1: Shell failed (error), Read succeeded (done)
	iter1 := history[1].Iterations[0]
	if len(iter1.Tools) != 2 {
		t.Fatalf("expected 2 tools in iter 1, got %d", len(iter1.Tools))
	}
	if iter1.Tools[0].Name != "Shell" || iter1.Tools[0].Status != "error" {
		t.Errorf("expected Shell status=error, got name=%s status=%s", iter1.Tools[0].Name, iter1.Tools[0].Status)
	}
	if iter1.Tools[1].Name != "Read" || iter1.Tools[1].Status != "done" {
		t.Errorf("expected Read status=done, got name=%s status=%s", iter1.Tools[1].Name, iter1.Tools[1].Status)
	}

	// Iteration 2: Grep failed (error)
	iter2 := history[1].Iterations[1]
	if len(iter2.Tools) != 1 {
		t.Fatalf("expected 1 tool in iter 2, got %d", len(iter2.Tools))
	}
	if iter2.Tools[0].Name != "Grep" || iter2.Tools[0].Status != "error" {
		t.Errorf("expected Grep status=error, got name=%s status=%s", iter2.Tools[0].Name, iter2.Tools[0].Status)
	}
}
