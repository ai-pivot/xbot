package tools

import "sync"

// GroupMembership defines a virtual group chat — it's just a set of agent members.
// There is NO separate message store. Each agent already has its own session
// with full message history. The group only constrains which agents can
// communicate: agents in the same group can SendMessage directly to each other.
//
// This is a deliberate simplification over the old GroupState model which
// duplicated agent session functionality (message store, history formatting).
type GroupMembership struct {
	mu      sync.RWMutex
	ID      string   // e.g. "g1"
	Name    string   // e.g. "group:g1"
	Members []string // agent addresses e.g. ["agent:reviewer/r1", "agent:tester/t1"]
	Closed  bool
}

// groupStore holds active group memberships.
var groupStore sync.Map // "group:<id>" -> *GroupMembership

// CreateGroup creates a new group membership and stores it.
func CreateGroup(id string, members []string) *GroupMembership {
	gm := &GroupMembership{
		ID:      id,
		Name:    "group:" + id,
		Members: members,
	}
	groupStore.Store(gm.Name, gm)
	return gm
}

// GetGroup retrieves a group membership by name (e.g., "group:g1").
func GetGroup(name string) (*GroupMembership, bool) {
	v, ok := groupStore.Load(name)
	if !ok {
		return nil, false
	}
	gm, ok := v.(*GroupMembership)
	if !ok {
		return nil, false
	}
	return gm, true
}

// DeleteGroup removes a group from the store.
func DeleteGroup(name string) {
	groupStore.Delete(name)
}

// Close marks the group as closed.
func (g *GroupMembership) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Closed = true
}

// IsClosed returns whether the group is closed.
func (g *GroupMembership) IsClosed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.Closed
}

// IsMember checks if an address is in this group.
func (g *GroupMembership) IsMember(addr string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, m := range g.Members {
		if m == addr {
			return true
		}
	}
	return false
}

// GetMembers returns a copy of the members slice.
func (g *GroupMembership) GetMembers() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]string, len(g.Members))
	copy(result, g.Members)
	return result
}

// MemberCount returns the number of members.
func (g *GroupMembership) MemberCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.Members)
}

// RemoveMember removes an address from the group. Returns true if the member was found.
// If no members remain, the group is deleted from the store.
func RemoveMember(groupName, memberAddr string) bool {
	gm, ok := GetGroup(groupName)
	if !ok {
		return false
	}
	gm.mu.Lock()
	defer gm.mu.Unlock()
	for i, m := range gm.Members {
		if m == memberAddr {
			gm.Members = append(gm.Members[:i], gm.Members[i+1:]...)
			break
		}
	}
	if len(gm.Members) == 0 {
		DeleteGroup(groupName)
	}
	return true
}

// GroupSummary is a lightweight snapshot for CLI listing.
type GroupSummary struct {
	ID      string
	Name    string
	Members []string
	Closed  bool
}

// ListGroups returns info about all active and closed groups.
func ListGroups() []GroupSummary {
	var results []GroupSummary
	groupStore.Range(func(key, value any) bool {
		gm, ok := value.(*GroupMembership)
		if !ok {
			return true
		}
		gm.mu.RLock()
		results = append(results, GroupSummary{
			ID:      gm.ID,
			Name:    gm.Name,
			Members: append([]string{}, gm.Members...),
			Closed:  gm.Closed,
		})
		gm.mu.RUnlock()
		return true
	})
	return results
}
