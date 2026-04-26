package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	channelpkg "xbot/channel"
	"xbot/clipanic"
	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"
)

// bgSessionCtxKey is a context value marker for background interactive session contexts.
// When present, it indicates the context belongs to a bg session (not a per-request ctx).
// Nested bg subagents detect this marker to derive from their parent session's lifecycle
// instead of the Agent-level ctx, ensuring they never outlive their direct parent.
type bgSessionCtxKey struct{}

// bgParentKey stores the parent session's interactiveSubAgents map key,
// enabling cascade cleanup when a parent session is unloaded or cancelled.
type bgParentKey struct{}

// interactiveAgent wraps an interactive SubAgent session.
// Stored in the parent Agent's interactiveSubAgents map.
type interactiveAgent struct {
	roleName         string              // Role name
	instance         string              // instance ID
	groupID          string              // Group chat ID (e.g. "group:g1", empty=not in group chat)
	messages         []llm.ChatMessage   // Accumulated conversation history (excluding system prompt)
	iterationHistory []IterationSnapshot // Recent iteration snapshots, for inspect/tail use
	mu               sync.Mutex          // Protect session state concurrent access
	systemPrompt     llm.ChatMessage     // System prompt at spawn time (maintain consistency, not rebuilt on subsequent sends)
	cfg              *RunConfig          // RunConfig template (Messages=nil, reused for send/unload)
	lastUsed         time.Time           // Last access time, for TTL cleanup
	running          bool                // Whether a Run is currently executing
	background       bool                // Whether background mode
	cancelCurrent    context.CancelFunc  // Cancel function for current run (nil = idle)
	parentKey        string              // parent session key (for cascade cleanup on unload/cancel)
	lastError        string              // Most recent error
	lastReply        string              // Most recent reply summary
	task             string              // Task description for one-shot subagent (empty for interactive)
}

// interactiveSessionTTL is the lifetime of interactive SubAgent sessions.
const interactiveSessionTTL = 30 * time.Minute

// cleanupExpiredSessions cleans up all expired interactive SubAgent sessions.
// sync.Map is inherently concurrency-safe, callers don't need any additional locks.
func (a *Agent) cleanupExpiredSessions() {
	now := time.Now()
	a.interactiveSubAgents.Range(func(k, v interface{}) bool {
		ia, ok := v.(*interactiveAgent)
		if !ok || ia == nil {
			a.interactiveSubAgents.Delete(k)
			return true
		}
		// Reading lastUsed requires locking to avoid write race with SendToInteractiveSession
		ia.mu.Lock()
		lastUsed := ia.lastUsed
		ia.mu.Unlock()
		if now.Sub(lastUsed) > interactiveSessionTTL {
			key, ok := k.(string)
			if !ok {
				return true
			}
			log.WithFields(log.Fields{
				"key":       key,
				"role":      ia.roleName,
				"idle_time": now.Sub(lastUsed).String(),
			}).Info("Cleaning up expired interactive session")
			a.destroyInteractiveSession(key)
		}
		return true
	})
}

// recordIterationSnapshot appends the previous snapshot to iteration history if the
// shouldAppend predicate returns true. Uses CAS loop to avoid TOCTOU races on sync.Map.
func (a *Agent) recordIterationSnapshot(key string, shouldAppend func(prev *channelpkg.CLIProgressPayload) bool) {
	prevSnap, loaded := a.lastProgressSnapshot.Load(key)
	if !loaded {
		return
	}
	prev := prevSnap.(*channelpkg.CLIProgressPayload)
	if !shouldAppend(prev) {
		return
	}
	for {
		histPtr, _ := a.iterationHistories.LoadOrStore(key, &[]channelpkg.CLIProgressPayload{})
		hist := *histPtr.(*[]channelpkg.CLIProgressPayload)
		already := false
		for _, h := range hist {
			if h.Iteration == prev.Iteration {
				already = true
				break
			}
		}
		if already {
			return
		}
		updated := append(hist, *prev)
		if a.iterationHistories.CompareAndSwap(key, histPtr, &updated) {
			return
		}
	}
}

// wireSubAgentCLIProgress sets up ProgressEventHandler and stream callbacks on cfg
// so the SubAgent's progress is pushed to CLI (both local and remote) with its own
// ChatID. This enables Ctrl+T session switching to show real-time progress for both
// interactive and one-shot SubAgents.
func (a *Agent) wireSubAgentCLIProgress(key, originChatID string, cfg *RunConfig) {
	if a.channelFinder == nil {
		return
	}
	ch, ok := a.channelFinder("cli")
	if !ok {
		return
	}
	var localCh *channelpkg.CLIChannel
	var remoteCh *channelpkg.RemoteCLIChannel
	if cc, ok := ch.(*channelpkg.CLIChannel); ok {
		localCh = cc
	} else if rc, ok := ch.(*channelpkg.RemoteCLIChannel); ok {
		remoteCh = rc
	}
	if localCh == nil && remoteCh == nil {
		return
	}

	agentProgressKey := "agent:" + key
	cfg.ProgressEventHandler = func(event *ProgressEvent) {
		if event == nil || event.Structured == nil {
			return
		}
		s := event.Structured

		cliPayload := &channelpkg.CLIProgressPayload{
			ChatID: agentProgressKey, Phase: string(s.Phase),
			Iteration: s.Iteration, Thinking: s.ThinkingContent,
			Reasoning: s.ReasoningContent, HistoryCompacted: s.HistoryCompacted,
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
		if len(s.Todos) > 0 {
			cliPayload.Todos = make([]channelpkg.CLITodoItem, len(s.Todos))
			for i, td := range s.Todos {
				cliPayload.Todos[i] = channelpkg.CLITodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
			}
		}
		if s.TokenUsage != nil {
			cliPayload.TokenUsage = &channelpkg.CLITokenUsage{
				PromptTokens: s.TokenUsage.PromptTokens, CompletionTokens: s.TokenUsage.CompletionTokens,
				TotalTokens: s.TokenUsage.TotalTokens, CacheHitTokens: s.TokenUsage.CacheHitTokens,
			}
		}

		if localCh != nil {
			localCh.SendProgress(key, cliPayload)
		} else if remoteCh != nil {
			wsPayload := &channelpkg.WsProgressPayload{
				ChatID: agentProgressKey, Phase: string(s.Phase),
				Iteration: s.Iteration, Thinking: s.ThinkingContent,
			}
			for _, t := range s.ActiveTools {
				wsPayload.ActiveTools = append(wsPayload.ActiveTools, channelpkg.WsToolProgress{
					Name: t.Name, Label: t.Label, Status: string(t.Status),
					Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary,
				})
			}
			for _, t := range s.CompletedTools {
				wsPayload.CompletedTools = append(wsPayload.CompletedTools, channelpkg.WsToolProgress{
					Name: t.Name, Label: t.Label, Status: string(t.Status),
					Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary,
				})
			}
			remoteCh.SendProgress(originChatID, wsPayload)
		}

		// Save snapshot + track iteration history for mid-session reconnect.
		a.recordIterationSnapshot(agentProgressKey, func(prev *channelpkg.CLIProgressPayload) bool {
			return s.Iteration > prev.Iteration && prev.Iteration >= 0
		})
		a.lastProgressSnapshot.Store(agentProgressKey, cliPayload)
	}

	// Wire stream callbacks for real-time rendering
	cfg.Stream = true
	if localCh != nil {
		cfg.StreamContentFunc = func(content string) {
			localCh.SendProgress(key, &channelpkg.CLIProgressPayload{ChatID: agentProgressKey, StreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*channelpkg.CLIProgressPayload)
				cp.StreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
		cfg.StreamReasoningFunc = func(content string) {
			localCh.SendProgress(key, &channelpkg.CLIProgressPayload{ChatID: agentProgressKey, ReasoningStreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*channelpkg.CLIProgressPayload)
				cp.ReasoningStreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
	} else if remoteCh != nil {
		cfg.StreamContentFunc = func(content string) {
			remoteCh.SendProgress(originChatID, &channelpkg.WsProgressPayload{ChatID: agentProgressKey, StreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*channelpkg.CLIProgressPayload)
				cp.StreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
		cfg.StreamReasoningFunc = func(content string) {
			remoteCh.SendProgress(originChatID, &channelpkg.WsProgressPayload{ChatID: agentProgressKey, ReasoningStreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*channelpkg.CLIProgressPayload)
				cp.ReasoningStreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
	}
}

// destroyInteractiveSession removes all resources for an interactive SubAgent session:
// interactiveSubAgents entry, progress snapshot/iteration history, and tenant session (DB).
// This ensures the next SubAgent with the same role/instance starts with a clean slate.
func (a *Agent) destroyInteractiveSession(key string) {
	// Auto-cleanup group membership: remove this agent from its group,
	// delete the group if no members remain.
	if val, ok := a.interactiveSubAgents.Load(key); ok {
		if ia, ok := val.(*interactiveAgent); ok && ia != nil && ia.groupID != "" {
			memberAddr := "agent:" + ia.roleName + "/" + ia.instance
			tools.RemoveMember(ia.groupID, memberAddr)
		}
	}

	a.interactiveSubAgents.Delete(key)

	// Clean up progress snapshot and iteration history
	agentProgressKey := "agent:" + key
	a.lastProgressSnapshot.Delete(agentProgressKey)
	a.iterationHistories.Delete(agentProgressKey)

	// Destroy tenant session (cache + DB with CASCADE to messages)
	if a.multiSession != nil {
		_ = a.multiSession.DestroySession("agent", key)
	}
}

// interactiveKey generates the interactive session key in the map.
// Uses channel:chatID/roleName[:instance] to ensure only one session per chat + role + instance.
// When instance is empty, behavior is consistent with old version (backward compatible).
// After setting instance, the same role can create multiple independent interactive sessions.
func interactiveKey(channel, chatID, roleName, instance string) string {
	key := channel + ":" + chatID + "/" + roleName
	if instance != "" {
		key += ":" + instance
	}
	return key
}

// SpawnInteractiveSession creates a new interactive SubAgent session and executes the first task.
// If a session with the same role name already exists, returns error.
//
// 锁Strategy:interactiveSubAgents 使用 sync.Map，本身concurrency safe，无需额外mutex。
// Uses LoadOrStore for atomic check-and-store, avoiding spawn races.
// Uses placeholder pattern: Store a minimal placeholder, replace with full data after Run() completes.
// Any error path must clean up the placeholder to avoid session getting stuck.
func (a *Agent) SpawnInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*bus.OutboundMessage, error) {
	originChannel, originChatID, originSender := resolveOriginIDs(msg)
	instance := msg.Metadata["instance_id"]
	background := msg.Metadata["background"] == "true"

	key := interactiveKey(originChannel, originChatID, roleName, instance)

	// --- Phase 1: Atomic check-and-store ---
	// Clean expired sessions first (sync.Map is concurrency-safe, no additional lock needed)
	a.cleanupExpiredSessions()

	// Atomic check-and-store: if key already exists, return directly
	placeholder := &interactiveAgent{roleName: roleName, instance: instance, lastUsed: time.Now(), background: background}
	// Track group membership for auto-cleanup on unload
	if msg.Metadata != nil {
		if gid, ok := msg.Metadata["group_id"]; ok && gid != "" {
			placeholder.groupID = gid
		}
	}
	// Track parent session for cascade cleanup
	if parentKey, ok := ctx.Value(bgParentKey{}).(string); ok {
		placeholder.parentKey = parentKey
	}
	if _, loaded := a.interactiveSubAgents.LoadOrStore(key, placeholder); loaded {
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("interactive session for role %q already exists, use action=\"send\" to continue or action=\"unload\" to end it", roleName),
		}, nil
	}

	// --- Phase 2: Build config outside lock (no lock needed) ---
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	cc := CallChainFromContext(ctx)
	if err := cc.CanSpawn(roleName, a.maxSubAgentDepth); err != nil {
		a.interactiveSubAgents.Delete(key) // Clean up placeholder
		return &bus.OutboundMessage{Content: err.Error(), Error: err}, nil
	}
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	caps := tools.CapabilitiesFromMap(msg.Capabilities)
	subModel := ""
	if msg.Metadata != nil {
		subModel = msg.Metadata["model"]
	}
	cfg := a.buildSubAgentRunConfig(subCtx, parentCtx, msg.Content, msg.SystemPrompt, msg.AllowedTools, caps, roleName, true, subModel)

	// Update placeholder with system prompt + user message so CLI session viewer
	// can display them while Run() is executing (before the full session data
	// replaces the placeholder).
	if len(cfg.Messages) > 0 {
		placeholder.systemPrompt = cfg.Messages[0]
	}
	if len(cfg.Messages) > 1 {
		placeholder.messages = []llm.ChatMessage{cfg.Messages[1]}
	}

	// Interactive SubAgent gets its own TenantSession for message persistence.
	// Channel="agent", ChatID=key → messages saved to DB like normal sessions.
	agentTenantSession, err := a.multiSession.GetOrCreateSession("agent", key)
	if err != nil {
		a.destroyInteractiveSession(key)
		return nil, fmt.Errorf("create agent tenant session: %w", err)
	}
	cfg.Session = agentTenantSession

	// Clear any stale messages from a previous session with the same key.
	// This can happen after server restart (DB retains old tenant data) or
	// if destroyInteractiveSession's DeleteTenant failed silently.
	_ = agentTenantSession.Clear()

	// Eager-save user message so get_history returns it during Run().
	// Without this, the CLI shows "已加载 0 条历史消息" and the DB has no
	// user message turn boundary. Run()'s incremental persistence skips
	// messages[0:lastPersistedCount] which includes this user message.
	if err := agentTenantSession.AddMessage(llm.NewUserMessage(msg.Content)); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to eager-save interactive agent user message")
	}

	// Wire CLI progress + stream callbacks (shared with one-shot SubAgents)
	if !background {
		a.wireSubAgentCLIProgress(key, originChatID, &cfg)
	}

	// SubAgent progress reporting: prefer parent Agent's injected callback (avoid concurrent SubAgents overwriting each other's patches),
	// 否则 fallback 到直接Send消息（非并行场景）。
	// Progress passthrough: child Agent not only reports its own progress, but also injects callbacks into subCtx for deeper SubAgents to recursively passthrough.
	// Background mode exception: bg subagent's progress should not passthrough to parent agent's TUI.

	// Override SendFunc to route outbound via agent session's channel/chatID.
	// This makes agent session outbound go through the same pipeline as the main session.
	cfg.SendFunc = func(channel, chatID, content string, metadata ...map[string]string) error {
		return a.sendMessage("agent", key, content, metadata...)
	}

	if !background {
		if cb, ok := SubAgentProgressFromContext(ctx); ok {
			rn := roleName
			myDepth := cc.Depth() + 1
			myPath := cc.Spawn(rn).Chain
			cfg.ProgressNotifier = func(lines []string) {
				if len(lines) > 0 {
					cb(SubAgentProgressDetail{
						Path:  myPath,
						Lines: lines,
						Depth: myDepth,
					})
				}
			}
		}
		// Note: don't use fallback sendMessage when there's no parent engine progress context.
		// Multiple interactive agents sharing sessionMsgIDs (key=channel:chatID) causes
		// later agent's progress to patch onto earlier agent's message (progress tree crosstalk).

		// Inject passthrough callback into subCtx, letting child Agent's execOne obtain and recursively report progress to parent Agent
		if cb, ok := SubAgentProgressFromContext(ctx); ok {
			myDepth := cc.Depth() + 1
			myPath := cc.Spawn(roleName).Chain
			subCtx = WithSubAgentProgress(subCtx, func(detail SubAgentProgressDetail) {
				detail.Depth = myDepth + detail.Depth
				if len(detail.Path) == 0 {
					detail.Path = myPath
				}
				cb(detail)
			})
		}
	}

	// --- Phase 3: Execute Run ---
	preLen := len(cfg.Messages)

	if background {
		// Background mode: launch Run in goroutine, return immediately.
		// Lifecycle rule: bg subagents never outlive their parent.
		// - First level: ctx is per-request (no marker) → derive from Agent-level ctx
		//   so the session survives across multiple parent requests.
		// - Nested level: ctx is a bg session's runCtx (has marker) → derive from
		//   parent's ctx so the child dies when the parent session is cancelled/unloaded.
		// - Agent exit: agentCancel() cascades through first-level → nested levels.
		var bgBase context.Context
		if ctx.Value(bgSessionCtxKey{}) != nil {
			// Nested: parent is a bg session → derive from parent's lifecycle
			bgBase = ctx
		} else {
			// First level: derive from Agent lifecycle
			bgBase = a.agentCtx
		}
		if bgBase == nil {
			bgBase = context.Background() // safety fallback for tests
		}
		runCtx, runCancel := context.WithCancel(bgBase)
		// Mark this context as a bg session context for nested detection
		runCtx = context.WithValue(runCtx, bgSessionCtxKey{}, true)
		// Store own key so nested bg sessions can identify this as parent
		runCtx = context.WithValue(runCtx, bgParentKey{}, key)
		// Copy call chain into derived context
		runCtx = WithCallChain(runCtx, CallChainFromContext(subCtx))

		placeholder.mu.Lock()
		placeholder.cancelCurrent = runCancel
		placeholder.running = true
		placeholder.mu.Unlock()

		// Wire incremental snapshot callback so iteration history is visible
		// during Run(), not only after it completes.
		// Also send progress notifications to the parent agent via BgTaskManager.
		sessionKey := originChannel + ":" + originChatID
		notifyMgr := a.bgTaskMgr
		cfg.OnIterationSnapshot = func(snap IterationSnapshot) {
			placeholder.mu.Lock()
			placeholder.iterationHistory = append(placeholder.iterationHistory, snap)
			placeholder.mu.Unlock()

			// Notify parent agent about iteration progress
			if notifyMgr != nil {
				var sb strings.Builder
				fmt.Fprintf(&sb, "Iteration %d completed.\n", snap.Iteration)
				if snap.Thinking != "" {
					thinking := snap.Thinking
					if len(thinking) > 200 {
						thinking = thinking[len(thinking)-200:]
					}
					fmt.Fprintf(&sb, "Thinking: %s\n", thinking)
				}
				for _, t := range snap.Tools {
					fmt.Fprintf(&sb, "- %s [%s, %dms]", t.Name, t.Status, t.ElapsedMS)
					if t.Summary != "" {
						fmt.Fprintf(&sb, " %s", t.Summary)
					}
					sb.WriteString("\n")
				}
				notifyMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
					Key:      sessionKey,
					Type:     tools.SubAgentBgNotifyProgress,
					Role:     roleName,
					Instance: instance,
					Content:  sb.String(),
				})
			}
		}

		go func() {
			startTime := time.Now()
			defer func() {
				if r := recover(); r != nil {
					clipanic.Report("agent.interactive.RunBackgroundSession", fmt.Sprintf("%s:%s", roleName, instance), r)
					log.WithFields(log.Fields{
						"role":     roleName,
						"instance": instance,
						"panic":    r,
					}).Error("Background interactive session Run() panicked")
					// Prevent zombie session: clean up state so send/spawn can proceed
					placeholder.mu.Lock()
					placeholder.running = false
					placeholder.cancelCurrent = nil
					placeholder.lastError = fmt.Sprintf("panic: %v", r)
					placeholder.mu.Unlock()
					runCancel()
					// Cascade: clean up children and remove self from panel
					a.cancelChildSessions(key)
					a.destroyInteractiveSession(key)
					// Notify parent
					if notifyMgr != nil {
						notifyMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
							Key:      sessionKey,
							Type:     tools.SubAgentBgNotifyCompleted,
							Role:     roleName,
							Instance: instance,
							Content:  fmt.Sprintf("Panic: %v", r),
							Elapsed:  time.Since(startTime),
						})
					}
				}
			}()

			out := Run(runCtx, cfg)
			runCancel()

			cancelled := runCtx.Err() != nil

			// Notify parent agent about completion
			if notifyMgr != nil {
				content := out.Content
				if out.Error != nil {
					content = fmt.Sprintf("Error: %v\n%s", out.Error, out.Content)
				}
				if cancelled {
					content = "[cancelled] " + content
				}
				if len(content) > 2000 {
					content = content[:2000] + "... [truncated, use inspect for details]"
				}
				notifyMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
					Key:      sessionKey,
					Type:     tools.SubAgentBgNotifyCompleted,
					Role:     roleName,
					Instance: instance,
					Content:  content,
					Elapsed:  time.Since(startTime),
				})
			}

			if cancelled {
				// Context was cancelled (parent unloaded, agent shutdown, etc.)
				// Clean up children and remove self from panel.
				// Check if key still exists — UnloadInteractiveSession may have
				// already cleaned up this session, preventing duplicate cleanup.
				if _, ok := a.interactiveSubAgents.Load(key); !ok {
					return
				}
				a.cancelChildSessions(key)
				a.destroyInteractiveSession(key)
				log.WithFields(log.Fields{
					"role":     roleName,
					"instance": instance,
					"key":      key,
				}).Info("Background interactive session cancelled, removed from panel")
				return
			}

			// Natural completion: session stays for future "send" interactions
			placeholder.mu.Lock()
			defer placeholder.mu.Unlock()

			placeholder.running = false
			placeholder.cancelCurrent = nil

			if out.Error != nil {
				placeholder.lastError = out.Error.Error()
				placeholder.lastReply = out.Content
			} else {
				placeholder.lastError = ""
				placeholder.lastReply = out.Content
			}

			// Iteration history was incrementally updated via OnIterationSnapshot during Run().
			// out.IterationHistory contains the same snapshots, no need to overwrite.

			// Store messages
			var newMsgs []llm.ChatMessage
			// Include the original user message so GetAgentSessionDump shows it
			if preLen > 1 {
				newMsgs = append(newMsgs, cfg.Messages[1])
			}
			if len(out.Messages) > preLen {
				newMsgs = append(newMsgs, out.Messages[preLen:]...)
			}
			placeholder.messages = newMsgs
			if len(cfg.Messages) > 0 {
				placeholder.systemPrompt = cfg.Messages[0]
			}
			placeholder.cfg = &cfg
			placeholder.cfg.Messages = nil
		}()

		log.WithFields(log.Fields{
			"role":       roleName,
			"instance":   instance,
			"background": true,
		}).Info("Interactive session spawned in background")

		return &bus.OutboundMessage{
			Content: fmt.Sprintf("Interactive sub-agent %q (instance=%q) started in background. Use action=\"inspect\" to check progress, action=\"send\" to send messages, action=\"interrupt\" to interrupt, or action=\"unload\" to terminate.", roleName, instance),
		}, nil
	}

	// Foreground mode: execute synchronously
	out := Run(subCtx, cfg)

	if out.Error != nil {
		a.destroyInteractiveSession(key) // Clean up placeholder + tenant session
		// BUG FIX: Append error annotation in Content to ensure main Agent LLM can identify abnormal state
		content := out.Content
		if content == "" {
			content = "⚠️ Interactive SubAgent 执行失败。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
		return out.OutboundMessage, nil
	}

	// --- Phase 4: Replace placeholder with full session data ---
	var newMessages []llm.ChatMessage
	// Include the original user message (cfg.Messages[1]) so GetAgentSessionDump
	// shows what the parent agent sent. cfg.Messages[0] is system prompt (stored separately).
	if preLen > 1 {
		newMessages = append(newMessages, cfg.Messages[1])
	}
	if len(out.Messages) > preLen {
		newMessages = append(newMessages, out.Messages[preLen:]...)
	}

	ia := &interactiveAgent{
		roleName:         roleName,
		instance:         instance,
		messages:         newMessages,
		iterationHistory: out.IterationHistory,
		cfg:              &cfg,
		lastUsed:         time.Now(),
		lastReply:        out.Content,
	}
	if len(cfg.Messages) > 0 {
		ia.systemPrompt = cfg.Messages[0]
	}
	ia.cfg.Messages = nil // Avoid duplication with ia.messages (actual messages are in ia.messages)
	// Append final assistant reply so GetAgentSessionDumpByFullKey returns it.
	// out.Messages (from Run) excludes the final text-only response — it's only
	// in out.Content / buildOutput. Without this, switching away and back loses
	// the assistant's final reply.
	if out.Content != "" {
		ia.messages = append(ia.messages, llm.NewAssistantMessage(out.Content))
	}
	// Carry ReasoningContent to the in-memory message for subsequent turns
	if out.ReasoningContent != "" && len(ia.messages) > 0 {
		ia.messages[len(ia.messages)-1].ReasoningContent = out.ReasoningContent
	}
	a.interactiveSubAgents.Store(key, ia)

	// Persist final assistant message with iteration history as Detail,
	// same as the main agent does in handleInboundMessage (agent.go:1884).
	// The incremental persistence in postToolProcessing saves assistant messages
	// WITHOUT Detail — this adds the one with full iteration history.
	if agentTenantSession != nil && out.Content != "" {
		assistantMsg := llm.NewAssistantMessage(out.Content)
		assistantMsg.ReasoningContent = out.ReasoningContent
		if len(out.IterationHistory) > 0 {
			if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
				assistantMsg.Detail = string(jsonBytes)
			}
		}
		if err := agentTenantSession.AddMessage(assistantMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save interactive agent assistant message with detail")
		}
	}

	log.WithFields(log.Fields{
		"role":     roleName,
		"messages": len(ia.messages),
	}).Info("Interactive session spawned")

	return out.OutboundMessage, nil
}

// SendToInteractiveSession 向已有的 interactive session Send新消息。
func (a *Agent) SendToInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*bus.OutboundMessage, error) {
	originChannel, originChatID, _ := resolveOriginIDs(msg)
	instance := msg.Metadata["instance_id"]

	key := interactiveKey(originChannel, originChatID, roleName, instance)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("no active interactive session for role %q, use interactive=true to create one first", roleName),
		}, nil
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		a.interactiveSubAgents.Delete(key)
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("corrupted interactive session for role %q", roleName),
		}, nil
	}

	// --- Phase 1: Prepare config within lock (read ia data) ---
	ia.mu.Lock()

	// Guard: reject send while a background Run is in progress
	if ia.running {
		ia.mu.Unlock()
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("interactive session for role %q (instance=%q) is currently running. Use action=\"interrupt\" first, or wait for it to finish, then send.", roleName, instance),
		}, nil
	}

	if ia.cfg == nil {
		ia.mu.Unlock()
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("interactive session for role %q is still initializing, please try again later", roleName),
		}, nil
	}

	ia.lastUsed = time.Now()

	cfg := *ia.cfg // Shallow copy RunConfig template
	originUserID := cfg.OriginUserID
	if originUserID == "" {
		originUserID = cfg.SenderID
	}
	llmClient, model, _, thinkingMode := a.llmFactory.GetLLM(originUserID)
	cfg.LLMClient = llmClient
	cfg.Model = model
	cfg.ThinkingMode = thinkingMode

	var newMessages []llm.ChatMessage
	newMessages = append(newMessages, ia.systemPrompt)
	newMessages = append(newMessages, ia.messages...)
	newMessages = append(newMessages, llm.NewUserMessage(msg.Content))
	cfg.Messages = newMessages

	// Eager-save user message so get_history returns it during Run().
	if cfg.Session != nil {
		if err := cfg.Session.AddMessage(llm.NewUserMessage(msg.Content)); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to eager-save interactive agent user message (send)")
		}
	}

	ia.mu.Unlock()

	// --- Phase 2: Build context and execute outside lock ---
	// BUG FIX: Cannot call Run() while holding ia.mu.
	// If Run() internally spawns a nested interactive agent (SubAgent tool → SpawnInteractiveSession),
	// the new agent's cleanupExpiredSessions() iterates all sessions and tries to acquire ia.mu → deadlock.
	cc := CallChainFromContext(ctx)
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// BUG FIX: Must rebuild ProgressNotifier and progress passthrough callback using current ctx.
	// ia.cfg stores the old closure from spawn time, capturing SubAgentProgressFromContext(ctx)
	// pointing to spawn-time pi. During send, sub-agent progress reports to old pi via old closure → progress tree crosstalk.
	// Background sub-agents don't passthrough progress to parent agent TUI.
	if !ia.background {
		if cb, ok := SubAgentProgressFromContext(ctx); ok {
			myDepth := cc.Depth() + 1
			myPath := cc.Spawn(roleName).Chain
			cfg.ProgressNotifier = func(lines []string) {
				if len(lines) > 0 {
					cb(SubAgentProgressDetail{
						Path:  myPath,
						Lines: lines,
						Depth: myDepth,
					})
				}
			}
			subCtx = WithSubAgentProgress(subCtx, func(detail SubAgentProgressDetail) {
				detail.Depth = myDepth + detail.Depth
				if len(detail.Path) == 0 {
					detail.Path = myPath
				}
				cb(detail)
			})
		} else {
			// fallback：无父引擎进度上下文时，禁用直接 sendMessage Progress notification，
			// avoiding multiple interactive agents competing for the same sessionMsgIDs causing progress tree crosstalk.
			cfg.ProgressNotifier = nil
		}
	} else {
		// Background mode: disable progress passthrough
		cfg.ProgressNotifier = nil
	}

	preLen := len(cfg.Messages)
	out := Run(subCtx, cfg)

	// --- Phase 3: Write back results within lock ---
	ia.mu.Lock()
	defer ia.mu.Unlock()

	if out.Error != nil {
		content := out.Content
		if content == "" {
			content = "⚠️ Interactive SubAgent 执行失败。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
		return out.OutboundMessage, nil
	}

	// Append new conversation messages to ia.messages
	// Include the user message sent via action=send so GetAgentSessionDump shows it.
	// cfg.Messages[preLen-1] is the last element before Run, which is the user message
	// appended at line ~670 (newMessages = append(..., llm.NewUserMessage(msg.Content)))
	if preLen > 0 {
		lastBeforeRun := cfg.Messages[preLen-1]
		if lastBeforeRun.Role == "user" {
			ia.messages = append(ia.messages, lastBeforeRun)
		}
	}
	if len(out.Messages) > preLen {
		ia.messages = append(ia.messages, out.Messages[preLen:]...)
	}
	// Append final assistant reply (missing from out.Messages when
	// handleFinalResponse returns directly without appending to s.messages).
	if out.Content != "" {
		ia.messages = append(ia.messages, llm.NewAssistantMessage(out.Content))
	}
	// Carry ReasoningContent to the in-memory message for subsequent turns
	if out.ReasoningContent != "" && len(ia.messages) > 0 {
		ia.messages[len(ia.messages)-1].ReasoningContent = out.ReasoningContent
	}
	// Save iteration history for inspect
	if len(out.IterationHistory) > 0 {
		ia.iterationHistory = append(ia.iterationHistory, out.IterationHistory...)
	}
	ia.lastReply = out.Content

	// Persist final assistant message with iteration history as Detail,
	// same as the main agent does in handleInboundMessage (agent.go:1884).
	if cfg.Session != nil && out.Content != "" {
		assistantMsg := llm.NewAssistantMessage(out.Content)
		assistantMsg.ReasoningContent = out.ReasoningContent
		if len(out.IterationHistory) > 0 {
			if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
				assistantMsg.Detail = string(jsonBytes)
			}
		}
		if err := cfg.Session.AddMessage(assistantMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save interactive agent assistant message with detail")
		}
	}

	return out.OutboundMessage, nil
}

// InterruptInteractiveSession cancels the current running iteration of an interactive session.
func (a *Agent) InterruptInteractiveSession(
	ctx context.Context,
	roleName string,
	channel, chatID string,
	instance string,
) error {
	key := interactiveKey(channel, chatID, roleName, instance)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return fmt.Errorf("no active interactive session for role %q (instance=%q)", roleName, instance)
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		return fmt.Errorf("corrupted interactive session for role %q", roleName)
	}

	ia.mu.Lock()
	defer ia.mu.Unlock()

	if !ia.running || ia.cancelCurrent == nil {
		return fmt.Errorf("interactive session %q (instance=%q) is not currently running", roleName, instance)
	}

	ia.cancelCurrent()
	log.WithFields(log.Fields{
		"role":     roleName,
		"instance": instance,
	}).Info("Interactive session interrupted")
	return nil
}

// InspectInteractiveSession returns a tail-style summary of recent activity in an interactive session.
//
// Output layout (newest first):
//  1. Header — status, message count
//  2. Last Reply — full final assistant output (if any)
//  3. Recent Messages — tail of conversation history (user ↔ assistant turns)
//  4. Recent Iterations — last N iteration snapshots (thinking, tool calls)
//  5. Last Error — if any
func (a *Agent) InspectInteractiveSession(
	ctx context.Context,
	roleName string,
	channel, chatID string,
	instance string,
	tailCount int,
) (string, error) {
	key := interactiveKey(channel, chatID, roleName, instance)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return "", fmt.Errorf("no active interactive session for role %q (instance=%q)", roleName, instance)
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		return "", fmt.Errorf("corrupted interactive session for role %q", roleName)
	}

	ia.mu.Lock()
	defer ia.mu.Unlock()

	if tailCount <= 0 {
		tailCount = 5
	}

	var sb strings.Builder

	// ── 1. Header ──
	status := "idle"
	if ia.running {
		status = "running"
	}
	if ia.task != "" {
		// One-shot subagent: show task instead of message count
		fmt.Fprintf(&sb, "## %s/%s  (%s)\n", roleName, instance, status)
		fmt.Fprintf(&sb, "\n**Task**: %s\n", ia.task)
	} else {
		fmt.Fprintf(&sb, "## %s/%s  (%s, %d messages)\n", roleName, instance, status, len(ia.messages))
	}

	// ── 2. Last Reply (full) — most useful info first ──
	if ia.lastReply != "" {
		fmt.Fprintf(&sb, "\n### Last Reply:\n%s\n", ia.lastReply)
	}

	// One-shot subagents: show status when no iterations yet (still running)
	if ia.task != "" && len(ia.messages) == 0 && len(ia.iterationHistory) == 0 {
		if ia.running {
			fmt.Fprintf(&sb, "\n_One-shot subagent is executing..._\n")
		}
		return sb.String(), nil
	}

	// One-shot subagents have no messages, skip that section.
	// But they do have iterationHistory (after completion).

	// ── 3. Recent Messages — tail of conversation history ──
	// Show the last tailCount messages so the parent agent can see
	// what was asked and what was answered.
	msgCount := len(ia.messages)
	msgStart := msgCount - tailCount
	if msgStart < 0 {
		msgStart = 0
	}
	if msgCount > 0 {
		fmt.Fprintf(&sb, "\n### Recent Messages (last %d of %d):\n", msgCount-msgStart, msgCount)
		if msgStart > 0 {
			fmt.Fprintf(&sb, "... %d earlier messages omitted ...\n", msgStart)
		}
		for _, msg := range ia.messages[msgStart:] {
			role := msg.Role
			content := msg.Content
			// Truncate very long individual messages but keep enough context
			if len(content) > 2000 {
				content = content[:2000] + "... (truncated)"
			}
			// Skip empty content (e.g. assistant messages with only tool_calls)
			if strings.TrimSpace(content) == "" {
				if len(msg.ToolCalls) > 0 {
					toolNames := make([]string, 0, len(msg.ToolCalls))
					for _, tc := range msg.ToolCalls {
						toolNames = append(toolNames, tc.Name)
					}
					fmt.Fprintf(&sb, "**%s**: [called tools: %s]\n", role, strings.Join(toolNames, ", "))
				}
				continue
			}
			fmt.Fprintf(&sb, "**%s**: %s\n", role, content)
		}
	}

	// ── 4. Recent Iterations — thinking + tool execution details ──
	snapshots := ia.iterationHistory
	if len(snapshots) > tailCount {
		snapshots = snapshots[len(snapshots)-tailCount:]
	}
	if len(snapshots) > 0 {
		fmt.Fprintf(&sb, "\n### Recent Iterations (last %d):\n", len(snapshots))
		for _, snap := range snapshots {
			fmt.Fprintf(&sb, "\n**Iteration %d**\n", snap.Iteration)
			if snap.Thinking != "" {
				thinking := snap.Thinking
				if len(thinking) > 300 {
					thinking = thinking[len(thinking)-300:]
					thinking = "..." + thinking
				}
				fmt.Fprintf(&sb, "Thinking: %s\n", thinking)
			}
			if snap.Reasoning != "" {
				reasoning := snap.Reasoning
				if len(reasoning) > 300 {
					reasoning = reasoning[len(reasoning)-300:]
					reasoning = "..." + reasoning
				}
				fmt.Fprintf(&sb, "Reasoning: %s\n", reasoning)
			}
			for _, t := range snap.Tools {
				summary := t.Summary
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				label := t.Label
				if len(label) > 60 {
					label = label[:57] + "..."
				}
				fmt.Fprintf(&sb, "- Tool: %s", t.Name)
				if label != "" {
					fmt.Fprintf(&sb, " (%s)", label)
				}
				fmt.Fprintf(&sb, " [%s, %dms]", t.Status, t.ElapsedMS)
				if summary != "" {
					fmt.Fprintf(&sb, "\n  %s", summary)
				}
				sb.WriteString("\n")
			}
		}
	}

	// ── 5. Last Error ──
	if ia.lastError != "" {
		fmt.Fprintf(&sb, "\n### Last Error:\n%s\n", ia.lastError)
	}

	return sb.String(), nil
}

// cancelChildSessions cancels and removes all interactive sessions whose parentKey
// matches the given key. Recursively cascades to grandchildren.
// This ensures that when a session is unloaded or its context is cancelled,
// all descendant sessions are also cleaned up and disappear from the panel.
// Collects keys first to avoid modifying sync.Map during Range iteration.
func (a *Agent) cancelChildSessions(parentKey string) {
	type childInfo struct {
		key       string
		parentKey string
	}
	var children []childInfo
	a.interactiveSubAgents.Range(func(k, v any) bool {
		childIA, ok := v.(*interactiveAgent)
		if !ok || childIA == nil {
			return true
		}
		childIA.mu.Lock()
		pk := childIA.parentKey
		if childIA.cancelCurrent != nil {
			childIA.cancelCurrent()
		}
		childIA.mu.Unlock()
		if pk == parentKey {
			childKey, _ := k.(string)
			children = append(children, childInfo{key: childKey, parentKey: parentKey})
		}
		return true
	})
	for _, c := range children {
		// Recurse: cancel grandchildren before they become orphaned
		a.cancelChildSessions(c.key)
		a.destroyInteractiveSession(c.key)
		log.WithFields(log.Fields{
			"parent": c.parentKey,
			"child":  c.key,
		}).Info("Cascade cancelled child interactive session")
	}
}

// UnloadInteractiveSession ends interactive session: consolidate memory and cleanup.
// When instance is empty, behavior is consistent with old version (backward compatible).
func (a *Agent) UnloadInteractiveSession(
	ctx context.Context,
	roleName string,
	channel, chatID string,
	instance string,
) error {
	key := interactiveKey(channel, chatID, roleName, instance)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return fmt.Errorf("no active interactive session for role %q", roleName)
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		a.interactiveSubAgents.Delete(key)
		return nil
	}

	ia.mu.Lock()
	// Guard: placeholder hasn't been replaced with full data
	if ia.cfg == nil {
		ia.mu.Unlock()
		a.interactiveSubAgents.Delete(key)
		return nil
	}
	// Cancel any running bg goroutine to prevent leaks
	if ia.cancelCurrent != nil {
		ia.cancelCurrent()
	}
	messages := make([]llm.ChatMessage, len(ia.messages))
	copy(messages, ia.messages)
	cfg := *ia.cfg // dereference pointer for consolidateSubAgentMemory
	ia.mu.Unlock()

	// Cascade: cancel and remove all child sessions spawned by this one
	a.cancelChildSessions(key)

	// Consolidate memory
	if cfg.Memory != nil && len(messages) > 0 {
		a.consolidateSubAgentMemory(ctx, cfg, messages, "interactive session cleanup", roleName, cfg.AgentID)
	}

	// Cleanup
	a.destroyInteractiveSession(key)

	log.WithField("role", roleName).Info("Interactive session unloaded")
	return nil
}

// buildParentToolContext builds the parent ToolContext needed by SubAgent from InboundMessage.
// Consistent with parentCtx construction in spawnSubAgent.
func (a *Agent) buildParentToolContext(ctx context.Context, channel, chatID, senderID string, msg bus.InboundMessage) *tools.ToolContext {
	workspaceRoot := a.workspaceRoot(senderID)
	if !a.isRemoteUser(senderID) {
		_ = os.MkdirAll(workspaceRoot, 0o755)
	} else {
		workspaceRoot = "" // remote: no host paths
	}

	tc := &tools.ToolContext{
		Ctx:                 ctx,
		WorkingDir:          workspaceRoot, // empty for remote
		WorkspaceRoot:       workspaceRoot,
		ReadOnlyRoots:       a.globalSkillDirs,
		SkillsDirs:          a.globalSkillDirs,
		AgentsDir:           a.agentsDir,
		MCPConfigPath:       tools.UserMCPConfigPath(a.workDir, senderID),
		GlobalMCPConfigPath: filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:             a.workDir,
		SandboxEnabled:      a.sandboxMode != "none",
		PreferredSandbox:    a.sandboxMode,
		Sandbox:             resolveSandbox(a.sandbox, senderID),
		AgentID:             msg.ParentAgentID,
		Channel:             channel,
		ChatID:              chatID,
		SenderID:            msg.ParentAgentID, // SubAgent's parent context: SenderID = parent Agent ID
		OriginUserID:        senderID,          // Original user ID
		SenderName:          msg.SenderName,
	}
	// Restore parent's CWD for SubAgent directory inheritance
	if msg.Metadata != nil {
		if cwd, ok := msg.Metadata["parent_cwd"]; ok && cwd != "" {
			tc.CurrentDir = cwd
		}
		// Restore group membership for cross-agent messaging
		if gid, ok := msg.Metadata["group_id"]; ok && gid != "" {
			tc.GroupID = gid
			if gms, ok := msg.Metadata["group_members"]; ok && gms != "" {
				tc.GroupMembers = strings.Split(gms, ",")
			}
		}
	}
	// Fallback: if parent never Cd'd, use workspaceRoot as initial CWD
	// so SubAgent starts in the same directory as the parent agent.
	if tc.CurrentDir == "" && workspaceRoot != "" {
		tc.CurrentDir = workspaceRoot
	}
	return tc
}

// GetActiveInteractiveRoles returns all active interactive SubAgent role names in the current session (including instance identifier).
// Return format: "roleName" or "roleName:instance".
func (a *Agent) GetActiveInteractiveRoles(channel, chatID string) []string {
	var roles []string
	prefix := channel + ":" + chatID + "/"
	a.interactiveSubAgents.Range(func(k, v interface{}) bool {
		key, ok := k.(string)
		if !ok {
			return true
		}
		if strings.HasPrefix(key, prefix) {
			role := strings.TrimPrefix(key, prefix)
			if ia, ok := v.(*interactiveAgent); ok && ia != nil {
				roles = append(roles, role)
			}
		}
		return true
	})
	return roles
}

// CleanupInteractiveSessions Cleanup指定 session 下所有 interactive sessions。
func (a *Agent) CleanupInteractiveSessions(ctx context.Context, channel, chatID string) {
	keysToClean := a.GetActiveInteractiveRoles(channel, chatID)
	for _, key := range keysToClean {
		// Key format: "roleName" or "roleName:instance"
		role, instance, hasInstance := strings.Cut(key, ":")
		if !hasInstance {
			instance = ""
		}
		_ = a.UnloadInteractiveSession(ctx, role, channel, chatID, instance)
	}
	if len(keysToClean) > 0 {
		log.WithFields(log.Fields{
			"session": channel + ":" + chatID,
			"roles":   keysToClean,
		}).Info("Cleaned up all interactive sessions")
	}
}

// resolveOriginIDs extracts origin channel/chatID/senderID from InboundMessage,
// with fallback logic to top-level fields.
func resolveOriginIDs(msg bus.InboundMessage) (channel, chatID, sender string) {
	channel = msg.OriginChannel()
	chatID = msg.OriginChatID()
	sender = msg.OriginSenderID()
	if channel == "" {
		channel = msg.Channel
	}
	if chatID == "" {
		chatID = msg.ChatID
	}
	if sender == "" {
		sender = msg.SenderID
	}
	return
}

// InteractiveSessionInfo represents a snapshot of an interactive agent session.
type InteractiveSessionInfo struct {
	Role       string
	Instance   string
	Running    bool
	Background bool
	Task       string // one-shot subagent task description (empty for interactive)
	Preview    string // latest progress/last reply summary for panel display
	ChatID     string // parent session's chatID (for cross-session listing)
}

// ListInteractiveSessions returns info about all interactive sessions matching the given channel/chatID prefix.
// If chatID is empty, all sessions for that channel are returned (for cross-session listing).
func (a *Agent) ListInteractiveSessions(channel, chatID string) []InteractiveSessionInfo {
	a.cleanupExpiredSessions()
	var prefix string
	if chatID != "" {
		prefix = channel + ":" + chatID + "/"
	} else {
		prefix = channel + ":"
	}
	var results []InteractiveSessionInfo

	a.interactiveSubAgents.Range(func(key, value any) bool {
		keyStr, ok := key.(string)
		if !ok {
			return true
		}
		// Only return sessions belonging to this channel (and chatID if specified)
		if !strings.HasPrefix(keyStr, prefix) {
			return true
		}
		ia, ok := value.(*interactiveAgent)
		if !ok || ia == nil {
			return true
		}
		ia.mu.Lock()
		info := InteractiveSessionInfo{
			Role:       ia.roleName,
			Instance:   ia.instance,
			Running:    ia.running,
			Background: ia.background,
			Task:       ia.task,
			Preview:    summarizeInteractivePreviewLocked(ia),
			ChatID:     parseInteractiveKeyChatID(keyStr),
		}
		ia.mu.Unlock()
		results = append(results, info)
		return true
	})
	return results
}

// parseInteractiveKeyChatID extracts the parent chatID from an interactive key.
// Key format: "channel:chatID/roleName:instance"
func parseInteractiveKeyChatID(key string) string {
	// Find the last "/" separator between chatID and roleName.
	// Use LastIndex because chatID can contain "/" (e.g. CLI absolute paths like /home/user/workspace).
	slashIdx := strings.LastIndex(key, "/")
	if slashIdx <= 0 {
		return ""
	}
	// Skip the "channel:" prefix to get chatID
	colonIdx := strings.Index(key, ":")
	if colonIdx < 0 || colonIdx >= slashIdx {
		return ""
	}
	return key[colonIdx+1 : slashIdx]
}

// CountInteractiveSessions returns the number of active interactive sessions for the given channel/chatID.
func (a *Agent) CountInteractiveSessions(channel, chatID string) int {
	return len(a.ListInteractiveSessions(channel, chatID))
}

func summarizeInteractivePreviewLocked(ia *interactiveAgent) string {
	if ia == nil {
		return ""
	}
	if n := len(ia.iterationHistory); n > 0 {
		snap := ia.iterationHistory[n-1]
		if snap.Thinking != "" {
			return snap.Thinking
		}
		if snap.Reasoning != "" {
			return snap.Reasoning
		}
		for i := len(snap.Tools) - 1; i >= 0; i-- {
			if snap.Tools[i].Summary != "" {
				return snap.Tools[i].Summary
			}
		}
	}
	if ia.lastError != "" {
		return "Error: " + ia.lastError
	}
	return ia.lastReply
}

// SessionMessage represents a single message in a SubAgent conversation.
type SessionMessage struct {
	Role    string `json:"role"` // "user", "assistant", "system"
	Content string `json:"content"`
}

// AgentSessionDump contains the full state of an interactive SubAgent session
// for rendering in a viewer. Includes messages and iteration snapshots.
type AgentSessionDump struct {
	Messages         []SessionMessage    `json:"messages"`
	IterationHistory []IterationSnapshot `json:"iterations"`
}

// GetAgentSessionDump returns the full session state for viewer rendering.
func (a *Agent) GetAgentSessionDump(channel, chatID, roleName, instance string) (*AgentSessionDump, bool) {
	key := interactiveKey(channel, chatID, roleName, instance)
	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return nil, false
	}
	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		return nil, false
	}

	ia.mu.Lock()
	defer ia.mu.Unlock()

	var msgs []SessionMessage
	if ia.systemPrompt.Content != "" {
		msgs = append(msgs, SessionMessage{Role: "system", Content: ia.systemPrompt.Content})
	}
	for _, m := range ia.messages {
		content := m.Content
		if content == "" && len(m.ToolCalls) > 0 {
			var toolNames []string
			for _, tc := range m.ToolCalls {
				toolNames = append(toolNames, tc.Name)
			}
			content = "[Tool calls: " + strings.Join(toolNames, ", ") + "]"
		}
		if content != "" {
			msgs = append(msgs, SessionMessage{Role: string(m.Role), Content: content})
		}
	}

	iters := make([]IterationSnapshot, len(ia.iterationHistory))
	copy(iters, ia.iterationHistory)

	return &AgentSessionDump{
		Messages:         msgs,
		IterationHistory: iters,
	}, true
}

// GetAgentSessionDumpByFullKey returns the session state using the full interactiveKey
// (e.g. "cli:/home/user/project/role:instance") directly, without needing to decompose it.
func (a *Agent) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {
	val, ok := a.interactiveSubAgents.Load(fullKey)
	if !ok {
		return nil, false
	}
	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		return nil, false
	}

	ia.mu.Lock()
	defer ia.mu.Unlock()

	var msgs []SessionMessage
	if ia.systemPrompt.Content != "" {
		msgs = append(msgs, SessionMessage{Role: "system", Content: ia.systemPrompt.Content})
	}
	for _, m := range ia.messages {
		content := m.Content
		if content == "" && len(m.ToolCalls) > 0 {
			var toolNames []string
			for _, tc := range m.ToolCalls {
				toolNames = append(toolNames, tc.Name)
			}
			content = "[Tool calls: " + strings.Join(toolNames, ", ") + "]"
		}
		if content != "" {
			msgs = append(msgs, SessionMessage{Role: string(m.Role), Content: content})
		}
	}

	iters := make([]IterationSnapshot, len(ia.iterationHistory))
	copy(iters, ia.iterationHistory)

	return &AgentSessionDump{
		Messages:         msgs,
		IterationHistory: iters,
	}, true
}

// GetSessionMessages returns the conversation history of a specific interactive SubAgent session.
// Returns the messages and true if found, nil and false otherwise.
func (a *Agent) GetSessionMessages(channel, chatID, roleName, instance string) ([]SessionMessage, bool) {
	key := interactiveKey(channel, chatID, roleName, instance)
	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return nil, false
	}
	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		return nil, false
	}

	ia.mu.Lock()
	defer ia.mu.Unlock()

	var msgs []SessionMessage
	// Include system prompt if available
	if ia.systemPrompt.Content != "" {
		msgs = append(msgs, SessionMessage{Role: "system", Content: ia.systemPrompt.Content})
	}
	for _, m := range ia.messages {
		content := m.Content
		if content == "" && len(m.ToolCalls) > 0 {
			// Summarize tool calls for display
			var toolNames []string
			for _, tc := range m.ToolCalls {
				toolNames = append(toolNames, tc.Name)
			}
			content = "[Tool calls: " + strings.Join(toolNames, ", ") + "]"
		}
		if content != "" {
			msgs = append(msgs, SessionMessage{Role: string(m.Role), Content: content})
		}
	}
	return msgs, true
}
