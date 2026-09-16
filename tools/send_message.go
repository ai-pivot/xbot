package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
)

// SendMessageTool sends a message to any addressable target.
// For groups, uses a meeting model: moderator controls who speaks via @mentions.
type SendMessageTool struct{}

func (t *SendMessageTool) Name() string { return "SendMessage" }

func (t *SendMessageTool) Description() string {
	return `Send a message to any addressable target (agent, group, peer group, session, or IM user).

## Addressing
- Agent: "agent:<role>/<instance>" (e.g., "agent:reviewer/cr1")
- Group: "group:<id>" (e.g., "group:g1") — SubAgent meeting mode
- Peer Group: "peer:<group_id>" (e.g., "peer:dev-team") — async broadcast to independent agent sessions
- Session: "session:<session_key>" (e.g., "session:cli:session-abc") — async message to a specific agent session
- IM user (Feishu): "feishu:<open_id>" (e.g., "feishu:ou_xxx")

## Agent target
Blocks until reply (RPC), returns the agent's response.

## Group target — Meeting Mode
Group chats work like a moderated meeting:
- Only the moderator's messages with @mentions trigger agents to speak.
- Messages without @mentions are added to the discussion history but do NOT trigger anyone.
- Each @mentioned agent receives the FULL discussion history + the current message.
- The agent's response is added to the history for future reference.

## Peer Group target — Async Broadcast
Sends a message to all members of a peer group (except yourself).
Messages are delivered asynchronously:
- If the target is busy (processing a turn), the message is injected as a tool result in their current iteration.
- If the target is idle, the message triggers a new conversation turn.
You must join the group first using JoinGroup. Use ListGroupMembers to see who's in the group.

Examples:
- SendMessage(to="group:g1", message="Let's discuss the API design.")
  → Adds moderator message to history. No agent triggered.
- SendMessage(to="group:g1", message="@agent:reviewer/r1 What do you think?")
  → Triggers agent:reviewer/r1 with full history + this question. Response added to history.
- SendMessage(to="group:g1", message="@agent:reviewer/r1 @agent:tester/t1 Please both review.")
  → Triggers both agents concurrently. Both see the same history. Both responses added.
  - SendMessage(to="peer:dev-team", message="Found a critical bug in auth module, please check.")
  → Async broadcast to all members in peer group "dev-team".

## IM target
Sends message immediately (fire-and-forget).`
}

type SendMessageParams struct {
	To      string `json:"to" jsonschema:"required,description=Target address (agent:xxx, group:xxx, feishu:xxx)"`
	Message string `json:"message" jsonschema:"required,description=Message content to send"`
}

func (t *SendMessageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "to", Type: "string", Description: "Target address (agent:xxx, group:xxx, peer:xxx, session:xxx, feishu:xxx)", Required: true},
		{Name: "message", Type: "string", Description: "Message content to send. For groups, use @agent:role/instance to trigger specific agents.", Required: true},
	}
}

func (t *SendMessageTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params SendMessageParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}

	addr := params.To
	if addr == "" {
		return nil, fmt.Errorf("invalid address format: empty")
	}

	// Dispatch by address prefix using HasPrefix for robustness.
	switch {
	case strings.HasPrefix(addr, "agent:"):
		return t.sendToAgent(ctx, addr, params.Message)

	case strings.HasPrefix(addr, "group:"):
		return t.sendToGroup(ctx, addr, params.Message)

	case strings.HasPrefix(addr, "peer:"):
		return t.sendToPeerGroup(ctx, addr, params.Message)

	case strings.HasPrefix(addr, "session:"):
		// Strip "session:" prefix, rest is the session key (e.g. "cli:/path:session-name")
		return t.sendToSession(ctx, addr[len("session:"):], params.Message)
	}

	// IM addresses go through Dispatcher — parse known IM prefixes
	channelName, chatID := parseAddress(addr)
	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}

	result, err := sendMessageWithCtx(ctx, channelName, chatID, params.Message)
	if err != nil {
		return nil, fmt.Errorf("send failed: %w", err)
	}

	if result != "" {
		return NewResult(result), nil
	}
	return NewResult(fmt.Sprintf("Message sent to %s", params.To)), nil
}

// sendToAgent sends a message to a single agent via Dispatcher.
// The agent must have been registered as an AgentChannel (by SubAgent or CreateChat).
// If the caller is in a group, the target must also be a member of the same group.
func (t *SendMessageTool) sendToAgent(ctx *ToolContext, addr, message string) (*ToolResult, error) {
	// Group membership check: if caller is in a group, target must be a fellow member.
	if ctx.GroupID != "" {
		if !isInGroup(ctx, addr) {
			return nil, fmt.Errorf("cross-group messaging not allowed: you are in group %s but %s is not a member", ctx.GroupID, addr)
		}
	}
	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}
	// **立刻成功**（用户 2026-09-14：「sendmessage 工具有可能卡死，必须立刻成功」）：
	// 旧实现整段等目标 agent 的回复（最多 AgentRPCTimeout=30s），目标忙/卡住时工具就卡死。
	// 现在只在 SendMessageAwaitReply（2s）窗口内顺手拿回复；拿不到就立即回"已投递"，
	// 投递在后台继续（独立 ctx，绝不用工具 ctx —— 工具返回后它会被取消）。
	baseCtx := ctx.Ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	type sendOutcome struct {
		reply string
		err   error
	}
	outcomeCh := make(chan sendOutcome, 1)
	go func() {
		detached := *ctx
		rCtx, cancel := context.WithTimeout(context.WithoutCancel(baseCtx), AgentRPCTimeout)
		defer cancel()
		detached.Ctx = rCtx
		reply, err := sendMessageWithCtx(&detached, addr, "", message)
		// ── P0（2026-09-16 用户报告：subagent 用 `agent:recov-e8e9/ws` 发消息失败）──
		// agent 地址是**作为频道名**投递的，而 `agent:<role>/<instance>` 频道只在
		// "同进程 spawn 且未卸载"期间存在（tools/subagent.go 注册/unload 注销、TTL
		// 淘汰）⇒ 查不到时 dispatcher 只回 `unknown channel: agent:…`，对调用方毫无
		// 指向性。这里补两件事：
		//   ① role/instance 写反是常见笔误 → 用**颠倒地址**再投一次（仅当确为两段式）；
		//   ② 仍失败 → 把错误翻译成可操作指引（而不是让模型自己猜）。
		if err != nil && isUnknownAgentChannelErr(err) {
			if swapped := swapAgentAddress(addr); swapped != "" {
				if r2, err2 := sendMessageWithCtx(&detached, swapped, "", message); err2 == nil {
					log.WithFields(log.Fields{"requested": addr, "used": swapped}).
						Warn("SendMessage: role/instance looked swapped — delivered to the swapped agent address")
					reply, err = r2, nil
				}
			}
		}
		if err != nil && isUnknownAgentChannelErr(err) {
			err = fmt.Errorf("%s 不可达：该 interactive agent 未在本进程注册"+
				"（interactive agent 仅在其存活期间可寻址；unload 或超时淘汰即注销）。"+
				"请确认地址形式为 agent:<role>/<instance>（不要颠倒），"+
				"必要时先用 CreateChat / SubAgent 把它拉起，或改用 session:<key> / peer:<group> 联系。"+
				"原始错误：%w", addr, err)
		}
		outcomeCh <- sendOutcome{reply: reply, err: err}
	}()
	select {
	case out := <-outcomeCh:
		if out.err != nil {
			return nil, fmt.Errorf("agent send failed: %w", out.err)
		}
		if out.reply == "" {
			return nil, fmt.Errorf("agent %s returned empty response (session may have ended)", addr)
		}
		return NewResult(out.reply), nil
	case <-time.After(SendMessageAwaitReply):
		return NewResult(fmt.Sprintf(
			"Message delivered to %s. It is still working, so no reply yet — keep going; any reply arrives asynchronously.",
			addr)), nil
	}
}

// isUnknownAgentChannelErr reports whether err is the Dispatcher's
// "unknown channel: <name>" failure — i.e. no channel is registered under that
// name in this process. For `agent:<role>/<instance>` targets that means the
// interactive agent is not currently alive here (never spawned in this process,
// unloaded, or TTL-evicted) — see 2026-09-16 user report.
func isUnknownAgentChannelErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unknown channel")
}

// swapAgentAddress rewrites `agent:<a>/<b>` into `agent:<b>/<a>`. Models
// (and humans) routinely mix up role and instance — the reported failure was
// `agent:recov-e8e9/ws`, where "recov-e8e9" reads like an instance and "ws" like
// a role. Returns "" when the address is not a two-segment `agent:` address
// (nothing to swap) or the segments are identical.
func swapAgentAddress(addr string) string {
	rest, ok := strings.CutPrefix(addr, "agent:")
	if !ok {
		return ""
	}
	role, instance, ok := strings.Cut(rest, "/")
	if !ok || role == "" || instance == "" || role == instance {
		return ""
	}
	return "agent:" + instance + "/" + role
}

// isInGroup checks if addr is a member of the caller's group.
func isInGroup(ctx *ToolContext, addr string) bool {
	for _, m := range ctx.GroupMembers {
		if m == addr {
			return true
		}
	}
	return false
}

// sendToGroup handles virtual group messaging.
// The group has NO message store — it only defines a membership boundary.
// Messages are sent directly to agent members (each agent has its own session).
// sendToGroup handles virtual group messaging.
// The group has NO message store — it only defines a membership boundary.
// Messages are sent directly to agent members (each agent has its own session).
// @mentioned agents receive the message prefixed with group context.
// Without @mentions, the message is broadcast to all members.
func (t *SendMessageTool) sendToGroup(ctx *ToolContext, groupName, message string) (*ToolResult, error) {
	gm, ok := GetGroup(groupName)
	if !ok {
		return nil, fmt.Errorf("group %q not found (create it with CreateChat first)", groupName)
	}
	// Snapshot members under lock to avoid concurrent modification.
	gm.mu.RLock()
	closed := gm.Closed
	members := make([]string, len(gm.Members))
	copy(members, gm.Members)
	gm.mu.RUnlock()

	if closed {
		return nil, fmt.Errorf("group %q is closed", groupName)
	}

	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}

	// Timeout protection to prevent indefinite blocking on agent RPCs.
	baseCtx := ctx.Ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(baseCtx, AgentRPCTimeout)
	defer cancel()
	ctxWithTimeout := *ctx
	ctxWithTimeout.Ctx = timeoutCtx

	mentions := parseMentions(message)

	// memberResult collects the result of a concurrent agent send.
	type memberResult struct {
		addr   string
		result string
		err    error
	}

	// Without @mentions, broadcast to all members concurrently.
	if len(mentions) == 0 {
		results := make(chan memberResult, len(members))
		var wg sync.WaitGroup
		for _, memberAddr := range members {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						results <- memberResult{addr: addr, err: fmt.Errorf("panic: %v", r)}
					}
				}()
				prefixedMsg := fmt.Sprintf("[group:%s] %s", groupName, message)
				result, err := sendMessageWithCtx(&ctxWithTimeout, addr, "", prefixedMsg)
				results <- memberResult{addr: addr, result: result, err: err}
			}(memberAddr)
		}
		wg.Wait()
		close(results)

		var responses []string
		for r := range results {
			if r.err != nil {
				responses = append(responses, fmt.Sprintf("[WARN] %s: %v", r.addr, r.err))
			} else {
				responses = append(responses, fmt.Sprintf("[→ %s]: %s", r.addr, truncateMsg(r.result, 200)))
			}
		}
		return NewResult(fmt.Sprintf("Broadcast to group %s (%d members):\n%s",
			groupName, len(members), strings.Join(responses, "\n"))), nil
	}

	// @mentioned agents: send concurrently with group context prefix.
	// Filter to valid members first, then launch concurrent sends.
	var responses []string
	var validMentions []string
	for _, agentAddr := range mentions {
		if !gm.IsMember(agentAddr) {
			responses = append(responses, fmt.Sprintf("[REJECT] %s is not a member of group %s", agentAddr, groupName))
			continue
		}
		validMentions = append(validMentions, agentAddr)
	}

	if len(validMentions) > 0 {
		results := make(chan memberResult, len(validMentions))
		var wg sync.WaitGroup
		for _, agentAddr := range validMentions {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						results <- memberResult{addr: addr, err: fmt.Errorf("panic: %v", r)}
					}
				}()
				prefixedMsg := fmt.Sprintf("[group:%s] %s", groupName, message)
				result, err := sendMessageWithCtx(&ctxWithTimeout, addr, "", prefixedMsg)
				results <- memberResult{addr: addr, result: result, err: err}
			}(agentAddr)
		}
		wg.Wait()
		close(results)

		for r := range results {
			if r.err != nil {
				responses = append(responses, fmt.Sprintf("[ERROR] %s: %v", r.addr, r.err))
			} else {
				responses = append(responses, fmt.Sprintf("[%s]:\n%s", r.addr, truncateMsg(r.result, 500)))
			}
		}
	}

	return NewResult(strings.Join(responses, "\n\n---\n\n")), nil
}

// sendToSession sends a message to a specific agent session by session key.
// Uses PeerMessageFn (injectPeerMessage) — same mechanism as bgtask:
// busy→fake tool result, idle→user message.
func (t *SendMessageTool) sendToSession(ctx *ToolContext, targetSessionKey, message string) (*ToolResult, error) {
	if ctx.PeerMessageFn == nil {
		return nil, fmt.Errorf("session messaging not available in this context")
	}
	if targetSessionKey == "" {
		return nil, fmt.Errorf("session key is empty")
	}

	// Don't allow sending to self
	selfKey := qualifySessionKey(ctx)
	if targetSessionKey == selfKey {
		return nil, fmt.Errorf("cannot send message to yourself")
	}

	result := ctx.PeerMessageFn(targetSessionKey, message)
	return NewResult(fmt.Sprintf("Message delivered to session %s: %s", targetSessionKey, result)), nil
}

// sendToPeerGroup sends a message to all members of a peer group (except the sender).
// Uses PeerMessageFn (wired from Agent.injectPeerMessage) which handles
// busy/idle injection: busy→fake tool result, idle→user message.
func (t *SendMessageTool) sendToPeerGroup(ctx *ToolContext, peerGroupName, message string) (*ToolResult, error) {
	pg, ok := GetPeerGroup(peerGroupName)
	if !ok {
		return nil, fmt.Errorf("peer group %q not found (use JoinGroup to create or join one)", peerGroupName)
	}

	if ctx.PeerMessageFn == nil {
		return nil, fmt.Errorf("peer group messaging not available in this context")
	}

	// Verify sender is a member
	sessionKey := qualifySessionKey(ctx)
	if !pg.IsMember(sessionKey) {
		return nil, fmt.Errorf("you are not a member of peer group %q (use JoinGroup to join)", peerGroupName)
	}

	members := pg.GetMembers()
	senderName := sessionKey
	// Find sender's display name
	for _, m := range members {
		if m.SessionKey == sessionKey {
			senderName = m.Name
			break
		}
	}

	var responses []string
	for _, m := range members {
		if m.SessionKey == sessionKey {
			continue // skip self
		}
		prefixedMsg := fmt.Sprintf("[peer:%s] %s:\n%s", pg.ID, senderName, message)
		result := ctx.PeerMessageFn(m.SessionKey, prefixedMsg)
		responses = append(responses, fmt.Sprintf("→ %s: %s", m.Name, result))
	}

	if len(responses) == 0 {
		return NewResult(fmt.Sprintf("You are the only member in peer group %q.", peerGroupName)), nil
	}

	return NewResult(fmt.Sprintf("Message sent to peer group **%s** (%d recipients):\n%s",
		peerGroupName, len(responses), strings.Join(responses, "\n"))), nil
}

// parseMentions extracts @agent:role/instance addresses from a message.
// Returns unique addresses in order of first appearance.
// Validates that each address contains a "/" (role/instance format).
func parseMentions(message string) []string {
	var result []string
	seen := make(map[string]bool)
	// Find all @agent:xxx/yyy patterns
	for i := 0; i < len(message); i++ {
		if message[i] == '@' && i+6 < len(message) && message[i+1:i+7] == "agent:" {
			// Find end of address (whitespace or end of string)
			end := len(message)
			for j := i + 7; j < len(message); j++ {
				if message[j] == ' ' || message[j] == '\n' || message[j] == '\t' || message[j] == '\r' {
					end = j
					break
				}
			}
			addr := message[i+1 : end] // strip the @
			// Validate: must contain "/" to be a valid agent:role/instance address.
			// Rejects bare "agent:" or "agent:role" (no instance).
			if addr != "" && strings.Contains(addr, "/") && !seen[addr] {
				seen[addr] = true
				result = append(result, addr)
			}
		}
	}
	return result
}

// loadRoleFromCtx loads a SubAgentRole using the ToolContext's sandbox and directory info.
func loadRoleFromCtx(ctx *ToolContext, roleName string) (*SubAgentRole, bool) {
	EnsureSynced(ctx)
	originUserID := ctx.OriginUserID
	if originUserID == "" {
		originUserID = ctx.SenderID
	}

	var roleSb Sandbox
	var roleUserID string
	var userAgentDirs []string
	if shouldUseSandbox(ctx) {
		roleSb = ctx.Sandbox
		roleUserID = originUserID
		if sbDir := sandboxBaseDir(ctx); sbDir != "" {
			userAgentDirs = append(userAgentDirs, filepath.Join(sbDir, "agents"))
		}
	} else {
		if originUserID != "" && ctx.WorkingDir != "" {
			userAgentDirs = append(userAgentDirs, UserAgentsRoot(ctx.WorkingDir, originUserID))
		}
		if ctx.WorkspaceRoot != "" {
			userAgentDirs = append(userAgentDirs, filepath.Join(ctx.WorkspaceRoot, ".agents"))
		}
	}

	role, ok := GetSubAgentRoleSandbox(ctx.Ctx, roleName, roleSb, roleUserID, userAgentDirs...)
	return role, ok
}

// parseAddress splits an address into (channelName, chatID).
// "agent:reviewer" → ("agent:reviewer", "")
// "feishu:ou_xxx" → ("feishu", "ou_xxx")
// "group:rt1" → ("group:rt1", "")
func parseAddress(addr string) (channelName, chatID string) {
	// Known IM prefixes: checked longest-first to avoid ambiguity
	imPrefixes := []string{"feishu", "web", "qq", "cli"}
	for _, prefix := range imPrefixes {
		if len(addr) > len(prefix)+1 && addr[:len(prefix)+1] == prefix+":" {
			return prefix, addr[len(prefix)+1:]
		}
	}
	// Agent or group: the whole address is the channel name
	return addr, ""
}

// truncateMsg limits a string to n bytes with a " ..." suffix, cutting on a
// UTF-8 rune boundary (message content is routinely CJK — a raw s[:n] would
// hand the model invalid UTF-8).
func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return TruncateHeadPreview(s, n)
}

// sendMessageWithCtx sends a message via MessageSender, using MessageSenderCtx
// (which propagates caller context for cancellation) when available.
// Falls back to plain MessageSender for backward compatibility.
func sendMessageWithCtx(ctx *ToolContext, channelName, chatID, content string) (string, error) {
	if senderCtx, ok := ctx.MessageSender.(bus.MessageSenderCtx); ok {
		return senderCtx.SendMessageCtx(ctx.Ctx, channelName, chatID, content)
	}
	return ctx.MessageSender.SendMessage(channelName, chatID, content)
}
