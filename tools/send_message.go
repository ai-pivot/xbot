package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"xbot/bus"
	"xbot/llm"
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
  → Triggers both agents sequentially. Both see the same history. Both responses added.
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
	result, err := sendMessageWithCtx(ctx, addr, "", message)
	if err != nil {
		return nil, fmt.Errorf("agent send failed: %w", err)
	}
	if result == "" {
		return nil, fmt.Errorf("agent %s returned empty response (session may have ended)", addr)
	}
	return NewResult(result), nil
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

	mentions := parseMentions(message)

	// Without @mentions, the message is broadcast to all members
	if len(mentions) == 0 {
		var responses []string
		for _, memberAddr := range members {
			prefixedMsg := fmt.Sprintf("[group:%s] %s", groupName, message)
			result, err := sendMessageWithCtx(ctx, memberAddr, "", prefixedMsg)
			if err != nil {
				responses = append(responses, fmt.Sprintf("[WARN] %s: %v", memberAddr, err))
			} else {
				responses = append(responses, fmt.Sprintf("[→ %s]: %s", memberAddr, truncateMsg(result, 200)))
			}
		}
		return NewResult(fmt.Sprintf("Broadcast to group %s (%d members):\n%s",
			groupName, len(members), strings.Join(responses, "\n"))), nil
	}

	// @mentioned agents: send directly with group context prefix.
	// Each agent already has its own session for full context.
	var responses []string
	for _, agentAddr := range mentions {
		if !gm.IsMember(agentAddr) {
			responses = append(responses, fmt.Sprintf("[REJECT] %s is not a member of group %s", agentAddr, groupName))
			continue
		}

		prefixedMsg := fmt.Sprintf("[group:%s] %s", groupName, message)
		result, err := sendMessageWithCtx(ctx, agentAddr, "", prefixedMsg)
		if err != nil {
			responses = append(responses, fmt.Sprintf("[ERROR] %s: %v", agentAddr, err))
			continue
		}
		responses = append(responses, fmt.Sprintf("[%s]:\n%s", agentAddr, truncateMsg(result, 500)))
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

// parseAgentAddress splits "agent:<role>/<instance>" into (role, instance).
// Returns ("", "") if the format doesn't match.
func parseAgentAddress(addr string) (role, instance string) {
	// addr is already confirmed to start with "agent:"
	rest := addr[6:]
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return "", ""
	}
	return rest[:idx], rest[idx+1:]
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

// truncateMsg limits a string to n chars with "..." suffix.
func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
