package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// GroupMembership defines a virtual group chat — it's just a set of agent members.
// There is NO separate message store. Each agent already has its own session
// with full message history. The group only constrains which agents can
// communicate: agents in the same group can SendMessage directly to each other.
//
// This is a deliberate simplification over the old GroupState model which
// duplicated agent session functionality (message store, history formatting).
type GroupMembership struct {
	ID      string   // e.g. "g1"
	Name    string   // e.g. "group:g1"
	Members []string // agent addresses e.g. ["agent:reviewer/r1", "agent:tester/t1"]
	Closed  bool

	mu sync.RWMutex // protects Members and Closed for concurrent access
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

// RemoveMember removes an address from the group. Returns true if the member was found.
// If no members remain, the group is deleted from the store.
func RemoveMember(groupName, memberAddr string) bool {
	gm, ok := GetGroup(groupName)
	if !ok {
		return false
	}
	gm.mu.Lock()
	defer gm.mu.Unlock()
	found := false
	for i, m := range gm.Members {
		if m == memberAddr {
			gm.Members = append(gm.Members[:i], gm.Members[i+1:]...)
			found = true
			break
		}
	}
	if found && len(gm.Members) == 0 {
		DeleteGroup(groupName) // group auto-cleanup when empty
	}
	return found
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
		members := append([]string{}, gm.Members...)
		closed := gm.Closed
		gm.mu.RUnlock()
		results = append(results, GroupSummary{
			ID:      gm.ID,
			Name:    gm.Name,
			Members: members,
			Closed:  closed,
		})
		return true
	})
	return results
}

// ---------------------------------------------------------------------------
// PeerGroup — independent agent sessions forming a communication channel
// ---------------------------------------------------------------------------

// peerGroupIDRegex validates peer group IDs: alphanumeric, hyphens, underscores.
var peerGroupIDRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// PeerGroupMember represents one agent session in a peer group.
type PeerGroupMember struct {
	SessionKey string `json:"session_key"` // "channel:chatID" — the address for PeerMessageFn
	Name       string `json:"name"`        // Human-readable display name
}

// PeerGroup is a communication channel among independent agent sessions.
// Unlike GroupMembership (SubAgent meeting mode), PeerGroup members are
// main-agent sessions that communicate asynchronously via PeerMessageFn.
type PeerGroup struct {
	ID      string            `json:"id"`
	Members []PeerGroupMember `json:"members"`
}

// peerGroupStore is the persistent peer group store.
var peerGroupStore struct {
	mu     sync.RWMutex
	groups map[string]*PeerGroup // "peer:<id>" -> *PeerGroup
	loaded bool
}

// peerGroupFilePath returns the path to the peer groups persistence file.
func peerGroupFilePath() string {
	dir := os.Getenv("XBOT_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".xbot")
	}
	return filepath.Join(dir, "peer_groups.json")
}

// loadPeerGroupsOnce loads peer groups from disk on first access.
func loadPeerGroupsOnce() {
	peerGroupStore.mu.Lock()
	defer peerGroupStore.mu.Unlock()
	if peerGroupStore.loaded {
		return
	}
	peerGroupStore.loaded = true
	peerGroupStore.groups = make(map[string]*PeerGroup)

	path := peerGroupFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return // no file yet, start empty
	}
	var groups map[string]*PeerGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return // corrupt file, start empty
	}
	peerGroupStore.groups = groups
}

// peerGroupSaveTimer provides debounced persistence for peer groups.
// Instead of spawning a goroutine on every mutation (which can cause overlapping
// file writes under rapid join/leave), this uses a single timer that coalesces
// multiple mutations within the debounce window into one disk write.
var peerGroupSaveTimer struct {
	mu    sync.Mutex
	timer *time.Timer
}

// savePeerGroups persists all peer groups to disk.
func savePeerGroups() {
	peerGroupStore.mu.RLock()
	data, err := json.MarshalIndent(peerGroupStore.groups, "", "  ")
	peerGroupStore.mu.RUnlock()
	if err != nil {
		return
	}
	path := peerGroupFilePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	os.Rename(tmp, path) // atomic
}

// ValidatePeerGroupID checks whether an ID is valid for a peer group.
func ValidatePeerGroupID(id string) bool {
	return peerGroupIDRegex.MatchString(id)
}

// CreatePeerGroup creates a new peer group (or returns existing one).
func CreatePeerGroup(id string) *PeerGroup {
	loadPeerGroupsOnce()
	peerGroupStore.mu.Lock()
	defer peerGroupStore.mu.Unlock()

	name := "peer:" + id
	if pg, ok := peerGroupStore.groups[name]; ok {
		return pg
	}
	pg := &PeerGroup{
		ID:      id,
		Members: []PeerGroupMember{},
	}
	peerGroupStore.groups[name] = pg
	savePeerGroupsLocked()
	return pg
}

// GetPeerGroup retrieves a peer group by its name (e.g., "peer:my-group").
func GetPeerGroup(name string) (*PeerGroup, bool) {
	loadPeerGroupsOnce()
	peerGroupStore.mu.RLock()
	defer peerGroupStore.mu.RUnlock()
	pg, ok := peerGroupStore.groups[name]
	return pg, ok
}

// DeletePeerGroup removes a peer group from the store.
func DeletePeerGroup(name string) {
	loadPeerGroupsOnce()
	peerGroupStore.mu.Lock()
	defer peerGroupStore.mu.Unlock()
	delete(peerGroupStore.groups, name)
	savePeerGroupsLocked()
}

// savePeerGroupsLocked schedules a debounced persist to disk.
// Caller must hold at least RLock on peerGroupStore.mu.
// Instead of spawning a goroutine per mutation, this uses a single timer
// that coalesces rapid mutations into one disk write (100ms debounce).
// The actual write happens asynchronously after the caller releases the lock
// (peerGroupSaveDebounce delay), so savePeerGroups() can safely acquire RLock.
const peerGroupSaveDebounce = 100 * time.Millisecond

func savePeerGroupsLocked() {
	peerGroupSaveTimer.mu.Lock()
	defer peerGroupSaveTimer.mu.Unlock()
	if peerGroupSaveTimer.timer != nil {
		peerGroupSaveTimer.timer.Stop()
	}
	peerGroupSaveTimer.timer = time.AfterFunc(peerGroupSaveDebounce, func() {
		savePeerGroups()
		peerGroupSaveTimer.mu.Lock()
		peerGroupSaveTimer.timer = nil
		peerGroupSaveTimer.mu.Unlock()
	})
}

// FlushPeerGroups flushes any pending debounced write to disk synchronously.
// Call during graceful shutdown to prevent data loss (e.g., Agent.Close).
func FlushPeerGroups() {
	peerGroupSaveTimer.mu.Lock()
	if peerGroupSaveTimer.timer != nil {
		peerGroupSaveTimer.timer.Stop()
		peerGroupSaveTimer.timer = nil
		peerGroupSaveTimer.mu.Unlock()
		savePeerGroups() // synchronous write
		return
	}
	peerGroupSaveTimer.mu.Unlock()
}

// Join adds a member to the peer group. Returns false if already a member.
func (pg *PeerGroup) Join(member PeerGroupMember) bool {
	loadPeerGroupsOnce()
	peerGroupStore.mu.Lock()
	defer peerGroupStore.mu.Unlock()
	for _, m := range pg.Members {
		if m.SessionKey == member.SessionKey {
			return false // already a member
		}
	}
	pg.Members = append(pg.Members, member)
	savePeerGroupsLocked()
	return true
}

// Leave removes a member by sessionKey. Returns true if found and removed.
// If no members remain, the group is deleted from the store.
func (pg *PeerGroup) Leave(sessionKey string) bool {
	loadPeerGroupsOnce()
	peerGroupStore.mu.Lock()
	defer peerGroupStore.mu.Unlock()
	for i, m := range pg.Members {
		if m.SessionKey == sessionKey {
			pg.Members = append(pg.Members[:i], pg.Members[i+1:]...)
			if len(pg.Members) == 0 {
				delete(peerGroupStore.groups, "peer:"+pg.ID)
			}
			savePeerGroupsLocked()
			return true
		}
	}
	return false
}

// IsMember checks if a session is in this peer group.
func (pg *PeerGroup) IsMember(sessionKey string) bool {
	loadPeerGroupsOnce()
	peerGroupStore.mu.RLock()
	defer peerGroupStore.mu.RUnlock()
	for _, m := range pg.Members {
		if m.SessionKey == sessionKey {
			return true
		}
	}
	return false
}

// GetMembers returns a copy of the member list.
func (pg *PeerGroup) GetMembers() []PeerGroupMember {
	loadPeerGroupsOnce()
	peerGroupStore.mu.RLock()
	defer peerGroupStore.mu.RUnlock()
	result := make([]PeerGroupMember, len(pg.Members))
	copy(result, pg.Members)
	return result
}

// PeerGroupSummary is a lightweight snapshot for listing.
type PeerGroupSummary struct {
	ID      string
	Members []PeerGroupMember
}

// ListPeerGroups returns all peer groups. If sessionKey is non-empty,
// only returns groups that contain that session.
func ListPeerGroups(sessionKey string) []PeerGroupSummary {
	loadPeerGroupsOnce()
	peerGroupStore.mu.RLock()
	defer peerGroupStore.mu.RUnlock()
	var results []PeerGroupSummary
	for _, pg := range peerGroupStore.groups {
		members := make([]PeerGroupMember, len(pg.Members))
		copy(members, pg.Members)
		if sessionKey != "" {
			found := false
			for _, m := range members {
				if m.SessionKey == sessionKey {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		results = append(results, PeerGroupSummary{
			ID:      pg.ID,
			Members: members,
		})
	}
	return results
}
