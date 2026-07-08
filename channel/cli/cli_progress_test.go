package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"xbot/channel"
	"xbot/protocol"
)

// initTestModel creates a model with channelName/chatID set for progress tests.
func initTestModel() *cliModel {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"
	return model
}

func TestStartNewMessageFinalizesStaleStreamingTurn(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    1,
	})
	model.streamingMsgIdx = 0
	model.agentTurnID = 1
	model.typing = false
	model.progressState.iterations = []cliIterationSnapshot{{
		Iteration: 1,
		Content:   "old completed iteration",
	}}
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:     1,
		StreamContent: "old live stream must not render under next user message",
	}

	model.finalizeStaleStreamingBeforeNewUserMessage()
	model.messages = append(model.messages, cliMessage{
		role:      "user",
		content:   "next user message",
		timestamp: time.Now(),
		dirty:     true,
	})
	model.updateViewportContent()

	if model.streamingMsgIdx != -1 {
		t.Fatalf("streamingMsgIdx = %d, want -1", model.streamingMsgIdx)
	}
	if model.progressState.current != nil {
		t.Fatal("progressState.current was not cleared")
	}
	if len(model.progressState.iterations) != 0 {
		t.Fatalf("progressState.iterations len = %d, want 0", len(model.progressState.iterations))
	}
	if model.messages[0].isPartial {
		t.Fatal("previous assistant message is still partial")
	}
	if len(model.messages[0].iterations) != 1 || model.messages[0].iterations[0].Content != "old completed iteration" {
		t.Fatalf("previous assistant iterations = %+v, want old completed iteration baked", model.messages[0].iterations)
	}
	viewportText := model.viewport.View()
	if strings.Contains(viewportText, "old live stream must not render") {
		t.Fatalf("old live stream leaked into viewport:\n%s", viewportText)
	}
	if !strings.Contains(viewportText, "next user message") {
		t.Fatalf("new user message missing from viewport:\n%s", viewportText)
	}
}

func sendProgress(model *cliModel, payload *protocol.ProgressEvent) {
	if payload.ChatID == "" {
		payload.ChatID = model.channelName + ":" + model.chatID
	}
	model.Update(cliProgressMsg{payload: payload})
}

// sendProgressWithHistory sends a progress event that also carries
// IterationHistory from the backend. Use this when the test expects
// completed iterations to be available in the local state — push events
// alone (without IterationHistory) do not create local snapshots.
func sendProgressWithHistory(model *cliModel, payload *protocol.ProgressEvent, history ...protocol.ProgressEvent) {
	if payload.ChatID == "" {
		payload.ChatID = model.channelName + ":" + model.chatID
	}
	payload.IterationHistory = history
	model.Update(cliProgressMsg{payload: payload})
}

func sendDone(model *cliModel, content string) {
	model.typing = false
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{Channel: model.channelName, ChatID: model.chatID,
			Content:   content,
			IsPartial: false,
		},
	})
}

func TestRenderTurnBodyMultiIterationLiveOutput(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	tests := []struct {
		name            string
		iterations      []cliIterationSnapshot
		progress        *protocol.ProgressEvent
		fallbackContent string
	}{
		{
			name: "previous tool done then active empty next iteration renders pulse",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous read succeeded", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
			},
			progress: &protocol.ProgressEvent{Iteration: 2},
		},
		{
			name: "previous content plus tool then active fallback stream text",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Content:   "previous content before current stream",
					Tools: []protocol.ToolProgress{
						{Name: "Glob", Label: "Previous glob succeeded", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
			},
			progress:        &protocol.ProgressEvent{Iteration: 2},
			fallbackContent: "current assistant fallback stream",
		},
		{
			name: "previous reasoning plus tool then active thinking overrides fallback",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Reasoning: "previous reasoning",
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous read after reasoning", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
			},
			progress:        &protocol.ProgressEvent{Iteration: 2, Content: "current thinking text"},
			fallbackContent: "fallback assistant text",
		},
		{
			name: "previous tool then active reasoning stream does not render pulse",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous tool before reasoning", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
			},
			progress: &protocol.ProgressEvent{
				Iteration:              2,
				ReasoningStreamContent: "current reasoning stream only",
			},
		},
		{
			name: "previous success and failure then active stream with success failure running",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous tool succeeded", Status: "done", Elapsed: 100, Iteration: 1},
						{Name: "Edit", Label: "Previous tool failed", Status: "error", Elapsed: 200, Iteration: 1},
					},
				},
			},
			progress: &protocol.ProgressEvent{
				Iteration:     2,
				Content:       "current thinking text",
				StreamContent: "current streamed content",
				ActiveTools: []protocol.ToolProgress{
					{Name: "Read", Label: "Current active succeeded", Status: "done", Elapsed: 300},
					{Name: "Edit", Label: "Current active failed", Status: "error", Elapsed: 400},
					{Name: "Shell", Label: "Current active running", Status: "running", Elapsed: 1500},
				},
			},
			fallbackContent: "fallback assistant text",
		},
		{
			name: "multiple previous tool only iterations then active running tool",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Iter one completed", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
				{
					Iteration: 2,
					Tools: []protocol.ToolProgress{
						{Name: "Edit", Label: "Iter two failed", Status: "error", Elapsed: 200, Iteration: 2},
					},
				},
			},
			progress: &protocol.ProgressEvent{
				Iteration: 3,
				ActiveTools: []protocol.ToolProgress{
					{Name: "Shell", Label: "Iter three still running", Status: "running", Elapsed: 2400},
				},
			},
		},
		{
			name: "previous content then active reasoning plus mixed tools",
			iterations: []cliIterationSnapshot{
				{Iteration: 1, Content: "previous content only"},
				{
					Iteration: 2,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous second iter done", Status: "done", Elapsed: 100, Iteration: 2},
					},
				},
			},
			progress: &protocol.ProgressEvent{
				Iteration:              3,
				ReasoningStreamContent: "current reasoning before tools",
				ActiveTools: []protocol.ToolProgress{
					{Name: "Read", Label: "Current read succeeded", Status: "done", Elapsed: 200},
					{Name: "Edit", Label: "Current edit failed", Status: "error", Elapsed: 300},
					{Name: "Shell", Label: "Current shell running", Status: "running", Elapsed: 2400},
				},
			},
		},
		{
			name: "previous reasoning then active content plus completed tools",
			iterations: []cliIterationSnapshot{
				{Iteration: 1, Reasoning: "previous reasoning only"},
				{
					Iteration: 2,
					Content:   "previous content and done tool",
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous done tool", Status: "done", Elapsed: 100, Iteration: 2},
					},
				},
			},
			progress: &protocol.ProgressEvent{
				Iteration:     3,
				StreamContent: "current content before completed tools",
				CompletedTools: []protocol.ToolProgress{
					{Name: "Glob", Label: "Current glob completed", Status: "done", Elapsed: 100},
					{Name: "ApplyPatch", Label: "Current patch failed", Status: "error", Elapsed: 400},
				},
			},
		},
		{
			name: "multiple previous iterations then active subagents only",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous tool done", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
				{Iteration: 2, Content: "previous content before subagent"},
			},
			progress: &protocol.ProgressEvent{
				Iteration: 3,
				SubAgents: []protocol.SubAgentInfo{
					{
						Role:     "explore",
						Instance: "alpha",
						Status:   "running",
						Desc:     "map rendering behavior",
						Children: []protocol.SubAgentInfo{
							{Role: "review", Instance: "done", Status: "done", Desc: "already finished"},
							{Role: "test", Instance: "beta", Status: "running", Desc: "verify snapshots"},
						},
					},
				},
			},
		},
	}

	var snapshot strings.Builder
	for _, tt := range tests {
		rendered := model.renderTurnBody(tt.iterations, tt.progress, 80, tt.fallbackContent)
		appendRenderSnapshotCase(&snapshot, tt.name, rendered)
	}
	assertRenderSnapshot(t, "render_turn_body_multi_iteration_live.snap", snapshot.String())
}

func TestRenderTurnBodyMultiIterationIdleOutput(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	iterations := []cliIterationSnapshot{
		{
			Iteration: 1,
			Reasoning: "completed reasoning",
			Content:   "completed iteration text",
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "Read completed file", Status: "done", Elapsed: 200, Iteration: 1},
			},
		},
		{
			Iteration: 2,
			Tools: []protocol.ToolProgress{
				{Name: "Glob", Label: "Second completed file search", Status: "done", Elapsed: 100, Iteration: 2},
			},
		},
	}

	tests := []struct {
		name            string
		iterations      []cliIterationSnapshot
		liveProgress    *protocol.ProgressEvent
		fallbackContent string
	}{
		{
			name:            "idle fallback is rendered after completed iterations",
			iterations:      iterations,
			fallbackContent: "final assistant answer",
		},
		{
			name:            "idle fallback is skipped when last iteration already has it",
			iterations:      []cliIterationSnapshot{{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "Read", Label: "Earlier completed tool", Status: "done", Elapsed: 100, Iteration: 1}}}, {Iteration: 2, Content: "final assistant answer"}},
			fallbackContent: "final assistant answer",
		},
		{
			name: "completed reasoning plus tool only",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Reasoning: "reasoning-only completed iteration",
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Read after reasoning", Status: "done", Elapsed: 100, Iteration: 1},
						{Name: "Edit", Label: "Edit failed after reasoning", Status: "error", Elapsed: 200, Iteration: 1},
					},
				},
				{
					Iteration: 2,
					Tools: []protocol.ToolProgress{
						{Name: "Shell", Label: "Second tool still marked running", Status: "running", Elapsed: 500, Iteration: 2},
					},
				},
			},
		},
		{
			name: "completed content plus tool only",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Reasoning: "reasoning before content iteration",
				},
				{
					Iteration: 2,
					Content:   "content-only completed iteration",
					Tools: []protocol.ToolProgress{
						{Name: "Shell", Label: "Run content command", Status: "done", Elapsed: 300, Iteration: 2},
					},
				},
			},
		},
		{
			name: "completed tools only mixed statuses",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "First completed tool", Status: "done", Elapsed: 100, Iteration: 1},
						{Name: "Edit", Label: "Second failed tool", Status: "error", Elapsed: 200, Iteration: 1},
					},
				},
				{
					Iteration: 2,
					Tools: []protocol.ToolProgress{
						{Name: "Shell", Label: "Third running tool", Status: "running", Elapsed: 300, Iteration: 2},
					},
				},
			},
		},
		{
			name: "multiple tool only iterations with prior done and next running",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "Previous iteration done", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
				{
					Iteration: 2,
					Tools: []protocol.ToolProgress{
						{Name: "Shell", Label: "Next iteration running", Status: "running", Elapsed: 2000, Iteration: 2},
					},
				},
			},
		},
		{
			name: "mixed completed iterations without live progress",
			iterations: []cliIterationSnapshot{
				{Iteration: 1, Reasoning: "reasoning only iteration"},
				{Iteration: 2, Content: "content only iteration"},
				{
					Iteration: 3,
					Tools: []protocol.ToolProgress{
						{Name: "Glob", Label: "tool only iteration", Status: "done", Elapsed: 100, Iteration: 3},
					},
				},
			},
		},
		{
			name: "multiple completed iterations with final content fallback",
			iterations: []cliIterationSnapshot{
				{
					Iteration: 1,
					Reasoning: "first completed reasoning",
					Tools: []protocol.ToolProgress{
						{Name: "Read", Label: "First completed tool", Status: "done", Elapsed: 100, Iteration: 1},
					},
				},
				{
					Iteration: 2,
					Content:   "second completed content",
					Tools: []protocol.ToolProgress{
						{Name: "Edit", Label: "Second completed failed", Status: "error", Elapsed: 200, Iteration: 2},
					},
				},
			},
			fallbackContent: "final fallback after multi iter",
		},
	}

	var snapshot strings.Builder
	for _, tt := range tests {
		rendered := model.renderTurnBody(tt.iterations, tt.liveProgress, 80, tt.fallbackContent)
		appendRenderSnapshotCase(&snapshot, tt.name, rendered)
	}
	assertRenderSnapshot(t, "render_turn_body_multi_iteration_idle.snap", snapshot.String())
}

func TestStreamingSeparatorUsesAdjacentBlockKinds(t *testing.T) {
	iterations := []cliIterationSnapshot{
		{Iteration: 1, Content: "earlier content makes whole history mixed"},
		{
			Iteration: 2,
			Tools: []protocol.ToolProgress{
				{Name: "Shell", Label: "completed build", Status: "done", Elapsed: 100, Iteration: 2},
			},
		},
	}
	liveBlocks := []turnBlock{
		{kind: turnBlockTools, text: "  · ◌ running qemu 2.1s"},
	}

	prevKind, hasPrev := lastIterationBlockKind(iterations)
	nextKind, hasNext := firstTurnBlockKind(liveBlocks)
	if !hasPrev || !hasNext {
		t.Fatal("expected adjacent block kinds")
	}
	if needsTurnBlockSeparator(prevKind, nextKind) {
		t.Fatal("same adjacent tools blocks should not insert a blank guide line")
	}
}

func TestRenderLiveIterationCompressingShowsStatus(t *testing.T) {
	model := initTestModel()
	model.locale = channel.GetLocale("en")

	rendered := stripAnsi(model.renderLiveIteration(&protocol.ProgressEvent{Phase: "compressing"}, 80, ""))

	if !strings.Contains(rendered, "compressing") {
		t.Fatalf("compressing phase should render status text, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "◇") {
		t.Fatalf("compressing phase should show pulse spinner animation, got:\n%s", rendered)
	}
}

func TestRenderToolTagsKeepsLabelsSingleLine(t *testing.T) {
	model := initTestModel()

	rendered := stripAnsi(model.renderToolTags([]protocol.ToolProgress{
		{Name: "Shell", Label: "ssh remote\ncargo build\r\n--release", Status: "done"},
	}, 80, &model.styles))

	if strings.Contains(rendered, "\n  cargo") || strings.Contains(rendered, "\n--release") {
		t.Fatalf("tool label should stay on one rendered line, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ssh remote cargo build --release") {
		t.Fatalf("tool label should normalize internal newlines, got:\n%s", rendered)
	}
}

func TestRenderToolTagsColorsDoneAndErrorLabels(t *testing.T) {
	model := initTestModel()
	tools := []protocol.ToolProgress{
		{Name: "Shell", Label: "success label", Status: "done"},
		{Name: "Read", Label: "failure label", Status: "error"},
	}

	rendered := model.renderToolTags(tools, 80, &model.styles)
	if !strings.Contains(rendered, model.styles.ProgressDone.Render("✓ success label")) {
		t.Fatalf("done tool label should use success color, got:\n%q", rendered)
	}
	if !strings.Contains(rendered, model.styles.ProgressError.Render("✗ failure label")) {
		t.Fatalf("error tool label should use error color, got:\n%q", rendered)
	}

	liveRendered := model.renderLiveToolTags(tools, 80)
	if !strings.Contains(liveRendered, model.styles.ProgressDone.Render("✓ success label")) {
		t.Fatalf("live done tool label should use success color, got:\n%q", liveRendered)
	}
	if !strings.Contains(liveRendered, model.styles.ProgressError.Render("✗ failure label")) {
		t.Fatalf("live error tool label should use error color, got:\n%q", liveRendered)
	}
}

func TestRenderMessageAddsSpacerBeforeFirstToolBlock(t *testing.T) {
	model := initTestModel()
	msg := &cliMessage{
		role:      "assistant",
		timestamp: time.Now(),
		iterations: []cliIterationSnapshot{
			{
				Iteration: 1,
				Tools: []protocol.ToolProgress{
					{Name: "Shell", Label: "first tool", Status: "done", Iteration: 1},
				},
			},
		},
		dirty: true,
	}

	rendered := stripAnsi(model.renderMessage(msg))
	lines := strings.Split(rendered, "\n")
	headerIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "Assistant") {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+2 >= len(lines) {
		t.Fatalf("assistant header not found or output too short:\n%s", rendered)
	}
	if strings.TrimSpace(lines[headerIdx+1]) != "┊" {
		t.Fatalf("expected blank guide spacer after Assistant header, got %q in:\n%s", lines[headerIdx+1], rendered)
	}
	if !strings.Contains(lines[headerIdx+2], "first tool") {
		t.Fatalf("expected first tool after spacer, got line %q in:\n%s", lines[headerIdx+2], rendered)
	}
}

func TestFullRebuildWithStreamingCachesOnlyHistoryMessages(t *testing.T) {
	model := initTestModel()
	model.messages = []cliMessage{
		{role: "user", content: "current user message", timestamp: time.Now(), dirty: true},
		{role: "assistant", content: "streaming reply", timestamp: time.Now(), isPartial: true, dirty: true},
	}
	model.streamingMsgIdx = 1

	model.fullRebuild()

	if model.rc.msgCount != model.streamingMsgIdx {
		t.Fatalf("rc.msgCount = %d, want streaming split index %d", model.rc.msgCount, model.streamingMsgIdx)
	}
	if strings.Contains(model.rc.history, "streaming reply") {
		t.Fatalf("streaming message should not be cached in history:\n%s", model.rc.history)
	}
}

func TestUpdateViewportContentClearsStaleStreamingIndex(t *testing.T) {
	model := initTestModel()
	model.messages = []cliMessage{{role: "user", content: "history", timestamp: time.Now(), dirty: true}}
	model.streamingMsgIdx = 3
	model.rc.valid = true

	model.updateViewportContent()

	if model.streamingMsgIdx != -1 {
		t.Fatalf("streamingMsgIdx = %d, want -1 after stale index cleanup", model.streamingMsgIdx)
	}
}

func TestCancelMessageIgnoresStaleStreamingIndex(t *testing.T) {
	model := initTestModel()
	model.messages = []cliMessage{{role: "user", content: "history", timestamp: time.Now(), dirty: true}}
	model.streamingMsgIdx = 3
	model.agentTurnID = 10

	model.handleAgentMessage(channel.OutboundMsg{Channel: model.channelName, ChatID: model.chatID, Metadata: map[string]string{"cancelled": "true"}})
}

func TestCancelMessagePreservesCurrentUnsnappedIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.cancelTargetTurnID = model.agentTurnID
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Content:   "previous iteration",
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "Previous read", Status: "done", Elapsed: 100, Iteration: 1},
			},
		},
	}
	model.progressState.lastIter = 2
	model.progressState.current = &protocol.ProgressEvent{
		Iteration: 2,
		Content:   "current unsnapped iteration",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Current build", Status: "done", Elapsed: 200, Iteration: 2},
		},
	}

	model.handleAgentMessage(channel.OutboundMsg{Channel: model.channelName, ChatID: model.chatID, Metadata: map[string]string{"cancelled": "true"}})

	if model.streamingMsgIdx != -1 {
		t.Fatalf("streamingMsgIdx = %d, want -1 after cancel", model.streamingMsgIdx)
	}
	// With iteration data preserved on cancel, the empty streaming message
	// is finalized (not removed) so the user keeps seeing tool tags/reasoning
	// that were rendered inline before Ctrl+C.
	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected assistant message to be preserved with iterations after cancel")
	}
	if assistantMsg.isPartial {
		t.Error("expected isPartial=false after cancel finalize")
	}
	if len(assistantMsg.iterations) == 0 {
		t.Error("expected iterations to be baked into finalized message")
	}
	// Verify both the previous iteration and current unsnapped iteration are captured.
	// cancelledTurnIterations combines iterationHistory + progress data.
	var foundShell bool
	for _, it := range assistantMsg.iterations {
		for _, tool := range it.Tools {
			if tool.Name == "Shell" {
				foundShell = true
			}
		}
	}
	if !foundShell {
		t.Error("expected Shell tool from current unsnapped iteration to be preserved")
	}
}

// TestStreamTokensOnlyEventDoesNotReplaceProgressState is a regression test for
// the root cause of double-assistant + flickering. streamUsageFunc sends a
// ProgressEvent with ONLY StreamTokens (Phase="", Iteration=0, no other stream
// fields). Before the fix, isStreamOnly did not check StreamTokens>0, so these
// events fell through to the structured path and REPLACED m.progressState.current
// with an empty shell — wiping Phase/Iteration/ActiveTools.
func TestCompReloadingClearedOnStaleAndErrorPaths(t *testing.T) {
	// Stale path: different chatID.
	model := initTestModel()
	model.splashState.compReloading = true
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName: "cli",
		chatID:      "/different-session",
	})
	if model.splashState.compReloading {
		t.Fatal("compReloading not cleared on stale session path")
	}

	// Error path: reload failed.
	model.splashState.compReloading = true
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName: model.channelName,
		chatID:      model.chatID,
		err:         errDummy,
	})
	if model.splashState.compReloading {
		t.Fatal("compReloading not cleared on error path")
	}
}

var errDummy = &dummyError{"reload failed"}

type dummyError struct{ msg string }

func (e *dummyError) Error() string { return e.msg }

func TestHistoryReloadForceFullRebuildDoesNotReuseStaleRenderedCache(t *testing.T) {
	model := initTestModel()
	now := time.Now()
	model.messages = []cliMessage{
		{
			role:         "assistant",
			content:      "compacted summary",
			timestamp:    now,
			rendered:     "STALE_RENDERED_OUTPUT",
			renderWidth:  model.chatWidth(),
			wrappedLines: []string{"STALE_RENDERED_OUTPUT"},
			wrappedWidth: model.chatWidth(),
			dirty:        false,
		},
	}
	model.rc.valid = true
	model.rc.history = "STALE_RENDERED_OUTPUT\n"
	model.rc.histLines = []string{"STALE_RENDERED_OUTPUT"}
	model.rc.msgCount = len(model.messages)

	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "assistant", Content: "compacted summary", Timestamp: now},
		},
	})

	if strings.Contains(model.rc.history, "STALE_RENDERED_OUTPUT") {
		t.Fatalf("force history reload reused stale rendered cache:\n%s", model.rc.history)
	}
	if model.messages[0].rendered == "STALE_RENDERED_OUTPUT" {
		t.Fatal("force history reload should re-render message instead of preserving stale rendered output")
	}
}

func TestHistoryReloadKeepsPendingUserUntilHistoryConfirmsIt(t *testing.T) {
	model := initTestModel()
	pending := cliMessage{role: "user", content: "just sent", timestamp: time.Now(), dirty: true}
	model.pendingUserMsg = &pending

	reload := cliHistoryReloadMsg{
		channelName: model.channelName,
		chatID:      model.chatID,
		history: []channel.HistoryMessage{
			{Role: "assistant", Content: "old reply", Timestamp: time.Now()},
		},
	}
	model.handleHistoryReload(reload)
	if model.pendingUserMsg == nil {
		t.Fatal("pending user should remain when it was only restored locally")
	}
	if !hasUserMessage(model.messages, "just sent") {
		t.Fatalf("pending user was not restored into messages: %+v", model.messages)
	}

	model.handleHistoryReload(reload)
	if model.pendingUserMsg == nil {
		t.Fatal("pending user should survive repeated stale history reloads")
	}
	if !hasUserMessage(model.messages, "just sent") {
		t.Fatalf("pending user disappeared after repeated reload: %+v", model.messages)
	}

	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName: model.channelName,
		chatID:      model.chatID,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "just sent", Timestamp: time.Now()},
			{Role: "assistant", Content: "old reply", Timestamp: time.Now()},
		},
	})
	if model.pendingUserMsg != nil {
		t.Fatal("pending user should clear once history confirms it")
	}
}

func TestHistoryReloadPreservesActiveStreamingTurn(t *testing.T) {
	model := initTestModel()
	model.messages = []cliMessage{
		{role: "user", content: "previous user", timestamp: time.Now(), dirty: true},
		{role: "assistant", content: "previous reply", timestamp: time.Now(), dirty: true},
		{role: "user", content: "current user", timestamp: time.Now(), dirty: true},
	}
	model.startAgentTurn()
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Content:   "current turn thinking",
			Tools: []protocol.ToolProgress{
				{Name: "Shell", Label: "running command", Status: "running", Iteration: 1},
			},
		},
	}
	model.progressState.current = &protocol.ProgressEvent{Phase: "tool_exec", Iteration: 1}
	streamingIdx := model.streamingMsgIdx

	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName: model.channelName,
		chatID:      model.chatID,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "previous user", Timestamp: time.Now()},
			{Role: "assistant", Content: "previous reply", Timestamp: time.Now()},
			{Role: "user", Content: "current user", Timestamp: time.Now()},
		},
	})

	if model.streamingMsgIdx != streamingIdx {
		t.Fatalf("streamingMsgIdx = %d, want active index %d", model.streamingMsgIdx, streamingIdx)
	}
	if model.streamingMsgIdx < 0 || model.streamingMsgIdx >= len(model.messages) {
		t.Fatalf("streamingMsgIdx out of range after reload: %d messages=%d", model.streamingMsgIdx, len(model.messages))
	}
	streaming := model.messages[model.streamingMsgIdx]
	if streaming.role != "assistant" || !streaming.isPartial {
		t.Fatalf("active streaming assistant was not preserved: %+v", streaming)
	}
	if len(model.progressState.iterations) != 1 || model.progressState.iterations[0].Content != "current turn thinking" {
		t.Fatalf("iteration history was not preserved: %+v", model.progressState.iterations)
	}
	if !strings.Contains(model.viewport.View(), "running command") {
		t.Fatalf("viewport lost current turn tools after reload:\n%s", stripAnsi(model.viewport.View()))
	}
}

func hasUserMessage(messages []cliMessage, content string) bool {
	for _, msg := range messages {
		if msg.role == "user" && msg.content == content {
			return true
		}
	}
	return false
}

func appendRenderSnapshotCase(sb *strings.Builder, name, rendered string) {
	if sb.Len() > 0 {
		sb.WriteString("\n")
	}
	sb.WriteString("=== ")
	sb.WriteString(name)
	sb.WriteString(" ===\n")
	sb.WriteString(normalizeRenderSnapshot(rendered))
}

func normalizeRenderSnapshot(rendered string) string {
	clean := strings.ReplaceAll(stripAnsi(rendered), "\r\n", "\n")
	lines := strings.Split(clean, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func assertRenderSnapshot(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/snapshots/" + name
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.MkdirAll("testdata/snapshots", 0o755); err != nil {
			t.Fatalf("create snapshot dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write snapshot %s: %v", path, err)
		}
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot %s: %v", path, err)
	}
	if got != string(wantBytes) {
		t.Fatalf("snapshot %s mismatch\n--- got ---\n%s\n--- want ---\n%s", path, got, string(wantBytes))
	}
}

func countToolsInSummary(model *cliModel) int {
	// Check assistant messages for iterations (unified model).
	// Also check any remaining tool_summary messages (AskUser).
	for _, msg := range model.messages {
		if len(msg.iterations) > 0 {
			count := 0
			for _, it := range msg.iterations {
				count += len(it.Tools)
			}
			return count
		}
		if len(msg.tools) > 0 {
			return len(msg.tools)
		}
	}
	return 0
}

// Basic: 2 iterations, no final empty iteration
func TestProgressNoDuplication(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Content: "A"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read file", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})
	sendProgressWithHistory(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Content: "B"},
		protocol.ProgressEvent{Iteration: 1, Content: "A", CompletedTools: []protocol.ToolProgress{{Name: "read", Label: "Read file", Status: "done", Elapsed: 1000, Iteration: 1}}})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 500, Iteration: 2},
		},
	})

	// Verify iterationHistory has entries and tools
	if len(model.progressState.iterations) == 0 {
		t.Error("Expected iterationHistory to have entries after progress events")
	}

	sendDone(model, "Final answer")

	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}
}

// Realistic: 2 iterations with 2+1 tools, then empty thinking iteration before done
func TestProgressRealisticSequence(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Content: "Let me look"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "Let me look",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read config", Status: "done", Elapsed: 500, Iteration: 1},
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 300, Iteration: 1},
		},
	})
	// Iter 1
	sendProgressWithHistory(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Content: "Based on results"},
		protocol.ProgressEvent{Iteration: 1, Content: "Let me look", CompletedTools: []protocol.ToolProgress{{Name: "read", Label: "Read config", Status: "done", Elapsed: 500, Iteration: 1}, {Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 300, Iteration: 1}}})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "Based on results",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Fix bug", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})
	// Iter 2: empty thinking (no tools)
	sendProgressWithHistory(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 3, Content: ""},
		protocol.ProgressEvent{Iteration: 1, Content: "Let me look", CompletedTools: []protocol.ToolProgress{{Name: "read", Label: "Read config", Status: "done", Elapsed: 500, Iteration: 1}, {Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 300, Iteration: 1}}},
		protocol.ProgressEvent{Iteration: 2, Content: "Based on results", CompletedTools: []protocol.ToolProgress{{Name: "edit", Label: "Fix bug", Status: "done", Elapsed: 200, Iteration: 2}}})

	if len(model.progressState.iterations) == 0 {
		t.Error("Expected iterationHistory to have entries")
	}

	sendDone(model, "Here is the fix.")

	if tools := countToolsInSummary(model); tools != 3 {
		t.Errorf("Expected 3 tools in summary, got %d", tools)
	}
}

// Error tool Iteration: verify error tools have correct Iteration and don't
// appear under the wrong iteration.
func TestErrorToolIterationAttribution(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: a tool that errors
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "Trying A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "error", Elapsed: 100, Iteration: 1},
		},
	})
	// Iter 1: a tool that succeeds
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "Trying B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 2},
		},
	}, protocol.ProgressEvent{Iteration: 1, Content: "Trying A", CompletedTools: []protocol.ToolProgress{{Name: "read", Label: "Read", Status: "error", Elapsed: 100, Iteration: 1}}})

	sendDone(model, "Done")

	// Verify both tools are in summary, each in their own iteration
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}

	// Check iteration attribution in the summary (in assistant messages)
	var foundIter0, foundIter1 bool
	for _, msg := range model.messages {
		for _, it := range msg.iterations {
			if it.Iteration == 1 && len(it.Tools) == 1 && it.Tools[0].Name == "read" && it.Tools[0].Status == "error" {
				foundIter0 = true
			}
			if it.Iteration == 2 && len(it.Tools) == 1 && it.Tools[0].Name == "edit" && it.Tools[0].Status == "done" {
				foundIter1 = true
			}
		}
	}
	if !foundIter0 {
		t.Error("Expected error tool 'read' in iteration 1 of summary")
	}
	if !foundIter1 {
		t.Error("Expected success tool 'edit' in iteration 2 of summary")
	}
}

// Out-of-order CompletedTools: even if the payload contains tools from
// multiple iterations (simulating event timing anomalies), tools should
// be correctly grouped by their Iteration field.
func TestCrossIterationToolsFiltered(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0 with tool from iter 0
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1},
		},
	})
	// Iter 1 payload that accidentally includes a tool from iter 0 (stale)
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1}, // stale
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 2},
		},
	}, protocol.ProgressEvent{Iteration: 1, Content: "A", CompletedTools: []protocol.ToolProgress{{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1}}})

	sendDone(model, "Done")

	// Summary should have exactly 2 tools (Read in iter 1, Edit in iter 2)
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}

	// Verify iteration attribution (in assistant messages, not tool_summary)
	for _, msg := range model.messages {
		if msg.role == "assistant" && len(msg.iterations) > 0 {
			for _, it := range msg.iterations {
				if it.Iteration == 1 {
					if len(it.Tools) != 1 || it.Tools[0].Name != "read" {
						t.Errorf("Iter 1 should have 1 'read' tool, got %+v", it.Tools)
					}
				}
				if it.Iteration == 2 {
					if len(it.Tools) != 1 || it.Tools[0].Name != "edit" {
						t.Errorf("Iter 2 should have 1 'edit' tool, got %+v", it.Tools)
					}
				}
			}
		}
	}
}

// ==================== Background Task Injection ====================

func TestBgTaskInjectedUserMessage_ShowsAsUserMessage(t *testing.T) {
	model := initTestModel()

	content := "[System Notification] Background task abc123 completed.\nCommand: sleep 30\nStatus: done | Elapsed: 30s\nExit Code: 0\n\nOutput:\nok"

	// Simulate InjectUserMessage
	model.Update(cliInjectedUserMsg{content: content})

	// Should have exactly 1 message with role "user"
	userMsgCount := 0
	for _, msg := range model.messages {
		if msg.role == "user" {
			userMsgCount++
			if !strings.Contains(msg.content, "abc123") {
				t.Error("user message should contain task ID")
			}
		}
	}
	if userMsgCount != 1 {
		t.Errorf("expected 1 user message, got %d", userMsgCount)
	}
}

func TestBgTaskInjectedUserMessage_StartsSpinner(t *testing.T) {
	model := initTestModel()

	// Before injection, not typing
	if model.typing {
		t.Error("should not be typing initially")
	}

	_, cmd := model.Update(cliInjectedUserMsg{content: "bg task done"})

	// After injection, should be typing and re-arm fast tick chain.
	// This prevents spinner/elapsed timers from freezing when a bg task
	// completion arrives while the UI was idle.
	if cmd == nil {
		t.Fatal("expected injected bg-task message to schedule follow-up commands (tick/toast)")
	}
	if !model.typing {
		t.Error("should be typing after bg injection")
	}
	if model.inputReady {
		t.Error("input should not be ready during processing")
	}
}

func TestBgTaskInjectedUserMessage_RefreshesBgCount(t *testing.T) {
	model := initTestModel()

	callCount := 0
	model.bgTaskCountFn = func() int {
		callCount++
		return 2
	}

	model.Update(cliInjectedUserMsg{content: "bg task done"})

	// Should have called bgTaskCountFn
	if callCount != 1 {
		t.Errorf("bgTaskCountFn should be called once, got %d", callCount)
	}
	if model.bgTaskCount != 2 {
		t.Errorf("bgTaskCount should be 2, got %d", model.bgTaskCount)
	}
}

// TestBgTaskInjection_QueuedWhileTyping_PreservesReply verifies the race
// condition fix: when a bg task notification arrives WHILE the current turn
// is still in progress (typing=true), the notification must be queued — NOT
// start a new turn. Starting a new turn would increment agentTurnID, causing
// the pending reply to be treated as stale and dropped, losing all iterations.
func TestBgTaskInjection_QueuedWhileTyping_PreservesReply(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	turn1 := model.agentTurnID

	// Simulate some iterations
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 0, Tools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build", Status: "done", Elapsed: 100, Iteration: 0},
		}},
	}
	model.progressState.lastIter = 0

	// ── BG notification arrives WHILE typing (reply hasn't arrived) ──
	model.Update(cliInjectedUserMsg{content: "[System Notification] bg task done"})

	// Must NOT have started a new turn
	if model.agentTurnID != turn1 {
		t.Fatalf("agentTurnID should NOT change: expected %d, got %d — "+
			"bg notification started a new turn, reply will be lost", turn1, model.agentTurnID)
	}
	if !model.typing {
		t.Error("typing should still be true — bg notification interrupted the turn")
	}

	// Notification should be queued, not rendered as a user message
	if len(model.messageQueue) != 1 {
		t.Fatalf("expected 1 queued message, got %d", len(model.messageQueue))
	}
	userCount := 0
	for _, msg := range model.messages {
		if msg.role == "user" {
			userCount++
		}
	}
	if userCount != 0 {
		t.Errorf("expected 0 user messages (notification should be queued, not rendered), got %d", userCount)
	}

	// ── Now PhaseDone + reply arrive ──
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 0,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build", Status: "done", Elapsed: 100, Iteration: 0},
		},
	})
	sendDone(model, "build succeeded")

	// Reply must be preserved — find the assistant message
	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == turn1 {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant reply for turn 1 was LOST — this is the regression bug")
	}
	if assistantMsg.content != "build succeeded" {
		t.Errorf("assistant content mismatch: got %q", assistantMsg.content)
	}
	// Iterations must be preserved
	if len(assistantMsg.iterations) != 1 {
		t.Errorf("expected 1 iteration preserved, got %d", len(assistantMsg.iterations))
	}
}

// TestBgTaskInjection_QueuedInDeadWindow_PreservesReply verifies the race
// condition in the "dead window": PhaseDone has arrived (typing=false,
// doneProcessed=true) but the reply hasn't been processed yet
// (replyReceived=false). This is the exact window where the agent's
// chatProcessLoop calls drainAndProcessNotifications after sendMessage but
// before the async reply reaches the TUI.
func TestBgTaskInjection_QueuedInDeadWindow_PreservesReply(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	turn1 := model.agentTurnID

	// Simulate iteration
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 0, Tools: []protocol.ToolProgress{
			{Name: "Shell", Label: "ls", Status: "done", Elapsed: 50, Iteration: 0},
		}},
	}
	model.progressState.lastIter = 0

	// PhaseDone arrives — sets doneProcessed=true, typing=false
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 0,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "ls", Status: "done", Elapsed: 50, Iteration: 0},
		},
	})

	// Verify dead window: typing=false after PhaseDone
	if model.typing {
		t.Error("typing should be false after PhaseDone")
	}

	// ── BG notification arrives in the dead window ──
	model.Update(cliInjectedUserMsg{content: "[System Notification] bg task done"})

	// Must NOT have started a new turn
	if model.agentTurnID != turn1 {
		t.Fatalf("agentTurnID changed in dead window: expected %d, got %d — "+
			"reply will be lost when it arrives", turn1, model.agentTurnID)
	}

	// Notification should be queued
	if len(model.messageQueue) != 1 {
		t.Fatalf("expected 1 queued message in dead window, got %d", len(model.messageQueue))
	}

	// ── Reply arrives ──
	sendDone(model, "ls output")

	// Reply must be preserved
	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == turn1 {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant reply was LOST in dead window — this is the regression bug")
	}
	if assistantMsg.content != "ls output" {
		t.Errorf("assistant content: got %q, want %q", assistantMsg.content, "ls output")
	}
}

// TestBgTaskInjection_FlushedAfterReply starts a new turn correctly verifies
// that after the reply is received and the queued message is flushed, the
// notification does start a new turn with correct content.
func TestBgTaskInjection_FlushedAfterReply_StartsNewTurn(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// PhaseDone + reply complete the first turn
	sendProgress(model, &protocol.ProgressEvent{Phase: "done", Iteration: 0})

	// Inject bg notification WHILE typing (will be queued)
	model.Update(cliInjectedUserMsg{content: "bg result: ok"})
	if len(model.messageQueue) != 1 {
		t.Fatalf("expected 1 queued, got %d", len(model.messageQueue))
	}

	// Reply arrives — triggers tryFlushMessageQueue
	sendDone(model, "A1")
	turn1Done := model.agentTurnID

	// The flush should NOT start a new turn immediately within handleAgentMessage.
	// tryFlushMessageQueue arms the tick handler to drain on the next tick.
	// Simulate the tick
	model.needFlushQueue = true
	model.typing = false // ensure idle for flush

	// Simulate tick flush
	if model.needFlushQueue && len(model.messageQueue) > 0 && !model.typing {
		queued := model.messageQueue[0]
		model.messageQueue = model.messageQueue[1:]
		model.messages = append(model.messages, cliMessage{
			role:      "user",
			content:   queued.content,
			timestamp: time.Now(),
			dirty:     true,
		})
		model.startAgentTurn()
	}

	// New turn should have started
	if model.agentTurnID != turn1Done+1 {
		t.Errorf("expected agentTurnID=%d (turn1+1), got %d", turn1Done+1, model.agentTurnID)
	}
	// The queued bg notification should now be a user message
	foundBg := false
	for _, msg := range model.messages {
		if msg.role == "user" && strings.Contains(msg.content, "bg result") {
			foundBg = true
		}
	}
	if !foundBg {
		t.Error("queued bg notification should be flushed as a user message")
	}
}

func TestBgDrainCompletedTool_AppearsInIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: normal tool + bg drain tool in same iteration
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read file", Status: "done", Elapsed: 100, Iteration: 1},
			{Name: "background_task_result", Label: "bg:abc123", Status: "done", Elapsed: 30000, Iteration: 1},
		},
	})

	// Final done — snapshot into summary
	sendDone(model, "all done")

	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("expected 2 tools in summary, got %d", tools)
	}
}

func TestBgDrainCrossIterationDoesNotLeak(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: bg tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "background_task_result", Label: "bg:old", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})

	// Iter 1: bg tool
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "background_task_result", Label: "bg:new", Status: "done", Elapsed: 2000, Iteration: 2},
		},
	}, protocol.ProgressEvent{Iteration: 1, Content: "working", CompletedTools: []protocol.ToolProgress{{Name: "background_task_result", Label: "bg:old", Status: "done", Elapsed: 1000, Iteration: 1}}})

	// Final done — snapshot both iterations into summary
	sendDone(model, "done")

	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("expected 2 tools in summary (one per iteration), got %d", tools)
	}
}

// TestAgentSession_PhaseDone_PreservesIterations verifies that when a SubAgent
// session finishes (PhaseDone), the synthetic assistant message preserves all
// intermediate iteration history — same as the main agent session.
//
// Regression test for: after SubAgent generates final reply, all intermediate
// iteration process disappears from the TUI viewport.
func TestAgentSession_PhaseDone_PreservesIterations(t *testing.T) {
	model := initTestModel()
	model.channelName = "agent"
	model.chatID = "agent:explore/debug-1"
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Simulate 3 iterations of tool execution
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "investigating...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "uname -a", Status: "done", Elapsed: 100, Iteration: 1},
		},
	})
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "checking logs...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Grep", Label: "error", Status: "done", Elapsed: 200, Iteration: 2},
		},
	}, protocol.ProgressEvent{Iteration: 1, Content: "investigating...", CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "uname -a", Status: "done", Elapsed: 100, Iteration: 1}}})
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 3, Content: "reading code...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "main.go", Status: "done", Elapsed: 300, Iteration: 3},
		},
	},
		protocol.ProgressEvent{Iteration: 1, Content: "investigating...", CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "uname -a", Status: "done", Elapsed: 100, Iteration: 1}}},
		protocol.ProgressEvent{Iteration: 2, Content: "checking logs...", CompletedTools: []protocol.ToolProgress{{Name: "Grep", Label: "error", Status: "done", Elapsed: 200, Iteration: 2}}})

	// Send the final PhaseDone with the assistant reply
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 3,
		Content:   "Bug found: the crash is caused by a null pointer dereference.",
	})

	// After PhaseDone, the synthetic assistant message should have iterations
	if tools := countToolsInSummary(model); tools != 3 {
		t.Errorf("expected 3 tools preserved in iterations after PhaseDone, got %d", tools)
	}

	// Verify the assistant message was created with content
	foundAssistant := false
	for _, msg := range model.messages {
		if msg.role == "assistant" && !msg.isPartial {
			foundAssistant = true
			if len(msg.iterations) == 0 {
				t.Error("assistant message has no iterations after PhaseDone")
			}
			if !strings.Contains(msg.content, "null pointer dereference") {
				t.Errorf("assistant content missing expected text: %q", msg.content)
			}
			break
		}
	}
	if !foundAssistant {
		t.Error("no completed assistant message found after PhaseDone")
	}
}

// TestAgentSession_MultipleSubAgents_DistinctToolEntries verifies that when a
// parent agent spawns multiple SubAgent tool calls with different instances,
// all tool progress entries are preserved (not deduplicated).
func TestAgentSession_MultipleSubAgents_DistinctToolEntries(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Simulate 3 parallel SubAgent tool calls — each in a separate iteration
	// so snapshotIterationChange properly captures all three.
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Content: "launching..."})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Content: "launching...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "SubAgent", Label: "SubAgent [explore/debug-1]: Investigate crash", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})
	sendProgressWithHistory(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Content: "waiting on others..."},
		protocol.ProgressEvent{Iteration: 1, Content: "launching...", CompletedTools: []protocol.ToolProgress{{Name: "SubAgent", Label: "SubAgent [explore/debug-1]: Investigate crash", Status: "done", Elapsed: 1000, Iteration: 1}}})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "waiting on others...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "SubAgent", Label: "SubAgent [explore/debug-2]: Investigate crash", Status: "done", Elapsed: 1200, Iteration: 2},
		},
	})
	sendProgressWithHistory(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 3, Content: "almost done..."},
		protocol.ProgressEvent{Iteration: 1, Content: "launching...", CompletedTools: []protocol.ToolProgress{{Name: "SubAgent", Label: "SubAgent [explore/debug-1]: Investigate crash", Status: "done", Elapsed: 1000, Iteration: 1}}},
		protocol.ProgressEvent{Iteration: 2, Content: "waiting on others...", CompletedTools: []protocol.ToolProgress{{Name: "SubAgent", Label: "SubAgent [explore/debug-2]: Investigate crash", Status: "done", Elapsed: 1200, Iteration: 2}}})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 3, Content: "almost done...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "SubAgent", Label: "SubAgent [explore/debug-3]: Investigate crash", Status: "done", Elapsed: 900, Iteration: 3},
		},
	})

	sendDone(model, "all three subagents completed, analyzing results...")

	if tools := countToolsInSummary(model); tools != 3 {
		t.Errorf("expected 3 SubAgent tools in summary, got %d (dedup bug)", tools)
	}
}

// =============================================================================
// Performance regression tests: O(1) complexity guards
// =============================================================================

// TestUpdateStreamingOnly_IncrementalAppend verifies that updateStreamingOnly
// uses the incremental path when iteration count increases but width is unchanged:
// only NEW iterations are rendered (O(1) per new iteration), old completedLines
// are preserved. Full rebuild only happens on width change or count reset.
func TestUpdateStreamingOnly_IncrementalAppend(t *testing.T) {
	setupModel := func() *cliModel {
		model := initTestModel()
		model.ticker.frame = 0
		model.rc.valid = true
		model.messages = append(model.messages, cliMessage{
			role:      "assistant",
			turnID:    1,
			timestamp: time.Now(),
			isPartial: true,
		})
		model.streamingMsgIdx = 0
		model.typing = true
		return model
	}

	t.Run("incremental-preserves-old-lines", func(t *testing.T) {
		model := setupModel()

		// Seed with 2 iterations, force full rebuild via width mismatch
		model.progressState.iterations = []cliIterationSnapshot{
			{Iteration: 1, Content: "first-iter-thinking"},
			{Iteration: 2, Content: "second-iter-thinking"},
		}
		model.rc.streamCompletedWidth = -1
		model.updateStreamingOnly()

		if model.rc.streamCompletedCount != 2 {
			t.Fatalf("expected count=2 after seed, got %d", model.rc.streamCompletedCount)
		}
		oldLines := make([]string, len(model.rc.streamCompletedLines))
		copy(oldLines, model.rc.streamCompletedLines)
		oldMaxW := model.rc.streamMaxW

		// Add a 3rd iteration — should trigger INCREMENTAL path (same width)
		model.progressState.iterations = append(model.progressState.iterations,
			cliIterationSnapshot{Iteration: 3, Content: "third-iter-thinking"})

		model.updateStreamingOnly()

		// Assert count increased
		if model.rc.streamCompletedCount != 3 {
			t.Fatalf("expected count=3 after increment, got %d", model.rc.streamCompletedCount)
		}

		// Assert old lines are preserved verbatim (prefix of new lines)
		newLines := model.rc.streamCompletedLines
		if len(newLines) <= len(oldLines) {
			t.Fatalf("newLines(%d) should be longer than oldLines(%d)", len(newLines), len(oldLines))
		}
		for i := range oldLines {
			if newLines[i] != oldLines[i] {
				t.Errorf("line %d changed: old=%q new=%q — old completedLines should be preserved", i, oldLines[i], newLines[i])
			}
		}

		// Assert new iteration marker appears ONLY in appended portion
		oldText := strings.Join(oldLines, "\n")
		appendedText := strings.Join(newLines[len(oldLines):], "\n")

		if !strings.Contains(appendedText, "third-iter-thinking") {
			t.Error("third-iter-thinking not found in appended portion")
		}
		if strings.Contains(oldText, "third-iter-thinking") {
			t.Error("third-iter-thinking leaked into old portion — should only be in appended area")
		}

		// Assert max width is maintained/updated correctly
		if model.rc.streamMaxW < oldMaxW {
			t.Errorf("maxW decreased from %d to %d", oldMaxW, model.rc.streamMaxW)
		}

		t.Logf("O(1) confirmed: %d lines preserved, %d new lines appended", len(oldLines), len(newLines)-len(oldLines))
	})

	t.Run("width-change-triggers-full-rebuild", func(t *testing.T) {
		model := setupModel()

		model.progressState.iterations = []cliIterationSnapshot{
			{Iteration: 1, Content: "iter-one"},
			{Iteration: 2, Content: "iter-two"},
		}
		// First call at width W1
		model.rc.streamCompletedWidth = -1
		model.updateStreamingOnly()
		firstWidth := model.rc.streamCompletedWidth

		// Add iteration + change cached width → should trigger full rebuild
		model.progressState.iterations = append(model.progressState.iterations,
			cliIterationSnapshot{Iteration: 3, Content: "iter-three"})
		model.rc.streamCompletedWidth = 999 // mismatched → forces full rebuild

		model.updateStreamingOnly()

		if model.rc.streamCompletedCount != 3 {
			t.Errorf("expected count=3 after width change rebuild, got %d", model.rc.streamCompletedCount)
		}
		// Full rebuild resets width to actual contentWidth, not the stale 999.
		// Width 999 triggered the full rebuild branch; the result has real width.
		if model.rc.streamCompletedWidth != firstWidth {
			t.Errorf("streamCompletedWidth should be %d (contentWidth) after rebuild, got %d", firstWidth, model.rc.streamCompletedWidth)
		}
		t.Logf("Full rebuild confirmed: width reset from 999 → %d, all %d iterations re-rendered", model.rc.streamCompletedWidth, model.rc.streamCompletedCount)
	})
	t.Run("cross-kind-separator-single-blank-line", func(t *testing.T) {
		model := setupModel()

		// Seed: iteration 1 ends with tools, iteration 2 is reasoning only.
		// Different block kinds (tools → reasoning) should produce exactly one
		// blank guide line as separator between old and new iteration groups.
		// PR #181: \n\n for different kinds → produces one blank guide line.
		model.progressState.iterations = []cliIterationSnapshot{
			{Iteration: 1, Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "read file", Status: "done", Elapsed: 100, Iteration: 1},
			}},
		}
		model.rc.streamCompletedWidth = -1
		model.updateStreamingOnly()

		if model.rc.streamCompletedCount != 1 {
			t.Fatalf("expected count=1 after seed, got %d", model.rc.streamCompletedCount)
		}
		oldLines := make([]string, len(model.rc.streamCompletedLines))
		copy(oldLines, model.rc.streamCompletedLines)

		// Add iteration 2: Reasoning block (different kind from tools).
		model.progressState.iterations = append(model.progressState.iterations,
			cliIterationSnapshot{Iteration: 2, Reasoning: "cross-kind reasoning text"})

		model.updateStreamingOnly()

		if model.rc.streamCompletedCount != 2 {
			t.Fatalf("expected count=2 after increment, got %d", model.rc.streamCompletedCount)
		}

		// Old lines preserved
		newLines := model.rc.streamCompletedLines
		for i := range oldLines {
			if newLines[i] != oldLines[i] {
				t.Errorf("line %d changed: old=%q new=%q", i, oldLines[i], newLines[i])
			}
		}

		// The appended portion should contain exactly one blank guide line
		// (separator) before the reasoning content.
		appended := newLines[len(oldLines):]
		if len(appended) < 2 {
			t.Fatalf("expected at least 2 appended lines (separator + content), got %d", len(appended))
		}
		// First appended line should be a blank guide line (just the guide symbol,
		// no content). Strip ANSI then check that only "┊ " remains.
		guideSym := "┊"
		firstStripped := strings.TrimRight(stripAnsi(appended[0]), " ")
		if firstStripped != guideSym {
			t.Errorf("first appended line should be blank guide (just %q), got %q", guideSym, firstStripped)
		}
		// Reasoning marker should appear somewhere in appended content
		appendedText := strings.Join(appended, "\n")
		if !strings.Contains(stripAnsi(appendedText), "cross-kind reasoning text") {
			t.Errorf("appended portion should contain reasoning marker, got:\n%s", stripAnsi(appendedText))
		}
		// Verify exactly one blank guide line (PR #181: \n\n for different kinds
		// produces one blank line after splitting)
		blankCount := 0
		for _, l := range appended {
			s := strings.TrimRight(stripAnsi(l), " ")
			if s == guideSym {
				blankCount++
			}
		}
		if blankCount != 1 {
			t.Errorf("expected exactly 1 blank guide line as separator (unified to 1 line), got %d", blankCount)
		}

		t.Logf("Cross-kind separator correct: 1 blank guide line between tools→reasoning")
	})
}
func TestSyncProgressTodos_SameCountPreservesCache(t *testing.T) {
	model := initTestModel()
	model.rc.valid = true

	// Initial todos: 3 items, none done
	model.todos = []protocol.TodoItem{
		{ID: 1, Text: "task-a", Done: false},
		{ID: 2, Text: "task-b", Done: false},
		{ID: 3, Text: "task-c", Done: false},
	}

	// Same count, different content: item 2 marked done
	payload := &protocol.ProgressEvent{
		Todos: []protocol.TodoItem{
			{ID: 1, Text: "task-a", Done: false},
			{ID: 2, Text: "task-b", Done: true}, // changed
			{ID: 3, Text: "task-c", Done: false},
		},
	}

	model.syncProgressTodos(payload)

	// O(1) fix: rc.valid must remain true — no fullRebuild triggered
	if !model.rc.valid {
		t.Error("P3 regression: syncProgressTodos with same-count todos set rc.valid=false — fullRebuild would be triggered on next updateViewportContent")
	} else {
		t.Log("P3 fixed: rc.valid preserved (O(1)) after same-count todo change")
	}

	// Verify todos were still updated correctly
	if len(model.todos) != 3 {
		t.Errorf("expected 3 todos, got %d", len(model.todos))
	}
	if !model.todos[1].Done {
		t.Error("todo item 2 should be marked done")
	}
	if model.todos[0].Done {
		t.Error("todo item 1 should still be not done")
	}
}

// TestUpdateStreamingOnly_LiveOnlyRendersCurrentIteration pins down the P1
// behavior: the live (per-tick) rendering path only uses m.progressState.current
// for the dynamic part. It does NOT iterate over m.progressState.iterations.
// Completed iterations come from the streamCompletedLines cache.
func TestUpdateStreamingOnly_LiveOnlyRendersCurrentIteration(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0
	model.rc.valid = true

	// Set up a streaming message
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		turnID:    1,
		timestamp: time.Now(),
		isPartial: true,
	})
	model.streamingMsgIdx = 0
	model.typing = true

	// Completed iterations (should land in streamCompletedLines cache)
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Content: "completed-thinking-iter1"},
		{Iteration: 2, Content: "completed-thinking-iter2"},
	}

	// Live iteration (should be rendered fresh every tick, NOT from cache)
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:              3,
		StreamContent:          "live-stream-content",
		ReasoningStreamContent: "live-reasoning-stream",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "live-running-tool", Status: "running", Elapsed: 500},
		},
	}

	// First call: populate cache
	model.rc.streamCompletedWidth = -1 // force cache miss
	model.updateStreamingOnly()

	// Verify completed cache is populated
	if model.rc.streamCompletedCount != 2 {
		t.Fatalf("expected streamCompletedCount=2, got %d", model.rc.streamCompletedCount)
	}

	// Verify streamCompletedLines contain ONLY completed iteration content
	completedText := strings.Join(model.rc.streamCompletedLines, "\n")
	if !strings.Contains(completedText, "completed-thinking-iter1") {
		t.Error("streamCompletedLines missing iter1 content")
	}
	if !strings.Contains(completedText, "completed-thinking-iter2") {
		t.Error("streamCompletedLines missing iter2 content")
	}
	// Live content must NOT leak into completed cache
	if strings.Contains(completedText, "live-stream-content") {
		t.Error("P1 violation: live-stream-content leaked into streamCompletedLines cache")
	}
	if strings.Contains(completedText, "live-running-tool") {
		t.Error("P1 violation: live tool leaked into streamCompletedLines cache")
	}

	// Second call with cache hit: verify completed cache is reused, not re-rendered
	beforeLen := len(model.rc.streamCompletedLines)
	model.updateStreamingOnly()
	if model.rc.streamCompletedCount != 2 {
		t.Error("completed count changed unexpectedly on cache hit")
	}
	if len(model.rc.streamCompletedLines) != beforeLen {
		t.Error("streamCompletedLines were re-rendered on cache hit — should have been reused")
	}
}

// TestIncrementalPathSeparatorMatchesFullRebuild verifies that the
// incremental rendering path in updateStreamingOnly produces the SAME
// number of blank guide lines between iteration groups as the full
// rebuild path. Regression: the incremental path prepended \n\n to
// bodyContent before Split, producing 2 blank guide lines, while
// appendTurnBlock's \n\n between blocks produces just 1.
func TestIncrementalPathSeparatorMatchesFullRebuild(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0
	model.typing = true
	model.streamingMsgIdx = 0
	// Set up a streaming message so updateStreamingOnly works
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		turnID:    1,
		timestamp: time.Now(),
		isPartial: true,
	})
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:              3,
		ReasoningStreamContent: "live reasoning",
	}

	// Test for each block kind transition
	tests := []struct {
		name    string
		iter1   cliIterationSnapshot
		iter2   cliIterationSnapshot
		wantSep int // expected blank guide lines between iter1 and iter2
	}{
		{
			name: "tools→reasoning",
			iter1: cliIterationSnapshot{Iteration: 1, Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "done tool", Status: "done", Elapsed: 100, Iteration: 1},
			}},
			iter2:   cliIterationSnapshot{Iteration: 2, Reasoning: "next reasoning"},
			wantSep: 1,
		},
		{
			name:    "reasoning→reasoning",
			iter1:   cliIterationSnapshot{Iteration: 1, Reasoning: "first reasoning"},
			iter2:   cliIterationSnapshot{Iteration: 2, Reasoning: "second reasoning"},
			wantSep: 0,
		},
		{
			name:    "content→reasoning",
			iter1:   cliIterationSnapshot{Iteration: 1, Content: "thinking text"},
			iter2:   cliIterationSnapshot{Iteration: 2, Reasoning: "next reasoning"},
			wantSep: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// --- Full rebuild path ---
			model.progressState.iterations = []cliIterationSnapshot{tt.iter1, tt.iter2}
			model.rc.streamCompletedWidth = -1 // force full rebuild
			model.rc.streamCompletedCount = 0
			model.updateStreamingOnly()
			fullLines := make([]string, len(model.rc.streamCompletedLines))
			copy(fullLines, model.rc.streamCompletedLines)

			// --- Incremental path ---
			model.progressState.iterations = []cliIterationSnapshot{tt.iter1}
			model.rc.streamCompletedWidth = -1
			model.rc.streamCompletedCount = 0
			model.updateStreamingOnly() // first render
			// Now add second iteration → incremental
			model.progressState.iterations = []cliIterationSnapshot{tt.iter1, tt.iter2}
			model.updateStreamingOnly() // incremental render

			incrLines := model.rc.streamCompletedLines

			// Compare: both paths should produce identical line sets
			if len(fullLines) != len(incrLines) {
				t.Errorf("line count mismatch: full=%d incremental=%d", len(fullLines), len(incrLines))
				return
			}
			for i := range fullLines {
				if fullLines[i] != incrLines[i] {
					t.Errorf("line %d mismatch:\n  full: %q\n  incr: %q", i, fullLines[i], incrLines[i])
				}
			}
		})
	}
}

// TestHistoryReloadForceFullRebuildDBAssistantBecomesStreamingTarget verifies
// that on forceFullRebuild (compression), the DB assistant is found and marked
// as the streaming target — no separate streaming message created.
func TestHistoryReloadForceFullRebuildDBAssistantBecomesStreamingTarget(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// forceFullRebuild (compression path): messages cleared, no streaming message
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "current user", Timestamp: time.Now()},
			{Role: "assistant", Content: "partial response from DB", Timestamp: time.Now()},
		},
	})

	// Exactly ONE assistant — by design, not by dedup
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected exactly 1 assistant (by design), got %d", assistantCount)
	}
	if model.streamingMsgIdx < 0 || model.streamingMsgIdx >= len(model.messages) {
		t.Fatalf("streamingMsgIdx out of range: %d (messages=%d)", model.streamingMsgIdx, len(model.messages))
	}
	streaming := model.messages[model.streamingMsgIdx]
	if !streaming.isPartial {
		t.Fatal("DB assistant should be marked as streaming target (isPartial=true)")
	}
	if streaming.content != "partial response from DB" {
		t.Fatalf("streaming target should retain DB content, got %q", streaming.content)
	}
}

// TestHistoryReloadForceFullRebuildNoAssistantCreatesOne verifies the edge case
// where DB history has no assistant (compression before first iteration persisted).
// This is the ONLY path that creates a streaming assistant during compression reload.
func TestHistoryReloadForceFullRebuildNoAssistantCreatesOne(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "current user", Timestamp: time.Now()},
		},
	})

	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected exactly 1 assistant (edge case creation), got %d", assistantCount)
	}
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should point to the created assistant")
	}
	streaming := model.messages[model.streamingMsgIdx]
	if !streaming.isPartial {
		t.Fatal("created streaming assistant should have isPartial=true")
	}
}

// TestNonCLI_Session_NoDuplicationAfterPhaseDone verifies that when viewing
// a non-CLI session (e.g., feishu) via /su, the final assistant content is
// NOT rendered twice after PhaseDone.
//
// Root cause: endAgentTurn preserves progressState.current (for flicker-free
// rendering). updateStreamingOnly renders BOTH completed iterations (which
// include the final iteration with Content + Reasoning) AND liveLines from
// progressState.current (same Content + Reasoning). For CLI sessions,
// handleAgentMessage clears streamingMsgIdx shortly after, fixing the
// duplication. For non-CLI sessions, handleAgentMessage never arrives →
// streamingMsgIdx stays valid → updateStreamingOnly runs every tick →
// persistent duplication.
//
// Fix: skip liveLines rendering when !m.typing (turn ended).
func TestNonCLI_Session_NoDuplicationAfterPhaseDone(t *testing.T) {
	model := initTestModel()
	model.channelName = "feishu"
	model.chatID = "feishu:ou_test"

	// Add a user message before the streaming message so histLines is populated
	model.messages = append(model.messages, cliMessage{
		role:      "user",
		content:   "你好",
		timestamp: time.Now(),
		dirty:     true,
	})
	// Create the streaming assistant message
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		turnID:    1,
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
	})
	model.streamingMsgIdx = 1
	model.agentTurnID = 1

	// Populate histLines via fullRebuild (renders user message into cache)
	model.fullRebuild()
	if len(model.rc.histLines) == 0 {
		t.Fatal("histLines should be populated after fullRebuild")
	}

	finalContent := "你好！定时巡检任务已经全部清除了。有什么需要帮忙的吗？"
	finalReasoning := "All cron jobs have been removed. Let me respond."

	// Simulate the post-PhaseDone state directly:
	// 1. progressState.iterations has the final iteration with Content + Reasoning
	//    (snapshotted by finalizeTurnFromSnapshot)
	// 2. progressState.current still has the same Content + Reasoning
	//    (preserved by endAgentTurn, NOT cleared)
	// 3. typing = false (turn ended)
	// 4. streamingMsgIdx still valid (non-CLI session, handleAgentMessage never arrives)
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Tools: []protocol.ToolProgress{
				{Name: "Cron", Label: "Cron: remove", Status: "done", Elapsed: 542, Iteration: 1},
			},
		},
		{
			Iteration: 2,
			Tools: []protocol.ToolProgress{
				{Name: "Cron", Label: "Cron: list", Status: "done", Elapsed: 607, Iteration: 2},
			},
		},
		{
			Iteration: 3,
			Content:   finalContent,
			Reasoning: finalReasoning,
		},
	}
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 3,
		Content:   finalContent,
		Reasoning: finalReasoning,
	}
	// Bake iterations into the streaming message (as finalizeTurnFromSnapshot does)
	baked := make([]cliIterationSnapshot, len(model.progressState.iterations))
	copy(baked, model.progressState.iterations)
	model.messages[model.streamingMsgIdx].iterations = baked
	model.messages[model.streamingMsgIdx].content = finalContent
	model.messages[model.streamingMsgIdx].dirty = true

	// Simulate post-endAgentTurn state: typing=false, streamingMsgIdx preserved
	model.typing = false
	// rc.valid is true from fullRebuild; streamingMsgIdx >= 0
	// → updateViewportContent will use updateStreamingOnly fast path

	// Simulate a tick rendering
	model.updateStreamingOnly()

	vpContent := stripANSI(model.viewport.View())

	// Count occurrences of the final content
	count := strings.Count(vpContent, finalContent)
	if count > 1 {
		t.Errorf("final content appears %d times in viewport (expected 1).\nViewport:\n%s",
			count, vpContent)
	}

	// Also verify reasoning isn't duplicated
	reasoningCount := strings.Count(vpContent, finalReasoning)
	if reasoningCount > 1 {
		t.Errorf("reasoning appears %d times in viewport (expected 1).\nViewport:\n%s",
			reasoningCount, vpContent)
	}
}

// TestCLI_Session_NoDuplicationAfterPhaseDone verifies the same fix doesn't
// break CLI sessions: after PhaseDone, the streaming message should show
// completed iterations without live duplication.
func TestCLI_Session_NoDuplicationAfterPhaseDone(t *testing.T) {
	model := initTestModel()

	// Add a user message before the streaming message
	model.messages = append(model.messages, cliMessage{
		role:      "user",
		content:   "run echo hi",
		timestamp: time.Now(),
		dirty:     true,
	})
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		turnID:    1,
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
	})
	model.streamingMsgIdx = 1
	model.agentTurnID = 1
	model.fullRebuild()

	finalContent := "Done! The output is: hi"
	finalReasoning := "The command succeeded."

	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Tools: []protocol.ToolProgress{
				{Name: "Shell", Label: "echo hi", Status: "done", Elapsed: 100, Iteration: 1},
			},
		},
		{
			Iteration: 2,
			Content:   finalContent,
			Reasoning: finalReasoning,
		},
	}
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		Content:   finalContent,
		Reasoning: finalReasoning,
	}
	baked := make([]cliIterationSnapshot, len(model.progressState.iterations))
	copy(baked, model.progressState.iterations)
	model.messages[model.streamingMsgIdx].iterations = baked
	model.messages[model.streamingMsgIdx].content = finalContent
	model.messages[model.streamingMsgIdx].dirty = true
	model.typing = false

	model.updateStreamingOnly()

	vpContent := stripANSI(model.viewport.View())

	count := strings.Count(vpContent, finalContent)
	if count > 1 {
		t.Errorf("final content appears %d times in viewport (expected 1).\nViewport:\n%s",
			count, vpContent)
	}
}

// TestPostRestoreSessionSetup_PreservesPerSessionModel verifies that switching
// back to a session preserves the per-session model choice (e.g. set via Ctrl+N).
//
// Root cause: postRestoreSessionSetup had its own LLM state resolution logic
// (LoadSessionLLMState → GetDefault fallback) that bypassed the DB tenants table.
// In remote mode, local JSON is never written (skipBackendFields=true), so
// LoadSessionLLMState always returned zero → GetDefault("") returned the
// subscription's default Model (often ""), overwriting the correct per-session
// model that restoreSession() had just restored from savedSessions.
//
// Fix: postRestoreSessionSetup now calls refreshCachedModelName() which checks
// the DB tenants table (GetSessionSubscription RPC) first, then local JSON,
// then savedSessions, then GetDefault as last resort.
func TestPostRestoreSessionSetup_PreservesPerSessionModel(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true, Enabled: true},
		},
		defaultID: "sub1",
		// Simulate DB tenants table: default session has per-session model "glm-5.2"
		// (user switched via Ctrl+N, which calls SelectModel → writes to tenants table)
		sessionLLM: map[string]sessionLLMEntry{
			"/test": {subID: "sub1", model: "glm-5.2"},
		},
	}

	model := initTestModel()
	model.handleResize(80, 24)
	model.subscriptionMgr = mgr
	model.remoteMode = true
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		config: &CLIChannelConfig{
			RefreshValuesCache: func(string) {},
			GetCurrentValues:   func() map[string]string { return nil },
		},
	}

	// Simulate: restoreSession already restored the correct model from savedSessions
	model.cachedModelName = "glm-5.2"
	model.activeSubID = "sub1"

	// Act: postRestoreSessionSetup should NOT overwrite with GetDefault's Model ("glm-4")
	model.postRestoreSessionSetup()

	// Assert: per-session model is preserved (from DB, not GetDefault)
	if model.cachedModelName != "glm-5.2" {
		t.Errorf("cachedModelName = %q, want glm-5.2 (per-session model from DB)\n"+
			"GetDefault would have returned glm-4 (subscription default)", model.cachedModelName)
	}
	if model.activeSubID != "sub1" {
		t.Errorf("activeSubID = %q, want sub1", model.activeSubID)
	}
}

// TestPostRestoreSessionSetup_DefaultFallbackForNewSession verifies that a
// brand-new session (no DB entry, no local JSON, no savedSessions) falls back
// to GetDefault correctly.
func TestPostRestoreSessionSetup_DefaultFallbackForNewSession(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true, Enabled: true},
		},
		defaultID: "sub1",
		// No sessionLLM entry for this chatID — simulates a brand-new session
	}

	model := initTestModel()
	model.handleResize(80, 24)
	model.subscriptionMgr = mgr
	model.remoteMode = true
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		config: &CLIChannelConfig{
			RefreshValuesCache: func(string) {},
			GetCurrentValues:   func() map[string]string { return nil },
		},
	}

	// No saved state — cachedModelName and activeSubID are empty
	model.cachedModelName = ""
	model.activeSubID = ""

	model.postRestoreSessionSetup()

	// Assert: fell back to GetDefault
	if model.cachedModelName != "glm-4" {
		t.Errorf("cachedModelName = %q, want glm-4 (from GetDefault fallback)", model.cachedModelName)
	}
	if model.activeSubID != "sub1" {
		t.Errorf("activeSubID = %q, want sub1", model.activeSubID)
	}
}

// TestPostRestoreSessionSetup_DBOverridesSavedSessions verifies that when
// restoreSession() restored an old model from savedSessions, but the DB has
// a different (newer) per-session model, the DB value wins.
func TestPostRestoreSessionSetup_DBOverridesSavedSessions(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true, Enabled: true},
		},
		defaultID: "sub1",
		// DB has the latest per-session model
		sessionLLM: map[string]sessionLLMEntry{
			"/test": {subID: "sub1", model: "glm-5.2"},
		},
	}

	model := initTestModel()
	model.handleResize(80, 24)
	model.subscriptionMgr = mgr
	model.remoteMode = true
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		config: &CLIChannelConfig{
			RefreshValuesCache: func(string) {},
			GetCurrentValues:   func() map[string]string { return nil },
		},
	}

	// Simulate: restoreSession() restored a STALE model from savedSessions
	model.cachedModelName = "old-model"
	model.activeSubID = "sub1"

	model.postRestoreSessionSetup()

	// Assert: DB value wins over savedSessions
	if model.cachedModelName != "glm-5.2" {
		t.Errorf("cachedModelName = %q, want glm-5.2 (DB is authoritative over savedSessions)",
			model.cachedModelName)
	}
}

// TestEnsureSessionModelBinding_BalanceTier verifies that when a session has
// no model, ensureSessionModelBinding auto-binds to the Balance tier model.
func TestEnsureSessionModelBinding_BalanceTier(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Model: "glm-4", Enabled: true},
		},
		defaultID: "sub1",
	}
	subscriber := &mockLLMSubscriber{}
	settingsSvc := &testSettingsService{
		getResult: map[string]string{
			"tier_balance": "sub1|glm-5.2",
		},
	}

	model := initTestModel()
	model.subscriptionMgr = mgr
	model.llmSubscriber = subscriber
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		settingsSvc:     settingsSvc,
		config:          &CLIChannelConfig{},
	}
	model.cachedModelName = ""
	model.activeSubID = ""

	model.ensureSessionModelBinding()

	if model.cachedModelName != "glm-5.2" {
		t.Errorf("cachedModelName = %q, want glm-5.2 (from Balance tier)", model.cachedModelName)
	}
	if model.activeSubID != "sub1" {
		t.Errorf("activeSubID = %q, want sub1", model.activeSubID)
	}
	if len(subscriber.selectModelCalls) != 1 {
		t.Fatalf("expected 1 SelectModel call, got %d", len(subscriber.selectModelCalls))
	}
	call := subscriber.selectModelCalls[0]
	if call.subID != "sub1" || call.model != "glm-5.2" {
		t.Errorf("SelectModel called with (sub=%s, model=%s), want (sub1, glm-5.2)", call.subID, call.model)
	}
}

// TestEnsureSessionModelBinding_FallbackToDefault verifies that when Balance
// tier is not configured, ensureSessionModelBinding falls back to GetDefault.
func TestEnsureSessionModelBinding_FallbackToDefault(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Model: "glm-4", Enabled: true},
		},
		defaultID: "sub1",
	}
	subscriber := &mockLLMSubscriber{}
	settingsSvc := &testSettingsService{
		getResult: map[string]string{}, // no tier_balance configured
	}

	model := initTestModel()
	model.subscriptionMgr = mgr
	model.llmSubscriber = subscriber
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		settingsSvc:     settingsSvc,
		config:          &CLIChannelConfig{},
	}
	model.cachedModelName = ""
	model.activeSubID = ""

	model.ensureSessionModelBinding()

	if model.cachedModelName != "glm-4" {
		t.Errorf("cachedModelName = %q, want glm-4 (from GetDefault fallback)", model.cachedModelName)
	}
	if model.activeSubID != "sub1" {
		t.Errorf("activeSubID = %q, want sub1", model.activeSubID)
	}
	if len(subscriber.selectModelCalls) != 1 {
		t.Fatalf("expected 1 SelectModel call, got %d", len(subscriber.selectModelCalls))
	}
}

// TestEnsureSessionModelBinding_NoModelAvailable verifies that when neither
// Balance tier nor GetDefault has a model, no binding is created.
func TestEnsureSessionModelBinding_NoModelAvailable(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Model: "", Enabled: true}, // empty Model
		},
		defaultID: "sub1",
	}
	subscriber := &mockLLMSubscriber{}
	settingsSvc := &testSettingsService{
		getResult: map[string]string{}, // no tier_balance
	}

	model := initTestModel()
	model.subscriptionMgr = mgr
	model.llmSubscriber = subscriber
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		settingsSvc:     settingsSvc,
		config:          &CLIChannelConfig{},
	}
	model.cachedModelName = ""
	model.activeSubID = ""

	model.ensureSessionModelBinding()

	// No model available — should remain empty, no SelectModel call.
	if model.cachedModelName != "" {
		t.Errorf("cachedModelName = %q, want empty (no model available)", model.cachedModelName)
	}
	if len(subscriber.selectModelCalls) != 0 {
		t.Errorf("expected 0 SelectModel calls, got %d", len(subscriber.selectModelCalls))
	}
}

// TestPostRestoreSessionSetup_AutoBindsBalanceTier verifies that
// postRestoreSessionSetup auto-binds the Balance tier model for a new session.
func TestPostRestoreSessionSetup_AutoBindsBalanceTier(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Model: "glm-4", Enabled: true},
		},
		defaultID: "sub1",
		// No sessionLLM entry — new session with no binding
	}
	subscriber := &mockLLMSubscriber{}
	settingsSvc := &testSettingsService{
		getResult: map[string]string{
			"tier_balance": "sub1|glm-5.2",
		},
	}

	model := initTestModel()
	model.handleResize(80, 24)
	model.subscriptionMgr = mgr
	model.llmSubscriber = subscriber
	model.remoteMode = true
	model.channel = &CLIChannel{
		subscriptionMgr: mgr,
		settingsSvc:     settingsSvc,
		config: &CLIChannelConfig{
			RefreshValuesCache: func(string) {},
			GetCurrentValues:   func() map[string]string { return nil },
		},
	}
	// Simulate a brand-new session: no saved state, no model
	model.cachedModelName = ""
	model.activeSubID = ""

	model.postRestoreSessionSetup()

	// After postRestoreSessionSetup, the session should be auto-bound to Balance tier
	if model.cachedModelName != "glm-5.2" {
		t.Errorf("cachedModelName = %q, want glm-5.2 (auto-bound from Balance tier)", model.cachedModelName)
	}
	if model.activeSubID != "sub1" {
		t.Errorf("activeSubID = %q, want sub1", model.activeSubID)
	}
	// Verify SelectModel was called to persist the binding
	if len(subscriber.selectModelCalls) != 1 {
		t.Fatalf("expected 1 SelectModel call, got %d", len(subscriber.selectModelCalls))
	}
}

// ── Corner case tests: UI/server data consistency ──
// These tests verify invariants that must hold regardless of event ordering,
// timing, or coalescing. They ensure the UI iterations always match the
// server's authoritative IterationHistory.

// TestConsistency_DBMoreEntriesThanLocal verifies that when the DB
// IterationHistory has more entries than the local state (e.g. after
// missing push events), restoreIterationsFromSnapshot appends the new
// entries from DB while preserving existing local entries.
//
// In production, local iterations are always created by a previous
// restoreIterationsFromSnapshot call (which copies from DB), so existing
// entries always match DB content. The incremental append path avoids
// invalidating the render cache, preventing flicker on iteration transition.
func TestConsistency_DBMoreEntriesThanLocal(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Local has 1 iteration (from a previous DB restore — content matches DB)
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Content: "first", Tools: []protocol.ToolProgress{{Name: "Read", Status: "done"}}},
	}

	// DB now has 3 iterations (server processed more while we weren't looking)
	dbHistory := []protocol.ProgressEvent{
		{Iteration: 1, Content: "first", CompletedTools: []protocol.ToolProgress{{Name: "Read", Label: "file1", Status: "done", Iteration: 1}}},
		{Iteration: 2, Content: "second", CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "ls", Status: "done", Iteration: 2}}},
		{Iteration: 3, Content: "third", CompletedTools: []protocol.ToolProgress{{Name: "Grep", Label: "pattern", Status: "done", Iteration: 3}}},
	}

	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "thinking", Iteration: 4,
	}, dbHistory...)

	// Local should have 3 iterations — existing iter 1 preserved + new 2,3 appended
	if len(model.progressState.iterations) != 3 {
		t.Fatalf("expected 3 iterations after incremental append, got %d", len(model.progressState.iterations))
	}
	// Iter 1 should be preserved (not rebuilt)
	if model.progressState.iterations[0].Content != "first" {
		t.Errorf("iter 1 content = %q, want %q (preserved)", model.progressState.iterations[0].Content, "first")
	}
	// Iter 2 and 3 should be appended from DB
	for i, want := range []string{"second", "third"} {
		if model.progressState.iterations[i+1].Content != want {
			t.Errorf("iter %d content = %q, want %q", i+2, model.progressState.iterations[i+1].Content, want)
		}
	}
}

// TestConsistency_PhaseDoneWhenDBHasFinalIter verifies no duplicate when
// DB IterationHistory already contains the final iteration.
func TestConsistency_PhaseDoneWhenDBHasFinalIter(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	finalContent := "Final report content"
	dbHistory := []protocol.ProgressEvent{
		{Iteration: 1, Content: "step1", CompletedTools: []protocol.ToolProgress{{Name: "Read", Status: "done", Iteration: 1}}},
		{Iteration: 2, Content: finalContent, CompletedTools: []protocol.ToolProgress{{Name: "Write", Status: "done", Iteration: 2}}},
	}

	// Send PhaseDone with IterationHistory that already has iter 2
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "done",
		Iteration:        2,
		Content:          finalContent,
		IterationHistory: dbHistory,
	})

	// Should have exactly 2 iterations — no duplicate of iter 2
	if len(model.progressState.iterations) != 2 {
		t.Fatalf("expected 2 iterations (no dup), got %d", len(model.progressState.iterations))
	}
	// Both should have correct content
	if model.progressState.iterations[1].Content != finalContent {
		t.Errorf("iter 2 content = %q, want %q", model.progressState.iterations[1].Content, finalContent)
	}
}

// TestConsistency_PhaseDoneWhenDBMissingFinalIter verifies that
// finalizeTurnFromSnapshot adds the final iteration when DB hasn't
// captured it yet (PhaseDone arrives before recordIterationSnapshot).
func TestConsistency_PhaseDoneWhenDBMissingFinalIter(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// DB has iter 1 but not iter 2 (the final one)
	dbHistory := []protocol.ProgressEvent{
		{Iteration: 1, Content: "step1", CompletedTools: []protocol.ToolProgress{{Name: "Read", Status: "done", Iteration: 1}}},
	}

	// First, restore from DB
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Content: "",
		CompletedTools: []protocol.ToolProgress{{Name: "Write", Label: "report", Status: "done", Iteration: 2}},
	}, dbHistory...)

	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected 1 iteration from DB, got %d", len(model.progressState.iterations))
	}

	// PhaseDone arrives — DB hasn't captured iter 2 yet (no IterationHistory)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		Content:   "Final report",
		Reasoning: "Done thinking",
	})

	// Should now have 2 iterations: iter 1 (from DB) + iter 2 (from finalize)
	if len(model.progressState.iterations) != 2 {
		t.Fatalf("expected 2 iterations after PhaseDone, got %d", len(model.progressState.iterations))
	}
	if model.progressState.iterations[1].Iteration != 2 {
		t.Errorf("iter[1].Iteration = %d, want 2", model.progressState.iterations[1].Iteration)
	}
	if model.progressState.iterations[1].Content != "Final report" {
		t.Errorf("iter[1].Content = %q, want 'Final report'", model.progressState.iterations[1].Content)
	}
}

// TestConsistency_RepeatedTickPullsNoRebuild verifies that multiple tick
// pulls with the same IterationHistory don't cause unnecessary rebuilds.
func TestConsistency_RepeatedTickPullsNoRebuild(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	dbHistory := []protocol.ProgressEvent{
		{Iteration: 1, Content: "step1", CompletedTools: []protocol.ToolProgress{{Name: "Read", Status: "done", Iteration: 1}}},
		{Iteration: 2, Content: "step2", CompletedTools: []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 2}}},
	}

	// First pull — builds from DB
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "thinking", Iteration: 3,
	}, dbHistory...)

	// Mutate local iterations to detect rebuild (if rebuild happens, mutation is lost)
	model.progressState.iterations[0].Content = "MUTATED"

	// Second pull — same count, should skip
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "thinking", Iteration: 3,
	}, dbHistory...)

	// Should NOT have rebuilt — mutation persists
	if model.progressState.iterations[0].Content != "MUTATED" {
		t.Error("restoreIterationsFromSnapshot rebuilt despite same count — O(1) skip broken")
	}
}

// TestConsistency_FinalizeThenTickPullNoDuplicate verifies that after
// finalizeTurnFromSnapshot adds the final iteration, a subsequent tick
// pull with DB data including that iteration doesn't create a duplicate.
func TestConsistency_FinalizeThenTickPullNoDuplicate(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// PhaseDone — finalize adds iter 2
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		Content:   "final",
		Reasoning: "done",
	})

	countAfterFinalize := len(model.progressState.iterations)
	if countAfterFinalize != 1 {
		t.Fatalf("expected 1 iteration after finalize, got %d", countAfterFinalize)
	}

	// Simulate a late tick pull with DB data that includes iter 2
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "done", Iteration: 2,
	}, protocol.ProgressEvent{Iteration: 2, Content: "final", CompletedTools: []protocol.ToolProgress{}})

	// Should still have 1 iteration — no duplicate
	if len(model.progressState.iterations) != countAfterFinalize {
		t.Errorf("iteration count changed from %d to %d after late tick pull (duplicate created)",
			countAfterFinalize, len(model.progressState.iterations))
	}
}

// TestConsistency_CancelCapturesStreamContent verifies that when a turn
// is cancelled mid-stream, finalizeTurnFromSnapshot captures the
// partial StreamContent (not just empty Content).
func TestConsistency_CancelCapturesStreamContent(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate streaming content (partial response)
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:     1,
		Phase:         "thinking",
		StreamContent: "Partial response being typed...",
		Content:       "", // Content not finalized yet
		ChatID:        "cli:/test",
	}

	// Simulate Ctrl+C cancel
	model.turnCancelled = true

	// PhaseDone arrives after cancel
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 1,
		Content:   "", // PhaseDone has no Content (stream was interrupted)
		ChatID:    "cli:/test",
	})

	// The finalized iteration should have the partial StreamContent
	if len(model.progressState.iterations) == 0 {
		t.Fatal("no iteration created after cancel + PhaseDone")
	}
	iter := model.progressState.iterations[0]
	if iter.Content != "Partial response being typed..." {
		t.Errorf("iter.Content = %q, want partial StreamContent", iter.Content)
	}
}

// TestConsistency_EmptyTurnNoIterations verifies that a turn with zero
// iterations (e.g. immediate final response, no tools) doesn't crash
// and produces correct (possibly empty) iterations.
func TestConsistency_EmptyTurnNoIterations(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// PhaseDone with iteration 0 and content — no prior iterations
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 0,
		Content:   "Hello!",
		ChatID:    "cli:/test",
	})

	// Should have at most 1 iteration (iter 0 with content)
	if len(model.progressState.iterations) > 1 {
		t.Errorf("expected 0-1 iterations for empty turn, got %d", len(model.progressState.iterations))
	}
}

// TestConsistency_StaleSeqSkipped verifies that an event with an old Seq
// is skipped by the seq guard in applyProgressSnapshot.
func TestConsistency_StaleSeqSkipped(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// First event with Seq=5
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Seq: 5,
		CompletedTools: []protocol.ToolProgress{{Name: "Read", Status: "done", Iteration: 1}},
	})

	originalCount := len(model.progressState.iterations)

	// Stale event with Seq=3 (older) — should be skipped
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Seq: 3,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 2}},
	})

	// No change — stale event was skipped
	if len(model.progressState.iterations) != originalCount {
		t.Errorf("stale event was not skipped: iterations changed from %d to %d",
			originalCount, len(model.progressState.iterations))
	}
}

// TestConsistency_PushEventsAloneDoNotCreateIterations verifies that
// push events without IterationHistory never create local iteration
// snapshots. This is the core guarantee after removing snapshotIterationLocal.
func TestConsistency_PushEventsAloneDoNotCreateIterations(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Send several push events with increasing iteration numbers
	// but NO IterationHistory field
	for i := 1; i <= 5; i++ {
		sendProgress(model, &protocol.ProgressEvent{
			Phase:          "tool_exec",
			Iteration:      i,
			Seq:            uint64(i),
			Content:        fmt.Sprintf("iter %d content", i),
			Reasoning:      fmt.Sprintf("iter %d reasoning", i),
			CompletedTools: []protocol.ToolProgress{{Name: "Read", Label: fmt.Sprintf("file%d", i), Status: "done", Elapsed: 100, Iteration: i}},
			ChatID:         "cli:/test",
		})
	}

	// No local iterations should have been created — all data lives
	// only in m.progressState.current (the live state). Iterations come
	// exclusively from DB IterationHistory or finalizeTurnFromSnapshot.
	if len(model.progressState.iterations) != 0 {
		t.Errorf("push events created %d local iterations — expected 0 (snapshotIterationLocal removed)",
			len(model.progressState.iterations))
	}

	// But current state should reflect the latest push event
	if model.progressState.current == nil {
		t.Fatal("progressState.current is nil after push events")
	}
	if model.progressState.current.Iteration != 5 {
		t.Errorf("current.Iteration = %d, want 5", model.progressState.current.Iteration)
	}
}

// TestConsistency_DBHistorySupersedesFinalize verifies that if both
// DB IterationHistory and finalizeTurnFromSnapshot provide data for the
// same iteration, the DB data wins (it's authoritative).
// This happens when PhaseDone arrives and DB already has the final iteration,
// then finalize is skipped (alreadySnapped=true).
func TestConsistency_DBHistorySupersedesFinalize(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	dbContent := "DB authoritative content"
	dbTools := []protocol.ToolProgress{{Name: "Read", Label: "file.go", Status: "done", Elapsed: 500, Iteration: 1}}

	// DB has iter 1 with authoritative data
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase: "thinking", Iteration: 2,
	}, protocol.ProgressEvent{Iteration: 1, Content: dbContent, CompletedTools: dbTools})

	// PhaseDone for iter 2 (final) — DB doesn't have iter 2 yet
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		Content:   "final reply",
	})

	// iter 1 should still have DB content (not overwritten by finalize)
	if model.progressState.iterations[0].Content != dbContent {
		t.Errorf("iter 1 content = %q, want %q (DB data should be authoritative)",
			model.progressState.iterations[0].Content, dbContent)
	}
	if len(model.progressState.iterations[0].Tools) != 1 || model.progressState.iterations[0].Tools[0].Name != "Read" {
		t.Errorf("iter 1 tools don't match DB data")
	}
}

// TestCancelDuringToolGeneratingPreservesPreviousIteration verifies that
// Ctrl+C during tool generating (LLM streaming tool args) does NOT lose
// the previous iteration's completed tools. Before the fix, the cancel
// path only collected tools from msg.payload (PhaseDone), missing prev
// which held the last structured event's completed tools. This caused
// finalTools to be empty → no snapshot → iterations empty → streaming
// message removed → previous iteration data permanently lost.
