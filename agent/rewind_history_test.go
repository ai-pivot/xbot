package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/channel"
	webchannel "xbot/channel/web"
	"xbot/llm"
	"xbot/protocol"
	"xbot/session"
)

type rewindCaptureTransport struct {
	method  string
	payload json.RawMessage
}

func (t *rewindCaptureTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	t.method = method
	t.payload = append(t.payload[:0], payload...)
	return json.RawMessage(`{"target_history_id":42,"draft":"rewrite me","history_rewound":true,"files_rewound":true}`), nil
}

func (*rewindCaptureTransport) Close() error { return nil }

type rewindEventChannel struct {
	events         []protocol.SessionEvent
	onSessionState func()
}

func (c *rewindEventChannel) Name() string { return "web" }
func (c *rewindEventChannel) Start() error { return nil }
func (c *rewindEventChannel) Stop()        {}
func (c *rewindEventChannel) Send(channel.OutboundMsg) (string, error) {
	return "", nil
}
func (c *rewindEventChannel) SendSessionState(event protocol.SessionEvent) {
	if c.onSessionState != nil {
		c.onSessionState()
	}
	c.events = append(c.events, event)
}

func TestEmitSessionStateUsesOnePublisherForSharedServerHub(t *testing.T) {
	webChannel := webchannel.NewWebChannel(webchannel.WebChannelConfig{}, bus.NewMessageBus())
	remoteCLI := webchannel.NewRemoteCLIChannel(webChannel.Hub())
	webEvents := &rewindEventChannel{}
	a := &Agent{channelFinder: func(name string) (channel.Channel, bool) {
		switch name {
		case "cli":
			return remoteCLI, true
		case "web":
			return webEvents, true
		default:
			return nil, false
		}
	}}

	a.emitSessionState(protocol.SessionEvent{Channel: "cli", ChatID: "shared", Action: "history_rewound"})
	if len(webEvents.events) != 0 {
		t.Fatalf("CLI event was published through the shared Web Hub twice: %#v", webEvents.events)
	}
	a.emitSessionState(protocol.SessionEvent{Channel: "web", ChatID: "shared", Action: "history_rewound"})
	if len(webEvents.events) != 1 || webEvents.events[0].Channel != "web" {
		t.Fatalf("Web event publisher selection = %#v", webEvents.events)
	}
}

func TestRewindHistoryUsesSessionGateAndEmitsReset(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/rewind.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })
	sess, err := mt.GetOrCreateSession("web", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := sess.AppendMessage(llm.NewUserMessage("rewrite me"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewAssistantMessage("old future")); err != nil {
		t.Fatal(err)
	}

	events := &rewindEventChannel{}
	a := &Agent{multiSession: mt, channelFinder: func(name string) (channel.Channel, bool) {
		return events, name == "web"
	}}
	progressKey := qualifyChatID("web", "chat-1")
	a.lastProgressSnapshot.Store(progressKey, &protocol.ProgressEvent{Phase: "done", Content: "deleted future"})
	a.iterationHistories.Store(progressKey, &[]protocol.ProgressEvent{{Iteration: 1, Content: "deleted future"}})
	a.updateStreamState(progressKey, func(progress *protocol.ProgressEvent) {
		progress.StreamContent = "deleted future"
	})
	gate := a.sessionOperationGate("web", "chat-1")
	if !gate.lock(context.Background()) {
		t.Fatal("failed to acquire test session gate")
	}
	if _, err := a.RewindHistory("web", "chat-1", targetID); err == nil || !strings.Contains(err.Error(), "processing") {
		t.Fatalf("rewind while turn gate is held = %v, want processing error", err)
	}
	gate.unlock()
	gateHeldDuringReset := false
	liveStateClearedBeforeReset := false
	events.onSessionState = func() {
		resetGate := a.sessionOperationGate("web", "chat-1")
		if resetGate.tryLock() {
			resetGate.unlock()
		} else {
			gateHeldDuringReset = true
		}
		liveStateClearedBeforeReset = a.GetActiveProgress("web", "chat-1", protocol.FetchAll()) == nil
	}

	result, err := a.RewindHistory("web", "chat-1", targetID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HistoryRewound || result.TargetHistoryID != targetID || result.Draft != "rewrite me" {
		t.Fatalf("rewind result = %+v", result)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("rewound history retained records: %+v", records)
	}
	if progress := a.GetActiveProgress("web", "chat-1", protocol.FetchAll()); progress != nil {
		t.Fatalf("rewound live progress survived: %+v", progress)
	}
	if _, ok := a.iterationHistories.Load(progressKey); ok {
		t.Fatal("rewound iteration history survived")
	}
	if _, ok := a.streamState.Load(progressKey); ok {
		t.Fatal("rewound stream state survived")
	}
	if len(events.events) != 1 {
		t.Fatalf("reset events = %+v", events.events)
	}
	if !gateHeldDuringReset || !liveStateClearedBeforeReset {
		t.Fatalf("reset published outside barrier: gate_held=%v live_state_cleared=%v", gateHeldDuringReset, liveStateClearedBeforeReset)
	}
	event := events.events[0]
	if event.Action != "history_rewound" || event.Channel != "web" || event.ChatID != "chat-1" || event.TargetHistoryID != targetID {
		t.Fatalf("reset event = %+v", event)
	}
}

func TestRewindAgentHistoryRefreshesQualifiedInteractiveState(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/rewind-agent.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })
	key := "web:chat-1/reviewer:one"
	sess, err := mt.GetOrCreateSession("agent", key)
	if err != nil {
		t.Fatal(err)
	}
	firstUserID, err := sess.AppendMessage(llm.NewUserMessage("keep"))
	if err != nil {
		t.Fatal(err)
	}
	firstAssistantID, err := sess.AppendMessage(llm.NewAssistantMessage("kept reply"))
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := sess.AppendMessage(llm.NewUserMessage("rewrite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewAssistantMessage("deleted future")); err != nil {
		t.Fatal(err)
	}
	stale, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}

	pendingReply := make(chan error, 1)
	cfg := &RunConfig{Session: sess, LastPromptTokens: 99, LastCompletionTokens: 44}
	ia := &interactiveAgent{
		roleName:         "reviewer",
		instance:         "one",
		messages:         stale,
		iterationHistory: make([]IterationSnapshot, 1),
		cfg:              cfg,
		lastReply:        "deleted future",
		lastError:        "stale error",
		promptTokens:     99,
		completionTokens: 44,
		pendingMessages:  []pendingUserMsg{{content: "stale pending", replyCh: pendingReply}},
	}
	a := &Agent{multiSession: mt}
	a.interactiveSubAgents.Store(key, ia)
	agentProgressKey := qualifyChatID("agent", key)
	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{Phase: "done", Content: "deleted future"})
	a.iterationHistories.Store(agentProgressKey, &[]protocol.ProgressEvent{{Iteration: 1, Content: "deleted future"}})
	a.updateStreamState(agentProgressKey, func(progress *protocol.ProgressEvent) {
		progress.StreamContent = "deleted future"
	})
	a.setPendingAskUser("agent", key, &protocol.ProgressEvent{RequestID: "agent-pending"})
	a.setPendingAskUser("web", key, &protocol.ProgressEvent{RequestID: "web-pending"})

	result, err := a.RewindHistory("agent", key, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft != "rewrite" || result.TargetHistoryID != targetID {
		t.Fatalf("rewind result=%+v", result)
	}

	ia.mu.Lock()
	if len(ia.messages) != 2 || ia.messages[0].HistoryID != firstUserID || ia.messages[1].HistoryID != firstAssistantID {
		t.Fatalf("interactive messages after rewind=%+v", ia.messages)
	}
	if len(ia.iterationHistory) != 0 || len(ia.pendingMessages) != 0 {
		t.Fatalf("interactive future state survived: iterations=%+v pending=%+v", ia.iterationHistory, ia.pendingMessages)
	}
	if ia.lastReply != "kept reply" || ia.lastError != "" || ia.promptTokens != 0 || ia.completionTokens != 0 {
		t.Fatalf("interactive summary state after rewind: reply=%q err=%q tokens=%d/%d", ia.lastReply, ia.lastError, ia.promptTokens, ia.completionTokens)
	}
	if cfg.LastPromptTokens != 0 || cfg.LastCompletionTokens != 0 {
		t.Fatalf("interactive RunConfig token state=%d/%d", cfg.LastPromptTokens, cfg.LastCompletionTokens)
	}
	ia.mu.Unlock()

	select {
	case err := <-pendingReply:
		if err == nil || !strings.Contains(err.Error(), "rewound") {
			t.Fatalf("pending delivery result=%v", err)
		}
	default:
		t.Fatal("rewind did not reject stale pending interactive delivery")
	}
	if pending := a.GetPendingAskUser("agent", key); pending != nil {
		t.Fatalf("agent pending AskUser survived rewind: %+v", pending)
	}
	if pending := a.GetPendingAskUser("web", key); pending == nil || pending.RequestID != "web-pending" {
		t.Fatalf("rewind cleared another channel's pending AskUser: %+v", pending)
	}
	if progress := a.GetActiveProgress("agent", key, protocol.FetchAll()); progress != nil {
		t.Fatalf("rewound agent progress survived: %+v", progress)
	}
	if _, ok := a.streamState.Load(agentProgressKey); ok {
		t.Fatal("rewound agent stream state survived")
	}
}

func TestClientRewindHistorySendsOnlyStableID(t *testing.T) {
	transport := &rewindCaptureTransport{}
	client := NewClient(transport, nil)
	result, err := client.RewindHistory("cli", "chat-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetHistoryID != 42 || transport.method != MethodRewindHistory {
		t.Fatalf("result=%+v method=%q", result, transport.method)
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 3 || payload["channel"] != "cli" || payload["chat_id"] != "chat-1" || payload["history_id"] != float64(42) {
		t.Fatalf("rewind payload = %#v", payload)
	}
}

func TestClientContinueInteractiveSessionSendsCanonicalKey(t *testing.T) {
	transport := &rewindCaptureTransport{}
	client := NewClient(transport, nil)
	if err := client.ContinueInteractiveSession("agent:web:chat-1/review:1/fix:2", "continue"); err != nil {
		t.Fatal(err)
	}
	if transport.method != MethodContinueInteractiveSession {
		t.Fatalf("method = %q", transport.method)
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["full_key"] != "agent:web:chat-1/review:1/fix:2" || payload["content"] != "continue" {
		t.Fatalf("continuation payload = %#v", payload)
	}
}
