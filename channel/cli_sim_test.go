package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

//
// Compiles into a standalone binary:
//
//	go test -c -o /tmp/xbot-tui-sim ./channel/
//
// Run:
//
//	XBOT_SIM_SCENARIO=scenario.json /tmp/xbot-tui-sim -test.run TestSimMain
//
// Output is written to stdout as JSON.

// ─── Scenario types ────────────────────────────────────────────────

// SimScenario defines a complete simulation scenario.
type SimScenario struct {
	// Config controls the TUI model initialization.
	Config SimConfig `json:"config"`
	// History pre-loads messages (like ConvertMessagesToHistory output).
	History []SimHistoryMsg `json:"history,omitempty"`
	// Steps are the events to replay in order.
	Steps []SimStep `json:"steps"`
}

// SimConfig controls model dimensions and mode.
type SimConfig struct {
	Width  int    `json:"width"`             // Terminal width (default 120)
	Height int    `json:"height"`            // Terminal height (default 40)
	Mode   string `json:"mode,omitempty"`    // "local" (default) or "remote"
	ChatID string `json:"chat_id,omitempty"` // default "/test"
}

// SimHistoryMsg pre-loads a message into the model.
type SimHistoryMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	Iterations []struct {
		Iteration int             `json:"iteration"`
		Thinking  string          `json:"thinking,omitempty"`
		Reasoning string          `json:"reasoning,omitempty"`
		Tools     []SimToolRecord `json:"tools,omitempty"`
	} `json:"iterations,omitempty"`
}

// SimToolRecord describes a tool in an iteration.
type SimToolRecord struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Status  string `json:"status,omitempty"`
	Elapsed int    `json:"elapsed_ms,omitempty"`
}

// SimStep is a single event in the simulation.
// The Action field determines which fields are used.
type SimStep struct {
	// Action is the event type. Required. One of:
	//   "user_msg"    — simulate user typing and sending a message
	//   "agent_msg"   — inject agent response (triggers tool_summary + assistant msg)
	//   "progress"    — send a progress event (tools, thinking, iteration)
	//   "phase_done"  — send a PhaseDone event
	//   "key"         — inject a key press
	//   "resize"      — change terminal dimensions
	//   "cancel"      — trigger turn cancellation (sets turnCancelled)
	//   "rewind"      — open rewind panel and select an item (by index from end)
	//   "snapshot"    — capture current view (output includes rendered text)
	//   "assert"      — verify current view matches expectation
	//   "wait_ms"     — simulated delay (no-op, but useful for documentation)
	//   "set_var"     — set a model variable for testing
	//   "tick"        — process a tick event (spinner, progress updates)
	Action string `json:"action"`

	// ─── user_msg fields ───
	Content string `json:"content,omitempty"`

	// ─── progress fields ───
	Phase     string          `json:"phase,omitempty"`
	Iteration int             `json:"iteration,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	Tools     []SimToolRecord `json:"tools,omitempty"`

	// ─── key fields ───
	Key string `json:"key,omitempty"` // e.g. "ctrl+c", "enter", "esc", "up"

	// ─── resize fields ───
	NewWidth  int `json:"new_width,omitempty"`
	NewHeight int `json:"new_height,omitempty"`

	// ─── rewind fields ───
	RewindIndex int `json:"rewind_index,omitempty"` // 0 = most recent, 1 = second most recent, etc.

	// ─── snapshot fields ───
	Label string `json:"label,omitempty"` // optional label for the snapshot

	// ─── assert fields ───
	Contains    string `json:"contains,omitempty"`     // view must contain this text
	NotContains string `json:"not_contains,omitempty"` // view must NOT contain this text
	Matches     string `json:"matches,omitempty"`      // view must match this regex
	Count       int    `json:"count,omitempty"`        // expected count of Contains (default: >= 1)
	ExactCount  bool   `json:"exact_count,omitempty"`  // if true, count must match exactly

	// ─── set_var fields ───
	Var   string `json:"var,omitempty"`   // variable name (e.g. "turnCancelled", "inputReady")
	Value bool   `json:"value,omitempty"` // variable value

	// ─── agent_msg fields ───
	StreamContent string `json:"stream_content,omitempty"` // streaming content before final
	IsPartial     bool   `json:"is_partial,omitempty"`     // true = streaming, false = final
	Detail        string `json:"detail,omitempty"`         // JSON iteration detail string

	// ─── progress tool fields (for active/completed tools) ───
	ActiveTools    []SimToolRecord `json:"active_tools,omitempty"`
	CompletedTools []SimToolRecord `json:"completed_tools,omitempty"`
}

// SimResult is the output of running a scenario.
type SimResult struct {
	OK         bool           `json:"ok"`
	Error      string         `json:"error,omitempty"`
	Snapshots  []SimSnapshot  `json:"snapshots,omitempty"`
	Assertions []SimAssertion `json:"assertions,omitempty"`
	StepsTotal int            `json:"steps_total"`
	StepsOK    int            `json:"steps_ok"`
}

// SimSnapshot captures the rendered view at a specific step.
type SimSnapshot struct {
	Step  int    `json:"step"`
	Label string `json:"label,omitempty"`
	View  string `json:"view"`  // ANSI-stripped rendered output
	Lines int    `json:"lines"` // number of lines in the view
}

// SimAssertion records the result of an assert step.
type SimAssertion struct {
	Step    int    `json:"step"`
	Type    string `json:"type"` // "contains", "not_contains", "matches"
	Pattern string `json:"pattern"`
	Passed  bool   `json:"passed"`
	Actual  string `json:"actual,omitempty"` // hint text on failure
}

// ─── Simulator ─────────────────────────────────────────────────────

// simRunner processes a scenario and produces results.
type simRunner struct {
	model    *cliModel
	scenario SimScenario
	result   SimResult
}

func newSimRunner(scenario SimScenario) *simRunner {
	cfg := scenario.Config
	if cfg.Width <= 0 {
		cfg.Width = 120
	}
	if cfg.Height <= 0 {
		cfg.Height = 40
	}
	if cfg.ChatID == "" {
		cfg.ChatID = "/test"
	}

	model := newCLIModel()
	model.channelName = "cli"
	model.chatID = cfg.ChatID

	if cfg.Mode == "remote" {
		model.remoteMode = true
	}

	// Initialize model dimensions (sets ready=true)
	model.handleResize(cfg.Width, cfg.Height)
	model.splashDone = true

	return &simRunner{
		model:    model,
		scenario: scenario,
		result: SimResult{
			OK:         true,
			Snapshots:  []SimSnapshot{},
			Assertions: []SimAssertion{},
		},
	}
}

// loadHistory pre-loads messages from the scenario's history section.
func (r *simRunner) loadHistory() {
	for _, hm := range r.scenario.History {
		msg := cliMessage{
			role:    hm.Role,
			content: hm.Content,
			dirty:   true,
		}
		if hm.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, hm.Timestamp); err == nil {
				msg.timestamp = t
			}
		}
		if msg.timestamp.IsZero() {
			msg.timestamp = time.Now()
		}

		// Convert iterations for tool_summary messages
		if hm.Role == "tool_summary" && len(hm.Iterations) > 0 {
			iters := make([]cliIterationSnapshot, len(hm.Iterations))
			for i, it := range hm.Iterations {
				tools := make([]CLIToolProgress, len(it.Tools))
				for j, t := range it.Tools {
					label := t.Label
					if label == "" {
						label = t.Name
					}
					tools[j] = CLIToolProgress{
						Name:      t.Name,
						Label:     label,
						Status:    t.Status,
						Elapsed:   int64(t.Elapsed),
						Iteration: it.Iteration,
					}
				}
				iters[i] = cliIterationSnapshot{
					Iteration: it.Iteration,
					Thinking:  it.Thinking,
					Reasoning: it.Reasoning,
					Tools:     tools,
				}
			}
			msg.iterations = iters
		}

		r.model.messages = append(r.model.messages, msg)
	}
	r.model.renderCacheValid = false
	r.model.updateViewportContent()
}

// run processes all steps and returns the result.
func (r *simRunner) run() SimResult {
	r.loadHistory()

	for i, step := range r.scenario.Steps {
		if err := r.processStep(i, step); err != nil {
			r.result.OK = false
			r.result.Error = fmt.Sprintf("step %d (%s): %s", i, step.Action, err)
			r.result.StepsTotal = len(r.scenario.Steps)
			r.result.StepsOK = i
			return r.result
		}
	}

	r.result.StepsTotal = len(r.scenario.Steps)
	r.result.StepsOK = len(r.scenario.Steps)
	return r.result
}

func (r *simRunner) processStep(idx int, step SimStep) error {
	m := r.model

	switch step.Action {
	case "user_msg":
		return r.doUserMsg(idx, step)
	case "agent_msg":
		return r.doAgentMsg(idx, step)
	case "progress":
		return r.doProgress(idx, step)
	case "phase_done":
		return r.doPhaseDone(idx, step)
	case "key":
		return r.doKey(idx, step)
	case "resize":
		return r.doResize(idx, step)
	case "cancel":
		m.turnCancelled = true
	case "rewind":
		return r.doRewind(idx, step)
	case "snapshot":
		view := r.captureView()
		r.result.Snapshots = append(r.result.Snapshots, SimSnapshot{
			Step:  idx,
			Label: step.Label,
			View:  view,
			Lines: len(strings.Split(view, "\n")),
		})
	case "assert":
		return r.doAssert(idx, step)
	case "wait_ms":
		// No-op in simulation; real time doesn't matter
	case "set_var":
		return r.doSetVar(idx, step)
	case "tick":
		// Process a tick to update spinner animation
		m.Update(cliTickMsg{})
	default:
		return fmt.Errorf("unknown action: %q", step.Action)
	}
	return nil
}

func (r *simRunner) doUserMsg(idx int, step SimStep) error {
	m := r.model
	// Add user message
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   step.Content,
		timestamp: time.Now(),
		dirty:     true,
	})
	// Start agent turn
	m.startAgentTurn()
	m.resetProgressState()
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doAgentMsg(idx int, step SimStep) error {
	m := r.model
	_ = idx

	// Build the outbound message
	outMsg := bus.OutboundMessage{
		Content:   step.Content,
		IsPartial: step.IsPartial,
	}

	// If this is a final message (not partial), typing will be set to false
	_ = m.typing

	m.Update(cliOutboundMsg{
		msg: outMsg,
	})

	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doProgress(idx int, step SimStep) error {
	m := r.model

	// Build tool progress records
	activeTools := make([]CLIToolProgress, len(step.ActiveTools))
	for i, t := range step.ActiveTools {
		label := t.Label
		if label == "" {
			label = t.Name
		}
		activeTools[i] = CLIToolProgress{
			Name:      t.Name,
			Label:     label,
			Status:    t.Status,
			Iteration: step.Iteration,
		}
	}
	completedTools := make([]CLIToolProgress, len(step.CompletedTools))
	for i, t := range step.CompletedTools {
		label := t.Label
		if label == "" {
			label = t.Name
		}
		completedTools[i] = CLIToolProgress{
			Name:      t.Name,
			Label:     label,
			Status:    t.Status,
			Elapsed:   int64(t.Elapsed),
			Iteration: step.Iteration,
		}
	}

	payload := &CLIProgressPayload{
		Phase:          step.Phase,
		Iteration:      step.Iteration,
		Thinking:       step.Thinking,
		Reasoning:      step.Reasoning,
		ActiveTools:    activeTools,
		CompletedTools: completedTools,
		ChatID:         m.channelName + ":" + m.chatID,
	}

	m.Update(cliProgressMsg{payload: payload})
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doPhaseDone(idx int, step SimStep) error {
	m := r.model

	payload := &CLIProgressPayload{
		Phase:     "done",
		Thinking:  step.Thinking,
		Reasoning: step.Reasoning,
		ChatID:    m.channelName + ":" + m.chatID,
	}

	// Prefer CompletedTools field, fall back to Tools field
	tools := step.CompletedTools
	if len(tools) == 0 {
		tools = step.Tools
	}
	completedTools := make([]CLIToolProgress, len(tools))
	for i, t := range tools {
		label := t.Label
		if label == "" {
			label = t.Name
		}
		completedTools[i] = CLIToolProgress{
			Name:      t.Name,
			Label:     label,
			Status:    t.Status,
			Elapsed:   int64(t.Elapsed),
			Iteration: step.Iteration,
		}
	}
	payload.CompletedTools = completedTools

	m.Update(cliProgressMsg{payload: payload})

	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doKey(idx int, step SimStep) error {
	m := r.model
	key := parseKeyInput(step.Key)
	m.Update(key)
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doResize(idx int, step SimStep) error {
	m := r.model
	w := step.NewWidth
	h := step.NewHeight
	if w <= 0 {
		w = m.width
	}
	if h <= 0 {
		h = m.height
	}
	m.handleResize(w, h)
	return nil
}

func (r *simRunner) doRewind(idx int, step SimStep) error {
	m := r.model

	// Build rewind items from user messages
	var items []rewindItem
	for i, msg := range m.messages {
		if msg.role == "user" {
			items = append(items, rewindItem{
				Content:  msg.content,
				Time:     msg.timestamp,
				MsgIndex: i,
			})
		}
	}
	if len(items) == 0 {
		return fmt.Errorf("no user messages to rewind to")
	}

	// Select item by RewindIndex (0 = most recent)
	ri := len(items) - 1 - step.RewindIndex
	if ri < 0 || ri >= len(items) {
		return fmt.Errorf("rewind_index %d out of range (have %d user messages)", step.RewindIndex, len(items))
	}

	// Apply rewind
	selectedItem := items[ri]
	cutIdx := selectedItem.MsgIndex
	m.messages = m.messages[:cutIdx]

	m.renderCacheValid = false
	m.cachedHistory = ""
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doAssert(idx int, step SimStep) error {
	view := r.captureView()

	if step.Contains != "" {
		count := strings.Count(view, step.Contains)
		expected := step.Count
		if expected <= 0 {
			expected = 1
		}
		passed := count >= expected
		if step.ExactCount {
			passed = count == expected
		}
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step:    idx,
			Type:    "contains",
			Pattern: step.Contains,
			Passed:  passed,
			Actual:  fmt.Sprintf("found %d occurrences", count),
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert contains: found %d of %q, expected %d", count, step.Contains, expected)
		}
	}

	if step.NotContains != "" {
		count := strings.Count(view, step.NotContains)
		passed := count == 0
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step:    idx,
			Type:    "not_contains",
			Pattern: step.NotContains,
			Passed:  passed,
			Actual:  fmt.Sprintf("found %d occurrences", count),
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert not_contains: found %d of %q", count, step.NotContains)
		}
	}

	if step.Matches != "" {
		re, err := regexp.Compile(step.Matches)
		if err != nil {
			return fmt.Errorf("invalid regex: %v", err)
		}
		passed := re.MatchString(view)
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step:    idx,
			Type:    "matches",
			Pattern: step.Matches,
			Passed:  passed,
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert matches: pattern %q not found in view", step.Matches)
		}
	}

	return nil
}

func (r *simRunner) doSetVar(idx int, step SimStep) error {
	m := r.model
	switch step.Var {
	case "turnCancelled":
		m.turnCancelled = step.Value
	case "inputReady":
		m.inputReady = step.Value
	case "typing":
		m.typing = step.Value
	default:
		return fmt.Errorf("unknown variable: %q", step.Var)
	}
	return nil
}

// captureView returns the current TUI view with ANSI codes stripped.
func (r *simRunner) captureView() string {
	view := r.model.View().Content
	return stripAnsi(view)
}

// stripAnsi is already defined in cli_helpers_test.go

// ─── Test entry point ──────────────────────────────────────────────

// TestSimMain is the entry point for the standalone simulator binary.
// It reads a scenario from XBOT_SIM_SCENARIO and outputs JSON results.
func TestSimMain(t *testing.T) {
	scenarioPath := os.Getenv("XBOT_SIM_SCENARIO")
	if scenarioPath == "" {
		t.Skip("XBOT_SIM_SCENARIO not set; simulator mode inactive")
	}

	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("Failed to read scenario: %v", err)
	}

	var scenario SimScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatalf("Failed to parse scenario: %v", err)
	}

	runner := newSimRunner(scenario)
	result := runner.run()

	outputPath := os.Getenv("XBOT_SIM_OUTPUT")
	if outputPath != "" {
		out, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(outputPath, out, 0644); err != nil {
			t.Fatalf("Failed to write output: %v", err)
		}
	} else {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
	}

	if !result.OK {
		t.Errorf("Simulation failed: %s", result.Error)
	}
}

// TestSimBasic validates the simulator with a simple scenario.
func TestSimBasic(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "snapshot", Label: "after_user_msg"},
			{Action: "agent_msg", Content: "Hi there!"},
			{Action: "snapshot", Label: "after_agent_msg"},
			{Action: "assert", Contains: "Hi there!"},
		},
	}

	runner := newSimRunner(scenario)
	result := runner.run()

	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Snapshots) != 2 {
		t.Errorf("Expected 2 snapshots, got %d", len(result.Snapshots))
	}
	if len(result.Assertions) != 1 || !result.Assertions[0].Passed {
		t.Errorf("Expected 1 passing assertion, got %v", result.Assertions)
	}
}

// TestSimProgressWithTools validates progress event handling.
func TestSimProgressWithTools(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "read the file"},
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{
					{Name: "Read", Label: "Read main.go", Status: "active"},
				},
			},
			{Action: "snapshot", Label: "tool_active"},
			{Action: "assert", Contains: "Read"},
			{Action: "progress", Phase: "done", Iteration: 0,
				CompletedTools: []SimToolRecord{
					{Name: "Read", Label: "Read main.go", Status: "done", Elapsed: 150},
				},
			},
			{Action: "agent_msg", Content: "Here is main.go content..."},
			{Action: "snapshot", Label: "after_response"},
		},
	}

	runner := newSimRunner(scenario)
	result := runner.run()

	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}

// TestSimCancelAndRewind validates the cancelled-turn rewind fix.
// The key assertion: after rewind, the tool_summary message exists in m.messages.
func TestSimCancelAndRewind(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			// Turn 1: normal turn with tools
			{Action: "user_msg", Content: "first message"},
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{
					{Name: "Read", Label: "Read file1.go", Status: "active"},
				},
			},
			{Action: "progress", Phase: "done", Iteration: 0,
				CompletedTools: []SimToolRecord{
					{Name: "Read", Label: "Read file1.go", Status: "done", Elapsed: 100},
				},
			},
			{Action: "agent_msg", Content: "First response"},

			// Turn 2: cancelled turn with tools
			{Action: "user_msg", Content: "second message"},
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{
					{Name: "Shell", Label: "Shell ls", Status: "active"},
				},
			},
			{Action: "cancel"}, // user cancels
			{Action: "phase_done", Iteration: 0,
				CompletedTools: []SimToolRecord{
					{Name: "Shell", Label: "Shell ls", Status: "done", Elapsed: 50},
				},
			},

			// Turn 3: another normal message
			{Action: "user_msg", Content: "third message"},
			{Action: "phase_done", Iteration: 0},
			{Action: "agent_msg", Content: "Third response"},

			// Rewind to before turn 3
			{Action: "rewind", RewindIndex: 0},

			// Verify: first response should still be visible
			{Action: "assert", Contains: "First response"},
			// Verify: third message (rewound) should NOT be visible
			{Action: "assert", NotContains: "Third response"},
		},
	}

	runner := newSimRunner(scenario)
	result := runner.run()

	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}

	// Verify tool_summary exists in messages for the cancelled turn
	foundToolSummary := false
	for _, msg := range runner.model.messages {
		if msg.role == "tool_summary" {
			for _, it := range msg.iterations {
				for _, tool := range it.Tools {
					if tool.Name == "Shell" {
						foundToolSummary = true
					}
				}
			}
		}
	}
	if !foundToolSummary {
		t.Error("Expected tool_summary with Shell tool in model.messages after cancelled turn")
	}
}

// TestSimResize validates that resize changes the view dimensions.
func TestSimResize(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "snapshot", Label: "wide"},
			{Action: "resize", NewWidth: 60, NewHeight: 20},
			{Action: "snapshot", Label: "narrow"},
			// Verify resize changed something (narrow view has different line widths)
			{Action: "assert", Matches: `hello`},
		},
	}

	runner := newSimRunner(scenario)
	result := runner.run()

	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Snapshots) != 2 {
		t.Errorf("Expected 2 snapshots, got %d", len(result.Snapshots))
	}
	// Verify the model actually resized
	if runner.model.width != 60 {
		t.Errorf("Expected model width 60, got %d", runner.model.width)
	}
	if runner.model.height != 20 {
		t.Errorf("Expected model height 20, got %d", runner.model.height)
	}
}
