package tools

import (
	"fmt"
	"slices"
	"testing"
)

func TestGroupCreateAndGet(t *testing.T) {
	gm := CreateGroup("test1", []string{"agent:a/r1", "agent:b/r2"})
	if gm.ID != "test1" {
		t.Errorf("expected ID test1, got %s", gm.ID)
	}
	if gm.Name != "group:test1" {
		t.Errorf("expected Name group:test1, got %s", gm.Name)
	}
	if len(gm.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(gm.Members))
	}
	if gm.Closed {
		t.Error("new group should not be closed")
	}

	got, ok := GetGroup("group:test1")
	if !ok {
		t.Fatal("GetGroup failed")
	}
	if got.ID != "test1" {
		t.Errorf("retrieved wrong group: %s", got.ID)
	}

	DeleteGroup("group:test1")
	_, ok = GetGroup("group:test1")
	if ok {
		t.Error("group should be deleted")
	}
}

func TestGroupIsMember(t *testing.T) {
	gm := CreateGroup("test2", []string{"agent:a/1", "agent:b/2"})
	defer DeleteGroup("group:test2")

	if !gm.IsMember("agent:a/1") {
		t.Error("agent:a/1 should be a member")
	}
	if gm.IsMember("agent:c/3") {
		t.Error("agent:c/3 should not be a member")
	}
}

func TestGroupClose(t *testing.T) {
	gm := CreateGroup("test3", []string{"agent:a/1"})
	defer DeleteGroup("group:test3")

	if gm.Closed {
		t.Error("group should not be closed initially")
	}
	gm.Close()
	if !gm.Closed {
		t.Error("group should be closed after Close()")
	}
}

func TestGroupConcurrentAccess(t *testing.T) {
	gm := CreateGroup("concurrent-test", []string{"agent:a/1", "agent:b/2", "agent:c/3"})
	defer DeleteGroup("group:concurrent-test")

	const workers = 50
	done := make(chan bool, workers*3)

	// Concurrent RemoveMember
	for i := 0; i < workers; i++ {
		go func(n int) {
			addr := fmt.Sprintf("agent:x/%d", n)
			gm.mu.Lock()
			gm.Members = append(gm.Members, addr)
			gm.mu.Unlock()
			done <- true
		}(i)
	}

	// Concurrent IsMember
	for i := 0; i < workers; i++ {
		go func() {
			gm.IsMember("agent:a/1")
			done <- true
		}()
	}

	// Concurrent ListGroups (reads Members via snapshot)
	for i := 0; i < workers; i++ {
		go func() {
			ListGroups()
			done <- true
		}()
	}

	for i := 0; i < workers*3; i++ {
		<-done
	}
	// If we get here without panicking, the concurrency protection works.
}

func TestGroupDeleteNonexistent(t *testing.T) {
	DeleteGroup("group:nonexistent") // should not panic
}

func TestListGroups(t *testing.T) {
	defer DeleteGroup("group:la1")
	defer DeleteGroup("group:la2")

	CreateGroup("la1", []string{"agent:x/1"})
	CreateGroup("la2", []string{"agent:y/2", "agent:z/3"})

	groups := ListGroups()
	if len(groups) < 2 {
		t.Fatalf("expected at least 2 groups, got %d", len(groups))
	}

	if !slices.ContainsFunc(groups, func(g GroupSummary) bool { return g.ID == "la1" && len(g.Members) == 1 }) {
		t.Error("la1 group not found in listing")
	}
}

// --- PeerGroup tests ---

func TestPeerGroupCreateAndGet(t *testing.T) {
	pg := CreatePeerGroup("test-pg1")
	if pg.ID != "test-pg1" {
		t.Errorf("expected ID test-pg1, got %s", pg.ID)
	}

	// Get existing
	pg2 := CreatePeerGroup("test-pg1")
	if pg2 != pg {
		t.Error("CreatePeerGroup should return same instance for same ID")
	}

	// Get via GetPeerGroup
	got, ok := GetPeerGroup("peer:test-pg1")
	if !ok {
		t.Fatal("GetPeerGroup failed")
	}
	if got.ID != "test-pg1" {
		t.Errorf("retrieved wrong group: %s", got.ID)
	}

	DeletePeerGroup("peer:test-pg1")
	_, ok = GetPeerGroup("peer:test-pg1")
	if ok {
		t.Error("group should be deleted")
	}
}

func TestPeerGroupJoinLeave(t *testing.T) {
	pg := CreatePeerGroup("test-pg2")
	defer DeletePeerGroup("peer:test-pg2")

	// Join
	joined := pg.Join(PeerGroupMember{SessionKey: "cli:session-a", Name: "Agent-A"})
	if !joined {
		t.Error("should join successfully")
	}

	// Duplicate join
	joined = pg.Join(PeerGroupMember{SessionKey: "cli:session-a", Name: "Agent-A"})
	if joined {
		t.Error("duplicate join should return false")
	}

	// Second member
	joined = pg.Join(PeerGroupMember{SessionKey: "cli:session-b", Name: "Agent-B"})
	if !joined {
		t.Error("second member should join successfully")
	}

	// IsMember
	if !pg.IsMember("cli:session-a") {
		t.Error("session-a should be a member")
	}
	if pg.IsMember("cli:session-c") {
		t.Error("session-c should not be a member")
	}

	// GetMembers
	members := pg.GetMembers()
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// Leave
	left := pg.Leave("cli:session-a")
	if !left {
		t.Error("should leave successfully")
	}

	left = pg.Leave("cli:session-c")
	if left {
		t.Error("non-member leave should return false")
	}

	// After leave, only 1 member
	members = pg.GetMembers()
	if len(members) != 1 || members[0].SessionKey != "cli:session-b" {
		t.Errorf("expected [cli:session-b], got %v", members)
	}
}

func TestPeerGroupAutoDeleteOnEmpty(t *testing.T) {
	pg := CreatePeerGroup("test-pg3")
	pg.Join(PeerGroupMember{SessionKey: "cli:only-one", Name: "Solo"})
	// Leave last member → group deleted
	pg.Leave("cli:only-one")

	_, ok := GetPeerGroup("peer:test-pg3")
	if ok {
		t.Error("empty group should be auto-deleted")
	}
}

func TestPeerGroupIDValidation(t *testing.T) {
	tests := []struct {
		id    string
		valid bool
	}{
		{"valid-group", true},
		{"valid_group", true},
		{"a", true},
		{"group123", true},
		{"", false},
		{"-starts-with-dash", false},
		{"_starts-with-underscore", false},
		{"has space", false},
		{"has.dot", false},
		{"has/slash", false},
		{"UPPERCASE", true},
		{"mix3d-CASE_123", true},
	}

	for _, tt := range tests {
		result := ValidatePeerGroupID(tt.id)
		if result != tt.valid {
			t.Errorf("ValidatePeerGroupID(%q): expected %v, got %v", tt.id, tt.valid, result)
		}
	}
}

func TestListPeerGroups(t *testing.T) {
	// Clean up
	DeletePeerGroup("peer:list-a")
	DeletePeerGroup("peer:list-b")

	CreatePeerGroup("list-a")
	CreatePeerGroup("list-b")

	pgA, _ := GetPeerGroup("peer:list-a")
	pgA.Join(PeerGroupMember{SessionKey: "cli:user-1", Name: "User1"})
	pgA.Join(PeerGroupMember{SessionKey: "cli:user-2", Name: "User2"})

	pgB, _ := GetPeerGroup("peer:list-b")
	pgB.Join(PeerGroupMember{SessionKey: "cli:user-2", Name: "User2"})

	defer DeletePeerGroup("peer:list-a")
	defer DeletePeerGroup("peer:list-b")

	// List all groups for user-1
	groups := ListPeerGroups("cli:user-1")
	if len(groups) != 1 {
		t.Fatalf("user-1 should be in 1 group, got %d", len(groups))
	}
	if groups[0].ID != "list-a" {
		t.Errorf("expected list-a, got %s", groups[0].ID)
	}

	// List all groups for user-2 (in 2 groups)
	groups = ListPeerGroups("cli:user-2")
	if len(groups) != 2 {
		t.Fatalf("user-2 should be in 2 groups, got %d", len(groups))
	}
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	if !slices.Contains(ids, "list-a") || !slices.Contains(ids, "list-b") {
		t.Errorf("expected both list-a and list-b, got %v", ids)
	}

	// List all groups (empty filter)
	allGroups := ListPeerGroups("")
	if len(allGroups) < 2 {
		t.Errorf("expected at least 2 groups total, got %d", len(allGroups))
	}
}

func TestPeerGroupConcurrency(t *testing.T) {
	pg := CreatePeerGroup("concurrent-test")
	defer DeletePeerGroup("peer:concurrent-test")

	// Concurrent joins
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func(n int) {
			pg.Join(PeerGroupMember{
				SessionKey: "cli:session-" + string(rune(n)),
				Name:       "Agent-" + string(rune(n)),
			})
			done <- true
		}(i)
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	members := pg.GetMembers()
	if len(members) != 100 {
		t.Errorf("expected 100 members, got %d", len(members))
	}
}
