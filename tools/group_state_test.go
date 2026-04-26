package tools

import "testing"

func TestGroupCreateAndGet(t *testing.T) {
	gm := CreateGroup("test1", []string{"agent:a/r1", "agent:b/r2"})
	if gm.ID != "test1" {
		t.Errorf("expected ID test1, got %s", gm.ID)
	}
	if gm.Name != "group:test1" {
		t.Errorf("expected Name group:test1, got %s", gm.Name)
	}
	if gm.MemberCount() != 2 {
		t.Fatalf("expected 2 members, got %d", gm.MemberCount())
	}
	if gm.IsClosed() {
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

	if gm.IsClosed() {
		t.Error("group should not be closed initially")
	}
	gm.Close()
	if !gm.IsClosed() {
		t.Error("group should be closed after Close()")
	}
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

	found := false
	for _, g := range groups {
		if g.ID == "la1" && len(g.Members) == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Error("la1 group not found in listing")
	}
}

func TestGetMembersReturnsCopy(t *testing.T) {
	gm := CreateGroup("testcopy", []string{"agent:a/1", "agent:b/2"})
	defer DeleteGroup("group:testcopy")

	members := gm.GetMembers()
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// Mutating the returned slice should not affect the group
	members[0] = "agent:modified/0"
	if gm.IsMember("agent:a/1") {
		// Still a member — good, copy was returned
	} else {
		t.Error("GetMembers() should return a copy, not a reference")
	}
}

func TestRemoveMember(t *testing.T) {
	gm := CreateGroup("testrm", []string{"agent:a/1", "agent:b/2", "agent:c/3"})
	defer DeleteGroup("group:testrm")

	ok := RemoveMember("group:testrm", "agent:b/2")
	if !ok {
		t.Error("RemoveMember should return true for existing member")
	}
	if gm.IsMember("agent:b/2") {
		t.Error("agent:b/2 should have been removed")
	}
	if gm.MemberCount() != 2 {
		t.Errorf("expected 2 members after removal, got %d", gm.MemberCount())
	}
}
