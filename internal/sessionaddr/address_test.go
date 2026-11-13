package sessionaddr

import "testing"

func TestAddressRoundTrip(t *testing.T) {
	for _, a := range []Address{{"web", "chat"}, {"cli", "/home/user/project"}, {"agent", "web:chat/taizi:one"}, {"agent", "agent:web:chat/parent:one/child:two"}, {"plugin", "custom:chat/with/slashes"}} {
		got, ok := Parse(a.String())
		if !ok || got != a {
			t.Errorf("round trip %+v -> %+v (%v)", a, got, ok)
		}
	}
	for _, bad := range []string{"", "main/taizi", "main/taizi:one", ":chat", "web:", "web\x00:chat"} {
		if _, ok := Parse(bad); ok {
			t.Errorf("accepted non-address %q", bad)
		}
	}
}

func TestScopeUsesExactOwnerNotRoleOrRoot(t *testing.T) {
	for _, caller := range []string{"web:a/taizi:one", "web:a/taizi:two", "web:b/taizi:one", "agent:web:a/parent:p/taizi:one"} {
		want := Key("agent", caller)
		for _, id := range []string{"taizi", "main/taizi", "main/parent/taizi"} {
			if got := Scope(id, caller, "shared-role-id", "web:parent", "web", "parent"); got != want {
				t.Errorf("%s/%s=%s, want %s", id, caller, got, want)
			}
		}
	}
	if got := Scope("main", "", "web:override", "cli:physical", "web", "override"); got != "cli:physical" {
		t.Fatal(got)
	}
	if got := Scope("taizi", "", "taizi", "web:parent", "web", "parent"); got != "taizi" {
		t.Fatal("legacy child regained main scope:", got)
	}
}

func TestNestedChildPreservesAllAddressLayers(t *testing.T) {
	key := Child("agent", "web:root/parent:one", "child", "two")
	if got := Agent(key); got != "agent:agent:web:root/parent:one/child:two" {
		t.Fatal(got)
	}
	ch, chat, role, instance := SplitChild(key)
	if ch != "agent" || chat != "web:root/parent:one" || role != "child" || instance != "two" {
		t.Fatalf("%s %s %s %s", ch, chat, role, instance)
	}
}

func FuzzAddressRoundTrip(f *testing.F) {
	f.Add("web", "chat:one/role:two")
	f.Add("agent", "agent:web:root/parent:a/child:b")
	f.Add("cli", "/home/user/project")
	f.Fuzz(func(t *testing.T, ch, id string) {
		a, ok := Parse(Key(ch, id))
		if ok && a.String() != Key(ch, id) {
			t.Fatal("lossy round trip")
		}
	})
}
