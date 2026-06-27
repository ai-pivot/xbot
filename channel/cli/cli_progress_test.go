package cli

import (
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

func sendProgress(model *cliModel, payload *protocol.ProgressEvent) {
	if payload.ChatID == "" {
		payload.ChatID = model.channelName + ":" + model.chatID
	}
	model.Update(cliProgressMsg{payload: payload})
}

func sendDone(model *cliModel, content string) {
	model.typing = false
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
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
					Thinking:  "previous content before current stream",
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
			progress:        &protocol.ProgressEvent{Iteration: 2, Thinking: "current thinking text"},
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
				Thinking:      "current thinking text",
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
				{Iteration: 1, Thinking: "previous content only"},
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
					Thinking:  "previous content and done tool",
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
				{Iteration: 2, Thinking: "previous content before subagent"},
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
			Thinking:  "completed iteration text",
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
			iterations:      []cliIterationSnapshot{{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "Read", Label: "Earlier completed tool", Status: "done", Elapsed: 100, Iteration: 1}}}, {Iteration: 2, Thinking: "final assistant answer"}},
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
					Thinking:  "content-only completed iteration",
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
				{Iteration: 2, Thinking: "content only iteration"},
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
					Thinking:  "second completed content",
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
		{Iteration: 1, Thinking: "earlier content makes whole history mixed"},
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

	model.handleAgentMessage(channel.OutboundMsg{Metadata: map[string]string{"cancelled": "true"}})
}

func TestCancelMessagePreservesCurrentUnsnappedIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.cancelTargetTurnID = model.agentTurnID
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "previous iteration",
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "Previous read", Status: "done", Elapsed: 100, Iteration: 1},
			},
		},
	}
	model.progressState.lastIter = 2
	model.progressState.current = &protocol.ProgressEvent{
		Iteration: 2,
		Thinking:  "current unsnapped iteration",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Current build", Status: "done", Elapsed: 200, Iteration: 2},
		},
	}

	model.handleAgentMessage(channel.OutboundMsg{Metadata: map[string]string{"cancelled": "true"}})

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
			Thinking:  "current turn thinking",
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
	if len(model.progressState.iterations) != 1 || model.progressState.iterations[0].Thinking != "current turn thinking" {
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

	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Thinking: "A"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read file", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: "B"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "B",
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
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Thinking: "Let me look"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "Let me look",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read config", Status: "done", Elapsed: 500, Iteration: 1},
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 300, Iteration: 1},
		},
	})
	// Iter 1
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: "Based on results"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "Based on results",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Fix bug", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})
	// Iter 2: empty thinking (no tools)
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 3, Thinking: ""})

	if len(model.progressState.iterations) == 0 {
		t.Error("Expected iterationHistory to have entries")
	}

	sendDone(model, "Here is the fix.")

	if tools := countToolsInSummary(model); tools != 3 {
		t.Errorf("Expected 3 tools in summary, got %d", tools)
	}
}

// Bug scenario: lastCompletedTools leaking across iterations
func TestLastCompletedToolsLeak(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	// Iter 0: 1 tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 0, Thinking: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 0},
		},
	})
	// Iter 1: 1 tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 1},
		},
	})
	// Iter 2: empty thinking (triggers iter 1 snapshot, should clear lastCompletedTools)
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: ""})

	// Verify lastCompletedTools was cleared after iter 1 snapshot
	if len(model.lastCompletedTools) != 0 {
		t.Errorf("lastCompletedTools should be empty after iter switch, got %d entries", len(model.lastCompletedTools))
	}

	sendDone(model, "Done")

	// Should have exactly 2 tools (Read + Edit), not 3 (no duplicate Edit)
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools (no leak), got %d", tools)
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
		Phase: "tool_exec", Iteration: 1, Thinking: "Trying A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "error", Elapsed: 100, Iteration: 1},
		},
	})
	// Iter 1: a tool that succeeds
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "Trying B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})

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
		Phase: "tool_exec", Iteration: 1, Thinking: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1},
		},
	})
	// Iter 1 payload that accidentally includes a tool from iter 0 (stale)
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1}, // stale
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})

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

func TestBgDrainCompletedTool_AppearsInIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: normal tool + bg drain tool in same iteration
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "working",
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
		Phase: "tool_exec", Iteration: 1, Thinking: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "background_task_result", Label: "bg:old", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})

	// Iter 1: bg tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "background_task_result", Label: "bg:new", Status: "done", Elapsed: 2000, Iteration: 2},
		},
	})

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
		Phase: "tool_exec", Iteration: 1, Thinking: "investigating...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "uname -a", Status: "done", Elapsed: 100, Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "checking logs...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Grep", Label: "error", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 3, Thinking: "reading code...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "main.go", Status: "done", Elapsed: 300, Iteration: 3},
		},
	})

	// Send the final PhaseDone with the assistant reply
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 3,
		Thinking:  "Bug found: the crash is caused by a null pointer dereference.",
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
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Thinking: "launching..."})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "launching...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "SubAgent", Label: "SubAgent [explore/debug-1]: Investigate crash", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: "waiting on others..."})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "waiting on others...",
		CompletedTools: []protocol.ToolProgress{
			{Name: "SubAgent", Label: "SubAgent [explore/debug-2]: Investigate crash", Status: "done", Elapsed: 1200, Iteration: 2},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 3, Thinking: "almost done..."})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 3, Thinking: "almost done...",
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
			{Iteration: 1, Thinking: "first-iter-thinking"},
			{Iteration: 2, Thinking: "second-iter-thinking"},
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
			cliIterationSnapshot{Iteration: 3, Thinking: "third-iter-thinking"})

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
			{Iteration: 1, Thinking: "iter-one"},
			{Iteration: 2, Thinking: "iter-two"},
		}
		// First call at width W1
		model.rc.streamCompletedWidth = -1
		model.updateStreamingOnly()
		firstWidth := model.rc.streamCompletedWidth

		// Add iteration + change cached width → should trigger full rebuild
		model.progressState.iterations = append(model.progressState.iterations,
			cliIterationSnapshot{Iteration: 3, Thinking: "iter-three"})
		model.rc.streamCompletedWidth = 999 // mismatched → forces full rebuild

		model.updateStreamingOnly()

		if model.rc.streamCompletedCount != 3 {
			t.Errorf("expected count=3 after width change rebuild, got %d", model.rc.streamCompletedCount)
		}
		// Full rebuild resets width to actual contentWidth, not the stale 999.
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
		// Verify no double blank guide lines (bug: "\n\n" prepend would produce 2)
		blankCount := 0
		for _, l := range appended {
			s := strings.TrimRight(stripAnsi(l), " ")
			if s == guideSym {
				blankCount++
			}
		}
		if blankCount != 1 {
			t.Errorf("expected exactly 1 blank guide line as separator, got %d", blankCount)
		}

		t.Logf("Cross-kind separator correct: 1 blank guide line between tools→reasoning")
	})
}

// TestSyncProgressTodos_SameCountPreservesCache locks in the P3 fix:
// when todos have the same length but content changes, rc.valid must stay true.
// Previously syncProgressTodos set rc.valid=false on same-count changes,
// triggering fullRebuild → O(all_messages) glamour on every todo status change.
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

	// O(1) invariant: rc.valid must remain true — no fullRebuild triggered
	if !model.rc.valid {
		t.Error("P3 regression: syncProgressTodos with same-count todos set rc.valid=false — fullRebuild would be triggered on next updateViewportContent")
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

// TestUpdateStreamingOnly_LiveOnlyRendersCurrentIteration verifies that the
// live (per-tick) rendering path only uses m.progressState.current for the
// dynamic part, not iterating over m.progressState.iterations for live content.
// Completed iterations come from the streamCompletedLines cache (O(1) hit).
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
		{Iteration: 1, Thinking: "completed-thinking-iter1"},
		{Iteration: 2, Thinking: "completed-thinking-iter2"},
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
