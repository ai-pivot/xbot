package serverapp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"xbot/tools"
)

// Peer groups are the DURABLE agent-group concept: a named set of sessions that
// can message each other (SendMessage(to="peer:<id>")). The tools layer has
// owned this store all along (join/leave via the agent's own tools) but nothing
// ever exposed it to a human — these handlers are the thin adapter that lets the
// Web UI show which agents share a group and edit the membership.
//
// (Meeting groups — CreateChat(type=group) — are deliberately NOT editable here:
// they live in memory, their members are Dispatcher channel names that only exist
// while that SubAgent is alive, and their membership is fixed when the members are
// pre-spawned. Adding a member from the UI would produce a group nobody can talk
// to, i.e. a lie. Read-only visibility can be added on request.)
func registerGroupHandlers(t RPCTable, h *RPCContext) {
	// peer_group_list returns every group with its members.
	t["peer_group_list"] = rpc0err(func(ctx context.Context) (any, error) {
		return map[string]any{"groups": peerGroupPayload()}, nil
	})

	// peer_group_create adds an empty group (idempotent: an existing id is returned
	// as-is rather than an error, so a double click is harmless).
	t["peer_group_create"] = rpc1(func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (any, error) {
		id := strings.TrimSpace(p.ID)
		if !tools.ValidatePeerGroupID(id) {
			return nil, fmt.Errorf("invalid group id %q: use letters, digits, '-' or '_' (max 64, must start alphanumeric)", id)
		}
		tools.CreatePeerGroup(id)
		tools.FlushPeerGroups() // an edit the user just made must not wait for a timer
		return map[string]any{"groups": peerGroupPayload()}, nil
	})

	// peer_group_delete removes a group entirely.
	t["peer_group_delete"] = rpc1(func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (any, error) {
		if _, ok := tools.GetPeerGroup("peer:" + p.ID); !ok {
			return nil, fmt.Errorf("group %q not found", p.ID)
		}
		tools.DeletePeerGroup("peer:" + p.ID)
		tools.FlushPeerGroups()
		return map[string]any{"groups": peerGroupPayload()}, nil
	})

	// peer_group_join adds one session to a group. session_key is the session
	// address the messaging pipeline routes on ("channel:chatID").
	t["peer_group_join"] = rpc1(func(ctx context.Context, p struct {
		ID         string `json:"id"`
		SessionKey string `json:"session_key"`
		Name       string `json:"name"`
	}) (any, error) {
		pg, ok := tools.GetPeerGroup("peer:" + p.ID)
		if !ok {
			return nil, fmt.Errorf("group %q not found", p.ID)
		}
		if err := validatePeerSessionKey(p.SessionKey); err != nil {
			return nil, err
		}
		name := p.Name
		if strings.TrimSpace(name) == "" {
			name = p.SessionKey
		}
		changed := pg.Join(tools.PeerGroupMember{SessionKey: p.SessionKey, Name: name})
		tools.FlushPeerGroups()
		return map[string]any{"groups": peerGroupPayload(), "changed": changed}, nil
	})

	// peer_group_leave removes one session from a group (the group itself is
	// dropped by the store when it becomes empty).
	t["peer_group_leave"] = rpc1(func(ctx context.Context, p struct {
		ID         string `json:"id"`
		SessionKey string `json:"session_key"`
	}) (any, error) {
		pg, ok := tools.GetPeerGroup("peer:" + p.ID)
		if !ok {
			return nil, fmt.Errorf("group %q not found", p.ID)
		}
		changed := pg.Leave(p.SessionKey)
		tools.FlushPeerGroups()
		return map[string]any{"groups": peerGroupPayload(), "changed": changed}, nil
	})
}

// peerGroupPayload snapshots every group, newest activity first (stable order so
// the panel does not jump around between refreshes).
func peerGroupPayload() []map[string]any {
	groups := tools.ListPeerGroups("")
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		members := make([]map[string]string, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, map[string]string{"session_key": m.SessionKey, "name": m.Name})
		}
		out = append(out, map[string]any{"id": g.ID, "members": members})
	}
	return out
}

// validatePeerSessionKey checks the routing address shape the store and the
// messaging pipeline both assume. A malformed key would be persisted happily and
// then never deliver anything, so it is rejected at the door.
func validatePeerSessionKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("session_key is required")
	}
	if !strings.Contains(key, ":") || strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("invalid session_key %q: expected the session address (channel:chatID)", key)
	}
	return nil
}
