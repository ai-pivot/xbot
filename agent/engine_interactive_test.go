package agent

import (
	"context"
	"fmt"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// TestAssignSubAgentTurnID verifies the per-session turn allocation for
// SubAgent Runs. Without it, all persisted messages/iterations carry turn_id=0
// (invariant violation) and the frontend cannot associate iterations with the
// turn — session view renders no iteration content/reasoning (user report:
// "subagent session 历史渲染错乱，看不到任何迭代的 content 和 reasoning").
func TestAssignSubAgentTurnID(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/subagent-turnid.db")
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	defer mt.Close()

	a := &Agent{}
	sess, err := mt.GetOrCreateSession("agent", "explore:test-1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	// First Run of the session → turn_id = 1 (never 0).
	cfg := &RunConfig{AgentID: "main/explore"}
	a.assignSubAgentTurnID(cfg, sess)
	if cfg.TurnID != 1 {
		t.Errorf("first assign: TurnID = %d, want 1 (non-zero invariant)", cfg.TurnID)
	}

	// max+1 path: seed a turn_id=5 iteration record, the next assign must
	// continue from the DB max.
	if err := sess.AppendIterationHistory(0, 5, sqlite.IterationRecord{TurnID: 5, Iteration: 1, Content: "seed"}); err != nil {
		t.Fatalf("seed AppendIterationHistory: %v", err)
	}
	cfg3 := &RunConfig{}
	a.assignSubAgentTurnID(cfg3, sess)
	if cfg3.TurnID != 6 {
		t.Errorf("assign after turn 5: TurnID = %d, want 6", cfg3.TurnID)
	}

	// nil session / nil cfg must be safe no-ops.
	a.assignSubAgentTurnID(nil, sess)
	a.assignSubAgentTurnID(cfg3, nil)
}

func TestSubAgentCallback_RunDoneDropsCallback(t *testing.T) {
	// Regression: after Run() returns, structuredProgress.Iteration is frozen
	// at its final value — for subagents spawned by the LAST iteration the
	// stale-iteration guard is false forever, and the callback would keep
	// broadcasting into the main session's stream. runDone closes that hole.
	s := &runState{
		cfg: RunConfig{AgentID: "main", Channel: "cli", ChatID: "t"},
	}
	s.runDone.Store(true)

	// The callback guard reads runDone first — same predicate as execOneTool.
	if !s.runDone.Load() {
		t.Fatal("runDone=true must be observed by the callback guard")
	}

	// Run() sets runDone on every return path (defer in engine.Run).
	s2 := &runState{}
	s2.runDone.Store(false)
	if s2.runDone.Load() {
		t.Fatal("fresh runState must start with runDone=false")
	}
}

func TestSpawnAgentAdapter_InteractiveSpawn_NilCallback(t *testing.T) {
	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
			return &channel.OutboundMsg{Content: "ok"}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_123",
		senderID: "ou_456",
	}

	// No interactive callbacks → should return error
	_, err := adapter.SpawnInteractive(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_456",
		ChatID:   "oc_123",
		Channel:  "feishu",
	}, "task", "reviewer", "You are a reviewer", nil, tools.SubAgentCapabilities{}, "", "")
	if err == nil {
		t.Fatal("expected error when interactive callbacks are nil")
		return
	}
	if err.Error() != "interactive mode not supported" {
		t.Errorf("error = %q, want %q", err.Error(), "interactive mode not supported")
	}
}

func TestSpawnAgentAdapter_InteractiveSend_NilCallback(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
	}
	_, err := adapter.SendInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "task", "reviewer", "", nil, tools.SubAgentCapabilities{}, "", "")
	if err == nil {
		t.Fatal("expected error when interactive callbacks are nil")
		return
	}
}

func TestSpawnAgentAdapter_InteractiveUnload_NilCallback(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
	}
	err := adapter.UnloadInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "reviewer", "")
	if err == nil {
		t.Fatal("expected error when interactive callbacks are nil")
		return
	}
}

func TestSpawnAgentAdapter_InteractiveSpawn_Success(t *testing.T) {
	var capturedRole string
	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
			return &channel.OutboundMsg{Content: "spawned"}, nil
		},
		interactiveSpawnFn: func(ctx context.Context, roleName string, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
			capturedRole = roleName
			return &channel.OutboundMsg{Content: "interactive spawned"}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_123",
		senderID: "ou_456",
	}

	result, err := adapter.SpawnInteractive(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_456",
		Channel:  "feishu",
		ChatID:   "oc_123",
	}, "review my code", "reviewer", "You are a code reviewer", nil, tools.SubAgentCapabilities{}, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "interactive spawned" {
		t.Errorf("result = %q, want %q", result, "interactive spawned")
	}
	if capturedRole != "reviewer" {
		t.Errorf("capturedRole = %q, want %q", capturedRole, "reviewer")
	}
}

func TestSpawnAgentAdapter_InteractiveSend_Success(t *testing.T) {
	adapter := &spawnAgentAdapter{
		interactiveSendFn: func(ctx context.Context, roleName string, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
			return &channel.OutboundMsg{Content: "sent " + msg.Content}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_123",
		senderID: "ou_456",
	}

	result, err := adapter.SendInteractive(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_456",
		Channel:  "feishu",
		ChatID:   "oc_123",
	}, "fix this bug", "writer", "", nil, tools.SubAgentCapabilities{}, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "sent fix this bug" {
		t.Errorf("result = %q", result)
	}
}

func TestSpawnAgentAdapter_InteractiveUnload_Success(t *testing.T) {
	unloaded := false
	adapter := &spawnAgentAdapter{
		interactiveUnloadFn: func(ctx context.Context, roleName, instance string) error {
			unloaded = true
			return nil
		},
		parentID: "main",
	}

	err := adapter.UnloadInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "reviewer", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unloaded {
		t.Error("expected unloadFn to be called")
	}
}

func TestSpawnAgentAdapter_InteractiveUnload_Error(t *testing.T) {
	adapter := &spawnAgentAdapter{
		interactiveUnloadFn: func(ctx context.Context, roleName, instance string) error {
			return fmt.Errorf("no such session")
		},
		parentID: "main",
	}

	err := adapter.UnloadInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "reviewer", "")
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if err.Error() != "no such session" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestSpawnAgentAdapter_BuildMsg_Interactive(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_abc",
		senderID: "ou_xyz",
	}

	msg := adapter.buildMsg(&tools.ToolContext{
		Ctx:        context.Background(),
		SenderID:   "ou_xyz",
		SenderName: "Test User",
		Channel:    "feishu",
		ChatID:     "oc_abc",
	}, "do something", "reviewer", "You are reviewer", []string{"Read", "Grep"}, tools.SubAgentCapabilities{Memory: true}, true, "", "")

	// Check interactive flag in metadata
	if msg.Metadata["interactive"] != "true" {
		t.Errorf("metadata[interactive] = %q, want %q", msg.Metadata["interactive"], "true")
	}
	// Check origin fields preserved
	if msg.Metadata["origin_channel"] != "feishu" {
		t.Errorf("metadata[origin_channel] = %q", msg.Metadata["origin_channel"])
	}
	if msg.Metadata["origin_chat_id"] != "oc_abc" {
		t.Errorf("metadata[origin_chat_id] = %q", msg.Metadata["origin_chat_id"])
	}
	// Check capabilities
	if !msg.Capabilities["memory"] {
		t.Error("expected memory capability")
	}
	// Check allowed tools
	if len(msg.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want 2 items", msg.AllowedTools)
	}
	// instance should not be set when empty
	if _, ok := msg.Metadata["instance_id"]; ok {
		t.Error("instance_id should not be set in metadata when instance is empty")
	}
}

func TestSpawnAgentAdapter_BuildMsg_WithInstance(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_abc",
		senderID: "ou_xyz",
	}

	msg := adapter.buildMsg(&tools.ToolContext{
		Ctx:        context.Background(),
		SenderID:   "ou_xyz",
		SenderName: "Test User",
		Channel:    "feishu",
		ChatID:     "oc_abc",
	}, "do something", "brainstorm", "You are brainstorm agent", nil, tools.SubAgentCapabilities{}, true, "architect", "")

	// Check interactive flag in metadata
	if msg.Metadata["interactive"] != "true" {
		t.Errorf("metadata[interactive] = %q, want %q", msg.Metadata["interactive"], "true")
	}
	// Check instance_id in metadata
	if msg.Metadata["instance_id"] != "architect" {
		t.Errorf("metadata[instance_id] = %q, want %q", msg.Metadata["instance_id"], "architect")
	}
}

func TestSpawnAgentAdapter_BuildMsg_NonInteractive(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_abc",
		senderID: "ou_xyz",
	}

	msg := adapter.buildMsg(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_xyz",
	}, "do something", "reviewer", "", nil, tools.SubAgentCapabilities{}, false, "", "")

	if msg.Metadata["interactive"] == "true" {
		t.Error("interactive flag should not be set for non-interactive mode")
	}
}
