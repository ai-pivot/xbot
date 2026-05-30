package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/bus"
	channelpkg "xbot/channel"
	"xbot/clipanic"
	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
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

// pendingUserMsg represents a message queued for delivery to a running SubAgent.
// The sender blocks on replyCh until the message is successfully injected into
// the SubAgent's Run loop (via DrainBgNotifications). A nil error means success.
type pendingUserMsg struct {
	content string
	replyCh chan error // buffered(1): nil on success, error on failure
}

// interactiveAgent 封装一个 interactive SubAgent 会话。
// 存储在 parent Agent 的 interactiveSubAgents map 中。
type interactiveAgent struct {
	roleName         string              // 角色名
	instance         string              // instance ID
	groupID          string              // 所属群聊 ID（如 "group:g1"，空=不属于群聊）
	messages         []llm.ChatMessage   // 累积的对话历史（不含 system prompt）
	iterationHistory []IterationSnapshot // 最近迭代快照，供 inspect/tail 使用
	mu               sync.Mutex          // 保护会话状态并发访问
	systemPrompt     llm.ChatMessage     // spawn 时的 system prompt（保持一致性，后续 send 不重建）
	cfg              *RunConfig          // RunConfig 模板（Messages=nil，复用于 send/unload）
	lastUsed         time.Time           // 最后访问时间，用于 TTL 清理
	running          bool                // 当前是否有 Run 在执行
	background       bool                // 是否后台模式
	cancelCurrent    context.CancelFunc  // 当前运行的取消函数（nil = idle）
	interrupted      bool                // set by InterruptInteractiveSession, read by goroutine to skip destroy
	parentKey        string              // parent session key (for cascade cleanup on unload/cancel)
	lastError        string              // 最近一次错误
	lastReply        string              // 最近一次回复摘要
	task             string              // one-shot subagent 的任务描述（交互式为空）
	promptTokens     int64               // last known prompt token count (for TUI status bar)
	completionTokens int64               // last known completion token count (for TUI status bar)
	pendingMessages  []pendingUserMsg    // messages queued while Run is in progress
}

// drainPendingMessages drains all pending user messages queued while the SubAgent
// was running. Called by the DrainBgNotifications callback in the SubAgent's Run loop
// to inject queued messages as synthetic tool call/result pairs.
// Returns a slice of pendingUserMsg that were drained (callers inject them).
// Thread-safe: acquires ia.mu.
func (ia *interactiveAgent) drainPendingMessages() []pendingUserMsg {
	ia.mu.Lock()
	defer ia.mu.Unlock()
	if len(ia.pendingMessages) == 0 {
		return nil
	}
	msgs := ia.pendingMessages
	ia.pendingMessages = nil
	return msgs
}

// wirePendingMessageDrain returns a DrainBgNotifications callback that drains
// pending user messages from the interactiveAgent and converts them to
// QueuedUserMessage notifications for injection by the Run loop.
func (ia *interactiveAgent) wirePendingMessageDrain(sessionKey string) func() []tools.BgNotification {
	return func() []tools.BgNotification {
		msgs := ia.drainPendingMessages()
		if len(msgs) == 0 {
			return nil
		}
		result := make([]tools.BgNotification, len(msgs))
		for i, pm := range msgs {
			pm := pm // capture
			result[i] = &tools.QueuedUserMessage{
				Key:     sessionKey,
				Content: pm.content,
				ReplyFn: func(err error) {
					select {
					case pm.replyCh <- err:
					default:
					}
				},
			}
		}
		return result
	}
}

// modelName returns the LLM model name. Must be called with ia.mu held.
func (ia *interactiveAgent) modelName() string {
	if ia.cfg == nil {
		return ""
	}
	return ia.cfg.Model
}

// maxContextTokens returns the effective max context for this session.
// Must be called with ia.mu held.
func (ia *interactiveAgent) maxContextTokens() int64 {
	if ia.cfg == nil || ia.cfg.ContextManagerConfig == nil {
		return 0
	}
	return int64(ia.cfg.ContextManagerConfig.MaxContextTokens)
}

// maxOutputTokens returns the max output tokens for this session.
// Must be called with ia.mu held.
func (ia *interactiveAgent) maxOutputTokens() int64 {
	if ia.cfg == nil {
		return 0
	}
	return int64(ia.cfg.MaxOutputTokens)
}

// compressRatio returns the compression trigger threshold.
// Must be called with ia.mu held.
func (ia *interactiveAgent) compressRatio() float64 {
	if ia.cfg == nil || ia.cfg.ContextManagerConfig == nil {
		return 0
	}
	return ia.cfg.ContextManagerConfig.CompressionThreshold
}

// interactiveSessionTTL 是 interactive SubAgent 会话的生存时间。
const interactiveSessionTTL = 30 * time.Minute

// cleanupExpiredSessions 清理所有过期的 interactive SubAgent 会话。
// sync.Map 本身并发安全，调用方不需要持有任何额外的锁。
// 跳过 running==true 的会话：正在执行 Run() 的 session 即使 lastUsed 较旧也不应被清理，
// 否则父 agent 的 SubAgent 工具调用会永远阻塞（Run() 是同步阻塞的）。
func (a *Agent) cleanupExpiredSessions() {
	now := time.Now()
	a.interactiveSubAgents.Range(func(k, v any) bool {
		ia, ok := v.(*interactiveAgent)
		if !ok || ia == nil {
			a.interactiveSubAgents.Delete(k)
			return true
		}
		// 读取 lastUsed 和 running 需要加锁，避免与 Run()/SendToInteractiveSession 的写入竞争
		ia.mu.Lock()
		lastUsed := ia.lastUsed
		running := ia.running
		ia.mu.Unlock()

		// 正在执行的 session 绝不清理：即使 lastUsed 很旧，
		// 父 agent 可能正在同步等待 Run() 返回。销毁会导致父 agent 永久卡死。
		if running {
			return true
		}

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
func (a *Agent) recordIterationSnapshot(key string, shouldAppend func(prev *protocol.ProgressEvent) bool) {
	prevSnap, loaded := a.lastProgressSnapshot.Load(key)
	if !loaded {
		return
	}
	prev := prevSnap.(*protocol.ProgressEvent)
	if !shouldAppend(prev) {
		return
	}
	for {
		histPtr, _ := a.iterationHistories.LoadOrStore(key, &[]protocol.ProgressEvent{})
		hist := *histPtr.(*[]protocol.ProgressEvent)
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
	var remoteCh channelpkg.ProgressSender
	if cc, ok := ch.(*channelpkg.CLIChannel); ok {
		localCh = cc
	} else if rc, ok := ch.(channelpkg.ProgressSender); ok {
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

		cliPayload := &protocol.ProgressEvent{
			ChatID: agentProgressKey, Seq: s.Seq, Phase: string(s.Phase),
			Iteration: s.Iteration, Thinking: s.ThinkingContent,
			Reasoning: s.ReasoningContent, HistoryCompacted: s.HistoryCompacted,
		}
		for _, t := range s.ActiveTools {
			cliPayload.ActiveTools = append(cliPayload.ActiveTools, protocol.ToolProgress{
				Name: t.Name, Label: t.Label, Status: string(t.Status),
				Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary, Detail: t.Detail, ToolHints: t.ToolHints,
			})
		}
		for _, t := range s.CompletedTools {
			cliPayload.CompletedTools = append(cliPayload.CompletedTools, protocol.ToolProgress{
				Name: t.Name, Label: t.Label, Status: string(t.Status),
				Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary, Detail: t.Detail, ToolHints: t.ToolHints,
			})
		}
		if len(s.Todos) > 0 {
			cliPayload.Todos = make([]protocol.TodoItem, len(s.Todos))
			for i, td := range s.Todos {
				cliPayload.Todos[i] = protocol.TodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
			}
		}
		if s.TokenUsage != nil {
			cliPayload.TokenUsage = &protocol.TokenUsage{
				PromptTokens: s.TokenUsage.PromptTokens, CompletionTokens: s.TokenUsage.CompletionTokens,
				TotalTokens: s.TokenUsage.TotalTokens, CacheHitTokens: s.TokenUsage.CacheHitTokens,
				MaxOutputTokens: s.TokenUsage.MaxOutputTokens,
			}
		}

		if localCh != nil {
			localCh.SendProgress(key, cliPayload)
		} else if remoteCh != nil {
			wsPayload := &protocol.ProgressEvent{
				ChatID: agentProgressKey, Seq: s.Seq, Phase: string(s.Phase),
				Iteration: s.Iteration, Thinking: s.ThinkingContent,
				Reasoning: s.ReasoningContent, HistoryCompacted: s.HistoryCompacted,
			}
			for _, t := range s.ActiveTools {
				wsPayload.ActiveTools = append(wsPayload.ActiveTools, protocol.ToolProgress{
					Name: t.Name, Label: t.Label, Status: string(t.Status),
					Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary, Detail: t.Detail, Args: t.Args, ToolHints: t.ToolHints,
				})
			}
			for _, t := range s.CompletedTools {
				wsPayload.CompletedTools = append(wsPayload.CompletedTools, protocol.ToolProgress{
					Name: t.Name, Label: t.Label, Status: string(t.Status),
					Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration, Summary: t.Summary, Detail: t.Detail, Args: t.Args, ToolHints: t.ToolHints,
				})
			}
			if len(s.Todos) > 0 {
				wsPayload.Todos = make([]protocol.TodoItem, len(s.Todos))
				for i, td := range s.Todos {
					wsPayload.Todos[i] = protocol.TodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
				}
			}
			if s.TokenUsage != nil {
				wsPayload.TokenUsage = &protocol.TokenUsage{
					PromptTokens: s.TokenUsage.PromptTokens, CompletionTokens: s.TokenUsage.CompletionTokens,
					TotalTokens: s.TokenUsage.TotalTokens, CacheHitTokens: s.TokenUsage.CacheHitTokens,
					MaxOutputTokens: s.TokenUsage.MaxOutputTokens,
				}
			}
			// Route progress to the agent session's hub key (interactiveKey format)
			// so the remote CLI client subscribed to this agent session receives it.
			remoteCh.SendProgress(key, wsPayload)
		}

		// Save snapshot + track iteration history for mid-session reconnect.
		a.recordIterationSnapshot(agentProgressKey, func(prev *protocol.ProgressEvent) bool {
			return s.Iteration > prev.Iteration && prev.Iteration >= 0
		})
		a.lastProgressSnapshot.Store(agentProgressKey, cliPayload)
	}

	// Wire stream callbacks for real-time rendering
	cfg.Stream = true
	var subAgentProgressSeq atomic.Uint64
	cfg.ProgressSeq = &subAgentProgressSeq
	if localCh != nil {
		cfg.StreamContentFunc = func(content string) {
			seq := subAgentProgressSeq.Add(1)
			localCh.SendProgress(key, &protocol.ProgressEvent{ChatID: agentProgressKey, Seq: seq, StreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*protocol.ProgressEvent)
				cp.StreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
		cfg.StreamReasoningFunc = func(content string) {
			seq := subAgentProgressSeq.Add(1)
			localCh.SendProgress(key, &protocol.ProgressEvent{ChatID: agentProgressKey, Seq: seq, ReasoningStreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*protocol.ProgressEvent)
				cp.ReasoningStreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
	} else if remoteCh != nil {
		cfg.StreamContentFunc = func(content string) {
			seq := subAgentProgressSeq.Add(1)
			remoteCh.SendProgress(key, &protocol.ProgressEvent{ChatID: agentProgressKey, Seq: seq, StreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*protocol.ProgressEvent)
				cp.StreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
		cfg.StreamReasoningFunc = func(content string) {
			seq := subAgentProgressSeq.Add(1)
			remoteCh.SendProgress(key, &protocol.ProgressEvent{ChatID: agentProgressKey, Seq: seq, ReasoningStreamContent: content})
			if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
				cp := *snap.(*protocol.ProgressEvent)
				cp.ReasoningStreamContent = content
				a.lastProgressSnapshot.Store(agentProgressKey, &cp)
			}
		}
	}
}

// sendSubAgentPhaseDone sends a synthetic PhaseDone progress event to the CLI
// so the TUI can properly end the current turn (reset typing, snapshot iterations, etc.).
// Used when a background subagent is interrupted, since the normal Run() exit path
// doesn't send PhaseDone on cancellation.
func (a *Agent) sendSubAgentPhaseDone(key string) {
	if a.channelFinder == nil {
		return
	}
	ch, ok := a.channelFinder("cli")
	if !ok {
		return
	}
	agentProgressKey := "agent:" + key

	// Build PhaseDone payload from the last known progress snapshot.
	var payload *protocol.ProgressEvent
	if snap, ok := a.lastProgressSnapshot.Load(agentProgressKey); ok {
		cp := *snap.(*protocol.ProgressEvent)
		cp.Phase = "done"
		payload = &cp
	} else {
		payload = &protocol.ProgressEvent{
			ChatID: agentProgressKey,
			Phase:  "done",
		}
	}

	if localCh, ok := ch.(*channelpkg.CLIChannel); ok {
		localCh.SendProgress(key, payload)
	} else if remoteCh, ok := ch.(channelpkg.ProgressSender); ok {
		remoteCh.SendProgress(key, payload)
	}
}

// destroyInteractiveSession removes all resources for an interactive SubAgent session:
// interactiveSubAgents entry, progress snapshot/iteration history, tenant session (DB),
// offload data, and mask data on disk.
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

	// Clean up offload data for this agent session.
	// The session key format matches qualifyChatID("agent", key) = "agent:key".
	offloadKey := qualifyChatID("agent", key)
	if a.offloadStore != nil {
		a.offloadStore.CleanSession(offloadKey)
	}
	// Note: mask cleanup is handled by CleanStale periodic timer — mask dirs are keyed by
	// numeric tenant ID which is not available here. CleanStale removes dirs older than 7 days.

	// Destroy tenant session (cache + DB with CASCADE to messages)
	if a.multiSession != nil {
		_ = a.multiSession.DestroySession("agent", key)
	}
}

// interactiveKey 生成 interactive session 在 map 中的 key。
// 使用 channel:chatID/roleName[:instance] 保证同一个 chat + role + instance 只有一个 session。
// instance 为空时，行为与旧版一致（向后兼容）。
// 设置 instance 后，同一个 role 可以创建多个独立的 interactive session。
func interactiveKey(channel, chatID, roleName, instance string) string {
	key := qualifyChatID(channel, chatID) + "/" + roleName
	if instance != "" {
		key += ":" + instance
	}
	return key
}

// SpawnInteractiveSession 创建一个新的 interactive SubAgent 会话并执行首次任务。
// 如果同名 role 的 session 已存在，返回 error。
//
// 锁策略：interactiveSubAgents 使用 sync.Map，本身并发安全，无需额外互斥锁。
// 使用 LoadOrStore 实现原子的 check-and-store，避免 spawn 竞态。
// 使用占位符模式：Store 一个最小占位符，Run() 完成后替换为完整数据。
// 任何错误路径都必须清理占位符，避免 session 卡死。
func (a *Agent) SpawnInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*channelpkg.OutboundMsg, error) {
	originChannel, originChatID, originSender := resolveOriginIDs(msg)
	instance := msg.Metadata["instance_id"]
	background := msg.Metadata["background"] == "true"

	key := interactiveKey(originChannel, originChatID, roleName, instance)

	// --- 阶段 1：原子 check-and-store ---
	// 先清理过期 session（sync.Map 并发安全，不需要额外锁）
	a.cleanupExpiredSessions()

	// 原子 check-and-store：如果 key 已存在，直接返回
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
		return &channelpkg.OutboundMsg{
			Content: fmt.Sprintf("interactive session for role %q already exists, use action=\"send\" to continue or action=\"unload\" to end it", roleName),
		}, nil
	}

	// Emit subagent_started event for instant sidebar push.
	a.emitSessionState(protocol.SessionEvent{
		Channel:  originChannel,
		ChatID:   originChatID,
		Action:   "subagent_started",
		Role:     roleName,
		Instance: instance,
		ParentID: originChatID,
	})

	// --- 阶段 2：锁外构建 config（不需要锁） ---
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	cc := CallChainFromContext(ctx)
	if err := cc.CanSpawn(roleName, a.maxSubAgentDepth); err != nil {
		a.interactiveSubAgents.Delete(key)

		// Emit subagent_stopped event for instant sidebar push.
		// Parse key to extract channel, chatID, role, instance.
		// key format: "channel:chatID/roleName[:instance]"
		parts := strings.SplitN(key, ":", 2)
		chanPart := ""
		rest := key
		if len(parts) == 2 {
			chanPart = parts[0]
			rest = parts[1]
		}
		chatIDAndRole := strings.SplitN(rest, "/", 2)
		chatIDPart := rest
		rolePart := ""
		instPart := ""
		if len(chatIDAndRole) == 2 {
			chatIDPart = chatIDAndRole[0]
			roleAndInst := chatIDAndRole[1]
			riParts := strings.SplitN(roleAndInst, ":", 2)
			rolePart = riParts[0]
			if len(riParts) > 1 {
				instPart = riParts[1]
			}
		}
		a.emitSessionState(protocol.SessionEvent{
			Channel:  chanPart,
			ChatID:   chatIDPart,
			Action:   "subagent_stopped",
			Role:     rolePart,
			Instance: instPart,
			ParentID: chatIDPart,
		}) // 清理占位符
		return &channelpkg.OutboundMsg{Content: err.Error(), Error: err}, nil
	}
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	caps := tools.CapabilitiesFromMap(msg.Capabilities)
	subModel := ""
	if msg.Metadata != nil {
		subModel = msg.Metadata["model"]
	}
	cfg := a.buildSubAgentRunConfig(subCtx, parentCtx, msg.Content, msg.SystemPrompt, msg.AllowedTools, caps, roleName, true, instance, subModel)

	// Update placeholder with cfg so GetAgentSessionDumpByFullKey returns the
	// correct model name, max context tokens, etc. — even during the first Run().
	// Without this, placeholder.cfg is nil, so ia.modelName() returns "" and the
	// TUI status bar falls back to the parent agent's default subscription model.
	placeholder.mu.Lock()
	placeholder.cfg = &cfg
	placeholder.mu.Unlock()

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

	// Wire CLI progress + stream callbacks for ALL sessions (foreground and background).
	// ChatID-based filtering in handleProgressMsg ensures events route to the correct session view.
	// Without this, background sessions have no live progress when viewed via Ctrl+T panel.
	a.wireSubAgentCLIProgress(key, originChatID, &cfg)

	// SubAgent 进度上报：优先使用父 Agent 注入的回调（避免并发 SubAgent 互相覆盖 patch），
	// 否则 fallback 到直接发送消息（非并行场景）。
	// 进度穿透：子 Agent 不仅上报自身进度，还注入回调到 subCtx 让更深层 SubAgent 也能递归穿透。
	// Background 模式例外：bg subagent 的进度不应穿透到父 agent 的 TUI。

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
			inst := instance
			cfg.ProgressNotifier = func(lines []string, thinking string) {
				if len(lines) > 0 {
					cb(SubAgentProgressDetail{
						Path:     myPath,
						Lines:    lines,
						Depth:    myDepth,
						Instance: inst,
						Thinking: thinking,
					})
				}
			}
		}
		// 注意：无父引擎进度上下文时不使用 fallback sendMessage。
		// 多个交互式 agent 共享 sessionMsgIDs（key=channel:chatID）会导致
		// 后一个 agent 的进度 patch 到前一个 agent 的消息上（进度树串扰）。

		// 注入穿透回调到 subCtx，让子 Agent 的 execOne 能获取并递归上报进度到父 Agent
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
	// Background mode: no ProgressNotifier needed — autoNotify in engine.Run()
	// now derives from ProgressEventHandler too, not just ProgressNotifier.
	// wireSubAgentCLIProgress already sets ProgressEventHandler for background
	// mode, so progress events will flow to the CLI channel automatically.

	// --- 阶段 3：执行 Run ---
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
					Sid:      originSender,
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
							Sid:      originSender,
						})
					}
					// Emit subagent_stopped so sidebar updates immediately
					a.emitSessionState(protocol.SessionEvent{
						Channel:  originChannel,
						ChatID:   originChatID,
						Action:   "subagent_stopped",
						Role:     roleName,
						Instance: instance,
						ParentID: originChatID,
					})
				}
			}()

			// Wire DrainBgNotifications so pending messages (sent via action=send
			// while this Run is in progress) are injected between iterations.
			cfg.DrainBgNotifications = placeholder.wirePendingMessageDrain(sessionKey)

			out := Run(runCtx, cfg)

			// Check cancellation BEFORE calling runCancel().
			// runCancel() cancels the context, making runCtx.Err() always non-nil.
			// We need to know if Run() was cancelled externally (parent unload,
			// agent shutdown) vs completed naturally — only externally cancelled
			// sessions should be destroyed.
			cancelled := runCtx.Err() != nil
			runCancel()

			// Notify parent agent about completion — but ONLY for natural
			// completion or interrupt. Unload/shutdown notifications cause
			// the parent agent to see stale "[SubAgent X completed]" messages
			// and try to "clean up" agents that are already gone, producing
			// endless "agent still running" hallucinations.
			if notifyMgr != nil && !cancelled {
				content := out.Content
				if out.Error != nil {
					content = fmt.Sprintf("Error: %v\n%s", out.Error, out.Content)
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
					Sid:      originSender,
				})
			}

			if cancelled {
				// Context was cancelled. Two possible causes:
				// 1. InterruptInteractiveSession → ia.interrupted=true → notify + keep session
				// 2. UnloadInteractiveSession / agent shutdown → ia.interrupted=false → destroy
				placeholder.mu.Lock()
				wasInterrupted := placeholder.interrupted
				placeholder.running = false
				placeholder.cancelCurrent = nil
				placeholder.interrupted = false // reset for next send

				// Add an interrupt marker to ia.messages so the TUI session view
				// can display the interruption. Without this, the session jumps
				// from old iterations directly to new ones with no visual break.
				if wasInterrupted && out.Content != "" {
					// Append partial assistant reply (if any) so it's not lost.
					if len(placeholder.messages) == 0 || placeholder.messages[len(placeholder.messages)-1].Content != out.Content {
						placeholder.messages = append(placeholder.messages, llm.NewAssistantMessage(out.Content))
					}
				}
				if wasInterrupted {
					placeholder.messages = append(placeholder.messages, llm.ChatMessage{
						Role:    "system",
						Content: "⏸ [interrupted by parent agent]",
					})
				}
				placeholder.mu.Unlock()

				if wasInterrupted {
					// Send PhaseDone to CLI so the TUI properly ends the turn
					// (sets m.typing=false, snapshots iterations). Without this,
					// the TUI stays in "typing" state and the auto-start guard
					// doesn't trigger when the next Run starts.
					a.sendSubAgentPhaseDone(key)

					// Interrupt: notify parent so it knows the agent stopped.
					// The session stays for future "send" interactions.
					if notifyMgr != nil {
						content := out.Content
						if out.Error != nil {
							content = fmt.Sprintf("Error: %v\n%s", out.Error, out.Content)
						}
						content = "[interrupted] " + content
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
							Sid:      originSender,
						})
					}
					log.WithFields(log.Fields{
						"role":     roleName,
						"instance": instance,
						"key":      key,
					}).Info("Background interactive session interrupted, session preserved for future send")
					// Emit subagent_stopped so sidebar updates immediately (busy→idle)
					a.emitSessionState(protocol.SessionEvent{
						Channel:  originChannel,
						ChatID:   originChatID,
						Action:   "subagent_stopped",
						Role:     roleName,
						Instance: instance,
						ParentID: originChatID,
					})
					return
				}

				// Unload/shutdown: NO notification — the parent already knows
				// (it triggered the unload). Sending a notification would cause
				// the parent agent to see "[SubAgent completed]" and try to
				// "clean up" agents that are already destroyed.
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
				}).Info("Background interactive session cancelled (unload), removed from panel")
				return
			}

			// Natural completion: session stays for future "send" interactions
			placeholder.mu.Lock()
			defer placeholder.mu.Unlock()

			placeholder.running = false
			placeholder.cancelCurrent = nil
			placeholder.lastReply = out.Content
			placeholder.promptTokens = out.LastPromptTokens
			placeholder.completionTokens = out.LastCompletionTokens

			if out.Error != nil {
				placeholder.lastError = out.Error.Error()
			} else {
				placeholder.lastError = ""
			}

			// Iteration history was incrementally updated via OnIterationSnapshot during Run().
			// out.IterationHistory contains the same snapshots, no need to overwrite.

			// Store messages: use out.Messages as the authoritative state.
			// If compression happened during Run(), out.Messages is the compressed
			// list (much shorter than the original). We must use it entirely,
			// not just append from preLen onward — otherwise compression results
			// are discarded and ia.messages keeps growing forever.
			var newMsgs []llm.ChatMessage
			if len(out.Messages) < preLen {
				// Compression happened: replace entirely with compressed messages
				newMsgs = make([]llm.ChatMessage, len(out.Messages))
				copy(newMsgs, out.Messages)
			} else {
				// Normal case: include original user message + messages from Run
				if preLen > 1 {
					newMsgs = append(newMsgs, cfg.Messages[1])
				}
				if len(out.Messages) > preLen {
					newMsgs = append(newMsgs, out.Messages[preLen:]...)
				}
			}
			// Append final assistant reply so GetAgentSessionDumpByFullKey returns it.
			// out.Messages (from Run) excludes the final text-only response — it's only
			// in out.Content. Without this, switching away and back loses the assistant's
			// final reply (same fix as foreground path below).
			if out.Content != "" {
				// Check if the last message is already this assistant reply
				if len(newMsgs) == 0 || newMsgs[len(newMsgs)-1].Content != out.Content || newMsgs[len(newMsgs)-1].Role != "assistant" {
					newMsgs = append(newMsgs, llm.NewAssistantMessage(out.Content))
				}
			} else if len(newMsgs) == 0 || newMsgs[len(newMsgs)-1].Role != "assistant" {
				newMsgs = append(newMsgs, llm.NewAssistantMessage("(empty response)"))
			}
			// Carry ReasoningContent to the in-memory message for subsequent turns
			if out.ReasoningContent != "" && len(newMsgs) > 0 {
				newMsgs[len(newMsgs)-1].ReasoningContent = out.ReasoningContent
			}
			placeholder.messages = newMsgs
			if len(cfg.Messages) > 0 {
				placeholder.systemPrompt = cfg.Messages[0]
			}
			placeholder.cfg = &cfg
			placeholder.cfg.Messages = nil

			// Persist final assistant message with iteration history as Detail,
			// same as the foreground path and the main agent does in
			// handleInboundMessage. The incremental persistence in
			// postToolProcessing saves assistant messages WITHOUT Detail —
			// this adds the one with full iteration history.
			if agentTenantSession != nil && out.Content != "" {
				assistantMsg := llm.NewAssistantMessage(out.Content)
				assistantMsg.ReasoningContent = out.ReasoningContent
				if len(out.IterationHistory) > 0 {
					if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
						assistantMsg.Detail = string(jsonBytes)
					}
				}
				if err := agentTenantSession.AddMessage(assistantMsg); err != nil {
					log.WithFields(log.Fields{
						"role": roleName, "instance": instance,
					}).WithError(err).Warn("Failed to save bg interactive agent assistant message with detail")
				}
			}

			// Emit subagent_stopped so sidebar updates immediately (busy→idle).
			// Must be called while placeholder.mu is still held (deferred unlock)
			// to guarantee the state transition is visible to concurrent readers.
			a.emitSessionState(protocol.SessionEvent{
				Channel:  originChannel,
				ChatID:   originChatID,
				Action:   "subagent_stopped",
				Role:     roleName,
				Instance: instance,
				ParentID: originChatID,
			})
		}()

		log.WithFields(log.Fields{
			"role":       roleName,
			"instance":   instance,
			"background": true,
		}).Info("Interactive session spawned in background")

		return &channelpkg.OutboundMsg{
			Content: fmt.Sprintf("Interactive sub-agent %q (instance=%q) started in background. Use action=\"inspect\" to check progress, action=\"send\" to send messages, action=\"interrupt\" to interrupt, or action=\"unload\" to terminate.", roleName, instance),
		}, nil
	}

	// Foreground mode: execute synchronously
	placeholder.mu.Lock()
	placeholder.running = true
	placeholder.mu.Unlock()

	// Wire DrainBgNotifications so pending messages (sent via action=send
	// while this Run is in progress) are injected between iterations.
	fgSessionKey := originChannel + ":" + originChatID
	cfg.DrainBgNotifications = placeholder.wirePendingMessageDrain(fgSessionKey)

	out := Run(subCtx, cfg)

	placeholder.mu.Lock()
	placeholder.running = false
	placeholder.mu.Unlock()

	if out.Error != nil {
		a.destroyInteractiveSession(key) // 清理占位符 + tenant session
		// BUG FIX: 在 Content 中附加错误标注，确保主 Agent LLM 能识别异常状态
		content := out.Content
		if content == "" {
			content = "⚠️ Interactive SubAgent 执行失败。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
		return out.OutboundMsg, nil
	}

	// --- 阶段 4：替换占位符为完整 session 数据 ---
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
	ia.cfg.Messages = nil // 避免与 ia.messages 重复（实际消息在 ia.messages 中）
	// Append final assistant reply so GetAgentSessionDumpByFullKey returns it.
	// out.Messages (from Run) excludes the final text-only response — it's only
	// in out.Content / buildOutput. Without this, switching away and back loses
	// the assistant's final reply.
	if out.Content != "" {
		ia.messages = append(ia.messages, llm.NewAssistantMessage(out.Content))
	} else {
		ia.messages = append(ia.messages, llm.NewAssistantMessage("(empty response)"))
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

	return out.OutboundMsg, nil
}

// SendToInteractiveSession 向已有的 interactive session 发送新消息。
func (a *Agent) SendToInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*channelpkg.OutboundMsg, error) {
	originChannel, originChatID, originSender := resolveOriginIDs(msg)
	instance := msg.Metadata["instance_id"]

	key := interactiveKey(originChannel, originChatID, roleName, instance)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return &channelpkg.OutboundMsg{
			Content: fmt.Sprintf("no active interactive session for role %q, use interactive=true to create one first", roleName),
		}, nil
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		a.interactiveSubAgents.Delete(key)
		return &channelpkg.OutboundMsg{
			Content: fmt.Sprintf("corrupted interactive session for role %q", roleName),
		}, nil
	}

	// --- 阶段 1：锁内准备配置（读取 ia 数据）---
	ia.mu.Lock()

	// Guard: when a Run is in progress, queue the message for async injection
	// instead of rejecting it. The SubAgent's DrainBgNotifications callback
	// will drain and inject it as a synthetic tool result between iterations.
	// The sender blocks until the message is successfully injected.
	if ia.running {
		if ia.cfg == nil {
			ia.mu.Unlock()
			return &channelpkg.OutboundMsg{
				Content: fmt.Sprintf("interactive session for role %q is still initializing, please try again later", roleName),
			}, nil
		}

		replyCh := make(chan error, 1)
		ia.pendingMessages = append(ia.pendingMessages, pendingUserMsg{
			content: msg.Content,
			replyCh: replyCh,
		})
		ia.mu.Unlock()

		// Block until the SubAgent's Run loop drains the message (via DrainBgNotifications)
		// or the parent context is cancelled.
		select {
		case err := <-replyCh:
			if err != nil {
				return &channelpkg.OutboundMsg{
					Content: fmt.Sprintf("failed to deliver message to %q (instance=%q): %v", roleName, instance, err),
				}, nil
			}
			return &channelpkg.OutboundMsg{
				Content: fmt.Sprintf("✅ Message delivered to %q (instance=%q). The sub-agent will process it during its current run.", roleName, instance),
			}, nil
		case <-ctx.Done():
			// Context cancelled — remove the pending message to avoid stale delivery
			ia.mu.Lock()
			for i, pm := range ia.pendingMessages {
				if pm.replyCh == replyCh {
					ia.pendingMessages = append(ia.pendingMessages[:i], ia.pendingMessages[i+1:]...)
					break
				}
			}
			ia.mu.Unlock()
			return &channelpkg.OutboundMsg{
				Content: fmt.Sprintf("message delivery to %q (instance=%q) cancelled: context deadline exceeded", roleName, instance),
			}, nil
		}
	}

	if ia.cfg == nil {
		ia.mu.Unlock()
		return &channelpkg.OutboundMsg{
			Content: fmt.Sprintf("interactive session for role %q is still initializing, please try again later", roleName),
		}, nil
	}

	ia.lastUsed = time.Now()

	cfg := *ia.cfg // 浅拷贝 RunConfig 模板（已有正确的 LLMClient + Model + ThinkingMode）
	// 不再无条件用 GetLLM 覆盖：ia.cfg 在 spawn 时已通过 buildSubAgentRunConfig
	// 正确解析了角色/tier 对应的 LLM，直接复用即可。

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

	// --- 阶段 2：锁外构建上下文和执行 ---
	// BUG FIX: 不能在持有 ia.mu 期间调用 Run()。
	// Run() 内部如果生成嵌套交互式 agent（SubAgent 工具 → SpawnInteractiveSession），
	// 新 agent 的 cleanupExpiredSessions() 会遍历所有 session 并尝试获取 ia.mu → 死锁。
	cc := CallChainFromContext(ctx)
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// BUG FIX: 必须使用当前 ctx 重建 ProgressNotifier 和进度穿透回调。
	// ia.cfg 中存储的是 spawn 期间的旧闭包，捕获的 SubAgentProgressFromContext(ctx)
	// 指向 spawn 时的 pi。send 期间子代理进度会通过旧闭包上报到旧 pi → 进度树串扰。
	// Background 子代理不穿透进度到父 agent TUI。
	if !ia.background {
		if cb, ok := SubAgentProgressFromContext(ctx); ok {
			myDepth := cc.Depth() + 1
			myPath := cc.Spawn(roleName).Chain
			inst := instance
			cfg.ProgressNotifier = func(lines []string, thinking string) {
				if len(lines) > 0 {
					cb(SubAgentProgressDetail{
						Path:     myPath,
						Lines:    lines,
						Depth:    myDepth,
						Instance: inst,
						Thinking: thinking,
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
			// fallback：无父引擎进度上下文时，禁用直接 sendMessage 进度通知，
			// 避免多个交互式 agent 竞争同一个 sessionMsgIDs 导致进度树串扰。
			cfg.ProgressNotifier = nil
		}
	} else {
		// Background 模式：禁用进度穿透
		cfg.ProgressNotifier = nil

		// Re-wire OnIterationSnapshot for incremental history updates
		// (same as SpawnInteractiveSession does for the initial Run).
		sessionKey := originChannel + ":" + originChatID
		notifyMgr := a.bgTaskMgr
		cfg.OnIterationSnapshot = func(snap IterationSnapshot) {
			ia.mu.Lock()
			ia.iterationHistory = append(ia.iterationHistory, snap)
			ia.mu.Unlock()

			if notifyMgr != nil {
				var sb strings.Builder
				fmt.Fprintf(&sb, "Iteration %d completed.\n", snap.Iteration)
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
					Sid:      originSender,
				})
			}
		}
	}

	preLen := len(cfg.Messages)

	ia.mu.Lock()
	ia.running = true
	ia.interrupted = false // clear any stale interrupt flag from previous Run

	// --- Pre-Run state reset for background mode ---
	// Before starting a new Run, reset iteration history and progress snapshots
	// so the TUI session view shows a clean break from the previous turn.
	// Without this, the new Run's iterations (starting from 0) are appended to
	// the old history, creating confusing numbering (e.g. #0-#35 from previous
	// Run, then #0-#N from new Run), and recordIterationSnapshot skips new
	// iterations because they're <= the old max iteration.
	if ia.background {
		// 1. Add user message to ia.messages NOW (before Run) so
		//    GetAgentSessionDump shows it immediately for TUI rendering.
		ia.messages = append(ia.messages, llm.NewUserMessage(msg.Content))

		// 2. Clear iteration history so the new Run starts fresh.
		ia.iterationHistory = nil

		// 3. Clear progress snapshots so recordIterationSnapshot works correctly
		//    for the new Run's iterations (which start from 0).
		agentProgressKey := "agent:" + key
		a.lastProgressSnapshot.Delete(agentProgressKey)
		a.iterationHistories.Delete(agentProgressKey)
	}

	// For background mode, create a cancellable context so UnloadInteractiveSession
	// can cancel the running goroutine. Must derive from the agent lifecycle context
	// (a.agentCtx), NOT from subCtx — subCtx inherits the parent's tool execution
	// deadline, which would kill the background goroutine when the parent's tool
	// call times out. Same pattern as the initial spawn path above.
	if ia.background {
		var bgBase context.Context
		if ctx.Value(bgSessionCtxKey{}) != nil {
			// Nested: parent is a bg session → derive from parent's lifecycle
			bgBase = ctx
		} else {
			// First level: derive from Agent lifecycle (no deadline)
			bgBase = a.agentCtx
		}
		if bgBase == nil {
			bgBase = context.Background()
		}
		runCtx, runCancel := context.WithCancel(bgBase)
		subCtx = runCtx
		ia.cancelCurrent = runCancel
	}
	ia.mu.Unlock()

	if ia.background {
		// Background agents: run asynchronously (same as initial spawn).
		// Without this, SubAgent(action="send") blocks the caller's tool
		// execution for the entire duration of Run() — which can be minutes.
		// The caller gets an immediate acknowledgment, and the result is
		// delivered via BgTaskMgr notification when Run() completes.
		go func() {
			startTime := time.Now()
			defer func() {
				if r := recover(); r != nil {
					clipanic.Report("agent.interactive.SendBackground", fmt.Sprintf("%s:%s", roleName, instance), r)
					ia.mu.Lock()
					ia.running = false
					ia.cancelCurrent = nil
					ia.lastError = fmt.Sprintf("panic: %v", r)
					ia.mu.Unlock()
					if a.bgTaskMgr != nil {
						sessionKey := originChannel + ":" + originChatID
						a.bgTaskMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
							Key:      sessionKey,
							Type:     tools.SubAgentBgNotifyCompleted,
							Role:     roleName,
							Instance: instance,
							Content:  fmt.Sprintf("Panic: %v", r),
							Elapsed:  time.Since(startTime),
							Sid:      originSender,
						})
					}
					// Emit subagent_stopped so sidebar updates immediately
					a.emitSessionState(protocol.SessionEvent{
						Channel:  originChannel,
						ChatID:   originChatID,
						Action:   "subagent_stopped",
						Role:     roleName,
						Instance: instance,
						ParentID: originChatID,
					})
				}
			}()

			// Wire DrainBgNotifications for pending message delivery.
			bgSendKey := originChannel + ":" + originChatID
			cfg.DrainBgNotifications = ia.wirePendingMessageDrain(bgSendKey)

			out := Run(subCtx, cfg)

			// Check if the context was cancelled (interrupt or unload).
			// Must check BEFORE clearing cancelCurrent so we can read ia.interrupted.
			// subCtx is derived from context.WithCancel, so subCtx.Err() tells us.
			wasCancelled := subCtx.Err() != nil

			ia.mu.Lock()
			ia.running = false
			ia.cancelCurrent = nil
			wasInterrupted := ia.interrupted
			ia.interrupted = false // reset for next send
			ia.mu.Unlock()

			if wasCancelled {
				if wasInterrupted {
					// Add interrupt marker to ia.messages (same as initial spawn path).
					ia.mu.Lock()
					if out.Content != "" {
						if len(ia.messages) == 0 || ia.messages[len(ia.messages)-1].Content != out.Content {
							ia.messages = append(ia.messages, llm.NewAssistantMessage(out.Content))
						}
					}
					ia.messages = append(ia.messages, llm.ChatMessage{
						Role:    "system",
						Content: "⏸ [interrupted by parent agent]",
					})
					ia.mu.Unlock()

					// Send PhaseDone to CLI so TUI properly ends the turn.
					a.sendSubAgentPhaseDone(key)

					// Interrupt: notify parent so it knows the agent stopped.
					// The session stays for future "send" interactions.
					if a.bgTaskMgr != nil {
						content := "[interrupted] "
						if out.Content != "" {
							content += out.Content
						} else {
							content += "Agent was interrupted."
						}
						if len(content) > 2000 {
							content = content[:2000] + "... [truncated, use inspect for details]"
						}
						sessionKey := originChannel + ":" + originChatID
						a.bgTaskMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
							Key:      sessionKey,
							Type:     tools.SubAgentBgNotifyCompleted,
							Role:     roleName,
							Instance: instance,
							Content:  content,
							Elapsed:  time.Since(startTime),
							Sid:      originSender,
						})
					}
					log.WithFields(log.Fields{
						"role":     roleName,
						"instance": instance,
					}).Info("Background send interrupted, session preserved for future send")
					// Emit subagent_stopped so sidebar updates immediately (busy→idle)
					a.emitSessionState(protocol.SessionEvent{
						Channel:  originChannel,
						ChatID:   originChatID,
						Action:   "subagent_stopped",
						Role:     roleName,
						Instance: instance,
						ParentID: originChatID,
					})
					return
				}
				// Unload/shutdown: NO notification — the parent already knows.
				log.WithFields(log.Fields{
					"role":     roleName,
					"instance": instance,
				}).Info("Background send cancelled (unload/shutdown)")
				return
			}

			// Notify parent via BgTaskManager (same as initial spawn).
			if a.bgTaskMgr != nil {
				content := out.Content
				if out.Error != nil {
					content = fmt.Sprintf("Error: %v\n%s", out.Error, out.Content)
				}
				if len(content) > 2000 {
					content = content[:2000] + "... [truncated, use inspect for details]"
				}
				sessionKey := originChannel + ":" + originChatID
				a.bgTaskMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
					Key:      sessionKey,
					Type:     tools.SubAgentBgNotifyCompleted,
					Role:     roleName,
					Instance: instance,
					Content:  content,
					Elapsed:  time.Since(startTime),
					Sid:      originSender,
				})
			}

			// Emit subagent_stopped so sidebar updates immediately (busy→idle).
			a.emitSessionState(protocol.SessionEvent{
				Channel:  originChannel,
				ChatID:   originChatID,
				Action:   "subagent_stopped",
				Role:     roleName,
				Instance: instance,
				ParentID: originChatID,
			})

			// --- Write back results (mirrors foreground 阶段 3 below) ---
			ia.mu.Lock()
			defer ia.mu.Unlock()

			if out.Error != nil {
				ia.lastError = out.Error.Error()
				ia.lastReply = out.Content
			} else {
				ia.lastError = ""
				ia.lastReply = out.Content
				ia.promptTokens = out.LastPromptTokens
				ia.completionTokens = out.LastCompletionTokens

				// Build ia.messages from the authoritative engine output.
				// out.Messages comes from Run()'s s.messages — it includes the
				// system prompt at [0] and ALL previous messages. We must NOT
				// include the system prompt in ia.messages (it's stored separately
				// in ia.systemPrompt), so we skip out.Messages[0].
				//
				// Note: ia.messages was already updated before Run (user message
				// appended), so we need to replace it entirely with the engine's
				// output (minus system prompt) to reflect tool calls, compression,
				// and other changes that happened during Run.
				if len(out.Messages) < preLen || len(out.Messages) == 0 {
					// Compression happened (or edge case): replace entirely.
					// Skip system prompt (out.Messages[0]) if present.
					start := 0
					if len(out.Messages) > 0 && out.Messages[0].Role == "system" {
						start = 1
					}
					ia.messages = make([]llm.ChatMessage, len(out.Messages)-start)
					copy(ia.messages, out.Messages[start:])
				} else {
					// Normal case (no compression): out.Messages includes all
					// messages from the start. Skip system prompt at [0].
					// Replace ia.messages entirely to keep in sync with engine.
					start := 0
					if len(out.Messages) > 0 && out.Messages[0].Role == "system" {
						start = 1
					}
					ia.messages = make([]llm.ChatMessage, len(out.Messages)-start)
					copy(ia.messages, out.Messages[start:])
				}

				// Append final assistant reply if not already in out.Messages.
				// handleFinalResponse returns directly without appending to s.messages,
				// so out.Messages may not contain the final text reply.
				if out.Content != "" {
					// Check if the last message is already this assistant reply
					if len(ia.messages) == 0 || ia.messages[len(ia.messages)-1].Content != out.Content || ia.messages[len(ia.messages)-1].Role != "assistant" {
						ia.messages = append(ia.messages, llm.NewAssistantMessage(out.Content))
					}
				} else if len(ia.messages) == 0 || ia.messages[len(ia.messages)-1].Role != "assistant" {
					ia.messages = append(ia.messages, llm.NewAssistantMessage("(empty response)"))
				}
				// Carry ReasoningContent to the in-memory message for subsequent turns
				if out.ReasoningContent != "" && len(ia.messages) > 0 {
					ia.messages[len(ia.messages)-1].ReasoningContent = out.ReasoningContent
				}
				// Save iteration history for inspect
				if len(out.IterationHistory) > 0 {
					ia.iterationHistory = out.IterationHistory
				}
			}

			// Persist final assistant message with iteration history as Detail,
			// same as foreground path below and the main agent does in handleInboundMessage.
			if cfg.Session != nil && out.Content != "" {
				assistantMsg := llm.NewAssistantMessage(out.Content)
				assistantMsg.ReasoningContent = out.ReasoningContent
				if len(out.IterationHistory) > 0 {
					if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
						assistantMsg.Detail = string(jsonBytes)
					}
				}
				if err := cfg.Session.AddMessage(assistantMsg); err != nil {
					log.Ctx(ctx).WithError(err).Warn("Failed to save bg send agent assistant message with detail")
				}
			}
		}()

		return &channelpkg.OutboundMsg{
			Content: fmt.Sprintf("Message sent to background agent %q (instance=%q). Results will be notified when complete.", roleName, instance),
		}, nil
	}

	// Foreground agents: run synchronously (blocks caller until complete)
	// Wire DrainBgNotifications for pending message delivery.
	fgSendKey := originChannel + ":" + originChatID
	cfg.DrainBgNotifications = ia.wirePendingMessageDrain(fgSendKey)

	out := Run(subCtx, cfg)

	ia.mu.Lock()
	ia.running = false
	ia.mu.Unlock()

	// --- 阶段 3：锁内写回结果 ---
	ia.mu.Lock()
	defer ia.mu.Unlock()

	if out.Error != nil {
		ia.lastError = out.Error.Error()
		content := out.Content
		if content == "" {
			content = "⚠️ Interactive SubAgent 执行失败。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
		return out.OutboundMsg, nil
	}

	// 追加新增对话消息到 ia.messages
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
	} else {
		// LLM returned empty response — generate a placeholder so
		// GetAgentSessionDump shows the session has completed this turn.
		// Without this, the session shows user2 but no assistant2,
		// making it look like the SubAgent never replied (Issue #bug3).
		ia.messages = append(ia.messages, llm.NewAssistantMessage("(empty response)"))
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

	return out.OutboundMsg, nil
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
	// Mark as not running immediately so subsequent send/unload can proceed
	// without waiting for the background goroutine to detect cancellation.
	// The goroutine will see runCtx.Err() != nil and know it was interrupted.
	ia.running = false
	ia.interrupted = true
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
		// Only cancel/destroy sessions that are CHILDREN of the parent being cleaned up.
		// The old code called cancelCurrent() on ALL sessions regardless of parentKey,
		// which killed sibling/peer background agents when any single agent was unloaded
		// or cancelled — the root cause of "all 5 bg agents die at 7 minutes".
		if pk == parentKey {
			if childIA.cancelCurrent != nil {
				childIA.cancelCurrent()
			}
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

// UnloadInteractiveSession 结束 interactive session：巩固记忆并清理。
// instance 为空时行为与旧版一致（向后兼容）。
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
	// 防护：占位符尚未被替换为完整数据
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

	// 巩固记忆
	if cfg.Memory != nil && len(messages) > 0 {
		a.consolidateSubAgentMemory(ctx, cfg, messages, "interactive session cleanup", roleName, cfg.AgentID)
	}

	// 清理
	a.destroyInteractiveSession(key)

	log.WithField("role", roleName).Info("Interactive session unloaded")
	return nil
}

// buildParentToolContext 从 InboundMessage 构建 SubAgent 需要的 parent ToolContext。
// 与 spawnSubAgent 中的 parentCtx 构建保持一致。
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
		SenderID:            msg.ParentAgentID, // SubAgent 的父上下文：SenderID = 父 Agent ID
		OriginUserID:        senderID,          // 原始用户 ID
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

// GetActiveInteractiveRoles 返回当前 session 下所有活跃的 interactive SubAgent role 名（含 instance 标识）。
// 返回格式："roleName" 或 "roleName:instance"。
func (a *Agent) GetActiveInteractiveRoles(channel, chatID string) []string {
	var roles []string
	prefix := qualifyChatID(channel, chatID) + "/"
	a.interactiveSubAgents.Range(func(k, v any) bool {
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

// CleanupInteractiveSessions 清理指定 session 下所有 interactive sessions。
func (a *Agent) CleanupInteractiveSessions(ctx context.Context, channel, chatID string) {
	keysToClean := a.GetActiveInteractiveRoles(channel, chatID)
	for _, key := range keysToClean {
		// key 格式: "roleName" 或 "roleName:instance"
		role, instance, hasInstance := strings.Cut(key, ":")
		if !hasInstance {
			instance = ""
		}
		_ = a.UnloadInteractiveSession(ctx, role, channel, chatID, instance)
	}
	if len(keysToClean) > 0 {
		log.WithFields(log.Fields{
			"session": qualifyChatID(channel, chatID),
			"roles":   keysToClean,
		}).Info("Cleaned up all interactive sessions")
	}
}

// resolveOriginIDs 从 InboundMessage 中提取 origin channel/chatID/senderID，
// 带有 fallback 到顶层字段的逻辑。
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
		prefix = qualifyChatID(channel, chatID) + "/"
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
// for rendering in a viewer. Includes messages, iteration snapshots, and
// all metadata needed by the TUI status bar (model name, context limits, token usage).
type AgentSessionDump struct {
	Messages         []SessionMessage    `json:"messages"`
	IterationHistory []IterationSnapshot `json:"iterations"`

	// Status bar metadata — used by TUI to render context bar, model name, etc.
	ModelName        string  `json:"modelName,omitempty"`
	MaxContextTokens int64   `json:"maxContextTokens,omitempty"`
	MaxOutputTokens  int64   `json:"maxOutputTokens,omitempty"`
	CompressRatio    float64 `json:"compressRatio,omitempty"`
	PromptTokens     int64   `json:"promptTokens,omitempty"`
	CompletionTokens int64   `json:"completionTokens,omitempty"`
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
		ModelName:        ia.modelName(),
		MaxContextTokens: ia.maxContextTokens(),
		MaxOutputTokens:  ia.maxOutputTokens(),
		CompressRatio:    ia.compressRatio(),
		PromptTokens:     ia.promptTokens,
		CompletionTokens: ia.completionTokens,
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
		ModelName:        ia.modelName(),
		MaxContextTokens: ia.maxContextTokens(),
		MaxOutputTokens:  ia.maxOutputTokens(),
		CompressRatio:    ia.compressRatio(),
		PromptTokens:     ia.promptTokens,
		CompletionTokens: ia.completionTokens,
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
