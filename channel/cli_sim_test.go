package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

// ─── Scenario types ────────────────────────────────────────────────

// SimScenario defines a complete simulation scenario.
type SimScenario struct {
	Config  SimConfig       `json:"config"`
	History []SimHistoryMsg `json:"history,omitempty"`
	Steps   []SimStep       `json:"steps"`
}

type SimConfig struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mode   string `json:"mode,omitempty"`
	ChatID string `json:"chat_id,omitempty"`
}

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

type SimToolRecord struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Status  string `json:"status,omitempty"`
	Elapsed int    `json:"elapsed_ms,omitempty"`
}

// SimStep is a single event in the simulation.
type SimStep struct {
	Action string `json:"action"`

	// ─── shared content field ───
	Content string `json:"content,omitempty"`

	// ─── progress / phase_done fields ───
	Phase                  string          `json:"phase,omitempty"`
	Iteration              int             `json:"iteration,omitempty"`
	Thinking               string          `json:"thinking,omitempty"`
	Reasoning              string          `json:"reasoning,omitempty"`
	StreamContent          string          `json:"stream_content,omitempty"`
	ReasoningStreamContent string          `json:"reasoning_stream_content,omitempty"`
	Tools                  []SimToolRecord `json:"tools,omitempty"`
	ActiveTools            []SimToolRecord `json:"active_tools,omitempty"`
	CompletedTools         []SimToolRecord `json:"completed_tools,omitempty"`

	// ─── key / resize / rewind fields ───
	Key         string `json:"key,omitempty"`
	NewWidth    int    `json:"new_width,omitempty"`
	NewHeight   int    `json:"new_height,omitempty"`
	RewindIndex int    `json:"rewind_index,omitempty"`

	// ─── snapshot / label ───
	Label string `json:"label,omitempty"`

	// ─── assert fields (view-level) ───
	Contains    string `json:"contains,omitempty"`
	NotContains string `json:"not_contains,omitempty"`
	Matches     string `json:"matches,omitempty"`
	Count       int    `json:"count,omitempty"`
	ExactCount  bool   `json:"exact_count,omitempty"`
	// View height assertion
	AssertViewLines    int `json:"assert_view_lines,omitempty"`     // expected view line count (exact)
	AssertViewLinesMax int `json:"assert_view_lines_max,omitempty"` // expected max view line count

	// ─── assert fields (message-level) ───
	AssertRole    string   `json:"assert_role,omitempty"`
	AssertCount   int      `json:"assert_count,omitempty"`
	AssertContent string   `json:"assert_content,omitempty"`
	AssertTools   []string `json:"assert_tools,omitempty"`
	// Assert at a specific message index
	AssertIndex     int    `json:"assert_index,omitempty"`      // 0-based index
	AssertIndexRole string `json:"assert_index_role,omitempty"` // expected role at that index
	// Assert total message count
	AssertTotal int `json:"assert_total,omitempty"` // expected total messages
	// Assert message role order
	AssertMessageOrder []string `json:"assert_message_order,omitempty"` // expected sequence of roles
	// Assert role content matches regex
	AssertContentRegex string `json:"assert_content_regex,omitempty"` // regex to match against role's content

	// ─── set_var fields ───
	Var   string `json:"var,omitempty"`
	Value bool   `json:"value,omitempty"`

	// ─── agent_msg fields ───
	IsPartial bool   `json:"is_partial,omitempty"`
	Detail    string `json:"detail,omitempty"`

	// ─── inspect fields ───
	InspectMessages bool     `json:"inspect_messages,omitempty"`
	InspectVars     []string `json:"inspect_vars,omitempty"`
	InspectAll      bool     `json:"inspect_all,omitempty"`

	// ─── subagent fields ───
	SubAgents []SimSubAgent `json:"sub_agents,omitempty"`

	// ─── queue fields ───
	QueueMessages []string `json:"queue_messages,omitempty"`

	// ─── system_msg fields ───
	// "system_msg" adds a system feedback message (like info/error feedback)
	Level string `json:"level,omitempty"` // "info" (default), "error", "warn"

	// ─── turn shortcut fields ───
	// "turn" is a shortcut that combines: user_msg + progress(tools) + phase_done + agent_msg
	// into a single step. It expands into multiple internal steps.
	Response string `json:"response,omitempty"` // agent response text (for "turn" action)
	// Multi-iteration support for "turn": each entry = one iteration with its own tools
	TurnIterations []SimTurnIter `json:"turn_iterations,omitempty"`

	// ─── export fields ───
	// "export" saves current messages as a history JSON file
	ExportPath string `json:"export_path,omitempty"`

	// ─── diff fields ───
	// "diff" compares two snapshots by label
	DiffFrom string `json:"diff_from,omitempty"` // first snapshot label
	DiffTo   string `json:"diff_to,omitempty"`   // second snapshot label

	// ─── loop fields ───
	// "loop" repeats a set of sub-steps N times
	LoopCount int       `json:"loop_count,omitempty"` // number of repetitions
	LoopSteps []SimStep `json:"loop_steps,omitempty"` // steps to repeat

	// ─── include fields ───
	// "include" loads steps from an external JSON file
	IncludePath string `json:"include_path,omitempty"` // path to steps JSON file

	// ─── assert tool timing ───
	AssertToolName  string `json:"assert_tool_name,omitempty"`   // tool name to check
	AssertToolMinMs int    `json:"assert_tool_min_ms,omitempty"` // minimum elapsed_ms
	AssertToolMaxMs int    `json:"assert_tool_max_ms,omitempty"` // maximum elapsed_ms
}

// SimSubAgent describes a SubAgent in the tree for simulation.
type SimSubAgent struct {
	Role     string        `json:"role"`
	Instance string        `json:"instance"`
	Status   string        `json:"status"`
	Task     string        `json:"task,omitempty"`
	Children []SimSubAgent `json:"children,omitempty"`
}

// SimTurnIter defines one iteration within a "turn" shortcut action.
type SimTurnIter struct {
	Tools []SimToolRecord `json:"tools,omitempty"` // completed tools for this iteration
}

// ─── Output types ──────────────────────────────────────────────────

type SimResult struct {
	OK          bool            `json:"ok"`
	Error       string          `json:"error,omitempty"`
	Snapshots   []SimSnapshot   `json:"snapshots,omitempty"`
	Assertions  []SimAssertion  `json:"assertions,omitempty"`
	Inspections []SimInspection `json:"inspections,omitempty"`
	Diffs       []SimDiff       `json:"diffs,omitempty"`
	StepsTotal  int             `json:"steps_total"`
	StepsOK     int             `json:"steps_ok"`
}

type SimDiff struct {
	Step     int    `json:"step"`
	From     string `json:"from"`
	To       string `json:"to"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Modified int    `json:"modified"`
	Summary  string `json:"summary,omitempty"`
}

type SimSnapshot struct {
	Step   int    `json:"step"`
	Label  string `json:"label,omitempty"`
	View   string `json:"view"`
	Lines  int    `json:"lines"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type SimAssertion struct {
	Step    int    `json:"step"`
	Type    string `json:"type"`
	Pattern string `json:"pattern,omitempty"`
	Passed  bool   `json:"passed"`
	Actual  string `json:"actual,omitempty"`
	Context string `json:"context,omitempty"`
}

type SimInspection struct {
	Step        int               `json:"step"`
	Label       string            `json:"label,omitempty"`
	Messages    []SimMessageDump  `json:"messages,omitempty"`
	Vars        map[string]any    `json:"vars,omitempty"`
	State       *SimModelSnapshot `json:"state,omitempty"`
	ViewSummary string            `json:"view_summary,omitempty"`
	Summary     string            `json:"summary,omitempty"` // markdown-formatted summary
}

type SimMessageDump struct {
	Index      int           `json:"index"`
	Role       string        `json:"role"`
	TurnID     uint64        `json:"turn_id"`
	Content    string        `json:"content"`
	ContentLen int           `json:"content_len"`
	Iterations []SimIterDump `json:"iterations,omitempty"`
	Dirty      bool          `json:"dirty"`
}

type SimIterDump struct {
	Iteration int             `json:"iteration"`
	Thinking  string          `json:"thinking,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	Tools     []SimToolRecord `json:"tools,omitempty"`
}

type SimModelSnapshot struct {
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Typing        bool   `json:"typing"`
	TurnCancelled bool   `json:"turn_cancelled"`
	InputReady    bool   `json:"input_ready"`
	AgentTurnID   uint64 `json:"agent_turn_id"`
	MessageCount  int    `json:"message_count"`
	IterHistCount int    `json:"iteration_history_count"`
	ProgressPhase string `json:"progress_phase,omitempty"`
	LastSeenIter  int    `json:"last_seen_iteration"`
	RemoteMode    bool   `json:"remote_mode"`
	QueueLen      int    `json:"queue_len"`
	ViewportAtTop bool   `json:"viewport_at_top"`
	ViewportAtBot bool   `json:"viewport_at_bottom"`
	TotalLines    int    `json:"total_lines"`
}

// ─── Simulator ─────────────────────────────────────────────────────

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
	model.handleResize(cfg.Width, cfg.Height)
	model.splashDone = true

	return &simRunner{
		model:    model,
		scenario: scenario,
		result: SimResult{
			OK:          true,
			Snapshots:   []SimSnapshot{},
			Assertions:  []SimAssertion{},
			Inspections: []SimInspection{},
			Diffs:       []SimDiff{},
		},
	}
}

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

func (r *simRunner) run() SimResult {
	r.loadHistory()
	for i, step := range r.scenario.Steps {
		if err := r.processStep(i, step); err != nil {
			r.result.OK = false
			r.result.Error = fmt.Sprintf("step %d (%s): %s", i, step.Action, err)
			r.result.StepsTotal = len(r.scenario.Steps)
			r.result.StepsOK = i
			// On failure, auto-capture inspection for debugging
			r.result.Inspections = append(r.result.Inspections, SimInspection{
				Step:     i,
				Label:    "auto_on_failure",
				Messages: r.dumpMessages(),
				State:    r.dumpState(),
			})
			return r.result
		}
	}
	r.result.StepsTotal = len(r.scenario.Steps)
	r.result.StepsOK = len(r.scenario.Steps)
	return r.result
}

func (r *simRunner) processStep(idx int, step SimStep) error {
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
		r.model.turnCancelled = true
	case "rewind":
		return r.doRewind(idx, step)
	case "snapshot":
		r.doSnapshot(idx, step)
	case "assert":
		return r.doAssert(idx, step)
	case "wait_ms":
	case "set_var":
		return r.doSetVar(idx, step)
	case "tick":
		r.model.Update(cliTickMsg{})
	case "inspect":
		return r.doInspect(idx, step)
	case "queue_add":
		return r.doQueueAdd(idx, step)
	case "subagent":
		return r.doSubAgent(idx, step)
	case "clear":
		return r.doClear(idx, step)
	case "summary":
		return r.doSummary(idx, step)
	case "export":
		return r.doExport(idx, step)
	case "diff":
		return r.doDiff(idx, step)
	case "loop":
		return r.doLoop(idx, step)
	case "include":
		return r.doInclude(idx, step)
	case "comment":
		// No-op: just a label/annotation in the scenario
		return nil
	case "system_msg":
		return r.doSystemMsg(idx, step)
	case "turn":
		return r.doTurn(idx, step)
	default:
		return fmt.Errorf("unknown action: %q", step.Action)
	}
	return nil
}

// ─── Action implementations ────────────────────────────────────────

func (r *simRunner) doUserMsg(idx int, step SimStep) error {
	m := r.model
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   step.Content,
		timestamp: time.Now(),
		dirty:     true,
	})
	m.startAgentTurn()
	m.resetProgressState()
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doAgentMsg(idx int, step SimStep) error {
	m := r.model
	outMsg := bus.OutboundMessage{
		Content:   step.Content,
		IsPartial: step.IsPartial,
	}
	m.Update(cliOutboundMsg{msg: outMsg})
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doProgress(idx int, step SimStep) error {
	m := r.model
	payload := &CLIProgressPayload{
		Phase:                  step.Phase,
		Iteration:              step.Iteration,
		Thinking:               step.Thinking,
		Reasoning:              step.Reasoning,
		StreamContent:          step.StreamContent,
		ReasoningStreamContent: step.ReasoningStreamContent,
		ActiveTools:            convertSimTools(step.ActiveTools, step.Iteration),
		CompletedTools:         convertSimTools(step.CompletedTools, step.Iteration),
		ChatID:                 m.channelName + ":" + m.chatID,
	}
	m.Update(cliProgressMsg{payload: payload})
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doPhaseDone(idx int, step SimStep) error {
	m := r.model
	tools := step.CompletedTools
	if len(tools) == 0 {
		tools = step.Tools
	}
	payload := &CLIProgressPayload{
		Phase:          "done",
		Thinking:       step.Thinking,
		Reasoning:      step.Reasoning,
		CompletedTools: convertSimTools(tools, step.Iteration),
		ChatID:         m.channelName + ":" + m.chatID,
	}
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
	w, h := step.NewWidth, step.NewHeight
	if w <= 0 {
		w = r.model.width
	}
	if h <= 0 {
		h = r.model.height
	}
	r.model.handleResize(w, h)
	return nil
}

func (r *simRunner) doRewind(idx int, step SimStep) error {
	m := r.model
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
	ri := len(items) - 1 - step.RewindIndex
	if ri < 0 || ri >= len(items) {
		return fmt.Errorf("rewind_index %d out of range (have %d user messages)", step.RewindIndex, len(items))
	}
	cutIdx := items[ri].MsgIndex
	m.messages = m.messages[:cutIdx]
	m.renderCacheValid = false
	m.cachedHistory = ""
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doSnapshot(idx int, step SimStep) {
	view := r.captureView()
	r.result.Snapshots = append(r.result.Snapshots, SimSnapshot{
		Step:   idx,
		Label:  step.Label,
		View:   view,
		Lines:  len(strings.Split(view, "\n")),
		Width:  r.model.width,
		Height: r.model.height,
	})
}

func (r *simRunner) doAssert(idx int, step SimStep) error {
	view := r.captureView()

	// ─── View-level assertions ───
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
		ctx := ""
		if !passed {
			ctx = extractContext(view, step.Contains, 120)
		}
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step: idx, Type: "contains", Pattern: step.Contains,
			Passed: passed, Actual: fmt.Sprintf("found %d occurrences", count), Context: ctx,
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert contains: found %d of %q, expected %d", count, step.Contains, expected)
		}
	}

	if step.NotContains != "" {
		count := strings.Count(view, step.NotContains)
		passed := count == 0
		ctx := ""
		if !passed {
			ctx = extractContext(view, step.NotContains, 120)
		}
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step: idx, Type: "not_contains", Pattern: step.NotContains,
			Passed: passed, Actual: fmt.Sprintf("found %d occurrences", count), Context: ctx,
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
			Step: idx, Type: "matches", Pattern: step.Matches, Passed: passed,
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert matches: pattern %q not found in view", step.Matches)
		}
	}

	// ─── Message-level assertions ───
	if step.AssertRole != "" {
		msgs := r.model.messages
		roleCount := 0
		for _, msg := range msgs {
			if msg.role == step.AssertRole {
				roleCount++
			}
		}

		if step.AssertCount > 0 {
			passed := roleCount == step.AssertCount
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_role_count",
				Pattern: fmt.Sprintf("role=%s count==%d", step.AssertRole, step.AssertCount),
				Passed:  passed,
				Actual:  fmt.Sprintf("found %d messages with role %q", roleCount, step.AssertRole),
			})
			if !passed {
				r.result.OK = false
				return fmt.Errorf("assert_role_count: expected %d messages with role %q, found %d",
					step.AssertCount, step.AssertRole, roleCount)
			}
		}

		if step.AssertContent != "" {
			found := false
			for _, msg := range msgs {
				if msg.role == step.AssertRole && strings.Contains(msg.content, step.AssertContent) {
					found = true
					break
				}
			}
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_role_content",
				Pattern: fmt.Sprintf("role=%s contains %q", step.AssertRole, step.AssertContent),
				Passed:  found,
				Actual:  fmt.Sprintf("role %q messages: %d", step.AssertRole, roleCount),
			})
			if !found {
				r.result.OK = false
				return fmt.Errorf("assert_role_content: no message with role %q contains %q",
					step.AssertRole, step.AssertContent)
			}
		}

		// Assert role content matches regex
		if step.AssertContentRegex != "" {
			re, err := regexp.Compile(step.AssertContentRegex)
			if err != nil {
				return fmt.Errorf("invalid assert_content_regex: %v", err)
			}
			found := false
			for _, msg := range msgs {
				if msg.role == step.AssertRole && re.MatchString(msg.content) {
					found = true
					break
				}
			}
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_role_content_regex",
				Pattern: fmt.Sprintf("role=%s matches %q", step.AssertRole, step.AssertContentRegex),
				Passed:  found,
				Actual:  fmt.Sprintf("role %q messages: %d", step.AssertRole, roleCount),
			})
			if !found {
				r.result.OK = false
				return fmt.Errorf("assert_role_content_regex: no message with role %q matches %q",
					step.AssertRole, step.AssertContentRegex)
			}
		}

		if len(step.AssertTools) > 0 && step.AssertRole == "tool_summary" {
			allToolNames := map[string]bool{}
			for _, msg := range msgs {
				if msg.role == "tool_summary" {
					for _, it := range msg.iterations {
						for _, t := range it.Tools {
							allToolNames[t.Name] = true
						}
					}
				}
			}
			var missing []string
			for _, name := range step.AssertTools {
				if !allToolNames[name] {
					missing = append(missing, name)
				}
			}
			passed := len(missing) == 0
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_tools",
				Pattern: fmt.Sprintf("tools: %v", step.AssertTools),
				Passed:  passed,
				Actual:  fmt.Sprintf("available: %v", sortedKeys(allToolNames)),
			})
			if !passed {
				r.result.OK = false
				return fmt.Errorf("assert_tools: missing tool names: %v", missing)
			}
		}
	}

	// ─── View height assertions ───
	if step.AssertViewLines > 0 || step.AssertViewLinesMax > 0 {
		viewLines := len(strings.Split(view, "\n"))
		if step.AssertViewLines > 0 {
			passed := viewLines == step.AssertViewLines
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_view_lines",
				Pattern: fmt.Sprintf("view_lines == %d", step.AssertViewLines),
				Passed:  passed,
				Actual:  fmt.Sprintf("view has %d lines", viewLines),
			})
			if !passed {
				r.result.OK = false
				return fmt.Errorf("assert_view_lines: expected %d lines, got %d", step.AssertViewLines, viewLines)
			}
		}
		if step.AssertViewLinesMax > 0 {
			passed := viewLines <= step.AssertViewLinesMax
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_view_lines_max",
				Pattern: fmt.Sprintf("view_lines <= %d", step.AssertViewLinesMax),
				Passed:  passed,
				Actual:  fmt.Sprintf("view has %d lines", viewLines),
			})
			if !passed {
				r.result.OK = false
				return fmt.Errorf("assert_view_lines_max: expected <= %d lines, got %d", step.AssertViewLinesMax, viewLines)
			}
		}
	}

	// ─── Total message count assertion ───
	if step.AssertTotal > 0 {
		msgCount := len(r.model.messages)
		passed := msgCount == step.AssertTotal
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step:    idx,
			Type:    "assert_total",
			Pattern: fmt.Sprintf("total == %d", step.AssertTotal),
			Passed:  passed,
			Actual:  fmt.Sprintf("have %d messages", msgCount),
			Context: r.messageRoleSummary(),
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert_total: expected %d messages, have %d\n%s", step.AssertTotal, msgCount, r.messageRoleSummary())
		}
	}

	// ─── Index-based assertions ───
	if step.AssertIndexRole != "" {
		idx := step.AssertIndex
		msgs := r.model.messages
		if idx < 0 || idx >= len(msgs) {
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_index_role",
				Pattern: fmt.Sprintf("[%d].role == %q", idx, step.AssertIndexRole),
				Passed:  false,
				Actual:  fmt.Sprintf("index %d out of range (have %d messages)", idx, len(msgs)),
			})
			r.result.OK = false
			return fmt.Errorf("assert_index: index %d out of range (have %d messages)", idx, len(msgs))
		}
		passed := msgs[idx].role == step.AssertIndexRole
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step: idx, Type: "assert_index_role",
			Pattern: fmt.Sprintf("[%d].role == %q", idx, step.AssertIndexRole),
			Passed:  passed,
			Actual:  fmt.Sprintf("messages[%d].role = %q", idx, msgs[idx].role),
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert_index_role: messages[%d].role = %q, expected %q",
				idx, msgs[idx].role, step.AssertIndexRole)
		}

		// Also check content at this index
		if step.AssertContent != "" {
			found := strings.Contains(msgs[idx].content, step.AssertContent)
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_index_content",
				Pattern: fmt.Sprintf("[%d] contains %q", idx, step.AssertContent),
				Passed:  found,
				Actual:  fmt.Sprintf("content = %q (len %d)", truncateStr(msgs[idx].content, 50), len(msgs[idx].content)),
			})
			if !found {
				r.result.OK = false
				return fmt.Errorf("assert_index_content: messages[%d] does not contain %q",
					idx, step.AssertContent)
			}
		}
	}

	// ─── Message order assertion ───
	if len(step.AssertMessageOrder) > 0 {
		msgs := r.model.messages
		actualRoles := make([]string, len(msgs))
		for i, msg := range msgs {
			actualRoles[i] = msg.role
		}
		passed := len(actualRoles) >= len(step.AssertMessageOrder)
		mismatch := ""
		if passed {
			for i, expected := range step.AssertMessageOrder {
				if actualRoles[i] != expected {
					passed = false
					mismatch = fmt.Sprintf("at index %d: expected %q, got %q", i, expected, actualRoles[i])
					break
				}
			}
		} else {
			mismatch = fmt.Sprintf("expected %d messages, have %d", len(step.AssertMessageOrder), len(actualRoles))
		}
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step:    idx,
			Type:    "assert_message_order",
			Pattern: fmt.Sprintf("%v", step.AssertMessageOrder),
			Passed:  passed,
			Actual:  fmt.Sprintf("%v", actualRoles),
			Context: mismatch,
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert_message_order: %s\nexpected: %v\nactual:   %v", mismatch, step.AssertMessageOrder, actualRoles)
		}
	}

	// ─── Tool timing assertion ───
	if step.AssertToolName != "" {
		var toolElapsed int
		found := false
		for _, msg := range r.model.messages {
			if msg.role == "tool_summary" {
				for _, it := range msg.iterations {
					for _, t := range it.Tools {
						if t.Name == step.AssertToolName {
							toolElapsed = int(t.Elapsed)
							found = true
						}
					}
				}
			}
		}
		if !found {
			r.result.Assertions = append(r.result.Assertions, SimAssertion{
				Step: idx, Type: "assert_tool_elapsed",
				Pattern: fmt.Sprintf("tool %q exists", step.AssertToolName),
				Passed:  false,
				Actual:  "tool not found in any tool_summary",
			})
			r.result.OK = false
			return fmt.Errorf("assert_tool_elapsed: tool %q not found", step.AssertToolName)
		}
		passed := true
		if step.AssertToolMinMs > 0 && toolElapsed < step.AssertToolMinMs {
			passed = false
		}
		if step.AssertToolMaxMs > 0 && toolElapsed > step.AssertToolMaxMs {
			passed = false
		}
		desc := fmt.Sprintf("tool %q elapsed=%dms", step.AssertToolName, toolElapsed)
		if step.AssertToolMinMs > 0 {
			desc += fmt.Sprintf(" >=%dms", step.AssertToolMinMs)
		}
		if step.AssertToolMaxMs > 0 {
			desc += fmt.Sprintf(" <=%dms", step.AssertToolMaxMs)
		}
		r.result.Assertions = append(r.result.Assertions, SimAssertion{
			Step: idx, Type: "assert_tool_elapsed",
			Pattern: desc,
			Passed:  passed,
			Actual:  fmt.Sprintf("%dms", toolElapsed),
		})
		if !passed {
			r.result.OK = false
			return fmt.Errorf("assert_tool_elapsed: %s (actual %dms)", desc, toolElapsed)
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

func (r *simRunner) doInspect(idx int, step SimStep) error {
	insp := SimInspection{Step: idx, Label: step.Label}

	// Always include view summary
	view := r.captureView()
	if len(view) > 500 {
		insp.ViewSummary = view[:500] + "..."
	} else {
		insp.ViewSummary = view
	}

	if step.InspectAll {
		insp.Messages = r.dumpMessages()
		insp.Vars = r.dumpVars()
		insp.State = r.dumpState()
	} else {
		if step.InspectMessages {
			insp.Messages = r.dumpMessages()
		}
		if len(step.InspectVars) > 0 {
			insp.Vars = r.dumpSpecificVars(step.InspectVars)
		}
		// Default: dump messages + state
		if !step.InspectMessages && len(step.InspectVars) == 0 {
			insp.Messages = r.dumpMessages()
			insp.State = r.dumpState()
		}
	}

	r.result.Inspections = append(r.result.Inspections, insp)
	return nil
}

func (r *simRunner) doQueueAdd(idx int, step SimStep) error {
	r.model.messageQueue = append(r.model.messageQueue, step.QueueMessages...)
	return nil
}

func (r *simRunner) doClear(idx int, step SimStep) error {
	m := r.model
	m.messages = nil
	m.cachedHistory = ""
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

func (r *simRunner) doSummary(idx int, step SimStep) error {
	m := r.model
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Summary (step %d, %dx%d)\n\n", idx, m.width, m.height)

	// State overview
	fmt.Fprintf(&sb, "**State**: typing=%v cancelled=%v inputReady=%v msgs=%d iterHist=%d queueLen=%d\n\n",
		m.typing, m.turnCancelled, m.inputReady, len(m.messages), len(m.iterationHistory), len(m.messageQueue))

	// Messages table
	if len(m.messages) > 0 {
		sb.WriteString("| # | Role | TurnID | Content (first 50) | Iterations | Tools |\n")
		sb.WriteString("|---|------|--------|-------------------|------------|-------|\n")
		for i, msg := range m.messages {
			content := truncateStr(msg.content, 50)
			iterCount := len(msg.iterations)
			var toolNames []string
			for _, it := range msg.iterations {
				for _, t := range it.Tools {
					name := t.Name
					if t.Status == "error" {
						name += "(!)"
					}
					toolNames = append(toolNames, name)
				}
			}
			toolStr := strings.Join(toolNames, ", ")
			if toolStr == "" {
				toolStr = "-"
			}
			fmt.Fprintf(&sb, "| %d | %s | %d | %s | %d | %s |\n",
				i, msg.role, msg.turnID, content, iterCount, toolStr)
		}
		sb.WriteString("\n")
	}

	// View preview (first 10 lines)
	view := r.captureView()
	lines := strings.Split(view, "\n")
	maxLines := 10
	if len(lines) < maxLines {
		maxLines = len(lines)
	}
	sb.WriteString("**View preview**:\n```\n")
	for i := 0; i < maxLines; i++ {
		sb.WriteString(lines[i] + "\n")
	}
	if len(lines) > maxLines {
		fmt.Fprintf(&sb, "... (%d more lines)\n", len(lines)-maxLines)
	}
	sb.WriteString("```\n")

	insp := SimInspection{
		Step:    idx,
		Label:   step.Label,
		Summary: sb.String(),
	}
	r.result.Inspections = append(r.result.Inspections, insp)
	return nil
}

func (r *simRunner) doExport(idx int, step SimStep) error {
	if step.ExportPath == "" {
		return fmt.Errorf("export_path is required for export action")
	}

	// Convert current messages to SimHistoryMsg format
	history := make([]SimHistoryMsg, len(r.model.messages))
	for i, msg := range r.model.messages {
		hm := SimHistoryMsg{
			Role:    msg.role,
			Content: msg.content,
		}
		if len(msg.iterations) > 0 {
			hm.Iterations = make([]struct {
				Iteration int             `json:"iteration"`
				Thinking  string          `json:"thinking,omitempty"`
				Reasoning string          `json:"reasoning,omitempty"`
				Tools     []SimToolRecord `json:"tools,omitempty"`
			}, len(msg.iterations))
			for j, it := range msg.iterations {
				hm.Iterations[j].Iteration = it.Iteration
				hm.Iterations[j].Thinking = it.Thinking
				hm.Iterations[j].Reasoning = it.Reasoning
				if len(it.Tools) > 0 {
					hm.Iterations[j].Tools = make([]SimToolRecord, len(it.Tools))
					for k, t := range it.Tools {
						hm.Iterations[j].Tools[k] = SimToolRecord{
							Name:    t.Name,
							Label:   t.Label,
							Status:  t.Status,
							Elapsed: int(t.Elapsed),
						}
					}
				}
			}
		}
		history[i] = hm
	}

	// Write as a scenario history file (just the history array)
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal history: %v", err)
	}
	if err := os.WriteFile(step.ExportPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write export file: %v", err)
	}

	// Record the export in an inspection
	r.result.Inspections = append(r.result.Inspections, SimInspection{
		Step:    idx,
		Label:   step.Label,
		Summary: fmt.Sprintf("Exported %d messages to %s", len(history), step.ExportPath),
	})
	return nil
}

func (r *simRunner) doDiff(idx int, step SimStep) error {
	var fromSnap, toSnap *SimSnapshot
	for i := range r.result.Snapshots {
		if r.result.Snapshots[i].Label == step.DiffFrom {
			fromSnap = &r.result.Snapshots[i]
		}
		if r.result.Snapshots[i].Label == step.DiffTo {
			toSnap = &r.result.Snapshots[i]
		}
	}
	if fromSnap == nil {
		return fmt.Errorf("diff: snapshot %q not found", step.DiffFrom)
	}
	if toSnap == nil {
		return fmt.Errorf("diff: snapshot %q not found", step.DiffTo)
	}

	fromLines := strings.Split(fromSnap.View, "\n")
	toLines := strings.Split(toSnap.View, "\n")

	// Simple line-based diff
	fromSet := make(map[string]int)
	toSet := make(map[string]int)
	for _, l := range fromLines {
		fromSet[l]++
	}
	for _, l := range toLines {
		toSet[l]++
	}

	added, removed, modified := 0, 0, 0
	for l, cnt := range toSet {
		if fromSet[l] == 0 {
			added += cnt
		}
	}
	for l, cnt := range fromSet {
		if toSet[l] == 0 {
			removed += cnt
		}
	}
	// Count lines that changed position/content
	maxLen := len(fromLines)
	if len(toLines) > maxLen {
		maxLen = len(toLines)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(fromLines) && i < len(toLines) && fromLines[i] != toLines[i] {
			modified++
		}
	}

	summary := fmt.Sprintf("Lines: %d → %d. Added: %d, Removed: %d, Modified: %d",
		len(fromLines), len(toLines), added, removed, modified)

	r.result.Diffs = append(r.result.Diffs, SimDiff{
		Step: idx, From: step.DiffFrom, To: step.DiffTo,
		Added: added, Removed: removed, Modified: modified,
		Summary: summary,
	})
	return nil
}

func (r *simRunner) doLoop(idx int, step SimStep) error {
	if step.LoopCount <= 0 {
		return fmt.Errorf("loop_count must be > 0")
	}
	if len(step.LoopSteps) == 0 {
		return fmt.Errorf("loop_steps is empty")
	}
	for i := 0; i < step.LoopCount; i++ {
		for j, subStep := range step.LoopSteps {
			if err := r.processStep(idx, subStep); err != nil {
				return fmt.Errorf("loop[%d].step[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}

func (r *simRunner) doInclude(idx int, step SimStep) error {
	if step.IncludePath == "" {
		return fmt.Errorf("include_path is required")
	}
	data, err := os.ReadFile(step.IncludePath)
	if err != nil {
		return fmt.Errorf("failed to read include file: %v", err)
	}
	var steps []SimStep
	if err := json.Unmarshal(data, &steps); err != nil {
		return fmt.Errorf("failed to parse include file: %v", err)
	}
	for i, s := range steps {
		if err := r.processStep(idx, s); err != nil {
			return fmt.Errorf("include[%d]: %w", i, err)
		}
	}
	return nil
}

func (r *simRunner) doSubAgent(idx int, step SimStep) error {
	m := r.model
	var agents []CLISubAgent
	for _, sa := range step.SubAgents {
		agents = append(agents, convertSimSubAgent(sa))
	}
	if len(agents) > 0 {
		payload := &CLIProgressPayload{
			Phase:     "thinking",
			SubAgents: agents,
			ChatID:    m.channelName + ":" + m.chatID,
		}
		m.Update(cliProgressMsg{payload: payload})
		m.renderCacheValid = false
		m.updateViewportContent()
	}
	return nil
}

func (r *simRunner) doSystemMsg(idx int, step SimStep) error {
	m := r.model
	content := step.Content
	switch step.Level {
	case "error", "err":
		content = "✗ " + content
	case "warn", "warning":
		content = "⚠ " + content
	default:
		content = "ℹ " + content
	}
	m.appendSystem(content)
	m.renderCacheValid = false
	m.updateViewportContent()
	return nil
}

// doTurn is a shortcut that expands into: user_msg → [progress → phase_done]* → agent_msg.
func (r *simRunner) doTurn(idx int, step SimStep) error {
	// 1. User message
	if err := r.doUserMsg(idx, SimStep{Content: step.Content}); err != nil {
		return err
	}

	// 2a. Multi-iteration mode
	if len(step.TurnIterations) > 0 {
		for i, iter := range step.TurnIterations {
			// Progress: show active tools
			if len(iter.Tools) > 0 {
				activeTools := make([]SimToolRecord, len(iter.Tools))
				for j, t := range iter.Tools {
					activeTools[j] = SimToolRecord{
						Name:   t.Name,
						Label:  t.Label,
						Status: "active",
					}
				}
				if err := r.doProgress(idx, SimStep{
					Phase:       "thinking",
					Iteration:   i,
					ActiveTools: activeTools,
				}); err != nil {
					return err
				}
			}
			// Phase done: mark tools as done
			completedTools := make([]SimToolRecord, len(iter.Tools))
			for j, t := range iter.Tools {
				completedTools[j] = SimToolRecord{
					Name:    t.Name,
					Label:   t.Label,
					Status:  "done",
					Elapsed: t.Elapsed,
				}
			}
			if err := r.doPhaseDone(idx, SimStep{
				Iteration:      i,
				CompletedTools: completedTools,
			}); err != nil {
				return err
			}
		}
	} else {
		// 2b. Single-iteration mode (backward compatible)
		if len(step.ActiveTools) > 0 || step.Thinking != "" {
			progStep := SimStep{
				Phase:       "thinking",
				Iteration:   0,
				Thinking:    step.Thinking,
				Reasoning:   step.Reasoning,
				ActiveTools: step.ActiveTools,
			}
			if err := r.doProgress(idx, progStep); err != nil {
				return err
			}
		}
		if len(step.CompletedTools) > 0 || len(step.Tools) > 0 {
			doneStep := SimStep{
				Iteration:      0,
				CompletedTools: step.CompletedTools,
				Tools:          step.Tools,
			}
			if err := r.doPhaseDone(idx, doneStep); err != nil {
				return err
			}
		}
	}

	// 3. Agent response
	if step.Response != "" {
		if err := r.doAgentMsg(idx, SimStep{Content: step.Response}); err != nil {
			return err
		}
	}

	return nil
}

// ─── Dump helpers ──────────────────────────────────────────────────

func (r *simRunner) dumpMessages() []SimMessageDump {
	m := r.model
	dumps := make([]SimMessageDump, len(m.messages))
	for i, msg := range m.messages {
		dump := SimMessageDump{
			Index:      i,
			Role:       msg.role,
			TurnID:     msg.turnID,
			Content:    msg.content,
			ContentLen: len(msg.content),
			Dirty:      msg.dirty,
		}
		if len(msg.iterations) > 0 {
			dump.Iterations = make([]SimIterDump, len(msg.iterations))
			for j, it := range msg.iterations {
				dump.Iterations[j] = SimIterDump{
					Iteration: it.Iteration,
					Thinking:  it.Thinking,
					Reasoning: it.Reasoning,
				}
				if len(it.Tools) > 0 {
					dump.Iterations[j].Tools = make([]SimToolRecord, len(it.Tools))
					for k, t := range it.Tools {
						dump.Iterations[j].Tools[k] = SimToolRecord{
							Name:    t.Name,
							Label:   t.Label,
							Status:  t.Status,
							Elapsed: int(t.Elapsed),
						}
					}
				}
			}
		}
		dumps[i] = dump
	}
	return dumps
}

func (r *simRunner) dumpState() *SimModelSnapshot {
	m := r.model
	snap := &SimModelSnapshot{
		Width:         m.width,
		Height:        m.height,
		Typing:        m.typing,
		TurnCancelled: m.turnCancelled,
		InputReady:    m.inputReady,
		AgentTurnID:   m.agentTurnID,
		MessageCount:  len(m.messages),
		IterHistCount: len(m.iterationHistory),
		LastSeenIter:  m.lastSeenIteration,
		RemoteMode:    m.remoteMode,
		QueueLen:      len(m.messageQueue),
		ViewportAtTop: m.viewport.AtTop(),
		ViewportAtBot: m.viewport.AtBottom(),
		TotalLines:    m.viewport.TotalLineCount(),
	}
	if m.progress != nil {
		snap.ProgressPhase = m.progress.Phase
	}
	return snap
}

func (r *simRunner) dumpVars() map[string]any {
	m := r.model
	return map[string]any{
		"width":             m.width,
		"height":            m.height,
		"typing":            m.typing,
		"turnCancelled":     m.turnCancelled,
		"inputReady":        m.inputReady,
		"agentTurnID":       m.agentTurnID,
		"lastSeenIteration": m.lastSeenIteration,
		"messageCount":      len(m.messages),
		"iterHistCount":     len(m.iterationHistory),
		"remoteMode":        m.remoteMode,
		"queueLen":          len(m.messageQueue),
		"splashDone":        m.splashDone,
		"ready":             m.ready,
	}
}

func (r *simRunner) dumpSpecificVars(names []string) map[string]any {
	all := r.dumpVars()
	result := make(map[string]any, len(names))
	for _, name := range names {
		if v, ok := all[name]; ok {
			result[name] = v
		} else {
			result[name] = "<unknown>"
		}
	}
	return result
}

// ─── Utility helpers ───────────────────────────────────────────────

// messageRoleSummary returns a one-line summary of all messages by role.
func (r *simRunner) messageRoleSummary() string {
	roleCounts := make(map[string]int)
	for _, msg := range r.model.messages {
		roleCounts[msg.role]++
	}
	parts := make([]string, 0, len(roleCounts))
	for role, count := range roleCounts {
		parts = append(parts, fmt.Sprintf("%s=%d", role, count))
	}
	slices.Sort(parts)
	return fmt.Sprintf("messages: %s (total=%d)", strings.Join(parts, ", "), len(r.model.messages))
}

func (r *simRunner) captureView() string {
	return stripAnsi(r.model.View().Content)
}

func convertSimTools(tools []SimToolRecord, iteration int) []CLIToolProgress {
	result := make([]CLIToolProgress, len(tools))
	for i, t := range tools {
		label := t.Label
		if label == "" {
			label = t.Name
		}
		result[i] = CLIToolProgress{
			Name:      t.Name,
			Label:     label,
			Status:    t.Status,
			Elapsed:   int64(t.Elapsed),
			Iteration: iteration,
		}
	}
	return result
}

func convertSimSubAgent(sa SimSubAgent) CLISubAgent {
	agent := CLISubAgent{
		Role:     sa.Role,
		Instance: sa.Instance,
		Status:   sa.Status,
		Desc:     sa.Task,
	}
	for _, child := range sa.Children {
		agent.Children = append(agent.Children, convertSimSubAgent(child))
	}
	return agent
}

// extractContext returns text around the first occurrence of needle.
func extractContext(haystack, needle string, radius int) string {
	idx := strings.Index(haystack, needle)
	if idx < 0 {
		return ""
	}
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + radius
	if end > len(haystack) {
		end = len(haystack)
	}
	return "..." + haystack[start:end] + "..."
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func sortedKeys[M ~map[K]V, K comparable, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, fmt.Sprint(k))
	}
	slices.Sort(keys)
	return keys
}

// ─── Test entry point ──────────────────────────────────────────────

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

	out, _ := json.MarshalIndent(result, "", "  ")
	outputPath := os.Getenv("XBOT_SIM_OUTPUT")
	if outputPath != "" {
		if err := os.WriteFile(outputPath, out, 0644); err != nil {
			t.Fatalf("Failed to write output: %v", err)
		}
		// Generate human-readable report alongside JSON output
		humanPath := os.Getenv("XBOT_SIM_HUMAN")
		if humanPath == "" && outputPath != "" {
			humanPath = strings.TrimSuffix(outputPath, ".json") + ".md"
		}
		if humanPath != "" {
			report := generateHumanReport(result)
			if err := os.WriteFile(humanPath, []byte(report), 0644); err != nil {
				// Non-fatal: human report is a convenience feature
				fmt.Fprintf(os.Stderr, "Warning: failed to write human report: %v\n", err)
			}
		}
	} else {
		fmt.Println(string(out))
	}
	if !result.OK {
		t.Errorf("Simulation failed: %s", result.Error)
	}
}

// ─── Built-in tests ────────────────────────────────────────────────

// generateHumanReport creates a markdown-formatted report from simulation results.
func generateHumanReport(r SimResult) string {
	var sb strings.Builder

	status := "✓ PASS"
	if !r.OK {
		status = "✗ FAIL"
	}
	fmt.Fprintf(&sb, "# TUI Simulation Report %s\n\n", status)
	fmt.Fprintf(&sb, "**Steps**: %d/%d  **Snapshots**: %d  **Assertions**: %d  **Diffs**: %d\n\n",
		r.StepsOK, r.StepsTotal, len(r.Snapshots), len(r.Assertions), len(r.Diffs))

	if r.Error != "" {
		fmt.Fprintf(&sb, "## Error\n```\n%s\n```\n\n", r.Error)
	}

	// Assertions
	if len(r.Assertions) > 0 {
		sb.WriteString("## Assertions\n\n")
		for _, a := range r.Assertions {
			mark := "✓"
			if !a.Passed {
				mark = "✗"
			}
			fmt.Fprintf(&sb, "- %s [%s] %s", mark, a.Type, a.Pattern)
			if a.Actual != "" {
				fmt.Fprintf(&sb, " (%s)", a.Actual)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Diffs
	for _, d := range r.Diffs {
		fmt.Fprintf(&sb, "## Diff: %s → %s\n%s\n\n", d.From, d.To, d.Summary)
	}

	// Summaries from inspections
	for _, i := range r.Inspections {
		if i.Summary != "" {
			sb.WriteString(i.Summary)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

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

func TestSimProgressWithTools(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "read the file"},
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{{Name: "Read", Label: "Read main.go", Status: "active"}}},
			{Action: "assert", Contains: "Read"},
			{Action: "progress", Phase: "done", Iteration: 0,
				CompletedTools: []SimToolRecord{{Name: "Read", Label: "Read main.go", Status: "done", Elapsed: 150}}},
			{Action: "agent_msg", Content: "Here is main.go content..."},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}

func TestSimDiff(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "snapshot", Label: "s1"},
			{Action: "agent_msg", Content: "world"},
			{Action: "snapshot", Label: "s2"},
			{Action: "diff", DiffFrom: "s1", DiffTo: "s2"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Diffs) != 1 {
		t.Fatalf("Expected 1 diff, got %d", len(result.Diffs))
	}
	df := result.Diffs[0]
	if df.From != "s1" || df.To != "s2" {
		t.Errorf("Unexpected diff labels: %s → %s", df.From, df.To)
	}
	if df.Added == 0 && df.Modified == 0 {
		t.Error("Expected some changes between snapshots")
	}
}

func TestSimLoop(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "loop", LoopCount: 3, LoopSteps: []SimStep{
				{Action: "turn", Content: "msg", Response: "resp"},
			}},
			{Action: "assert", AssertRole: "user", AssertCount: 3},
			{Action: "assert", AssertRole: "assistant", AssertCount: 3},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}

func TestSimExport(t *testing.T) {
	tmpFile := "/tmp/sim_export_test_history.json"
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "turn", Content: "hello", Response: "world"},
			{Action: "export", ExportPath: tmpFile},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	// Verify exported file exists and is valid JSON
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("Export file not found: %v", err)
	}
	var history []SimHistoryMsg
	if err := json.Unmarshal(data, &history); err != nil {
		t.Fatalf("Invalid export JSON: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("Expected 2 exported messages, got %d", len(history))
	}
	if history[0].Role != "user" || history[1].Role != "assistant" {
		t.Errorf("Unexpected roles: %s, %s", history[0].Role, history[1].Role)
	}
	os.Remove(tmpFile)
}

func TestSimSummary(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "turn", Content: "hello", Response: "world"},
			{Action: "summary", Label: "test_summary"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Inspections) != 1 {
		t.Fatalf("Expected 1 inspection, got %d", len(result.Inspections))
	}
	insp := result.Inspections[0]
	if insp.Summary == "" {
		t.Error("Expected non-empty summary")
	}
	if !strings.Contains(insp.Summary, "user") || !strings.Contains(insp.Summary, "assistant") {
		t.Errorf("Summary should contain role names: %s", insp.Summary[:100])
	}
}

func TestSimClearAndAssertTotal(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "turn", Content: "hello", Response: "Hi!"},
			{Action: "turn", Content: "bye", Response: "Bye!"},
			{Action: "assert", AssertTotal: 4}, // 2 user + 2 assistant
			{Action: "clear"},
			{Action: "assert", AssertTotal: 0},
			{Action: "turn", Content: "fresh start", Response: "Hello again!"},
			{Action: "assert", AssertTotal: 2}, // 1 user + 1 assistant
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	// Verify all 4 assertions passed
	for _, a := range result.Assertions {
		if !a.Passed {
			t.Errorf("Unexpected failure: %v", a)
		}
	}
}

func TestSimTurnMultiIteration(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "turn", Content: "analyze and fix",
				TurnIterations: []SimTurnIter{
					{Tools: []SimToolRecord{
						{Name: "Grep", Label: "Grep TODO", Elapsed: 200},
						{Name: "Read", Label: "Read file.go", Elapsed: 100},
					}},
					{Tools: []SimToolRecord{
						{Name: "FileReplace", Label: "Fix bug", Elapsed: 50},
					}},
					{Tools: []SimToolRecord{
						{Name: "Shell", Label: "Shell go test", Elapsed: 3000},
					}},
				},
				Response: "Fixed and verified!",
			},
			{Action: "inspect", Label: "multi_iter"},
			{Action: "assert", AssertRole: "user", AssertCount: 1},
			{Action: "assert", AssertRole: "tool_summary", AssertCount: 3},
			{Action: "assert", AssertRole: "tool_summary", AssertTools: []string{"Grep", "Read", "FileReplace", "Shell"}},
			{Action: "assert", AssertRole: "assistant", AssertContent: "Fixed and verified"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	// Verify the tool_summary messages cover all 3 iterations
	insp := result.Inspections[0]
	iterCounts := 0
	for _, m := range insp.Messages {
		if m.Role == "tool_summary" {
			iterCounts += len(m.Iterations)
		}
	}
	if iterCounts != 3 {
		t.Errorf("Expected 3 total iterations across tool_summaries, got %d", iterCounts)
	}
}

func TestSimAssertIndex(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "agent_msg", Content: "world"},
			// Verify exact message structure by index
			{Action: "assert", AssertIndex: 0, AssertIndexRole: "user"},
			{Action: "assert", AssertIndex: 1, AssertIndexRole: "assistant", AssertContent: "world"},
			// Negative: wrong role should fail
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Assertions) != 3 {
		t.Errorf("Expected 3 assertions, got %d", len(result.Assertions))
	}
	for _, a := range result.Assertions {
		if !a.Passed {
			t.Errorf("Assertion failed: %v", a)
		}
	}
}

func TestSimCancelAndRewind(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "first"},
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{{Name: "Read", Label: "Read f1", Status: "active"}}},
			{Action: "progress", Phase: "done", Iteration: 0,
				CompletedTools: []SimToolRecord{{Name: "Read", Label: "Read f1", Status: "done", Elapsed: 100}}},
			{Action: "agent_msg", Content: "First response"},

			{Action: "user_msg", Content: "second"},
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{{Name: "Shell", Label: "Shell ls", Status: "active"}}},
			{Action: "cancel"},
			{Action: "phase_done", Iteration: 0,
				CompletedTools: []SimToolRecord{{Name: "Shell", Label: "Shell ls", Status: "done", Elapsed: 50}}},

			{Action: "user_msg", Content: "third"},
			{Action: "phase_done", Iteration: 0},
			{Action: "agent_msg", Content: "Third response"},

			{Action: "rewind", RewindIndex: 0},
			{Action: "assert", AssertRole: "tool_summary", AssertCount: 2},
			{Action: "assert", AssertRole: "tool_summary", AssertTools: []string{"Shell"}},
			{Action: "assert", NotContains: "Third response"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}

func TestSimResize(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "resize", NewWidth: 60, NewHeight: 20},
			{Action: "assert", Matches: "hello"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if runner.model.width != 60 || runner.model.height != 20 {
		t.Errorf("Expected 60x20, got %dx%d", runner.model.width, runner.model.height)
	}
}

func TestSimInspect(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "agent_msg", Content: "world"},
			{Action: "inspect", Label: "after_turn", InspectMessages: true,
				InspectVars: []string{"typing", "messageCount"}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Inspections) != 1 {
		t.Fatalf("Expected 1 inspection, got %d", len(result.Inspections))
	}
	insp := result.Inspections[0]
	if len(insp.Messages) != 2 {
		t.Errorf("Expected 2 messages in dump, got %d", len(insp.Messages))
	}
	if insp.Messages[0].Role != "user" || insp.Messages[1].Role != "assistant" {
		t.Errorf("Unexpected roles: %s, %s", insp.Messages[0].Role, insp.Messages[1].Role)
	}
	if insp.Vars["messageCount"] != 2 {
		t.Errorf("Expected messageCount=2, got %v", insp.Vars["messageCount"])
	}
}

func TestSimAssertRoleCount(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "msg1"},
			{Action: "progress", Phase: "done", Iteration: 0},
			{Action: "agent_msg", Content: "resp1"},
			{Action: "user_msg", Content: "msg2"},
			{Action: "progress", Phase: "done", Iteration: 0},
			{Action: "agent_msg", Content: "resp2"},
			{Action: "assert", AssertRole: "user", AssertCount: 2},
			{Action: "assert", AssertRole: "assistant", AssertCount: 2},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}

func TestSimQueueMessages(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "first"},
			{Action: "queue_add", QueueMessages: []string{"queued msg 1", "queued msg 2"}},
			{Action: "inspect", Label: "with_queue", InspectVars: []string{"queueLen"}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	insp := result.Inspections[0]
	if insp.Vars["queueLen"] != 2 {
		t.Errorf("Expected queueLen=2, got %v", insp.Vars["queueLen"])
	}
}

func TestSimAutoInspectionOnFailure(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "assert", Contains: "this_text_does_not_exist"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if result.OK {
		t.Fatal("Expected simulation to fail")
	}
	// Check auto-inspection was captured
	autoInspCount := 0
	for _, insp := range result.Inspections {
		if insp.Label == "auto_on_failure" {
			autoInspCount++
			if len(insp.Messages) == 0 {
				t.Error("Auto-inspection should have messages")
			}
			if insp.State == nil {
				t.Error("Auto-inspection should have state")
			}
		}
	}
	if autoInspCount != 1 {
		t.Errorf("Expected 1 auto-inspection, got %d", autoInspCount)
	}
}

func TestSimSubAgent(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "explore the codebase"},
			{Action: "subagent", SubAgents: []SimSubAgent{
				{Role: "explore", Instance: "exp1", Status: "running", Task: "Explore code"},
				{Role: "explore", Instance: "exp2", Status: "pending", Task: "Read files"},
			}},
			{Action: "snapshot", Label: "with_subagents"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Snapshots) != 1 {
		t.Errorf("Expected 1 snapshot, got %d", len(result.Snapshots))
	}
}

func TestSimStreaming(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "write a function"},
			// Reasoning streaming phase
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ReasoningStreamContent: "Let me think about this..."},
			{Action: "snapshot", Label: "reasoning_start"},
			// More reasoning accumulated
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ReasoningStreamContent: "Let me think about this... I need to consider edge cases and error handling."},
			// Tools
			{Action: "progress", Phase: "thinking", Iteration: 0,
				ActiveTools: []SimToolRecord{{Name: "FileCreate", Label: "Create func.go", Status: "active"}}},
			{Action: "progress", Phase: "done", Iteration: 0,
				CompletedTools: []SimToolRecord{{Name: "FileCreate", Label: "Create func.go", Status: "done", Elapsed: 100}}},
			// Content streaming phase
			{Action: "progress", Phase: "thinking", Iteration: 1,
				StreamContent: "Here is the function:\n\n```go\nfunc add(a, b int) int {"},
			{Action: "snapshot", Label: "streaming_content"},
			{Action: "progress", Phase: "thinking", Iteration: 1,
				StreamContent: "Here is the function:\n\n```go\nfunc add(a, b int) int {\n    return a + b\n}\n```"},
			{Action: "agent_msg", Content: "Here is the function:\n\n```go\nfunc add(a, b int) int {\n    return a + b\n}\n```"},
			{Action: "assert", AssertRole: "assistant", AssertCount: 1},
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
}

func TestSimHistoryPreload(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		History: []SimHistoryMsg{
			{Role: "user", Content: "previous question"},
			{Role: "assistant", Content: "previous answer"},
		},
		Steps: []SimStep{
			{Action: "inspect", Label: "preloaded", InspectMessages: true},
			{Action: "assert", AssertRole: "user", AssertCount: 1},
			{Action: "assert", AssertRole: "assistant", AssertCount: 1},
			{Action: "assert", AssertRole: "assistant", AssertContent: "previous answer"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
	if len(result.Inspections) != 1 {
		t.Fatalf("Expected 1 inspection")
	}
	if len(result.Inspections[0].Messages) != 2 {
		t.Errorf("Expected 2 preloaded messages, got %d", len(result.Inspections[0].Messages))
	}
}

func TestSimSystemMsg(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			{Action: "user_msg", Content: "hello"},
			{Action: "system_msg", Content: "Connected to server"},
			{Action: "system_msg", Content: "API rate limit exceeded", Level: "error"},
			{Action: "system_msg", Content: "Retrying in 5s", Level: "warn"},
			{Action: "inspect", Label: "with_system_msgs", InspectMessages: true},
			{Action: "assert", AssertRole: "system", AssertCount: 3},
			{Action: "assert", AssertRole: "system", AssertContent: "rate limit"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}

func TestSimTurnShortcut(t *testing.T) {
	scenario := SimScenario{
		Config: SimConfig{Width: 120, Height: 40},
		Steps: []SimStep{
			// Simple turn with no tools
			{Action: "turn", Content: "hello", Response: "Hi there!"},
			// Turn with tools
			{Action: "turn", Content: "read the file",
				ActiveTools:    []SimToolRecord{{Name: "Read", Label: "Read main.go", Status: "active"}},
				CompletedTools: []SimToolRecord{{Name: "Read", Label: "Read main.go", Status: "done", Elapsed: 100}},
				Response:       "Here is main.go..."},
			// Verify
			{Action: "assert", AssertRole: "user", AssertCount: 2},
			{Action: "assert", AssertRole: "assistant", AssertCount: 2},
			{Action: "assert", AssertRole: "tool_summary", AssertCount: 1},
			{Action: "assert", AssertRole: "assistant", AssertContent: "Here is main.go"},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Simulation failed: %s", result.Error)
	}
}
