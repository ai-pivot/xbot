package channel

import (
	"os"
	"strings"
	"testing"
	"time"
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
		msg: OutboundMsg{
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
	model.locale = GetLocale("en")

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

	model.handleAgentMessage(OutboundMsg{Metadata: map[string]string{"cancelled": "true"}})
}

func TestCancelMessagePreservesCurrentUnsnappedIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.cancelTargetTurnID = model.agentTurnID
	model.iterationHistory = []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "previous iteration",
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "Previous read", Status: "done", Elapsed: 100, Iteration: 1},
			},
		},
	}
	model.lastSeenIteration = 2
	model.progress = &protocol.ProgressEvent{
		Iteration: 2,
		Thinking:  "current unsnapped iteration",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Current build", Status: "done", Elapsed: 200, Iteration: 2},
		},
	}

	model.handleAgentMessage(OutboundMsg{Metadata: map[string]string{"cancelled": "true"}})

	if model.streamingMsgIdx != -1 {
		t.Fatalf("streamingMsgIdx = %d, want -1 after cancel", model.streamingMsgIdx)
	}
	// Empty streaming message should be removed on cancel — it is a shell
	// created by startAgentTurn with no content. Keeping it produces a
	// phantom assistant message with stale iterations in the viewport.
	for i := range model.messages {
		if model.messages[i].role == "assistant" {
			t.Fatalf("empty streaming message should have been removed on cancel, got assistant at index %d", i)
		}
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
		history: []HistoryMessage{
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
		history: []HistoryMessage{
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
		history: []HistoryMessage{
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
	model.iterationHistory = []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "current turn thinking",
			Tools: []protocol.ToolProgress{
				{Name: "Shell", Label: "running command", Status: "running", Iteration: 1},
			},
		},
	}
	model.progress = &protocol.ProgressEvent{Phase: "tool_exec", Iteration: 1}
	streamingIdx := model.streamingMsgIdx

	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName: model.channelName,
		chatID:      model.chatID,
		history: []HistoryMessage{
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
	if len(model.iterationHistory) != 1 || model.iterationHistory[0].Thinking != "current turn thinking" {
		t.Fatalf("iteration history was not preserved: %+v", model.iterationHistory)
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
	if len(model.iterationHistory) == 0 {
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

	if len(model.iterationHistory) == 0 {
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
