package serverapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// callGroupRPC drives one RPC through the table the Web UI talks to, so the test
// covers the real path (param decoding + handler + payload shape).
func callGroupRPC(t *testing.T, method string, params map[string]any) map[string]any {
	t.Helper()
	tbl := RPCTable{}
	registerGroupHandlers(tbl, &RPCContext{})
	h, ok := tbl[method]
	if !ok {
		t.Fatalf("rpc %q not registered", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	out, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("rpc %s: %v", method, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	return decoded
}

func groupIDs(resp map[string]any) []string {
	groups, _ := resp["groups"].([]any)
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		if m, ok := g.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func groupMembers(resp map[string]any, id string) []string {
	groups, _ := resp["groups"].([]any)
	for _, g := range groups {
		m, ok := g.(map[string]any)
		if !ok || m["id"] != id {
			continue
		}
		members, _ := m["members"].([]any)
		keys := make([]string, 0, len(members))
		for _, mm := range members {
			if entry, ok := mm.(map[string]any); ok {
				keys = append(keys, entry["session_key"].(string))
			}
		}
		return keys
	}
	return nil
}

// TestPeerGroupRPCs_EditAndPersist covers the whole editor path: create → add →
// remove → delete, plus the durability contract (each edit is written to disk
// immediately — the store's 100ms debounce must not lose a user's edit).
func TestPeerGroupRPCs_EditAndPersist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)

	resp := callGroupRPC(t, "peer_group_create", map[string]any{"id": "dev-team"})
	if ids := groupIDs(resp); len(ids) != 1 || ids[0] != "dev-team" {
		t.Fatalf("after create: %v, want [dev-team]", ids)
	}

	resp = callGroupRPC(t, "peer_group_join", map[string]any{
		"id": "dev-team", "session_key": "cli:/repo", "name": "/repo",
	})
	if members := groupMembers(resp, "dev-team"); len(members) != 1 || members[0] != "cli:/repo" {
		t.Fatalf("after join: %v, want [cli:/repo]", members)
	}

	// The edit must already be on disk (not waiting for the debounce timer).
	list := callGroupRPC(t, "peer_group_list", nil)
	if members := groupMembers(list, "dev-team"); len(members) != 1 {
		t.Fatalf("in-memory list lost the member: %v", members)
	}
	if _, err := filepath.Glob(filepath.Join(home, "peer_groups.json")); err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !peerGroupsFileHasMember(t, home, "dev-team", "cli:/repo") {
		t.Fatal("join was not persisted synchronously — a restart would lose the user's edit")
	}

	resp = callGroupRPC(t, "peer_group_leave", map[string]any{"id": "dev-team", "session_key": "cli:/repo"})
	// The store drops a group once it holds no members (same rule the agent-facing
	// LeaveGroup tool relies on) — so the group is gone, not empty.
	if ids := groupIDs(resp); len(ids) != 0 {
		t.Fatalf("after leaving the last member: %v, want the empty group dropped", ids)
	}

	// Deleting a group that still exists.
	callGroupRPC(t, "peer_group_create", map[string]any{"id": "scratch"})
	callGroupRPC(t, "peer_group_join", map[string]any{"id": "scratch", "session_key": "cli:/repo", "name": "/repo"})
	resp = callGroupRPC(t, "peer_group_delete", map[string]any{"id": "scratch"})
	if ids := groupIDs(resp); len(ids) != 0 {
		t.Fatalf("after delete: %v, want none", ids)
	}
}

// peerGroupsFileHasMember reads the persisted store from disk (not memory) —
// the point of the assertion is that a restart would see the edit.
func peerGroupsFileHasMember(t *testing.T, home, groupID, sessionKey string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "peer_groups.json"))
	if err != nil {
		t.Fatalf("read peer_groups.json: %v", err)
	}
	var stored map[string]struct {
		ID      string `json:"id"`
		Members []struct {
			SessionKey string `json:"session_key"`
		} `json:"members"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode peer_groups.json: %v", err)
	}
	for _, g := range stored {
		if g.ID != groupID {
			continue
		}
		for _, m := range g.Members {
			if m.SessionKey == sessionKey {
				return true
			}
		}
	}
	return false
}

// TestPeerGroupRPCs_RejectBadInput: a malformed id or session key must fail at
// the door — persisting it would create membership that can never receive a
// message.
func TestPeerGroupRPCs_RejectBadInput(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())

	callGroupRPC(t, "peer_group_create", map[string]any{"id": "ok-group"})

	bad := []struct {
		method string
		params map[string]any
	}{
		{"peer_group_join", map[string]any{"id": "ok-group", "session_key": "no-colon"}},
		{"peer_group_join", map[string]any{"id": "ok-group", "session_key": "channel:has space"}},
		{"peer_group_join", map[string]any{"id": "missing-group", "session_key": "cli:/repo"}},
		{"peer_group_leave", map[string]any{"id": "missing-group", "session_key": "cli:/repo"}},
		{"peer_group_delete", map[string]any{"id": "missing-group"}},
	}
	for _, tc := range bad {
		tbl := RPCTable{}
		registerGroupHandlers(tbl, &RPCContext{})
		raw, _ := json.Marshal(tc.params)
		if _, err := tbl[tc.method](context.Background(), raw); err == nil {
			t.Errorf("%s(%v) must fail explicitly", tc.method, tc.params)
		}
	}

	// An invalid group id is rejected before anything is stored.
	tbl := RPCTable{}
	registerGroupHandlers(tbl, &RPCContext{})
	raw, _ := json.Marshal(map[string]any{"id": "bad id!"})
	if _, err := tbl["peer_group_create"](context.Background(), raw); err == nil {
		t.Error("creating a group with an invalid id must fail")
	}
}
