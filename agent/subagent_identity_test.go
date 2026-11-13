package agent

import "testing"

func TestIsSubAgentID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{id: "", want: false},
		{id: "main", want: false},
		{id: " main ", want: false},
		{id: "main/taizi", want: true},
		{id: "main/court.shoufu/court.bingbu", want: true},
		{id: "taizi", want: true}, // Legacy child ID must not regain main privileges.
		{id: "legacy/nested", want: true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if got := isSubAgentID(tc.id); got != tc.want {
				t.Errorf("isSubAgentID(%q)=%v, want %v", tc.id, got, tc.want)
			}
		})
	}
}
