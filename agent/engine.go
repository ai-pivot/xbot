package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"
)

// SubAgentProgressCallback is the type for SubAgent progress callback.
// It carries depth information for recursive SubAgent progress penetration.
type SubAgentProgressCallback func(detail SubAgentProgressDetail)

type subAgentProgressKey struct{}

// SubAgentProgressFromContext extracts the SubAgent progress callback from context.
func SubAgentProgressFromContext(ctx context.Context) (SubAgentProgressCallback, bool) {
	cb, ok := ctx.Value(subAgentProgressKey{}).(SubAgentProgressCallback)
	return cb, ok
}

// WithSubAgentProgress returns a new context with the SubAgent progress callback.
func WithSubAgentProgress(ctx context.Context, cb SubAgentProgressCallback) context.Context {
	return context.WithValue(ctx, subAgentProgressKey{}, cb)
}

// RunConfig unified Agent runtime configuration.
// Main Agent and SubAgent use the same Run() method, differences injected via configuration.
type RunConfig struct {
	// === Required ===
	LLMClient    llm.LLM
	Model        string
	ThinkingMode string // Thinking mode (e.g. "enabled", "auto")
	Stream       bool   // Use streaming API for LLM calls (compatible with Copilot and other proxies)
	Tools        *tools.Registry
	Messages     []llm.ChatMessage

	// === Identity (extracted from InboundMessage) ===
	AgentID      string // "main", "main/code-reviewer"
	Channel      string // Original IM channel (for ToolContext)
	ChatID       string // Original IM session
	SenderID     string // Direct caller ID (parent Agent ID in SubAgent scenario)
	OriginUserID string // Original user ID (always the end user, for LLM configuration, workspace path, etc.)
	SenderName   string
	FeishuUserID string // 非空表示通过飞书Identity登录 web（用于 runner 路由）

	// === Workspace & Sandbox ===
	WorkingDir          string   // Agent working directory (host machine)
	WorkspaceRoot       string   // User read-write workspace root directory (host machine path)
	ReadOnlyRoots       []string // Additional read-only directories
	SkillsDirs          []string // Global skill directory list
	AgentsDir           string
	MCPConfigPath       string        // User MCP configuration path
	GlobalMCPConfig     string        // Global MCP configuration path (read-only)
	DataDir             string        // Data persistence directory
	SandboxEnabled      bool          // Whether to enable command sandbox
	PreferredSandbox    string        // Sandbox type (docker preferred)
	Sandbox             tools.Sandbox // Sandbox instance reference (added in V4)
	SandboxMode         string        // Actual sandbox mode: "none", "docker", "remote"
	InitialCWD          string        // Initial current working directory (host machine path, for SubAgent inheriting parent Agent's CWD)
	InitialGroupID      string        // Group ID (inherited by SubAgent, for SendMessage cross-group validation)
	InitialGroupMembers []string      // Group member list (for system prompt injection)

	// === Loop control ===
	MaxIterations   int // 0 = use default value 100
	MaxOutputTokens int // 0 = use LLM client default (DefaultMaxOutputTokens)

	// === Optional capabilities (nil = disabled) ===

	// Session persistence (nil = in-memory only, no persistence)
	Session *session.TenantSession

	// SessionKey session key for tool activation (generated from Channel+ChatID when empty)
	SessionKey string

	// RootSessionKey top-level Agent's session key.
	// In SubAgent scenario, points to main Agent's session key, for scenarios like offload_recall that need parent session data access.
	// Empty in main Agent scenario (same as SessionKey).
	RootSessionKey string

	// ProgressNotifier: progress notification callback (nil = no notification)
	ProgressNotifier func(lines []string)

	// ProgressEventHandler: structured progress event callback (nil = don't send)
	ProgressEventHandler func(event *ProgressEvent)

	// ContextManager context manager (nil = no compression)
	ContextManager ContextManager

	// ContextManagerConfig context manager configuration (Phase 2 smart trigger needs access to MaxContextTokens etc.)
	ContextManagerConfig *ContextManagerConfig

	// SendFunc: send message to IM channel (nil = cannot send)
	SendFunc func(channel, chatID, content string, metadata ...map[string]string) error

	// InjectInbound injects inbound message, triggering full Agent processing loop (nil = not supported)
	InjectInbound func(channel, chatID, senderID, content string)

	// Memory memory provider (nil = no memory)
	Memory memory.MemoryProvider

	// ToolContextExtras Letta memory-related ToolContext extension fields
	ToolContextExtras *ToolContextExtras

	// SpawnAgent SubAgent creation capability (nil = cannot create child Agent)
	// Both input and output are unified messages: InboundMessage → OutboundMessage
	SpawnAgent func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)

	// OAuthHandler OAuth auto-trigger handler (nil = don't handle OAuth)
	// Returns (content, handled): when handled=true, use content to replace tool error
	OAuthHandler func(ctx context.Context, tc llm.ToolCall, execErr error) (content string, handled bool)

	// ToolExecutor: tool execution function.
	// Main Agent injects full version with session MCP, activation check, Letta memory;
	// SubAgent uses nil (defaultToolExecutor looks up and executes from cfg.Tools).
	ToolExecutor func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error)

	// ToolTimeout is deprecated and no longer used for wrapping tool contexts.
	// Individual tools (e.g. Shell) manage their own timeouts.
	// Engine only passes through the parent context (user Ctrl+C cancels it).
	ToolTimeout time.Duration

	// EnableReadWriteSplit enables read-write split parallel execution (default false = all serial)
	EnableReadWriteSplit bool

	// SessionFinalSentCallback Callback when tool sends final reply (e.g. Feishu card).
	// Returns true if final reply was sent; subsequent progress notifications should stop.
	SessionFinalSentCallback func() bool

	// InteractiveCallbacks Interactive SubAgent callbacks (nil = no interactive support).
	// Injected by main Agent, not by SubAgent.
	InteractiveCallbacks *InteractiveCallbacks

	// HookManager tool execution hook manager (nil = no hooks).
	HookManager *hooks.Manager

	// SettingsSvc provides access to user settings (nil = settings not available).
	SettingsSvc *SettingsService

	// OffloadStore Layer 1 offload store (nil = disabled)
	OffloadStore *OffloadStore

	// MaskStore Observation Masking store (nil = disabled)
	MaskStore *ObservationMaskStore

	// ContextEditor Context Editing editor (nil = disabled)
	ContextEditor *ContextEditor

	// MemoryToolDefs memory tool definition list (nil = don't use memory tools during compression)
	MemoryToolDefs []llm.ToolDefinition

	// MemoryToolExec: memory tool execution function (nil = don't use memory tool during compression)
	MemoryToolExec func(ctx context.Context, tc llm.ToolCall) (content string, err error)

	// TodoManager TODO manager (optional)
	TodoManager TodoManagerProvider

	// DrainBgNotifications is called between iterations to check for completed bg tasks
	// and bg subagent notifications. Returns notifications that should be injected
	// as tool results into the current Run loop.
	// Returns nil when no notifications are pending. Called on each iteration.
	DrainBgNotifications func() []tools.BgNotification

	// LLMSemAcquire is called before each LLM call to acquire a per-tenant
	// concurrency slot. Returns a release function that must be called after
	// the LLM call completes. If nil, no concurrency limiting is applied.
	LLMSemAcquire func(context.Context) func()

	// RecordUserTokenUsage is called at the end of Run() to persist per-user
	// token usage (inputTokens, outputTokens, conversationCount, llmCallCount).
	// If nil, per-user tracking is skipped.
	RecordUserTokenUsage func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int)

	// EnableConcurrentSubAgents enables parallel execution of SubAgent tool calls.
	// When true, multiple SubAgent calls in the same iteration run concurrently,
	// bounded by SubAgentSem. Default false (backward compatible: sequential).
	EnableConcurrentSubAgents bool

	// SubAgentSem acquires a per-tenant semaphore slot for SubAgent execution.
	// It blocks until a slot is available and returns a release function.
	// If nil and EnableConcurrentSubAgents is true, no limit is applied.
	SubAgentSem func(context.Context) func()

	// LastPromptTokens is the prompt_tokens from the previous Run()'s last LLM call.
	// Restored from agent state or DB to avoid starting from 0 after restart.
	LastPromptTokens int64
	// LastCompletionTokens is the completion_tokens from the previous Run()'s last LLM call.
	LastCompletionTokens int64
	// SaveTokenState persists token counts after Run() completes.
	// Called with the final promptTokens and completionTokens values.
	// If nil, token counts are only kept in memory (lost on restart).
	SaveTokenState func(promptTokens, completionTokens int64)

	// BgTaskManager background task manager (nil = no background task support)
	BgTaskManager *tools.BackgroundTaskManager
	// MessageSender allows Agent to send messages to any Channel (IM, Agent, Group).
	// nil = disabled (SubAgent inherits main Agent's MessageSender).
	MessageSender bus.MessageSender
	// RegisterAgentChannel registers an AgentChannel in the Dispatcher.
	RegisterAgentChannel func(name string, runFn bus.RunFn) error
	// UnregisterAgentChannel removes an AgentChannel from the Dispatcher.
	UnregisterAgentChannel func(name string)

	// OnIterationSnapshot is called after each iteration snapshot is created.
	// Used by background interactive sessions to incrementally expose iteration
	// history for real-time inspect, instead of waiting for Run() to finish.
	OnIterationSnapshot func(snap IterationSnapshot)

	// StreamContentFunc is called with accumulated text content on each content delta
	// during LLM streaming. When set (and Stream=true), generateResponse uses
	// CollectStreamWithCallback instead of CollectStream. Nil by default (no streaming).
	StreamContentFunc func(content string)

	// StreamReasoningFunc is called with accumulated reasoning content on each
	// reasoning delta during LLM streaming. Nil by default (no reasoning streaming).
	StreamReasoningFunc func(content string)
}

// TodoManagerProvider provides TODO status query and cleanup
type TodoManagerProvider interface {
	GetTodoSummary(sessionKey string) string
	GetTodoItems(sessionKey string) []TodoProgressItem
	ClearTodos(sessionKey string)
}

// InteractiveCallbacks interactive callbacks provided by main Agent to buildToolContext.
type InteractiveCallbacks struct {
	SpawnFn     func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	SendFn      func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	UnloadFn    func(ctx context.Context, roleName, instance string) error
	InterruptFn func(ctx context.Context, roleName, instance string) error
	InspectFn   func(ctx context.Context, roleName, instance string, tail int) (string, error)
}

// ToolContextExtras Letta memory-related ToolContext extension fields。
// Only contains Letta memory-specific fields; common fields (InjectInbound, Registry, etc.)
// have been migrated to RunConfig.
type ToolContextExtras struct {
	TenantID                int64
	CoreMemory              *sqlite.CoreMemoryService
	ArchivalMemory          *vectordb.ArchivalService
	MemorySvc               *sqlite.MemoryService
	RecallTimeRange         vectordb.RecallTimeRangeFunc
	ToolIndexer             memory.ToolIndexer
	InvalidateAllSessionMCP func()
}

// DefaultMaxIterations default max iteration count.
const DefaultMaxIterations = 2000

// DefaultMaxOutputTokens is the default maximum output token count when
// not specified by config or model capabilities.
const DefaultMaxOutputTokens = 8192

// Tool name constants used for cross-reference with the tools package.
const (
	toolSubAgent      = "SubAgent"
	toolOffloadRecall = "offload_recall"
	toolRecallMasked  = "recall_masked"
	toolSendMessage   = "SendMessage"
	toolCreateChat    = "CreateChat"
)

// readOnlyTools read-only tool set, for read-write split parallel execution.
var readOnlyTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true,
	"WebSearch": true, "ChatHistory": true,
}

// RunOutput is the result of a Run() call.
// It extends OutboundMessage with internal messages needed for post-run processing
// (e.g., SubAgent memory consolidation).
type RunOutput struct {
	*bus.OutboundMessage
	// Messages contains the full conversation messages from the Run loop.
	// Only populated when Memory is set in RunConfig (used for memorize after exit).
	Messages []llm.ChatMessage
	// EngineMessages contains assistant+tool messages produced during the Run loop.
	// These are the messages appended to the original cfg.Messages during execution.
	// Used by processMessage to persist context when WaitingUser is true.
	EngineMessages []llm.ChatMessage
	// IterationHistory contains snapshots of completed iterations for UI display.
	IterationHistory []IterationSnapshot
	// LastPromptTokens is the prompt_tokens from the last LLM API call.
	// This is the authoritative token count for the full input (messages + tool defs).
	LastPromptTokens int64
	// LastCompletionTokens is the completion_tokens from the last LLM API call.
	LastCompletionTokens int64
	// ReasoningContent is the final response's reasoning_content (thinking).
	// Required for DeepSeek thinking mode — must be persisted so it can be
	// passed back to the API in subsequent turns.
	ReasoningContent string
}

// IterationSnapshot captures the tool summary of a completed iteration.
type IterationSnapshot struct {
	Iteration int                     `json:"iteration"`
	Thinking  string                  `json:"thinking,omitempty"`
	Reasoning string                  `json:"reasoning,omitempty"`
	Tools     []IterationToolSnapshot `json:"tools"`
}

// IterationToolSnapshot captures a single tool's execution result within an iteration.
type IterationToolSnapshot struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"` // done | error
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// readArgsHasOffsetOrLimit checks whether a Read tool call's JSON arguments contain
// offset > 0 or max_lines > 0. Used to skip offloading when the LLM intentionally
// narrowed the read range — offloading would replace actual content with a summary.
func readArgsHasOffsetOrLimit(argsJSON string) bool {
	var args struct {
		Offset   int `json:"offset"`
		MaxLines int `json:"max_lines"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	return args.Offset > 0 || args.MaxLines > 0
}

// Run unified Agent loop.
//
// Input: RunConfig (built from InboundMessage)
// Output: *RunOutput (can be sent directly to IM or returned to parent Agent)
//
// Main Agent and SubAgent use the same Run(), differences injected via RunConfig:
//   - Main Agent: ToolExecutor=buildToolExecutor, ProgressNotifier=sendMessage, ContextManager=enabled, ...

// generateResponse calls the LLM using non-streaming mode.
func generateResponse(ctx context.Context, client llm.LLM, model string, messages []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string, stream bool, streamContentFn func(string), streamReasoningFn func(string)) (*llm.LLMResponse, error) {
	if stream {
		if sc, ok := client.(llm.StreamingLLM); ok {
			eventCh, err := sc.GenerateStream(ctx, model, messages, tools, thinkingMode)
			if err != nil {
				return nil, err
			}
			if streamContentFn != nil || streamReasoningFn != nil {
				return llm.CollectStreamWithCallback(ctx, eventCh, streamContentFn, streamReasoningFn)
			}
			return llm.CollectStream(ctx, eventCh)
		}
		// Fallback: client doesn't support streaming, use non-stream
	}
	return client.Generate(ctx, model, messages, tools, thinkingMode)
}

// Run unified Agent loop.
//
// Input: RunConfig (built from InboundMessage)
// Output: *RunOutput (can be sent directly to IM or returned to parent Agent)
//
// Main Agent and SubAgent use the same Run(), differences injected via RunConfig:
//   - Main Agent: ToolExecutor=buildToolExecutor, ProgressNotifier=sendMessage, ContextManager=enabled, ...
//   - SubAgent: ToolExecutor=simpleExecutor, ProgressNotifier=nil, ContextManager=independent_phase1, ...
func Run(ctx context.Context, cfg RunConfig) *RunOutput {
	s := newRunState(cfg)

	// Cleanup completed TODOs on exit
	defer s.cleanupTodos()

	// Sync ContextEditor reference
	s.messages = s.syncMessages(s.messages)

	// Record conversation metrics on exit
	defer s.recordMetrics()

	// Setup structured progress tracking
	s.initProgress()

	// Ensure PhaseDone event is sent on exit
	if s.progressFinalizer != nil {
		defer s.progressFinalizer()
	}

	// Setup dynamic context injector for CWD change detection
	s.initDynamicInjector()

	// Advance round counter for tool activation cleanup
	s.tickSession()

	// Wrap context with LLM retry notification
	retryNotifyCtx := s.setupRetryNotify(ctx)

	// Emit AgentStop event on exit (notification, non-blocking)
	if s.cfg.HookManager != nil {
		defer func() {
			s.cfg.HookManager.Emit(ctx, &hooks.AgentStopEvent{
				BasePayload: hooks.BasePayload{
					SessionID: s.cfg.ChatID, Channel: s.cfg.Channel,
					SenderID: s.cfg.OriginUserID, ChatID: s.cfg.ChatID,
				},
			})
		}()
	}

	// Emit UserPromptSubmit event (notification, non-blocking)
	if s.cfg.HookManager != nil {
		prompt := ""
		for i := len(s.messages) - 1; i >= 0; i-- {
			if s.messages[i].Role == "user" {
				prompt = s.messages[i].Content
				break
			}
		}
		s.cfg.HookManager.Emit(ctx, &hooks.UserPromptSubmitEvent{
			BasePayload: hooks.BasePayload{
				SessionID: s.cfg.ChatID, Channel: s.cfg.Channel,
				SenderID: s.cfg.OriginUserID, ChatID: s.cfg.ChatID,
			},
			Prompt: prompt,
		})
	}

	// --- Main loop ---
	for i := 0; i < s.maxIter; i++ {
		// Check for cancellation before starting each iteration
		select {
		case <-ctx.Done():
			out := s.buildOutput(&bus.OutboundMessage{
				Channel: s.cfg.Channel,
				ChatID:  s.cfg.ChatID,
				Content: "Agent was cancelled.",
			})
			out.Error = ctx.Err()
			return out
		default:
		}

		s.beginIteration(i)
		s.maybeCompress(ctx)
		s.notifyThinking(i)

		if out := s.assertSystemMessages(ctx); out != nil {
			return out
		}

		response, err := s.callLLM(ctx, retryNotifyCtx)

		// If ctx was cancelled during LLM call, exit immediately
		if ctx.Err() != nil {
			out := s.buildOutput(&bus.OutboundMessage{
				Channel: s.cfg.Channel,
				ChatID:  s.cfg.ChatID,
				Content: "Agent was cancelled.",
			})
			out.Error = ctx.Err()
			return out
		}

		if out := s.handleLLMError(ctx, err, response, i); out != nil {
			return out
		}

		out, retry := s.handleFinalResponse(ctx, response)
		if retry {
			continue
		}
		if out != nil {
			return out
		}

		s.recordAssistantMsg(ctx, response)

		results := s.executeToolCalls(ctx, response, i)

		// Always process tool results (preserves engine messages for session continuity)
		s.processToolResults(ctx, response, results)

		// Emit PostToolBatch event (notification, non-blocking)
		if s.cfg.HookManager != nil && len(response.ToolCalls) > 0 {
			batchResults := make([]hooks.ToolBatchResult, len(response.ToolCalls))
			for idx, tc := range response.ToolCalls {
				r := results[idx]
				batchResults[idx] = hooks.ToolBatchResult{
					ToolName: tc.Name,
					Success:  r.err == nil && (r.result == nil || !r.result.IsError),
					Elapsed:  r.elapsed,
				}
				if r.err != nil {
					batchResults[idx].Error = r.err.Error()
				} else if r.result != nil && r.result.IsError {
					batchResults[idx].Error = r.result.Summary
				}
			}
			s.cfg.HookManager.Emit(ctx, &hooks.PostToolBatchEvent{
				BasePayload: hooks.BasePayload{
					SessionID: s.cfg.ChatID, Channel: s.cfg.Channel,
					SenderID: s.cfg.OriginUserID, ChatID: s.cfg.ChatID,
				},
				ToolCount: len(response.ToolCalls),
				Results:   batchResults,
			})
		}

		// If ctx was cancelled during tool execution, exit after preserving results
		if ctx.Err() != nil {
			// Strip trailing unpaired tool_calls so they don't get persisted
			// to DB and cause API errors on the next Run.
			s.messages = llm.FixupTrailingToolCalls(s.messages)
			out := s.buildOutput(&bus.OutboundMessage{
				Channel: s.cfg.Channel,
				ChatID:  s.cfg.ChatID,
				Content: "Agent was cancelled.",
			})
			out.Error = ctx.Err()
			return out
		}

		if out := s.postToolProcessing(ctx, response, i); out != nil {
			return out
		}
	}

	return s.buildMaxIterOutput()
}

// executeWithHooks wraps tool execution with pre/post hook calls via hooks.Manager.
// Both defaultToolExecutor (SubAgents) and buildToolExecutor (main Agent)
// MUST use this function to ensure hooks are called identically.
//
// The function:
//  1. Runs pre-tool hooks via Manager.Emit (PreToolUseEvent)
//  2. Executes the tool
//  3. Runs post-tool hooks via Manager.Emit (PostToolUseEvent or PostToolUseFailureEvent)
//
// toolExecCtx is the base context (with perm users etc. injected).
// toolCtx is the ToolContext (with WorkingDir resolved).
func executeWithHooks(
	hookMgr *hooks.Manager,
	toolExecCtx context.Context,
	toolCtx *tools.ToolContext,
	toolName, toolArgs string,
	tool tools.Tool,
	base hooks.BasePayload,
) (*tools.ToolResult, error) {
	// Parse toolArgs to map for event payload
	var toolInput map[string]any
	json.Unmarshal([]byte(toolArgs), &toolInput)

	// Fill timestamp and CWD from context.
	base.Timestamp = time.Now().Format(time.RFC3339)
	if wd := tools.WorkingDirFromContext(toolExecCtx); wd != "" && base.CWD == "" {
		base.CWD = wd
	}

	// Pre-tool hooks via Manager.Emit
	if hookMgr != nil {
		hookCtx := tools.WithWorkingDir(toolExecCtx, toolCtx.WorkingDir)
		preEvent := &hooks.PreToolUseEvent{
			BasePayload: base,
			ToolName_:   toolName,
			ToolInput_:  toolInput,
		}
		decision, err := hookMgr.Emit(hookCtx, preEvent)
		if err != nil {
			return nil, fmt.Errorf("pre-tool hook error for %q: %w", toolName, err)
		}
		if decision.Action == hooks.Deny {
			return nil, fmt.Errorf("pre-tool hook blocked %q: %s", toolName, decision.Reason)
		}
		// If decision has UpdatedInput, re-serialize toolArgs
		if decision.UpdatedInput != nil {
			if updated, err := json.Marshal(decision.UpdatedInput); err == nil {
				toolArgs = string(updated)
			}
		}
	}

	start := time.Now()
	result, err := tool.Execute(toolCtx, toolArgs)
	elapsed := time.Since(start)

	// Post-tool hooks via Manager.Emit (always, even on error)
	if hookMgr != nil {
		var postEvent hooks.Event
		if err != nil {
			postEvent = &hooks.PostToolUseFailureEvent{
				BasePayload: base,
				ToolName_:   toolName,
				ToolInput_:  toolInput,
				ToolError:   err.Error(),
			}
		} else {
			postEvent = &hooks.PostToolUseEvent{
				BasePayload:   base,
				ToolName_:     toolName,
				ToolInput_:    toolInput,
				ToolElapsedMs: elapsed.Milliseconds(),
			}
		}
		hookMgr.Emit(toolExecCtx, postEvent)
	}

	return result, err
}

// defaultToolExecutor creates the default tool executor (looks up from Registry and executes).
// Used for SubAgent and other scenarios that don't need session MCP / activation checks.
func defaultToolExecutor(cfg *RunConfig) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		tool, ok := cfg.Tools.Get(tc.Name)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		toolExecCtx := withApprovalTarget(ctx, cfg.ChatID, cfg.OriginUserID)
		if cfg.SettingsSvc != nil {
			permUsers := cfg.SettingsSvc.GetPermUsers(cfg.Channel, cfg.OriginUserID)
			if permUsers != nil {
				toolExecCtx = tools.WithPermUsers(toolExecCtx, permUsers.DefaultUser, permUsers.PrivilegedUser)
			}
		}
		toolCtx := buildToolContext(toolExecCtx, cfg)

		return executeWithHooks(cfg.HookManager, toolExecCtx, toolCtx, tc.Name, tc.Arguments, tool, hooks.BasePayload{
			SessionID: cfg.ChatID,
			Channel:   cfg.Channel,
			SenderID:  cfg.OriginUserID,
			ChatID:    cfg.ChatID,
		})
	}
}

// spawnAgentAdapter adapts the SpawnAgent function to the SubAgentManager interface.
// Core responsibility: convert (task, prompt, tools) function signature to unified InboundMessage.
//
// This enables SubAgentTool zero changes: it still calls SubAgentManager.RunSubAgent(),
// while adapter internally handles string ↔ InboundMessage/OutboundMessage conversion.
type spawnAgentAdapter struct {
	spawnFn  func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	parentID string
	channel  string
	chatID   string
	senderID string

	// Interactive mode callbacks (nil = interactive not supported)
	interactiveSpawnFn     func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	interactiveSendFn      func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	interactiveUnloadFn    func(ctx context.Context, roleName, instance string) error
	interactiveInterruptFn func(ctx context.Context, roleName, instance string) error
	interactiveInspectFn   func(ctx context.Context, roleName, instance string, tail int) (string, error)
}

// RunSubAgent implements the tools.SubAgentManager interface。
func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName, model string) (string, error) {
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, false, "", model)
	out, err := a.spawnFn(parentCtx.Ctx, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// SpawnInteractive implements InteractiveSubAgentManager.SpawnInteractive.
func (a *spawnAgentAdapter) SpawnInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, instance, model string) (string, error) {
	if a.interactiveSpawnFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true, instance, model)
	out, err := a.interactiveSpawnFn(parentCtx.Ctx, roleName, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// SendInteractive implements InteractiveSubAgentManager.SendInteractive.
func (a *spawnAgentAdapter) SendInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, instance, model string) (string, error) {
	if a.interactiveSendFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true, instance, model)
	out, err := a.interactiveSendFn(parentCtx.Ctx, roleName, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// UnloadInteractive implements InteractiveSubAgentManager.UnloadInteractive.
func (a *spawnAgentAdapter) UnloadInteractive(parentCtx *tools.ToolContext, roleName, instance string) error {
	if a.interactiveUnloadFn == nil {
		return fmt.Errorf("interactive mode not supported")
	}
	return a.interactiveUnloadFn(parentCtx.Ctx, roleName, instance)
}

// InspectInteractive implements InteractiveSubAgentManager.InspectInteractive.
func (a *spawnAgentAdapter) InspectInteractive(parentCtx *tools.ToolContext, roleName, instance string, tailCount int) (string, error) {
	if a.interactiveInspectFn == nil {
		return "", fmt.Errorf("interactive inspect not supported")
	}
	return a.interactiveInspectFn(parentCtx.Ctx, roleName, instance, tailCount)
}

// InterruptInteractive implements InteractiveSubAgentManager.InterruptInteractive.
func (a *spawnAgentAdapter) InterruptInteractive(parentCtx *tools.ToolContext, roleName, instance string) error {
	if a.interactiveInterruptFn == nil {
		return fmt.Errorf("interactive interrupt not supported")
	}
	return a.interactiveInterruptFn(parentCtx.Ctx, roleName, instance)
}

// buildMsg constructs SubAgent InboundMessage.
func (a *spawnAgentAdapter) buildMsg(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, interactive bool, instance, model string) bus.InboundMessage {
	metadata := map[string]string{
		"origin_channel": a.channel,
		"origin_chat_id": a.chatID,
		"origin_sender":  a.senderID,
	}
	if interactive {
		metadata["interactive"] = "true"
	}
	if instance != "" {
		metadata["instance_id"] = instance
	}
	// Propagate background flag from ToolContext metadata
	if parentCtx.Metadata != nil {
		if bg, ok := parentCtx.Metadata["background"]; ok {
			metadata["background"] = bg
		}
		if gid, ok := parentCtx.Metadata["group_id"]; ok {
			metadata["group_id"] = gid
		}
		if gms, ok := parentCtx.Metadata["group_members"]; ok {
			metadata["group_members"] = gms
		}
	}
	// Also propagate group from ToolContext fields (set by SpawnInteractive for group agents)
	if parentCtx.GroupID != "" {
		metadata["group_id"] = parentCtx.GroupID
		metadata["group_members"] = strings.Join(parentCtx.GroupMembers, ",")
	}
	// Propagate model override from SubAgent role definition
	if model != "" {
		metadata["model"] = model
	}
	// Propagate parent's CWD so SubAgent inherits working directory.
	// If CurrentDir is empty (parent never Cd'd), fall back to WorkingDir.
	parentCWD := parentCtx.CurrentDir
	if parentCWD == "" {
		parentCWD = parentCtx.WorkingDir
	}
	if parentCWD != "" {
		metadata["parent_cwd"] = parentCWD
	}

	return bus.InboundMessage{
		From: bus.NewIMAddress(a.channel, a.senderID),
		To:   bus.NewAgentAddress(a.parentID),

		Channel:    bus.SchemeAgent,
		Content:    task,
		SenderID:   parentCtx.SenderID,
		SenderName: parentCtx.SenderName,
		ChatID:     a.chatID,
		ChatType:   "agent",
		Time:       time.Now(),

		ParentAgentID: a.parentID,
		RoleName:      roleName,
		SystemPrompt:  systemPrompt,
		AllowedTools:  allowedTools,
		Capabilities:  caps.ToMap(),
		Metadata:      metadata,
	}
}

// sandboxReadOnlyRoots converts host-path ReadOnlyRoots to sandbox paths.
// Only converts when sandboxWorkDir is non-empty and differs from WorkspaceRoot.
func sandboxReadOnlyRoots(hostRoots []string, sandboxWorkDir, workspaceRoot string) []string {
	if sandboxWorkDir == "" || sandboxWorkDir == workspaceRoot {
		return hostRoots
	}
	result := make([]string, 0, len(hostRoots))
	for _, ro := range hostRoots {
		if strings.HasPrefix(ro, workspaceRoot) {
			result = append(result, sandboxWorkDir+strings.TrimPrefix(ro, workspaceRoot))
		} else {
			result = append(result, ro)
		}
	}
	return result
}

// buildToolContext builds ToolContext uniformly.
// Extracts all fields from RunConfig; main Agent and SubAgent use the same build path.
// resolveSandbox resolves the per-user sandbox instance if the global sandbox
// implements SandboxResolver (e.g., SandboxRouter). Falls back to the global instance.
func resolveSandbox(sandbox tools.Sandbox, userID string) tools.Sandbox {
	if sandbox == nil {
		return nil
	}
	if resolver, ok := sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID)
	}
	return sandbox
}

func buildToolContext(ctx context.Context, cfg *RunConfig) *tools.ToolContext {
	// Resolve per-user sandbox BEFORE building ToolContext.
	// For remote users, the resolved sandbox is RemoteSandbox (Name() == "remote").
	// If FeishuUserID is set (web login via Feishu identity), use it for routing
	// so the user gets the same runner as on the Feishu side.
	sandboxUserID := cfg.SenderID
	if cfg.FeishuUserID != "" {
		sandboxUserID = cfg.FeishuUserID
	}
	resolvedSandbox := resolveSandbox(cfg.Sandbox, sandboxUserID)
	isRemote := resolvedSandbox != nil && resolvedSandbox.Name() == "remote"

	// For remote users, leave WorkspaceRoot/WorkingDir empty — the runner
	// manages its own filesystem. Host paths must not leak into ToolContext
	// for remote users (they cause server-side directory creation and
	// confuse path resolution).
	var workspaceRoot, workingDir string
	if !isRemote {
		workspaceRoot = cfg.WorkspaceRoot
		workingDir = cfg.WorkingDir
	}

	tc := &tools.ToolContext{
		Ctx:            ctx,
		AgentID:        cfg.AgentID,
		Channel:        cfg.Channel,
		ChatID:         cfg.ChatID,
		SenderID:       cfg.SenderID,
		OriginUserID:   cfg.OriginUserID,
		SenderName:     cfg.SenderName,
		SendFunc:       cfg.SendFunc,
		RootSessionKey: cfg.RootSessionKey,

		// Workspace & Sandbox
		WorkingDir:           workingDir,
		WorkspaceRoot:        workspaceRoot,
		ReadOnlyRoots:        cfg.ReadOnlyRoots,
		SandboxReadOnlyRoots: sandboxReadOnlyRoots(cfg.ReadOnlyRoots, "", workspaceRoot),
		SkillsDirs:           cfg.SkillsDirs,
		AgentsDir:            cfg.AgentsDir,
		MCPConfigPath:        cfg.MCPConfigPath,
		GlobalMCPConfigPath:  cfg.GlobalMCPConfig,
		SandboxEnabled:       cfg.SandboxEnabled,
		PreferredSandbox:     cfg.PreferredSandbox,
		Sandbox:              resolvedSandbox,
		DataDir:              cfg.DataDir,

		// Inject inbound message
		InjectInbound: cfg.InjectInbound,

		// Tool registry
		Registry: cfg.Tools,

		// Streaming settings inheritance
		Stream: cfg.Stream,
	}

	// Inject SpawnAgent (wrapped as SubAgentManager interface)
	if cfg.SpawnAgent != nil {
		// Use OriginUserID to build adapter (for message tracing)
		originUserID := cfg.OriginUserID
		if originUserID == "" {
			originUserID = cfg.SenderID // fallback: backward compatible with old data
		}
		adapter := &spawnAgentAdapter{
			spawnFn:  cfg.SpawnAgent,
			parentID: cfg.AgentID,
			channel:  cfg.Channel,
			chatID:   cfg.ChatID,
			senderID: originUserID, // Use original user ID (for message tracing)
		}
		// Inject Interactive callbacks (main Agent specific)
		if cb := cfg.InteractiveCallbacks; cb != nil {
			adapter.interactiveSpawnFn = cb.SpawnFn
			adapter.interactiveSendFn = cb.SendFn
			adapter.interactiveUnloadFn = cb.UnloadFn
			adapter.interactiveInterruptFn = cb.InterruptFn
			adapter.interactiveInspectFn = cb.InspectFn
		}
		tc.Manager = adapter
	}

	// Inject Letta memory fields (overriding the defaults above)
	if ext := cfg.ToolContextExtras; ext != nil {
		tc.TenantID = ext.TenantID
		tc.CoreMemory = ext.CoreMemory
		tc.ArchivalMemory = ext.ArchivalMemory
		tc.MemorySvc = ext.MemorySvc
		tc.RecallTimeRange = ext.RecallTimeRange
		tc.ToolIndexer = ext.ToolIndexer
		if ext.InvalidateAllSessionMCP != nil {
			tc.InvalidateAllSessionMCP = ext.InvalidateAllSessionMCP
		}
	}

	// Inject BgTaskManager background task manager
	if cfg.BgTaskManager != nil {
		tc.BgTaskManager = cfg.BgTaskManager
		sessionKey := cfg.SessionKey
		if sessionKey == "" {
			sessionKey = cfg.Channel + ":" + cfg.ChatID
		}
		tc.BgSessionKey = sessionKey
		// NOTE: OnComplete callback registration moved to Agent.bgNotifyLoop.
		// Engine no longer registers callbacks per-buildToolContext call.
	}

	// Inject MessageSender (Dispatcher reference, allows Agent to send messages to any Channel)
	tc.MessageSender = cfg.MessageSender
	// Inject AgentChannel register/unregister callbacks
	tc.RegisterAgentChannel = cfg.RegisterAgentChannel
	tc.UnregisterAgentChannel = cfg.UnregisterAgentChannel
	// Inject ToolContext extras (memory, MCP, etc.)

	// Inject session cwd (PWD tool optimization)
	if cfg.Session != nil {
		tc.CurrentDir = cfg.Session.GetCurrentDir()
		// Fallback: new session has empty CWD, use InitialCWD (inherited from parent).
		if tc.CurrentDir == "" && cfg.InitialCWD != "" {
			tc.CurrentDir = cfg.InitialCWD
		}
		tc.SetCurrentDir = func(dir string) {
			cfg.Session.SetCurrentDir(dir)
		}
	} else {
		// No session — use InitialCWD for CWD persistence (SubAgent or sessionless mode).
		// SetCurrentDir must ALWAYS be set so Cd can persist CWD even when InitialCWD
		// starts empty (e.g., parent Agent never Cd'd before spawning SubAgent).
		cwd := cfg.InitialCWD
		if cwd != "" && cfg.Sandbox != nil && cfg.Sandbox.Name() != "none" && cfg.WorkspaceRoot != "" {
			sandboxWS := cfg.Sandbox.Workspace(cfg.OriginUserID)
			if sandboxWS != "" && strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
				cwd = sandboxWS + cwd[len(cfg.WorkspaceRoot):]
			}
		}
		if cwd != "" {
			tc.CurrentDir = cwd
		}
		tc.SetCurrentDir = func(dir string) {
			cfg.InitialCWD = dir
		}
	}
	// Propagate group membership for cross-agent messaging
	if cfg.InitialGroupID != "" {
		tc.GroupID = cfg.InitialGroupID
		tc.GroupMembers = cfg.InitialGroupMembers
	}

	return tc
}

// CallChain call chain context, for tracking inter-Agent call relationships and preventing recursion.
type CallChain struct {
	Chain []string // Call chain: ["main", "main/code-reviewer"]
}

// DefaultMaxSubAgentDepth default SubAgent nesting depth.
const DefaultMaxSubAgentDepth = 6

type callChainKey struct{}

// CallChainFromContext extracts the call chain from context.
func CallChainFromContext(ctx context.Context) *CallChain {
	if cc, ok := ctx.Value(callChainKey{}).(*CallChain); ok {
		return cc
	}
	return &CallChain{Chain: []string{"main"}}
}

// WithCallChain injects the call chain into context.
func WithCallChain(ctx context.Context, cc *CallChain) context.Context {
	return context.WithValue(ctx, callChainKey{}, cc)
}

// CanSpawn checks if a SubAgent of the specified role can be created.
// Returns nil if allowed, returns error if not (depth exceeded or circular call).
// maxDepth is the maximum allowed depth; if <= 0, uses default DefaultMaxSubAgentDepth.
func (cc *CallChain) CanSpawn(targetRole string, maxDepth int) error {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxSubAgentDepth
	}
	if len(cc.Chain) >= maxDepth {
		return fmt.Errorf("max SubAgent depth %d reached (chain: %v)", maxDepth, cc.Chain)
	}
	for _, id := range cc.Chain {
		role := id
		if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
			role = id[idx+1:]
		}
		if role == targetRole {
			return fmt.Errorf("circular SubAgent call: role %q already in chain %v", targetRole, cc.Chain)
		}
	}
	return nil
}

// Spawn creates a new call chain (appends target role).
func (cc *CallChain) Spawn(targetRole string) *CallChain {
	currentID := cc.Chain[len(cc.Chain)-1]
	newChain := make([]string, len(cc.Chain)+1)
	copy(newChain, cc.Chain)
	newChain[len(cc.Chain)] = currentID + "/" + targetRole
	return &CallChain{Chain: newChain}
}

// Depth returns the current call depth.
func (cc *CallChain) Depth() int {
	return len(cc.Chain)
}

// Current returns the current Agent ID.
func (cc *CallChain) Current() string {
	if len(cc.Chain) == 0 {
		return "main"
	}
	return cc.Chain[len(cc.Chain)-1]
}
