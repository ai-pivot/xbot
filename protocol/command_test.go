package protocol

import "testing"

func TestMergeCommandInfoListsPreservesOrderAndFirstNonEmptyMetadata(t *testing.T) {
	local := []CommandInfo{
		{Name: "/help", Usage: "/help", Description: "local", Aliases: []string{"/?"}},
		{Name: "/clear", Usage: "/clear"},
		{Name: "/partial"},
	}
	agent := []CommandInfo{
		{Name: "/help", Usage: "/help <topic>", Description: "agent", Aliases: []string{"/h"}},
		{Name: "/partial", Usage: "/partial <arg>", Description: "filled later", Aliases: []string{"/p"}},
		{Name: "/continue", Usage: "/continue", Description: "resume"},
	}

	got := MergeCommandInfoLists(local, agent)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(got), got)
	}
	if got[0].Name != "/help" || got[0].Usage != "/help" || got[0].Description != "local" || len(got[0].Aliases) != 1 || got[0].Aliases[0] != "/?" {
		t.Fatalf("first command = %#v, want local /help metadata preserved", got[0])
	}
	if got[2].Usage != "/partial <arg>" || got[2].Description != "filled later" || len(got[2].Aliases) != 1 || got[2].Aliases[0] != "/p" {
		t.Fatalf("partial command = %#v, want missing fields filled", got[2])
	}
	if got[1].Name != "/clear" || got[3].Name != "/continue" {
		t.Fatalf("order = [%s %s %s %s]", got[0].Name, got[1].Name, got[2].Name, got[3].Name)
	}
}

func TestMergeCommandInfoListsExcludesHiddenCommands(t *testing.T) {
	got := MergeCommandInfoLists([]CommandInfo{
		{Name: "/visible"},
		{Name: "/hidden", Hidden: true},
	})
	if len(got) != 1 || got[0].Name != "/visible" {
		t.Fatalf("merged commands = %#v, want only /visible", got)
	}
}
