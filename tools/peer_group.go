package tools

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"xbot/llm"
)

// peerGroupAdjs and peerGroupNouns provide word lists for auto-generating group IDs.
var peerGroupAdjs = []string{
	"swift", "calm", "bold", "keen", "warm",
	"sharp", "brisk", "astute", "bright", "steady",
	"noble", "silent", "swift", "brave", "deft",
}

var peerGroupNouns = []string{
	"team", "crew", "squad", "pack", "band",
	"wing", "core", "cell", "hub", "node",
	"loop", "link", "mesh", "ring", "vault",
}

// JoinGroupTool allows an agent to create or join a peer group.
// If group_id is not provided, a random one is auto-generated.
type JoinGroupTool struct{}

func (t *JoinGroupTool) Name() string { return "JoinGroup" }

func (t *JoinGroupTool) Description() string {
	return `Join or create a peer group for inter-agent communication.

Peer groups let independent agent sessions discover each other and exchange messages asynchronously.
After joining, use SendMessage(to="peer:<group_id>", message="...") to broadcast to all members.
Use SendMessage(to="session:<session_key>", message="...") to DM a specific member.
Use ListGroupMembers to see who else is in the group.

If group_id is omitted, a random ID is auto-generated and returned in the result.`
}

type JoinGroupParams struct {
	GroupID string `json:"group_id,omitempty" jsonschema:"description=Peer group ID to join. Omit to auto-generate a new group."`
	// Name is YOUR display name in the group — how other members will see you.
	// Not the group name. Defaults to your session key if omitted.
	Name string `json:"name,omitempty" jsonschema:"description=Your own display name in this group (NOT the group name). How other members identify you. Optional."`
}

func (t *JoinGroupTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "group_id", Type: "string", Description: "Peer group ID to join. Omit to auto-generate a new unique group ID."},
		{Name: "name", Type: "string", Description: "Your own display name in this group — NOT the group name. How other members will identify you. Optional, defaults to session key."},
	}
}

func (t *JoinGroupTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params JoinGroupParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}

	sessionKey := qualifySessionKey(ctx)
	if sessionKey == "" {
		return nil, fmt.Errorf("session key not available — peer groups require a valid session")
	}

	// Auto-generate group ID if not provided
	groupID := params.GroupID
	if groupID == "" {
		groupID = generatePeerGroupID()
	} else if !ValidatePeerGroupID(groupID) {
		return nil, fmt.Errorf("invalid group_id %q: must be 1-64 chars, alphanumeric/hyphens/underscores, start with letter or digit", groupID)
	}

	displayName := params.Name
	if displayName == "" {
		displayName = sessionKey
	}

	pg := CreatePeerGroup(groupID)
	member := PeerGroupMember{
		SessionKey: sessionKey,
		Name:       displayName,
	}

	if !pg.Join(member) {
		members := pg.GetMembers()
		return NewResult(fmt.Sprintf(
			"You are already a member of peer group **%s**.\nMembers (%d): %s\n\nSend: SendMessage(to=\"peer:%s\", message=\"...\") to broadcast.\nDirect: SendMessage(to=\"session:<key>\", message=\"...\") to DM a member.",
			groupID, len(members), formatMemberList(members), groupID,
		)), nil
	}

	members := pg.GetMembers()
	return NewResult(fmt.Sprintf(
		"Joined peer group **%s**.\nMembers (%d): %s\n\nSend: SendMessage(to=\"peer:%s\", message=\"...\") to broadcast.\nDirect: SendMessage(to=\"session:<key>\", message=\"...\") to DM a member.",
		groupID, len(members), formatMemberList(members), groupID,
	)), nil
}

// LeaveGroupTool removes the caller from a peer group.
type LeaveGroupTool struct{}

func (t *LeaveGroupTool) Name() string { return "LeaveGroup" }

func (t *LeaveGroupTool) Description() string {
	return `Leave a peer group. You will no longer receive or send messages to the group.`
}

type LeaveGroupParams struct {
	GroupID string `json:"group_id" jsonschema:"required,description=Peer group ID to leave"`
}

func (t *LeaveGroupTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "group_id", Type: "string", Description: "Peer group ID to leave", Required: true},
	}
}

func (t *LeaveGroupTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params LeaveGroupParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}

	pgName := "peer:" + params.GroupID
	pg, ok := GetPeerGroup(pgName)
	if !ok {
		return nil, fmt.Errorf("peer group %q not found", params.GroupID)
	}

	sessionKey := qualifySessionKey(ctx)
	if !pg.Leave(sessionKey) {
		return nil, fmt.Errorf("you are not a member of peer group %q", params.GroupID)
	}

	return NewResult(fmt.Sprintf("Left peer group **%s**.", params.GroupID)), nil
}

// ListGroupMembersTool shows members of a peer group (or all groups the caller is in).
type ListGroupMembersTool struct{}

func (t *ListGroupMembersTool) Name() string { return "ListGroupMembers" }

func (t *ListGroupMembersTool) Description() string {
	return `List members of a peer group. If no group_id is specified, lists all groups you belong to and their members.`
}

type ListGroupMembersParams struct {
	GroupID string `json:"group_id,omitempty" jsonschema:"description=Peer group ID (omit to list all your groups)"`
}

func (t *ListGroupMembersTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "group_id", Type: "string", Description: "Peer group ID (omit to list all your groups)"},
	}
}

func (t *ListGroupMembersTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params ListGroupMembersParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}

	sessionKey := qualifySessionKey(ctx)

	// Specific group
	if params.GroupID != "" {
		pgName := "peer:" + params.GroupID
		pg, ok := GetPeerGroup(pgName)
		if !ok {
			return nil, fmt.Errorf("peer group %q not found", params.GroupID)
		}
		members := pg.GetMembers()
		return NewResult(fmt.Sprintf("Peer group **%s** — %d members:\n%s",
			params.GroupID, len(members), formatMemberListDetailed(members, sessionKey))), nil
	}

	// List all groups for this session
	groups := ListPeerGroups(sessionKey)
	if len(groups) == 0 {
		return NewResult("You are not in any peer groups. Use JoinGroup to create or join one."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "You are in %d peer group(s):\n", len(groups))
	for _, g := range groups {
		members := g.Members
		fmt.Fprintf(&sb, "\n**%s** (%d members):\n", g.ID, len(members))
		sb.WriteString(formatMemberListDetailed(members, sessionKey))
	}
	return NewResult(sb.String()), nil
}

// generatePeerGroupID creates a random adj-noun group ID like "bold-crew" or "swift-pack".
func generatePeerGroupID() string {
	adj := peerGroupAdjs[rand.Intn(len(peerGroupAdjs))]
	noun := peerGroupNouns[rand.Intn(len(peerGroupNouns))]
	return adj + "-" + noun
}

// qualifySessionKey builds a session key from the ToolContext.
// Format: "channel:chatID" — matches the key used by injectPeerMessage.
func qualifySessionKey(ctx *ToolContext) string {
	if ctx.Channel != "" && ctx.ChatID != "" {
		return ctx.Channel + ":" + ctx.ChatID
	}
	if ctx.BgSessionKey != "" {
		return ctx.BgSessionKey
	}
	// Fallback: use RootSessionKey if available
	if ctx.RootSessionKey != "" {
		return ctx.RootSessionKey
	}
	return ""
}

func formatMemberList(members []PeerGroupMember) string {
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}

func formatMemberListDetailed(members []PeerGroupMember, selfKey string) string {
	var sb strings.Builder
	for _, m := range members {
		if m.SessionKey == selfKey {
			fmt.Fprintf(&sb, "  → %s **(you)** (session: %s)\n", m.Name, m.SessionKey)
		} else {
			fmt.Fprintf(&sb, "    %s (session: %s)\n", m.Name, m.SessionKey)
		}
	}
	return sb.String()
}
