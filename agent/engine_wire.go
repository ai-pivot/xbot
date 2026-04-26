package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	channelpkg "xbot/channel"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/oauth"
	"xbot/session"
	"xbot/tools"
)

// todoManagerAdapter wraps tools.TodoManager to implement TodoManagerProvider.
type todoManagerAdapter struct {
	mgr *tools.TodoManager
}

func (a *todoManagerAdapter) GetTodoSummary(sessionKey string) string {
	return a.mgr.GetTodoSummary(sessionKey)
}

func (a *todoManagerAdapter) GetTodoItems(sessionKey string) []TodoProgressItem {
	items := a.mgr.GetTodos(sessionKey)
	result := make([]TodoProgressItem, len(items))
	for i, item := range items {
		result[i] = TodoProgressItem{ID: item.ID, Text: item.Text, Done: item.Done}
	}
	return result
}

func (a *todoManagerAdapter) ClearTodos(sessionKey string) {
	a.mgr.SetTodos(sessionKey, nil)
}

// applyUserMaxContext if the user has set max_context in Settings,
// creates a new ContextManagerConfig copy and overrides MaxContextTokens,
// to avoid polluting the Agent-level original config (which has sync.RWMutex).
func applyUserMaxContext(base *ContextManagerConfig, userMaxCtx int) *ContextManagerConfig {
	if userMaxCtx <= 0 || base == nil {
		return base
	}
	return &ContextManagerConfig{
		MaxContextTokens:     userMaxCtx,
		CompressionThreshold: base.CompressionThreshold,
		DefaultMode:          base.DefaultMode,
	}
}

// buildBaseRunConfig builds the base RunConfig shared by main Agent (main/cron).
// Includes LLM, identity, workspace, tool executor, loop control, HookManager and other common fields.
// Returns (RunConfig, userMaxContext) — userMaxContext is the value set by the user in Settings, 0 means not set.
func (a *Agent) buildBaseRunConfig(
	channel, chatID, senderID string,
	messages []llm.ChatMessage,
	senderName string,
	sandboxUserID string,
) (RunConfig, int) {
	sessionKey := channel + ":" + chatID

	llmClient, model, userMaxCtx, thinkingMode := a.llmFactory.GetLLMForChat(senderID, chatID)

	// LLM concurrency rate-limiting callback (per-tenant)
	llmSemAcquire := a.llmFactory.LLMSemAcquireForUser(senderID)
	subAgentSem := a.llmFactory.SubAgentSemAcquireForUser(senderID)

	return RunConfig{
		// Required
		LLMClient:    llmClient,
		Model:        model,
		ThinkingMode: thinkingMode,
		Tools:        a.tools,
		Messages:     messages,

		// Identity
		AgentID:      "main",
		Channel:      channel,
		ChatID:       chatID,
		SenderID:     senderID,      // direct caller = original user (for message routing + settings/usage storage key)
		OriginUserID: sandboxUserID, // 沙箱/工作区用户（飞书Identity登录 web 时为飞书 ou_xxx）
		SenderName:   senderName,

		// Workspace & Sandbox
		WorkingDir:       a.workDir,
		WorkspaceRoot:    a.workspaceRoot(sandboxUserID),
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, sandboxUserID),
		GlobalMCPConfig:  filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   a.sandboxMode != "none",
		PreferredSandbox: a.sandboxMode,
		Sandbox:          resolveSandbox(a.sandbox, sandboxUserID),
		SandboxMode:      a.sandboxMode,

		// Loop control
		MaxIterations:   a.getMaxIterations(),
		MaxOutputTokens: a.llmFactory.GetMaxOutputTokens(senderID),

		// Session
		SessionKey: sessionKey,

		// Send
		SendFunc:      a.sendMessage,
		InjectInbound: a.injectInbound,

		// Tool execution
		ToolExecutor: a.buildToolExecutor(channel, chatID, senderID, senderName, sandboxUserID),
		// ToolTimeout: no longer used. Tools manage their own timeouts.

		// Read-write split (always enabled for main Agent)
		EnableReadWriteSplit: true,

		// SessionFinalSent callback
		SessionFinalSentCallback: func() bool {
			_, sent := a.sessionFinalSent.Load(sessionKey)
			return sent
		},

		// Letta memory fields
		ToolContextExtras: a.buildToolContextExtras(channel, chatID),

		// HookManager — inherit from Agent
		HookManager: a.hookManager,

		// SettingsSvc — inherit from Agent
		SettingsSvc: a.settingsSvc,

		// LLM concurrency rate-limiting callback (per-tenant)
		LLMSemAcquire:             llmSemAcquire,
		EnableConcurrentSubAgents: true,
		SubAgentSem:               subAgentSem,
	}, userMaxCtx
}

// buildMainRunConfig builds the complete RunConfig for the main Agent.
// Called from processMessage / handleCardResponse.
func (a *Agent) buildMainRunConfig(
	_ context.Context,
	msg bus.InboundMessage,
	messages []llm.ChatMessage,
	tenantSession *session.TenantSession,
	autoNotify bool,
) RunConfig {
	channel, chatID, senderID, senderName := msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName
	sessionKey := channel + ":" + chatID

	// When Feishu identity login web, use Feishu user ID as sandbox user ID,
	// ensuring web and Feishu share the same Docker container and workspace.
	feishuUserID := msg.Metadata["feishu_user_id"]
	sandboxUserID := senderID
	if feishuUserID != "" {
		sandboxUserID = feishuUserID
	}

	cfg, userMaxCtx := a.buildBaseRunConfig(channel, chatID, senderID, messages, senderName, sandboxUserID)

	// Keep FeishuUserID for use in buildToolContext etc.
	cfg.FeishuUserID = feishuUserID

	// Main Agent specific fields
	cfg.Session = tenantSession

	// Token state persistence: written to DB after Run() completes, restored after restart
	if extras := cfg.ToolContextExtras; extras != nil && extras.MemorySvc != nil && extras.TenantID != 0 {
		memSvc := extras.MemorySvc
		tenantID := extras.TenantID
		cfg.SaveTokenState = func(promptTokens, completionTokens int64) {
			if err := memSvc.SetTokenState(context.Background(), tenantID, promptTokens, completionTokens); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to persist token state")
			}
		}
	}

	// OAuth handling
	cfg.OAuthHandler = a.buildOAuthHandler(channel, chatID, senderID, sessionKey)

	// Progress notification
	// Web channel always enables structured progress, but does not send text progress messages
	if channel == "web" || channel == "cli" {
		// Web: no-op notifier — structured progress goes via ProgressEventHandler
		// Setting ProgressNotifier to non-nil enables autoNotify in engine.Run()
		cfg.ProgressNotifier = func(lines []string) {}
	} else if autoNotify {
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				_ = a.sendMessage(channel, chatID, lines[0])
			}
		}
	}

	// Structured progress event push (web and CLI channels)
	if (channel == "web" || channel == "cli") && a.channelFinder != nil {
		// CLI channel progress handling
		switch channel {
		case "cli":
			var cliCh *channelpkg.CLIChannel
			var remoteCLICh *channelpkg.RemoteCLIChannel
			if a.channelFinder != nil {
				if ch, ok := a.channelFinder("cli"); ok {
					if cc, ok := ch.(*channelpkg.CLIChannel); ok {
						cliCh = cc
					} else if rc, ok := ch.(*channelpkg.RemoteCLIChannel); ok {
						remoteCLICh = rc
					} else {
						log.WithField("type", fmt.Sprintf("%T", ch)).Warn("buildMainRunConfig: channelFinder('cli') returned unexpected type")
					}
				} else {
					log.Warn("buildMainRunConfig: channelFinder('cli') returned not found")
				}
			} else {
				log.Warn("buildMainRunConfig: channelFinder is nil")
			}
			log.WithFields(log.Fields{
				"hasCliCh":       cliCh != nil,
				"hasRemoteCLICh": remoteCLICh != nil,
				"progressKey":    channel + ":" + chatID,
			}).Info("buildMainRunConfig: cli channel resolution")
			if cliCh != nil || remoteCLICh != nil {
				progressKey := channel + ":" + chatID
				cfg.ProgressEventHandler = func(event *ProgressEvent) {
					if event == nil || event.Structured == nil {
						return
					}
					s := event.Structured
					if cliCh != nil {
						payload := &channelpkg.CLIProgressPayload{
							ChatID:           progressKey,
							Phase:            string(s.Phase),
							Iteration:        s.Iteration,
							Thinking:         s.ThinkingContent,
							Reasoning:        s.ReasoningContent,
							HistoryCompacted: s.HistoryCompacted,
						}
						for _, t := range s.ActiveTools {
							payload.ActiveTools = append(payload.ActiveTools, channelpkg.CLIToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Iteration: t.Iteration,
								Summary:   t.Summary,
							})
						}
						for _, t := range s.CompletedTools {
							payload.CompletedTools = append(payload.CompletedTools, channelpkg.CLIToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Iteration: t.Iteration,
								Summary:   t.Summary,
							})
						}
						if len(event.Lines) > 0 {
							subAgents := ExtractSubAgentTree(event.Lines)
							if len(subAgents) > 0 {
								cliSubAgents := make([]channelpkg.CLISubAgent, len(subAgents))
								for i, sa := range subAgents {
									cliSubAgents[i] = channelpkg.CLISubAgent{
										Role:     sa.Role,
										Status:   sa.Status,
										Desc:     sa.Desc,
										Children: convertCLISubAgentTree(sa.Children),
									}
								}
								payload.SubAgents = cliSubAgents
							}
						}
						if len(s.Todos) > 0 {
							payload.Todos = make([]channelpkg.CLITodoItem, len(s.Todos))
							for i, td := range s.Todos {
								payload.Todos[i] = channelpkg.CLITodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
							}
						}
						if s.TokenUsage != nil {
							payload.TokenUsage = &channelpkg.CLITokenUsage{
								PromptTokens:     s.TokenUsage.PromptTokens,
								CompletionTokens: s.TokenUsage.CompletionTokens,
								TotalTokens:      s.TokenUsage.TotalTokens,
								CacheHitTokens:   s.TokenUsage.CacheHitTokens,
							}
						}
						cliCh.SendProgress(chatID, payload)
						// Save snapshot + track iteration history for mid-session reconnect.
						a.recordIterationSnapshot(progressKey, func(prev *channelpkg.CLIProgressPayload) bool {
							return s.Iteration > prev.Iteration && prev.Iteration >= 0
						})
						a.lastProgressSnapshot.Store(progressKey, payload)
					}
					if remoteCLICh != nil {
						payload := &channelpkg.WsProgressPayload{
							ChatID:    progressKey,
							Phase:     string(s.Phase),
							Iteration: s.Iteration,
							Thinking:  s.ThinkingContent,
							Reasoning: s.ReasoningContent,
						}
						for _, t := range s.ActiveTools {
							payload.ActiveTools = append(payload.ActiveTools, channelpkg.WsToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Summary:   t.Summary,
								Iteration: t.Iteration,
							})
						}
						for _, t := range s.CompletedTools {
							payload.CompletedTools = append(payload.CompletedTools, channelpkg.WsToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Summary:   t.Summary,
								Iteration: t.Iteration,
							})
						}
						if len(event.Lines) > 0 {
							subAgents := ExtractSubAgentTree(event.Lines)
							if len(subAgents) > 0 {
								wsSubAgents := make([]channelpkg.WsSubAgent, len(subAgents))
								for i, sa := range subAgents {
									wsSubAgents[i] = channelpkg.WsSubAgent{Role: sa.Role, Status: sa.Status, Desc: sa.Desc, Children: convertWsSubAgentTree(sa.Children)}
								}
								payload.SubAgents = wsSubAgents
							}
						}
						if len(s.Todos) > 0 {
							payload.Todos = make([]channelpkg.WsTodoItem, len(s.Todos))
							for i, td := range s.Todos {
								payload.Todos[i] = channelpkg.WsTodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
							}
						}
						if s.TokenUsage != nil {
							payload.TokenUsage = &channelpkg.WsTokenUsage{
								PromptTokens:     s.TokenUsage.PromptTokens,
								CompletionTokens: s.TokenUsage.CompletionTokens,
								TotalTokens:      s.TokenUsage.TotalTokens,
								CacheHitTokens:   s.TokenUsage.CacheHitTokens,
							}
						}
						remoteCLICh.SendProgress(chatID, payload)
						// Store progress snapshot for remote CLI reconnect recovery.
						// Without this, GetActiveProgress returns nil after CLI restart
						// because only the local cliCh path stored snapshots.
						cliPayload := &channelpkg.CLIProgressPayload{
							ChatID:    progressKey,
							Phase:     string(s.Phase),
							Iteration: s.Iteration,
							Thinking:  s.ThinkingContent,
							Reasoning: s.ReasoningContent,
						}
						for _, t := range s.ActiveTools {
							cliPayload.ActiveTools = append(cliPayload.ActiveTools, channelpkg.CLIToolProgress{
								Name: t.Name, Label: t.Label, Status: string(t.Status),
								Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary,
							})
						}
						for _, t := range s.CompletedTools {
							cliPayload.CompletedTools = append(cliPayload.CompletedTools, channelpkg.CLIToolProgress{
								Name: t.Name, Label: t.Label, Status: string(t.Status),
								Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary,
							})
						}
						if len(event.Lines) > 0 {
							subAgents := ExtractSubAgentTree(event.Lines)
							if len(subAgents) > 0 {
								cliSubAgents := make([]channelpkg.CLISubAgent, len(subAgents))
								for i, sa := range subAgents {
									cliSubAgents[i] = channelpkg.CLISubAgent{
										Role:     sa.Role,
										Status:   sa.Status,
										Desc:     sa.Desc,
										Children: convertCLISubAgentTree(sa.Children),
									}
								}
								cliPayload.SubAgents = cliSubAgents
							}
						}
						if len(s.Todos) > 0 {
							cliPayload.Todos = make([]channelpkg.CLITodoItem, len(s.Todos))
							for i, td := range s.Todos {
								cliPayload.Todos[i] = channelpkg.CLITodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
							}
						}
						if s.TokenUsage != nil {
							cliPayload.TokenUsage = &channelpkg.CLITokenUsage{
								PromptTokens:     s.TokenUsage.PromptTokens,
								CompletionTokens: s.TokenUsage.CompletionTokens,
								TotalTokens:      s.TokenUsage.TotalTokens,
								CacheHitTokens:   s.TokenUsage.CacheHitTokens,
							}
						}
						a.recordIterationSnapshot(progressKey, func(prev *channelpkg.CLIProgressPayload) bool {
							return s.Iteration > prev.Iteration && prev.Iteration >= 0
						})
						a.lastProgressSnapshot.Store(progressKey, cliPayload)
						log.WithFields(log.Fields{
							"key":       progressKey,
							"phase":     cliPayload.Phase,
							"iteration": cliPayload.Iteration,
							"active":    len(cliPayload.ActiveTools),
							"completed": len(cliPayload.CompletedTools),
						}).Info("remote CLI: stored progress snapshot")
					}
				}
			}
		case "web":
			if ch, ok := a.channelFinder("web"); ok {
				if wc, ok := ch.(*channelpkg.WebChannel); ok {
					progressKey := channel + ":" + chatID
					cfg.ProgressEventHandler = func(event *ProgressEvent) {
						if event == nil || event.Structured == nil {
							return
						}
						s := event.Structured
						payload := &channelpkg.WsProgressPayload{
							Phase:     string(s.Phase),
							Iteration: s.Iteration,
							Thinking:  s.ThinkingContent,
						}
						for _, t := range s.ActiveTools {
							payload.ActiveTools = append(payload.ActiveTools, channelpkg.WsToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Summary:   t.Summary,
								Iteration: t.Iteration,
							})
						}
						for _, t := range s.CompletedTools {
							payload.CompletedTools = append(payload.CompletedTools, channelpkg.WsToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Summary:   t.Summary,
								Iteration: t.Iteration,
							})
						}
						// Parse sub-agent tree from progress lines
						if len(event.Lines) > 0 {
							subAgents := ExtractSubAgentTree(event.Lines)
							if len(subAgents) > 0 {
								wsSubAgents := make([]channelpkg.WsSubAgent, len(subAgents))
								for i, sa := range subAgents {
									wsSubAgents[i] = channelpkg.WsSubAgent{
										Role:     sa.Role,
										Status:   sa.Status,
										Desc:     sa.Desc,
										Children: convertWsSubAgentTree(sa.Children),
									}
								}
								payload.SubAgents = wsSubAgents
							}
						}
						// Copy todo items for web display
						if len(s.Todos) > 0 {
							payload.Todos = make([]channelpkg.WsTodoItem, len(s.Todos))
							for i, td := range s.Todos {
								payload.Todos[i] = channelpkg.WsTodoItem{
									ID:   td.ID,
									Text: td.Text,
									Done: td.Done,
								}
							}
						}
						// Pass token usage snapshot
						if s.TokenUsage != nil {
							payload.TokenUsage = &channelpkg.WsTokenUsage{
								PromptTokens:     s.TokenUsage.PromptTokens,
								CompletionTokens: s.TokenUsage.CompletionTokens,
								TotalTokens:      s.TokenUsage.TotalTokens,
								CacheHitTokens:   s.TokenUsage.CacheHitTokens,
							}
						}

						// Keep event order stable for frontend rendering. SendProgress itself is non-blocking.
						wc.SendProgress(chatID, payload)

						// Track iteration history: when iteration advances, snapshot the
						// PREVIOUS iteration into the history list for mid-session reconnect.
						cliSnapshot := payload.ToCLIProgressPayload()
						a.recordIterationSnapshot(progressKey, func(prev *channelpkg.CLIProgressPayload) bool {
							return s.Iteration > prev.Iteration && prev.Iteration >= 0
						})
						// Save current iteration snapshot
						a.lastProgressSnapshot.Store(progressKey, cliSnapshot)
					}
				} else {
					log.WithField("channel", channel).Warn("Web channel found but type assertion failed, skipping ProgressEventHandler")
				}
			}
		}
	}

	// Inject ContextManager
	cfg.ContextManager = a.GetContextManager()
	cfg.ContextManagerConfig = applyUserMaxContext(a.contextManagerConfig, userMaxCtx)

	// Per-user token usage tracking (persisted to SQLite)
	cfg.RecordUserTokenUsage = func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int) {
		if err := a.multiSession.RecordUserTokenUsage(senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount); err != nil {
			log.WithError(err).WithField("sender_id", senderID).Warn("Failed to record user token usage")
		}
	}

	// SpawnAgent (main Agent can create SubAgent)
	cfg.SpawnAgent = func(ctx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
		return a.spawnSubAgent(ctx, inMsg)
	}

	// OffloadStore — Layer 1 offload
	cfg.OffloadStore = a.offloadStore

	// MaskStore — Observation Masking (enabled by default, can be disabled via settings enable_masking)
	cfg.MaskStore = a.maskStore
	streamDisabled := false
	if a.settingsSvc != nil {
		if vals, err := a.settingsSvc.GetSettings(channel, senderID); err == nil {
			if vals["enable_masking"] == "false" {
				cfg.MaskStore = nil
			}
			if vals["enable_stream"] == "false" {
				streamDisabled = true
			}
		}
	}

	// Stream — default ON for all channels; wire callbacks per channel type.
	if !streamDisabled {
		cfg.Stream = true
		if a.channelFinder != nil {
			var cliCh *channelpkg.CLIChannel
			var remoteCLICh *channelpkg.RemoteCLIChannel
			if ch, ok := a.channelFinder("cli"); ok {
				if cc, ok := ch.(*channelpkg.CLIChannel); ok {
					cliCh = cc
				} else if rc, ok := ch.(*channelpkg.RemoteCLIChannel); ok {
					remoteCLICh = rc
				}
			}
			var webCh *channelpkg.WebChannel
			if ch, ok := a.channelFinder("web"); ok {
				if wc, ok := ch.(*channelpkg.WebChannel); ok {
					webCh = wc
				}
			}
			cfg.StreamContentFunc = func(content string) {
				if cliCh != nil {
					cliCh.SendProgress(chatID, &channelpkg.CLIProgressPayload{ChatID: channel + ":" + chatID, StreamContent: content})
				}
				if remoteCLICh != nil {
					remoteCLICh.SendStreamContent(chatID, content, "")
				}
				if webCh != nil {
					webCh.SendStreamContent(chatID, content, "")
				}
			}
			cfg.StreamReasoningFunc = func(content string) {
				if cliCh != nil {
					cliCh.SendProgress(chatID, &channelpkg.CLIProgressPayload{ChatID: channel + ":" + chatID, ReasoningStreamContent: content})
				}
				if remoteCLICh != nil {
					remoteCLICh.SendStreamContent(chatID, "", content)
				}
				if webCh != nil {
					webCh.SendStreamContent(chatID, "", content)
				}
			}
		}
	}

	// ContextEditor — Context Editing (precise context editing)
	cfg.ContextEditor = a.contextEditor

	// TodoManager — TODO status query
	if a.todoManager != nil {
		cfg.TodoManager = &todoManagerAdapter{mgr: a.todoManager}
	}

	// InteractiveCallbacks — interactive SubAgent support
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, channel, chatID, instance, tail)
		},
	}

	// Memory tools for compaction — allows the compaction LLM to archive
	// important context into core/archival memory before it gets compacted away.
	// Uses the real tool registry instead of hand-written execution logic.
	if defs, exec := a.buildMemoryToolSetup(channel, chatID); defs != nil {
		cfg.MemoryToolDefs = defs
		cfg.MemoryToolExec = exec
	}

	return cfg
}

// buildCronRunConfig builds RunConfig for Cron messages.
// Cron messages do not need auto-compression, progress notification, or session persistence.
func (a *Agent) buildCronRunConfig(
	_ context.Context,
	msg bus.InboundMessage,
	messages []llm.ChatMessage,
) RunConfig {
	channel, chatID, senderID := msg.Channel, msg.ChatID, msg.SenderID

	cfg, _ := a.buildBaseRunConfig(channel, chatID, senderID, messages, "", senderID)
	return cfg
}

// buildSubAgentRunConfig builds RunConfig for SubAgent.
// SubAgent uses independent tool set, no session, has compression (independent ContextManager), no progress notification.
// Phase 2: SubAgent inherits parent Agent's workspace configuration via RunConfig,
// uses unified defaultToolExecutor + buildToolContext to build ToolContext.
func (a *Agent) buildSubAgentRunConfig(
	ctx context.Context,
	parentCtx *tools.ToolContext,
	task string,
	systemPrompt string,
	allowedTools []string,
	caps tools.SubAgentCapabilities,
	roleName string,
	interactive bool,
	model string, // optional: model specified by the role, inherits from main Agent when empty
) RunConfig {
	parentAgentID := parentCtx.AgentID

	// Interactive SubAgent defaults to having send_message capability (required for group/inter-agent communication)
	if interactive {
		caps.SendMessage = true
	}

	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// Sub-Agent tool set: decide whether to keep SubAgent tool based on capabilities
	subTools := a.tools.Clone()
	if !caps.SpawnAgent {
		subTools.Unregister(toolSubAgent)
	}

	// If a tool whitelist is specified, only keep whitelisted tools
	// The following tools are always available, not restricted by whitelist:
	//   - SubAgent (if caps.SpawnAgent=true)
	//   - offload_recall, recall_masked (SubAgent needs access to parent Agent's offload/mask data)
	//   - SendMessage, CreateChat (required for interactive SubAgent group/inter-agent communication)
	if len(allowedTools) > 0 {
		allowed := make(map[string]bool, len(allowedTools))
		for _, name := range allowedTools {
			allowed[name] = true
		}
		for _, tool := range subTools.List() {
			toolName := tool.Name()
			// SubAgent tool: if SpawnAgent=true, always keep
			if toolName == toolSubAgent && caps.SpawnAgent {
				continue
			}
			// offload_recall / recall_masked: always available for SubAgent
			if toolName == toolOffloadRecall || toolName == toolRecallMasked {
				continue
			}
			// SendMessage / CreateChat: always available for interactive SubAgent (group chat communication)
			if interactive && (toolName == toolSendMessage || toolName == toolCreateChat) {
				continue
			}
			if !allowed[toolName] {
				subTools.Unregister(toolName)
			}
		}
	}

	// Build SubAgent's system prompt: common template + role-specific capability description
	// parentCtx.WorkspaceRoot is empty in remote mode (buildToolContext cleared host paths),
	// Fallback to a.workDir to ensure the prompt always contains the correct working directory.
	workDir := parentCtx.WorkspaceRoot
	if workDir == "" {
		workDir = a.workDir
	}
	if parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none" {
		workDir = parentCtx.Sandbox.Workspace(parentCtx.OriginUserID)
	}
	now := time.Now().Format(timeFmtDatetime)

	// CWD inherits parent Agent's current directory, defaults to workDir if absent
	cwd := parentCtx.CurrentDir
	if cwd == "" {
		cwd = workDir
	}
	cwdPart := "\n- Current directory: " + cwd

	// role.SystemPrompt serves as role-specific capability description (not a generic prompt)
	rolePrompt := strings.TrimSpace(systemPrompt)
	if rolePrompt == "" {
		rolePrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// Common template + role description (use concise template when whitelist is present)
	var sysPrompt string
	if len(allowedTools) > 0 {
		sysPrompt = fmt.Sprintf(subagentSystemPromptTemplateConcise, workDir, cwdPart, roleName, parentAgentID, now)
	} else {
		sysPrompt = fmt.Sprintf(subagentSystemPromptTemplate, workDir, cwdPart, roleName, parentAgentID, now)
	}
	if interactive {
		sysPrompt += subagentExecutionModeInteractive
	} else {
		sysPrompt += subagentExecutionModeOneShot
	}
	sysPrompt += "\n## 角色描述\n\n" + rolePrompt + "\n"

	// Inject group info (current agent is a member of a virtual group)
	if parentCtx.GroupID != "" && len(parentCtx.GroupMembers) > 0 {
		sysPrompt += "\n## 群组协作\n\n"
		sysPrompt += fmt.Sprintf("你是虚拟群组 **%s** 的成员。群组成员：\n", parentCtx.GroupID)
		for _, m := range parentCtx.GroupMembers {
			sysPrompt += fmt.Sprintf("- %s\n", m)
		}
		sysPrompt += "\n你可以使用 **SendMessage** 工具直接向群组中的其他成员Send消息：\n"
		sysPrompt += "- `SendMessage(to=\"agent:角色/实例\", message=\"...\")` → 直接Send消息给该成员\n"
		sysPrompt += "- `SendMessage(to=\"" + parentCtx.GroupID + "\", message=\"...\")` → 广播发给所有成员\n"
		sysPrompt += "- `SendMessage(to=\"" + parentCtx.GroupID + "\", message=\"@agent:角色/实例 ...\")` → @提及特定成员\n"
		sysPrompt += "\n**注意**：你只能向同组成员发消息，不能跨群组通信。群组通信是直接的——消息会进入对方的 session，他们能看到完整的上下文并自行判断如何回应。\n"
	}

	// Inject available agent directory (only injected when spawn_agent=true)
	if caps.SpawnAgent {
		if agentsCatalog := a.agents.GetAgentsCatalog(ctx, parentCtx.SenderID); agentsCatalog != "" {
			sysPrompt += "\n" + agentsCatalog
		}
	}

	// Inject skills directory (SubAgent can use Skill tool to load skills)
	originUserID := parentCtx.OriginUserID
	if originUserID == "" {
		originUserID = parentCtx.SenderID
	}
	if skillsCatalog := a.skills.GetSkillsCatalog(ctx, originUserID); skillsCatalog != "" {
		sysPrompt += "\n" + skillsCatalog
	}

	// Pre-compute parentExtras once (shared between Phase 4 and buildSubAgentMemory)
	parentExtras := a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID)

	// Phase 4: Inject project context from AGENT.md in current working directory
	if projectCtx := LoadProjectContextFile(a.workDir); projectCtx != "" {
		sysPrompt += projectCtx
	}

	// Phase 5: Inject user language preference into SubAgent prompt.
	// Only inject if not already present in the inherited system prompt
	// (LanguageMiddleware on the main Agent already adds it via SystemParts).
	if a.settingsSvc != nil {
		if vals, err := a.settingsSvc.GetSettings(parentCtx.Channel, originUserID); err == nil {
			if lang, ok := vals["language"]; ok && lang != "" {
				// Check if language instruction is already in sysPrompt (inherited from main Agent)
				if !strings.Contains(sysPrompt, "## Language") {
					sysPrompt += "\n" + LanguageInstruction(lang)
				}
			}
		}
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage(sysPrompt),
		llm.NewUserMessage(task),
	}

	subAgentID := parentAgentID + "/" + roleName

	// SubAgent inherits parent Agent's LLM configuration (uses OriginUserID to get original user's config)
	// If the role specifies a model (including tier name like vanguard/balance/swift), use GetLLMForModel
	// to intelligently find the matching subscription. When tier has no model configured, auto-fallback to GetLLM(originUserID),
	// i.e., the model and subscription currently used by the parent agent. Same logic when model is empty.
	var llmClient llm.LLM
	var subModel string
	var userMaxCtx int
	var thinkingMode string
	if model != "" {
		llmClient, subModel, userMaxCtx, thinkingMode, _ = a.llmFactory.GetLLMForModel(originUserID, model)
	} else {
		llmClient, subModel, userMaxCtx, thinkingMode = a.llmFactory.GetLLM(originUserID)
	}

	// Stream — default ON; inherit from parent config unless explicitly disabled.
	stream := true
	if a.settingsSvc != nil {
		if vals, err := a.settingsSvc.GetSettings(parentCtx.Channel, originUserID); err == nil {
			if vals["enable_stream"] == "false" {
				stream = false
			}
		}
	}

	cfg := RunConfig{
		LLMClient:       llmClient,
		Model:           subModel,
		ThinkingMode:    thinkingMode,
		Stream:          stream,
		MaxOutputTokens: a.llmFactory.GetMaxOutputTokens(originUserID),
		Tools:           subTools,
		Messages:        messages,
		AgentID:         subAgentID,
		Channel:         parentCtx.Channel,
		ChatID:          parentCtx.ChatID,
		SenderID:        parentAgentID, // SubAgent: direct caller = parent Agent
		OriginUserID:    originUserID,  // SubAgent: inherits original user ID

		// Inherit workspace & sandbox config from parent Agent
		WorkingDir:       parentCtx.WorkingDir,
		WorkspaceRoot:    parentCtx.WorkspaceRoot,
		ReadOnlyRoots:    parentCtx.ReadOnlyRoots,
		SkillsDirs:       parentCtx.SkillsDirs,
		AgentsDir:        parentCtx.AgentsDir,
		MCPConfigPath:    parentCtx.MCPConfigPath,
		GlobalMCPConfig:  parentCtx.GlobalMCPConfigPath,
		DataDir:          parentCtx.DataDir,
		SandboxEnabled:   parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none",
		PreferredSandbox: parentCtx.PreferredSandbox,
		Sandbox:          parentCtx.Sandbox,
		SandboxMode: func() string {
			if parentCtx.Sandbox != nil {
				return parentCtx.Sandbox.Name()
			}
			return "none"
		}(),
		// Inherit parent Agent's CWD. In remote mode parentCtx.CurrentDir may be empty
		//(buildToolContext cleared host paths, and session hasn't stored CWD),
		// Fallback to a.workDir to ensure sub-Agent has the correct initial directory.
		InitialCWD: func() string {
			if parentCtx.CurrentDir != "" {
				return parentCtx.CurrentDir
			}
			return a.workDir
		}(),
		InitialGroupID:      parentCtx.GroupID,
		InitialGroupMembers: parentCtx.GroupMembers,

		MaxIterations: a.getMaxIterations(), // Inherit main Agent configuration
		// SubAgent doesn't set independent timeout, directly uses deadline from parent context

		// LLM concurrency rate-limiting: inherit parent Agent's per-tenant semaphore
		LLMSemAcquire: a.llmFactory.LLMSemAcquireForUser(originUserID),

		// ToolExecutor = nil → use defaultToolExecutor (unified buildToolContext)
	}

	// Per-user token usage tracking: SubAgent's token consumption is attributed to the original user
	cfg.RecordUserTokenUsage = func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int) {
		if err := a.multiSession.RecordUserTokenUsage(originUserID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"sender_id":    originUserID,
				"sub_agent_id": subAgentID,
			}).Warn("Failed to record SubAgent token usage")
		}
	}

	// Independent sessionKey: uses subAgentID to ensure isolation from parent Agent,
	// avoiding data pollution in tool activation, OffloadStore, MaskStore etc. indexed by sessionKey.
	cfg.SessionKey = subAgentID

	// RootSessionKey: records the top-level Agent (main Agent)'s session key,
	// for scenarios like offload_recall that need access to parent session data (e.g. SubAgent recalling parent Agent's offload data).
	rootKey := parentCtx.RootSessionKey
	if rootKey == "" {
		rootKey = parentCtx.Channel + ":" + parentCtx.ChatID
	}
	cfg.RootSessionKey = rootKey

	// === Context Mask unified mechanism: inject 6 missing fields ===
	// SubAgent and main Agent share the same Run() loop; context mask (offload/mask/context-edit)
	// depends on these fields to trigger correctly. Previous absence caused SubAgent context compression/masking to never take effect.

	// 1. ContextManager: create independent instance (don't share parent Agent's triggers, avoid counter cross-contamination)
	//    Moved out of caps.Memory condition; all SubAgents need compression capability.
	if a.contextManagerConfig != nil {
		cmCfg := applyUserMaxContext(a.contextManagerConfig, userMaxCtx)
		cfg.ContextManager = newPhase1Manager(cmCfg)
		cfg.ContextManagerConfig = cmCfg
	}

	// 2. OffloadStore: share parent Agent instance (isolated by sessionKey, fully safe)
	cfg.OffloadStore = a.offloadStore

	// 3. MaskStore: share parent Agent instance (looked up by random ID, capacity shared but SubAgent has short lifecycle, impact negligible)
	cfg.MaskStore = a.maskStore

	// 4. ContextEditor: create independent instance (each Agent needs its own messages reference and edit history)
	cfg.ContextEditor = NewContextEditor(NewContextEditStore(100))

	// Capability: send_message — allows SubAgent to send messages to IM channels
	if caps.SendMessage {
		cfg.SendFunc = a.sendMessage
	}

	// Capability: memory — create independent memory system
	// SubAgent's session = private chat with the calling Agent. Caller is "user", SubAgent is "xbot".
	// Isolated via deriveSubAgentTenantID: each (parentTenantID, parentAgentID, roleName) combination
	// produces a unique tenantID, ensuring SubAgent and parent Agent read/write completely different memory data.
	if caps.Memory {
		extras, mem := a.buildSubAgentMemory(ctx, parentCtx, parentExtras, parentAgentID, roleName)
		if extras != nil && mem != nil {
			cfg.ToolContextExtras = extras
			cfg.Memory = mem

			// Inject memory usage guide into system prompt
			messages[0].Content += subagentMemorySection

			// Inject memory into system prompt (SubAgent doesn't use pipeline, needs manual Recall call)
			subSenderID := subAgentHumanBlockSenderID(parentAgentID)
			memCtx := letta.WithUserID(ctx, subSenderID)
			if recallText, err := mem.Recall(memCtx, task); err == nil && recallText != "" {
				messages[0].Content += "\n\n" + recallText
			}

		}
	} else {
		// When no memory capability, remove memory tools to prevent SubAgent from failing when trying to call them
		subTools.Unregister("core_memory_append")
		subTools.Unregister("core_memory_replace")
		subTools.Unregister("rethink")
		subTools.Unregister("archival_memory_insert")
		subTools.Unregister("archival_memory_search")
		subTools.Unregister("recall_memory_search")
	}

	// Capability: spawn_agent — allows SubAgent to create child Agents
	if caps.SpawnAgent {
		cfg.SpawnAgent = func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return a.spawnSubAgent(ctx, msg)
		}
	}
	// HookManager — SubAgent inherits parent Agent's hook manager
	cfg.HookManager = a.hookManager
	cfg.SettingsSvc = a.settingsSvc
	cfg.MessageSender = a.messageSender
	cfg.RegisterAgentChannel = a.registerAgentChannel
	cfg.UnregisterAgentChannel = a.unregisterAgentChannel

	// Interactive callbacks are injected independently, not dependent on SpawnAgent
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance, tail)
		},
	}

	return cfg
}

// buildToolExecutor builds the main Agent's tool executor.
// Includes complete logic for session MCP lookup, activation check, tool usage tracking, etc.
// This is the executor used by main Agent and Cron; SubAgent uses defaultToolExecutor.
func (a *Agent) buildToolExecutor(channel, chatID, senderID, senderName, sandboxUserID string) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	sessionKey := channel + ":" + chatID

	// Pre-build RunConfig outside closure to avoid reallocating on every tool call.
	// Only ctx (from the caller) changes per-call; all config fields are stable.
	wsRoot := a.workspaceRoot(sandboxUserID)
	isRemote := a.isRemoteUser(sandboxUserID)
	// For remote users, leave WorkspaceRoot/WorkingDir empty — the runner
	// manages its own filesystem. Keep SkillsDirs/AgentsDir as host paths
	// for server-side sync (EnsureSynced reads global skills from host).
	var workspaceRoot, workingDir string
	if !isRemote {
		workspaceRoot = wsRoot
		workingDir = a.workDir
	}
	cfg := &RunConfig{
		AgentID:      "main",
		Channel:      channel,
		ChatID:       chatID,
		SenderID:     senderID,      // Main Agent: direct caller (for message routing)
		OriginUserID: sandboxUserID, // 沙箱/工作区用户（飞书Identity登录 web 时为飞书 ou_xxx）
		SenderName:   senderName,
		SendFunc:     a.sendMessage,

		WorkingDir:             workingDir,
		WorkspaceRoot:          workspaceRoot,
		ReadOnlyRoots:          a.globalSkillDirs,
		SkillsDirs:             a.globalSkillDirs,
		AgentsDir:              a.agentsDir,
		MCPConfigPath:          tools.UserMCPConfigPath(a.workDir, sandboxUserID),
		GlobalMCPConfig:        filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:                a.workDir,
		SandboxEnabled:         a.sandboxMode != "none",
		PreferredSandbox:       a.sandboxMode,
		Sandbox:                resolveSandbox(a.sandbox, sandboxUserID),
		SandboxMode:            a.sandboxMode,
		InjectInbound:          a.injectInbound,
		Tools:                  a.tools,
		BgTaskManager:          a.bgTaskMgr,
		MessageSender:          a.messageSender,
		RegisterAgentChannel:   a.registerAgentChannel,
		UnregisterAgentChannel: a.unregisterAgentChannel,
	}

	cfg.SpawnAgent = func(spawnCtx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
		return a.spawnSubAgent(spawnCtx, inMsg)
	}

	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, channel, chatID, instance, tail)
		},
	}

	// Pre-build Letta memory extras (involves GetOrCreateSession + LettaMemory lookup).
	cfg.ToolContextExtras = a.buildToolContextExtras(channel, chatID)

	// Inherit hook manager from Agent.
	cfg.HookManager = a.hookManager
	cfg.SettingsSvc = a.settingsSvc

	var sessionOnce sync.Once

	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		// Lazy-inject session so buildToolContext can persist CWD across tool calls.
		// Without this, Cd stores CWD in a ToolContext that is discarded on next call.
		// Use sync.Once to prevent concurrent goroutines from racing on cfg.Session.
		sessionOnce.Do(func() {
			if cfg.Session == nil {
				if sess, err := a.multiSession.GetOrCreateSession(channel, chatID); err == nil {
					cfg.Session = sess
				}
			}
		})

		// 1. tool lookup: session MCP first, then global registry
		var tool tools.Tool
		ok := false

		if mcpMgr := a.multiSession.GetSessionMCPManager(sessionKey); mcpMgr != nil {
			for _, st := range mcpMgr.GetSessionTools() {
				if st.Name() == tc.Name {
					tool = st
					ok = true
					break
				}
			}
		}
		if !ok {
			tool, ok = a.tools.Get(tc.Name)
		}
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		// 2. Activation check: unactivated tools return a prompt
		if !a.tools.IsToolActive(sessionKey, tc.Name) {
			return &tools.ToolResult{
				Summary: fmt.Sprintf("Tool %q is not loaded yet. Call load_tools(tools=%q) first to load it before use.", tc.Name, tc.Name),
			}, nil
		}

		// 3. Refresh tool's last-used round, extending activation validity
		a.tools.TouchTool(sessionKey, tc.Name)

		// 4. Ensure user working directory exists (skip in remote mode, runner manages filesystem itself)
		if !a.isRemoteUser(senderID) {
			if err := os.MkdirAll(wsRoot, 0o755); err != nil {
				return nil, fmt.Errorf("create user workspace: %w", err)
			}
		}

		toolExecCtx := withApprovalTarget(ctx, cfg.ChatID, cfg.OriginUserID)
		if cfg.SettingsSvc != nil {
			permUsers := cfg.SettingsSvc.GetPermUsers(cfg.Channel, cfg.OriginUserID)
			if permUsers != nil {
				toolExecCtx = tools.WithPermUsers(toolExecCtx, permUsers.DefaultUser, permUsers.PrivilegedUser)
			}
		}

		// 5. Build ToolContext (unified path, only ctx changes)
		toolCtx := buildToolContext(toolExecCtx, cfg)

		// 6-8. Execute with hooks (shared implementation — same as defaultToolExecutor)
		return executeWithHooks(cfg.HookManager, toolExecCtx, toolCtx, tc.Name, tc.Arguments, tool, hooks.BasePayload{
			SessionID: cfg.ChatID,
			Channel:   cfg.Channel,
			SenderID:  cfg.OriginUserID,
			ChatID:    cfg.ChatID,
		})
	}
}

// buildOAuthHandler builds the OAuth auto-trigger handler.
func (a *Agent) buildOAuthHandler(channel, chatID, senderID, sessionKey string) func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
	return func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
		if !oauth.IsTokenNeededError(execErr) {
			return "", false
		}

		// Skip if already triggered, avoid duplicate OAuth state
		if _, sent := a.sessionFinalSent.Load(sessionKey); sent {
			log.Ctx(ctx).WithFields(log.Fields{
				"tool":   tc.Name,
				"reason": "sessionFinalSent already set, skipping duplicate oauth_authorize",
			}).Info("Skip duplicate OAuth auto-trigger")
			return "OAuth authorization already in progress.", true
		}

		log.Ctx(ctx).WithFields(log.Fields{
			"tool": tc.Name,
		}).Info("OAuth token needed, auto-triggering oauth_authorize tool")

		oauthTool, ok := a.tools.Get("oauth_authorize")
		if !ok {
			return "OAuth authorization required but oauth_authorize tool not found. Please enable OAuth in configuration.", true
		}

		oauthInput := fmt.Sprintf(`{"provider": "feishu", "reason": "needed to access %s"}`, tc.Name)
		oauthCtx := &tools.ToolContext{
			Ctx:      ctx,
			Channel:  channel,
			ChatID:   chatID,
			SenderID: senderID,
			SendFunc: a.sendMessage,
		}
		oauthResult, oauthErr := oauthTool.Execute(oauthCtx, oauthInput)
		if oauthErr == nil && oauthResult != nil {
			a.sessionFinalSent.Store(sessionKey, true)
			return oauthResult.Summary, true
		}

		log.Ctx(ctx).WithError(oauthErr).Error("Failed to execute oauth_authorize tool")
		return "OAuth authorization required. Please configure OAUTH_ENABLE=true and OAUTH_BASE_URL in your environment.", true
	}
}

// buildMemoryToolSetup returns tool definitions and executor for memory tools during compaction.
// Uses the real tool registry instead of hand-written execution logic,
// ensuring tool behavior stays in sync with the main agent loop.
// Returns (nil, nil) if memory tools are not available.
func (a *Agent) buildMemoryToolSetup(channel, chatID string) ([]llm.ToolDefinition, func(ctx context.Context, tc llm.ToolCall) (string, error)) {
	extras := a.buildToolContextExtras(channel, chatID)
	if extras == nil || extras.CoreMemory == nil {
		return nil, nil
	}

	memToolNames := []string{
		"core_memory_append", "core_memory_replace", "rethink",
		"archival_memory_insert", "archival_memory_search",
	}
	var defs []llm.ToolDefinition
	for _, name := range memToolNames {
		if t, ok := a.tools.Get(name); ok {
			defs = append(defs, t)
		}
	}
	if len(defs) == 0 {
		return nil, nil
	}

	// Minimal RunConfig for building ToolContext — memory tools only need ToolContextExtras.
	memCfg := &RunConfig{
		Channel:           channel,
		ChatID:            chatID,
		ToolContextExtras: extras,
	}

	exec := func(ctx context.Context, tc llm.ToolCall) (string, error) {
		tool, ok := a.tools.Get(tc.Name)
		if !ok {
			return "Unknown tool: " + tc.Name, nil
		}
		toolCtx := buildToolContext(ctx, memCfg)
		result, err := tool.Execute(toolCtx, tc.Arguments)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return result.Summary, nil
	}

	return defs, exec
}

// buildToolContextExtras builds ToolContext extension fields.
// Common fields (TenantID, MemorySvc) are obtained directly from TenantSession, effective for all memory types.
// LettaMemory-specific fields (CoreMemory, ArchivalMemory, ToolIndexer) are only set when using LettaMemory.
func (a *Agent) buildToolContextExtras(channel, chatID string) *ToolContextExtras {
	extras := &ToolContextExtras{
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	ts, err := a.multiSession.GetOrCreateSession(channel, chatID)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"channel": channel,
			"chat_id": chatID,
		}).Warn("buildToolContextExtras: GetOrCreateSession failed, fields will be empty")
	} else {
		// Tenant-level fields: work for all memory provider types
		extras.TenantID = ts.TenantID()
		extras.MemorySvc = ts.MemoryService()
		extras.RecallTimeRange = a.multiSession.RecallTimeRangeFunc()

		// LettaMemory-specific fields
		if lm, ok := ts.Memory().(*letta.LettaMemory); ok {
			extras.CoreMemory = lm.CoreService()
			extras.ArchivalMemory = lm.ArchivalService()
			extras.ToolIndexer = lm
		}
	}

	return extras
}

// buildSubAgentMemory builds an independent memory system for SubAgent.
//
// Core design: SubAgent's session = private chat with the calling Agent.
// Caller is "user", SubAgent is "xbot". This maintains a highly consistent agent logic abstraction.
//
// Isolation strategy:
//   - tenantID: generated via deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName)
//   - persona: fully independent (SubAgent's own identity, not inherited from parent)
//   - human: isolated via parentAgentID (records calling agent's characteristics, not the end user)
//   - archival memory / working_context: automatically isolated via tenantID
//
// Returns (ToolContextExtras, MemoryProvider). If creation fails, returns nil, nil and logs a warning.
func (a *Agent) buildSubAgentMemory(
	ctx context.Context,
	parentCtx *tools.ToolContext,
	parentExtras *ToolContextExtras,
	parentAgentID, roleName string,
) (*ToolContextExtras, memory.MemoryProvider) {
	// 1. Get parent Agent's tenantID (for deriving SubAgent's tenantID)
	if parentExtras.TenantID == 0 {
		log.Ctx(ctx).WithField("parent", parentAgentID).Warn("SubAgent memory: parent tenantID is 0, skipping memory setup")
		return nil, nil
	}

	// 2. Derive SubAgent's independent tenantID
	subTenantID := deriveSubAgentTenantID(parentExtras.TenantID, parentAgentID, roleName)

	// 3. Get shared services (accessed via multiSession)
	coreSvc := a.multiSession.CoreMemoryService()
	archivalSvc := a.multiSession.ArchivalService()
	memorySvc := a.multiSession.MemoryService()

	// 4. Initialize SubAgent's core memory blocks (persona + human)
	//    persona: empty, accumulated by SubAgent via memorize (not pre-filled with systemPrompt to avoid duplicate injection)
	//    human: isolated by parentAgentID as senderID
	subSenderID := subAgentHumanBlockSenderID(parentAgentID)
	if err := coreSvc.InitBlocks(subTenantID, subSenderID); err != nil {
		log.Ctx(ctx).WithError(err).WithFields(log.Fields{
			"tenant_id":     subTenantID,
			"parent_agent":  parentAgentID,
			"role":          roleName,
			"sub_sender_id": subSenderID,
		}).Warn("SubAgent memory: failed to init core blocks")
		return nil, nil
	}

	// 5. Create independent LettaMemory instance
	toolIndexSvc := a.multiSession.ToolIndexService()
	mem := letta.New(subTenantID, coreSvc, archivalSvc, memorySvc, toolIndexSvc)

	// 6. Build ToolContextExtras (for SubAgent's tools to use)
	extras := &ToolContextExtras{
		TenantID:                subTenantID,
		CoreMemory:              coreSvc,
		ArchivalMemory:          archivalSvc,
		MemorySvc:               memorySvc,
		RecallTimeRange:         a.multiSession.RecallTimeRangeFunc(),
		ToolIndexer:             mem,
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"sub_tenant_id": subTenantID,
		"parent_agent":  parentAgentID,
		"role":          roleName,
		"sub_sender_id": subSenderID,
	}).Info("SubAgent memory: created independent memory system")

	return extras, mem
}

// subAgentHumanBlockSenderID returns the virtual senderID used for the SubAgent's
// human block. This isolates SubAgent's human block from the parent's by using
// parentAgentID as the key, so each SubAgent role sees a different "user".
func subAgentHumanBlockSenderID(parentAgentID string) string {
	return "agent:" + parentAgentID
}

// consolidateSubAgentMemory runs a lightweight memorize pass after SubAgent exits.
// It extracts key information from the SubAgent's conversation messages and
// persists them to the SubAgent's independent memory via Memorize().
func (a *Agent) consolidateSubAgentMemory(
	ctx context.Context,
	cfg RunConfig,
	messages []llm.ChatMessage,
	task string,
	roleName string,
	parentAgentID string,
) {
	mem := cfg.Memory
	extras := cfg.ToolContextExtras
	if mem == nil || extras == nil {
		return
	}

	// Build memorize input with all conversation messages and LLM client
	memInput := memory.MemorizeInput{
		Messages:  messages,
		LLMClient: cfg.LLMClient,
		Model:     cfg.Model,
	}

	// Call Memorize with the SubAgent's virtual senderID context
	subSenderID := subAgentHumanBlockSenderID(parentAgentID)
	memCtx := letta.WithUserID(ctx, subSenderID)

	if _, err := mem.Memorize(memCtx, memInput); err != nil {
		log.Ctx(ctx).WithError(err).WithFields(log.Fields{
			"role":      roleName,
			"tenant_id": extras.TenantID,
		}).Warn("SubAgent memory consolidation failed")
	}
}

// setupSubAgentProgress configures the progress reporting chain for a SubAgent.
// It builds a subCtx with CallChain and passthrough callback, sets ProgressNotifier on cfg,
// and returns the subCtx to use for Run().
func (a *Agent) setupSubAgentProgress(
	ctx context.Context,
	cfg *RunConfig,
	cc *CallChain,
	roleName, originChannel, originChatID string,
	parentAgentID string,
) context.Context {
	myDepth := cc.Depth() + 1
	myPath := cc.Spawn(roleName).Chain

	// Determine the parent callback for current level (may be nil)
	parentCB, _ := SubAgentProgressFromContext(ctx)

	// Build subCtx: pass CallChain + passthrough callback
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// Inject passthrough callback into subCtx, letting deeper SubAgents recursively report progress to top level
	// Passthrough callback wraps parentCB, accumulating depth and path
	subCtx = WithSubAgentProgress(subCtx, func(detail SubAgentProgressDetail) {
		detail.Depth = myDepth + detail.Depth
		if len(detail.Path) == 0 {
			detail.Path = myPath
		}
		if parentCB != nil {
			parentCB(detail)
		}
	})

	// Set current level's ProgressNotifier
	if parentCB != nil {
		// Non-top-level: passthrough progress to parent agent (handled by the passthrough callback above)
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				parentCB(SubAgentProgressDetail{
					Path:  myPath,
					Lines: lines,
					Depth: myDepth,
				})
			}
		}
	} else if originChannel != "" && originChatID != "" {
		// Top-level agent (interactive): only send depth=1 progress to chat window
		rn := roleName
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				last := lines[len(lines)-1]
				if idx := strings.LastIndex(last, "\n"); idx >= 0 {
					last = last[idx+1:]
				}
				prefixed := "📋 subagent: [" + rn + "] " + last + "\n"
				_ = a.sendMessage(originChannel, originChatID, prefixed)
			}
		}
	}

	return subCtx
}

// oneshotSubAgentSession holds the state for a registered one-shot SubAgent.
type oneshotSubAgentSession struct {
	instance string
	key      string
	ia       *interactiveAgent
}

// registerOneshotSubAgent registers a one-shot subagent in interactiveSubAgents,
// creates a TenantSession, wires CLI progress and iteration snapshot callbacks.
// Returns the session state or an error if tenant session creation fails.
func (a *Agent) registerOneshotSubAgent(
	ctx context.Context,
	cfg *RunConfig,
	originChannel, originChatID, roleName, task string,
) (*oneshotSubAgentSession, error) {
	// Register one-shot subagent in interactiveSubAgents so it's visible
	// in the Ctrl+T panel. Kept after completion for history viewing; TTL cleans it up.
	oneshotInstance := fmt.Sprintf("oneshot-%s-%d", roleName, time.Now().UnixNano())
	oneshotKey := interactiveKey(originChannel, originChatID, roleName, oneshotInstance)
	oneshotIA := &interactiveAgent{
		roleName:   roleName,
		instance:   oneshotInstance,
		lastUsed:   time.Now(),
		running:    true,
		background: false,
		task:       task,
	}
	a.interactiveSubAgents.Store(oneshotKey, oneshotIA)

	// Create TenantSession for message persistence (same as interactive SubAgents).
	agentTenantSession, err := a.multiSession.GetOrCreateSession("agent", oneshotKey)
	if err != nil {
		a.interactiveSubAgents.Delete(oneshotKey)
		return nil, fmt.Errorf("create oneshot agent tenant session: %w", err)
	}
	cfg.Session = agentTenantSession
	if err := agentTenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).WithField("instance", oneshotInstance).Warn("Failed to clear old sub-agent session")
	}

	// Eager-save user message so get_history returns it during Run().
	if err := agentTenantSession.AddMessage(llm.NewUserMessage(task)); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to eager-save oneshot agent user message")
	}

	// Wire CLI progress + stream callbacks so Ctrl+T shows real-time progress.
	a.wireSubAgentCLIProgress(oneshotKey, originChatID, cfg)

	// Wire incremental snapshot callback so iteration history is available
	// during Run() for panel preview and inspect — not only after completion.
	// Lock mu to avoid data race with ListInteractiveSessions/summarizeInteractivePreviewLocked.
	cfg.OnIterationSnapshot = func(snap IterationSnapshot) {
		oneshotIA.mu.Lock()
		oneshotIA.iterationHistory = append(oneshotIA.iterationHistory, snap)
		oneshotIA.mu.Unlock()
	}

	return &oneshotSubAgentSession{
		instance: oneshotInstance,
		key:      oneshotKey,
		ia:       oneshotIA,
	}, nil
}

// finalizeSubAgentRun handles post-run cleanup: marks the agent as not running,
// destroys the session, emits hook events, handles errors, and consolidates memory.
// Returns the final OutboundMessage.
func (a *Agent) finalizeSubAgentRun(
	ctx context.Context,
	cfg RunConfig,
	out *RunOutput,
	sess *oneshotSubAgentSession,
	originChannel, originChatID, originSender, roleName, task, parentAgentID string,
) (*bus.OutboundMessage, error) {
	// Populate iteration history so inspect can show results after completion
	sess.ia.mu.Lock()
	sess.ia.running = false
	if out != nil {
		sess.ia.lastReply = out.Content
		log.Ctx(ctx).WithField("iteration_count", len(sess.ia.iterationHistory)).Info("oneshot subagent completed")
	} else {
		log.Ctx(ctx).Warn("oneshot subagent returned nil output")
		sess.ia.mu.Unlock()
		a.destroyInteractiveSession(sess.key)
		return &bus.OutboundMessage{}, nil
	}
	sess.ia.mu.Unlock()
	// Cascade-cancel any bg sessions spawned during this one-shot's Run(),
	// then destroy the one-shot session immediately (no TTL retention).
	a.cancelChildSessions(sess.key)
	a.destroyInteractiveSession(sess.key)

	log.Ctx(ctx).WithFields(log.Fields{
		"parent":    parentAgentID,
		"role":      roleName,
		"tools":     out.ToolsUsed,
		"has_error": out.Error != nil,
	}).Info("SubAgent completed (via Run)")

	// Emit SubAgentStop event (notification, non-blocking)
	if a.hookManager != nil {
		a.hookManager.Emit(ctx, &hooks.SubAgentStopEvent{
			BasePayload: hooks.BasePayload{
				SessionID: originChatID, Channel: originChannel,
				SenderID: originSender, ChatID: originChatID,
			},
			AgentType: roleName,
			Instance:  sess.instance,
			Content:   out.Content,
		})
	}

	if out.Error != nil {
		content := out.Content
		if content == "" {
			content = "⚠️ SubAgent 执行失败，未产生任何输出。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
	}

	// SubAgent memory consolidation: write key information from this conversation into SubAgent's independent memory
	// Execute synchronously to ensure memory is written before returning, avoiding memory loss from session unload.
	if cfg.Memory != nil && len(out.Messages) > 0 {
		a.consolidateSubAgentMemory(ctx, cfg, out.Messages, task, roleName, parentAgentID)
	}

	return out.OutboundMessage, nil
}

// spawnSubAgent creates and runs a SubAgent via Run().
// This is the SpawnAgent callback implementation, converting InboundMessage to RunConfig and calling Run().
func (a *Agent) spawnSubAgent(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	parentAgentID := msg.ParentAgentID
	task := msg.Content
	systemPrompt := msg.SystemPrompt
	allowedTools := msg.AllowedTools
	roleName := msg.RoleName

	// --- CallChain depth & loop check ---
	cc := CallChainFromContext(ctx)
	if roleName != "" {
		if err := cc.CanSpawn(roleName, a.maxSubAgentDepth); err != nil {
			log.Ctx(ctx).WithFields(log.Fields{
				"parent": parentAgentID,
				"role":   roleName,
				"chain":  cc.Chain,
			}).Warn("SubAgent spawn blocked by CallChain")
			return &bus.OutboundMessage{
				Channel: "",
				ChatID:  "",
				Content: err.Error(),
				Error:   err,
			}, nil
		}
	}

	// Build parentCtx (restored from InboundMessage)
	originChannel, originChatID, originSender := resolveOriginIDs(msg)
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	log.Ctx(ctx).WithFields(log.Fields{
		"parent": parentAgentID,
		"role":   roleName,
		"task":   tools.Truncate(task, 80),
	}).Info("SubAgent started (via Run)")

	// Restore capabilities from InboundMessage
	caps := tools.CapabilitiesFromMap(msg.Capabilities)

	// Get role-specified model from InboundMessage metadata
	subModel := ""
	if msg.Metadata != nil {
		subModel = msg.Metadata["model"]
	}

	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName, false, subModel)

	// Set up progress reporting chain
	subCtx := a.setupSubAgentProgress(ctx, &cfg, cc, roleName, originChannel, originChatID, parentAgentID)

	// Register one-shot subagent and wire session/callbacks
	sess, err := a.registerOneshotSubAgent(ctx, &cfg, originChannel, originChatID, roleName, task)
	if err != nil {
		return nil, err
	}

	// Emit SubAgentStart event (notification, non-blocking)
	if a.hookManager != nil {
		a.hookManager.Emit(ctx, &hooks.SubAgentStartEvent{
			BasePayload: hooks.BasePayload{
				SessionID: originChatID, Channel: originChannel,
				SenderID: originSender, ChatID: originChatID,
			},
			AgentType: roleName,
			Task:      task,
		})
	}

	out := Run(subCtx, cfg)

	return a.finalizeSubAgentRun(ctx, cfg, out, sess, originChannel, originChatID, originSender, roleName, task, parentAgentID)
}

// convertWsSubAgentTree converts agent.SubAgentNode to channelpkg.WsSubAgent tree.
func convertWsSubAgentTree(nodes []SubAgentNode) []channelpkg.WsSubAgent {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]channelpkg.WsSubAgent, len(nodes))
	for i, n := range nodes {
		result[i] = channelpkg.WsSubAgent{
			Role:     n.Role,
			Status:   n.Status,
			Desc:     n.Desc,
			Children: convertWsSubAgentTree(n.Children),
		}
	}
	return result
}

// convertCLISubAgentTree converts agent.SubAgentNode to channelpkg.CLISubAgent tree.
func convertCLISubAgentTree(nodes []SubAgentNode) []channelpkg.CLISubAgent {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]channelpkg.CLISubAgent, len(nodes))
	for i, n := range nodes {
		result[i] = channelpkg.CLISubAgent{
			Role:     n.Role,
			Status:   n.Status,
			Desc:     n.Desc,
			Children: convertCLISubAgentTree(n.Children),
		}
	}
	return result
}
